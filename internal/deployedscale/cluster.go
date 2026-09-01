/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package deployedscale

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClusterConfig resolves the cluster a run targets.
//
// A kubeconfig and nothing else (FR-001): kind is one way to produce one, a
// managed cluster is another, and a harness that could tell them apart would
// be a harness with an opinion about which it was written for.
// The context is named separately from the kubeconfig because "whatever is
// current" is the wrong default for something that creates workloads: a run
// meant for a throwaway local cluster, started while the current context
// points somewhere else, would deploy into somewhere else. Naming it costs a
// word and removes that.
func ClusterConfig(kubeconfigPath, context string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if context != "" {
		overrides.CurrentContext = context
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		if context != "" {
			return nil, fmt.Errorf("no cluster to run against in context %q: %w", context, err)
		}
		return nil, fmt.Errorf("no cluster to run against: %w", err)
	}
	return cfg, nil
}

// ClusterReachable confirms the API server answers, so a run that cannot
// happen says so before it creates anything.
func ClusterReachable(ctx context.Context, cfg *rest.Config) error {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building a clientset: %w", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		return fmt.Errorf("the cluster at %s did not answer: %w", cfg.Host, err)
	}
	return nil
}

// Apply creates each object, updating one that already exists.
//
// Ordered, and one at a time: Options.Objects returns them in creation order
// because a Deployment whose Secret does not exist yet does not fail, it
// starts a pod that cannot mount and waits.
func Apply(ctx context.Context, cl client.Client, objects []client.Object) error {
	for _, obj := range objects {
		err := cl.Create(ctx, obj)
		if err == nil {
			continue
		}
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
		}
		// A namespace left behind by an interrupted run is reused; anything
		// else is updated so a rerun picks up changed images and arguments
		// rather than silently measuring the previous run's build.
		if _, isNamespace := obj.(*corev1.Namespace); isNamespace {
			continue
		}
		if err := cl.Update(ctx, obj); err != nil {
			return fmt.Errorf("updating %T %s: %w", obj, obj.GetName(), err)
		}
	}
	return nil
}

// Teardown removes everything a run created by deleting its namespace.
func Teardown(ctx context.Context, cl client.Client, namespace string) error {
	ns := &corev1.Namespace{}
	ns.Name = namespace
	if err := cl.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting namespace %s: %w", namespace, err)
	}
	return nil
}

// TeardownAndWait deletes the run's namespace and waits until it is gone.
//
// The wait is not tidiness. Namespace deletion is asynchronous, and a second
// run that deployed while the first was still terminating would measure both:
// kcp and every manager from the previous spread are still scheduled and still
// reconciling. A measurement taken then is of a cluster doing twice the work,
// and nothing in the report would say so.
func TeardownAndWait(ctx context.Context, cl client.Client, namespace string, timeout, poll time.Duration) error {
	if err := Teardown(ctx, cl, namespace); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	for {
		var ns corev1.Namespace
		err := cl.Get(ctx, client.ObjectKey{Name: namespace}, &ns)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("waiting for namespace %s to go: %w", namespace, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("namespace %s was still terminating after %s: a run starting now would measure "+
				"the previous one as well", namespace, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// WaitForDeployment waits until a Deployment reports an available replica.
func WaitForDeployment(ctx context.Context, cl client.Client, namespace, name string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string

	for {
		var d appsv1.Deployment
		err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &d)
		switch {
		case err != nil:
			last = err.Error()
		case d.Status.AvailableReplicas > 0:
			return nil
		default:
			last = fmt.Sprintf("%d/%d available", d.Status.AvailableReplicas, d.Status.Replicas)
			// What the cluster already knows about why. Without this a wait
			// reports only a replica count, which is the one thing that does
			// not say what to fix.
			detail, terminal := podTrouble(ctx, cl, namespace, name)
			if detail != "" {
				last = fmt.Sprintf("%s (%s)", last, detail)
			}
			if terminal {
				return fmt.Errorf("%s will not come up: %s", name, detail)
			}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s was not available within %s: %s", name, timeout, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// TerminalWaitReasons are the container states a wait gives up on rather than
// sits through.
//
// Both mean the kubelet has already retried and failed repeatedly — they are
// reached through backoff, not on the first attempt — so waiting out a
// ten-minute timeout on them buys nothing and costs the person running it ten
// minutes and the reason.
var TerminalWaitReasons = []string{"ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff", "CreateContainerConfigError"}

// podTrouble reports what is wrong with a component's pods, and whether it is
// worth continuing to wait.
//
// Reporting the reason at all is the point. A wait that can only say
// "0/1 available" after ten minutes has thrown away everything the cluster
// knew: that the image could not be pulled, that the container is crash
// looping, that no node would take it. Each of those is one line the kubelet
// already wrote down.
func podTrouble(ctx context.Context, cl client.Client, namespace, component string) (detail string, terminal bool) {
	pods, err := ComponentPods(ctx, cl, namespace, component)
	if err != nil || len(pods) == 0 {
		return "", false
	}

	for i := range pods {
		pod := &pods[i]

		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == corev1.PodReasonUnschedulable {
				// Nothing about a cluster that will not schedule a pod
				// changes by waiting for it.
				return fmt.Sprintf("%s cannot be scheduled: %s", pod.Name, cond.Message), true
			}
		}

		for j := range pod.Status.ContainerStatuses {
			status := &pod.Status.ContainerStatuses[j]
			if status.Ready {
				continue
			}
			if w := status.State.Waiting; w != nil {
				detail = fmt.Sprintf("%s/%s is %s: %s", pod.Name, status.Name, w.Reason, w.Message)
				if slices.Contains(TerminalWaitReasons, w.Reason) {
					return detail, true
				}
			}
			if term := status.LastTerminationState.Terminated; term != nil {
				detail = fmt.Sprintf("%s/%s last exited %d (%s): %s",
					pod.Name, status.Name, term.ExitCode, term.Reason, strings.TrimSpace(term.Message))
			}
		}
	}
	return detail, false
}

// ComponentPods lists the pods belonging to one component.
func ComponentPods(ctx context.Context, cl client.Client, namespace, component string) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(labels(component))); err != nil {
		return nil, fmt.Errorf("listing pods for %s: %w", component, err)
	}
	return pods.Items, nil
}

// Scraper reads a manager's metrics endpoint.
//
// Through the API server's pod proxy rather than by dialling the pod. A pod IP
// is routable from inside the cluster and from nowhere else, so a harness that
// dialled one would work only when run in-cluster — and the driver runs
// outside it, because that is what lets one measurement address a managed
// cluster it cannot be scheduled into.
type Scraper struct {
	clientset kubernetes.Interface
}

// NewScraper builds a scraper from the cluster's config.
func NewScraper(cfg *rest.Config) (*Scraper, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a clientset: %w", err)
	}
	return &Scraper{clientset: clientset}, nil
}

// Scrape reads and parses one pod's process metrics.
func (s *Scraper) Scrape(ctx context.Context, namespace, pod string) (ProcessSample, error) {
	raw, err := s.clientset.CoreV1().Pods(namespace).
		ProxyGet("http", pod, strconv.Itoa(MetricsPort), "/metrics", nil).
		DoRaw(ctx)
	if err != nil {
		return ProcessSample{}, fmt.Errorf("scraping %s/%s: %w", namespace, pod, err)
	}
	return ParseProcessSample(bytes.NewReader(raw))
}

// SampleComponents takes one sample of every component named.
//
// A component with no running pod is an error rather than an omission: a
// sample missing a deployment is a fleet measured without one of its
// providers, and reporting it as a smaller fleet would be wrong in the
// direction nobody checks.
func (s *Scraper) SampleComponents(ctx context.Context, cl client.Client, namespace string, components []Component) ([]ComponentSample, error) {
	out := make([]ComponentSample, 0, len(components))
	for _, c := range components {
		pods, err := ComponentPods(ctx, cl, namespace, c.Name)
		if err != nil {
			return nil, err
		}
		if len(pods) == 0 {
			return nil, fmt.Errorf("%s has no pod: a sample without it would report a fleet missing a provider", c.Name)
		}
		// The first pod: every component here is single-replica, and a second
		// one would mean the deployment was rolling, whose metrics belong to
		// two different processes.
		pod := pods[0]

		process, err := s.Scrape(ctx, namespace, pod.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, ComponentSample{
			Component: c.Name,
			Process:   process,
			Pod:       PodFactsFrom(&pod, c.Name),
		})
	}
	return out, nil
}

// ContainerLogs returns the tail of a component's container output, preferring
// the previous container when there was one.
//
// The previous container is the one that matters: a pod in CrashLoopBackOff is
// waiting to start again, so its current container has said nothing yet and
// everything worth reading belongs to the run that failed.
//
// Best effort by design. This is called when something has already gone wrong,
// and a diagnostic that can itself fail the run would replace one confusing
// error with another.
func ContainerLogs(ctx context.Context, cfg *rest.Config, cl client.Client, namespace, component string, lines int64) string {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return ""
	}
	pods, err := ComponentPods(ctx, cl, namespace, component)
	if err != nil || len(pods) == 0 {
		return ""
	}

	pod := pods[0]
	previous := false
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == component && pod.Status.ContainerStatuses[i].RestartCount > 0 {
			previous = true
		}
	}

	read := func(prev bool) string {
		raw, err := clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: component, Previous: prev, TailLines: &lines,
		}).DoRaw(ctx)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}

	if previous {
		if out := read(true); out != "" {
			return out
		}
	}
	return read(false)
}

// PortForward opens a local port onto a pod and returns the local address.
//
// This is how the driver reaches kcp. It cannot go through the API server's
// proxy as the metrics scrape does: a driver needs full API semantics against
// kcp — watches, its own bearer token, its own trust of kcp's CA — and a proxy
// that re-terminates the request gives none of them. A forwarded port is a
// plain TCP tunnel, so what arrives at kcp is the client's own TLS session.
//
// The credentials cover 127.0.0.1 for exactly this reason: kcp is addressed by
// its Service name from inside the cluster and by a loopback address from
// outside it, and one certificate has to satisfy both.
func PortForward(ctx context.Context, cfg *rest.Config, namespace, pod string, remotePort int) (local string, stop func(), err error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("building a clientset: %w", err)
	}

	transport, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("building the port-forward transport: %w", err)
	}
	url := clientset.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)

	ready := make(chan struct{})
	stopCh := make(chan struct{})
	// Port 0 asks the forwarder for any free local port, which is what keeps
	// two runs on one machine from colliding.
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", remotePort)}, stopCh, ready, os.Stderr, os.Stderr)
	if err != nil {
		close(stopCh)
		return "", nil, fmt.Errorf("building the port forwarder: %w", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-ready:
	case err := <-errCh:
		close(stopCh)
		return "", nil, fmt.Errorf("forwarding a port to %s/%s: %w", namespace, pod, err)
	case <-ctx.Done():
		close(stopCh)
		return "", nil, ctx.Err()
	case <-time.After(time.Minute):
		close(stopCh)
		return "", nil, errors.New("the port forward did not become ready within a minute")
	}

	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return "", nil, fmt.Errorf("reading the forwarded port: %w", err)
	}

	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ports[0].Local))), func() { close(stopCh) }, nil
}
