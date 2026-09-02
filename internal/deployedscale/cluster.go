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
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpconfig"
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
			// A container that is running and not ready is the case the two
			// above miss entirely: it has no Waiting state and has never
			// terminated, so both leave detail empty and the wait reports
			// "0/1 available" with nothing in the parentheses.
			//
			// That is exactly how a whole run was lost. The manager was up and
			// visibly reconciling for the full ten minutes while its readiness
			// probe answered 404, and every line of evidence for that lived
			// somewhere this function did not look. The kubelet writes the
			// reason down as an Event; all that was needed was to read it.
			if status.State.Running != nil && detail == "" {
				detail = fmt.Sprintf("%s/%s is running but not ready after %d restart(s)",
					pod.Name, status.Name, status.RestartCount)
				if reason := notReadyReason(pod); reason != "" {
					detail += ": " + reason
				}
				if ev := latestWarning(ctx, cl, pod); ev != "" {
					detail += " (" + ev + ")"
				}
			}
		}
	}
	return detail, false
}

// notReadyReason is what the pod itself says about not being ready.
func notReadyReason(pod *corev1.Pod) string {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status != corev1.ConditionTrue {
			return strings.TrimSpace(cond.Reason + " " + cond.Message)
		}
	}
	return ""
}

// latestWarning returns the most recent Warning Event about a pod.
//
// This is where a failing probe is written down — "Readiness probe failed:
// HTTP probe failed with statuscode: 404" — and it is the difference between a
// diagnosis and a replica count. Best effort: a diagnostic that fails is worse
// than one that says less.
func latestWarning(ctx context.Context, cl client.Client, pod *corev1.Pod) string {
	var events corev1.EventList
	if err := cl.List(ctx, &events, client.InNamespace(pod.Namespace),
		client.MatchingFields{"involvedObject.name": pod.Name}); err != nil {
		// The field index is not always available through a cached client;
		// fall back to listing the namespace and filtering here.
		if err := cl.List(ctx, &events, client.InNamespace(pod.Namespace)); err != nil {
			return ""
		}
	}

	var newest *corev1.Event
	for i := range events.Items {
		ev := &events.Items[i]
		if ev.InvolvedObject.Name != pod.Name || ev.Type != corev1.EventTypeWarning {
			continue
		}
		if newest == nil || eventTime(ev).After(eventTime(newest)) {
			newest = ev
		}
	}
	if newest == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", newest.Reason, strings.TrimSpace(newest.Message))
}

func eventTime(ev *corev1.Event) time.Time {
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if !ev.EventTime.IsZero() {
		return ev.EventTime.Time
	}
	return ev.CreationTimestamp.Time
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

// InitializationLogPatterns match the lines that bear on a workspace failing
// to initialize, and nothing else.
//
// Deliberately narrow. An earlier version of this also matched "err=" and
// "Unhandled Error", which sounds thorough and is not: kcp emits a burst of
// those in its first second — RBAC informers that cannot list yet, a
// kube-system namespace it will never have — and taking the earliest matches
// then returns a screenful of startup noise and truncates before reaching
// anything about the workspace. A filter that matches everything interesting
// is a filter that shows the first thing rather than the right thing.
var InitializationLogPatterns = []string{"apibinder", "initializ", "createdby", "apibinding"}

// LogNoisePatterns are dropped from a match even when a narrow pattern hit
// them.
//
// Narrowing by keyword is not enough on its own, because kcp names its own
// machinery after the thing under diagnosis. Every start prints the enabled
// admission plugins — a list containing APIBinding — names its controllers in
// full, including kcp-apibinder-initializer, and announces each one waiting for
// its informers to sync. All of that matches "apibinding", "apibinder" and
// "initializ" before any workspace exists, and taking the earliest matches then
// fills the window with a banner and truncates before the reconcile.
//
// A run diagnosed this way twice, and both times the output was kcp starting up
// correctly, which reads as evidence of nothing. Excluding the banner leaves the
// per-workspace lines, which are the ones worth printing.
var LogNoisePatterns = []string{
	"waiting for sync",
	"caches are synced",
	"starting controller",
	"shutting down",
	"admission plugin",
	"enabled admission",
	"loaded admission",
	"initializing cache",
	"skipping apibinding crd",
	"skipping local crd",
}

// StartupFailurePatterns are the fallback, used only when nothing matched the
// narrow set — a server that never mentioned the workspace at all did
// something else wrong, and then the noise is the best evidence available.
var StartupFailurePatterns = []string{"unhandled error", "forbidden", "failed to start", "panic"}

// ContainerLogsMatching returns the lines of a component's output matching the
// narrow patterns, falling back to the broad ones when the narrow set finds
// nothing.
//
// Matching is a plain case-insensitive substring test: the patterns are fixed
// strings chosen for this, and a diagnostic is not the place to discover that
// somebody's regular expression does not compile.
//
// The whole life of the container is read rather than a tail. kcp's apibinder
// acts within a second of a workspace being created and then says nothing
// more, while the server emits a couple of lines of CRD bookkeeping every
// second for ever — so by the time a two-minute wait gives up, the lines that
// explain it are thousands back and a tail contains only noise. Two separate
// diagnoses of this failure were handed exactly that.
func ContainerLogsMatching(ctx context.Context, cfg *rest.Config, cl client.Client, namespace, component string,
	narrow, fallback []string, max int,
) (lines string, matchedNarrow bool) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", false
	}
	pods, err := ComponentPods(ctx, cl, namespace, component)
	if err != nil || len(pods) == 0 {
		return "", false
	}

	raw, err := clientset.CoreV1().Pods(namespace).GetLogs(pods[0].Name, &corev1.PodLogOptions{
		Container: component,
	}).DoRaw(ctx)
	if err != nil {
		return "", false
	}
	return FilterLog(string(raw), narrow, fallback, max)
}

// FilterLog is the whole of ContainerLogsMatching that does not need a cluster:
// given a component's output, it picks the lines worth printing.
//
// Split out so it can be tested against real kcp output, which is the only way
// to know whether a pattern set actually surfaces the reconcile rather than the
// startup banner that mentions the same words.
func FilterLog(log string, narrow, fallback []string, max int) (lines string, matchedNarrow bool) {
	all := strings.Split(log, "\n")

	match := func(patterns []string) []string {
		var kept []string
		for _, line := range all {
			haystack := strings.ToLower(line)
			if matchesAny(haystack, LogNoisePatterns) {
				continue
			}
			if matchesAny(haystack, patterns) {
				kept = append(kept, line)
			}
		}
		return kept
	}

	if kept := match(narrow); len(kept) > 0 {
		if len(kept) > max {
			kept = kept[:max]
		}
		return strings.Join(kept, "\n"), true
	}

	kept := match(fallback)
	if len(kept) == 0 {
		return "", false
	}
	if len(kept) > max {
		kept = kept[:max]
	}
	return strings.Join(kept, "\n"), false
}

func matchesAny(loweredLine string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(loweredLine, strings.ToLower(p)) {
			return true
		}
	}
	return false
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
// Forward is a supervised port-forward: a local address that stays valid while
// the tunnel underneath it is re-established as often as it needs to be.
//
// # Why supervision rather than one forwarder
//
// A port-forward is a single SPDY stream through the API server, and it dies.
// Earlier runs showed it fraying — "error copying from remote stream to local
// connection: broken pipe", "connection reset by peer" — and survived, because
// the driver's next request happened to land on a healthy moment. At 200
// clusters of 50 nodes it died outright, and the run reported:
//
//	waiting for 50 workspaces to reach the end state: timed out after 20m0s:
//	50 of 50 workspaces short (listing control planes in 2wypd8khv58vy4t6:
//	dial tcp 127.0.0.1:41579: connect: connection refused)
//
// Nothing was listening because the forwarder had exited and nobody was
// watching: ForwardPorts ran in a goroutine whose error was read once, at
// startup, and never again. The failure then reads as a fleet that would not
// converge, which is the opposite of what it was — the managers reach kcp
// through the Service inside the cluster, so a dead tunnel blinds the driver
// and leaves the fleet alone.
//
// So the tunnel is now watched and rebuilt on the same local port, and the
// number of times that happened is recorded. A measurement taken across a flap
// is still a measurement — the fleet never noticed — but a run that flapped
// twenty times was fighting its instrument and should say so.
type Forward struct {
	// Local is the address to dial. It does not change across restarts.
	Local string

	restarts atomic.Int64
	stop     func()
}

// Restarts is how many times the tunnel had to be rebuilt.
func (f *Forward) Restarts() int { return int(f.restarts.Load()) }

// Stop tears the forward down and stops supervising it.
func (f *Forward) Stop() {
	if f.stop != nil {
		f.stop()
	}
}

// tunnel starts one port-forward. A localPort of 0 asks for any free port; the
// port actually bound is returned, along with a channel that receives when the
// tunnel dies and a function that closes it.
type tunnel func(localPort int) (bound int, died <-chan error, closeTunnel func(), err error)

// PortForward forwards a local port to a pod's port and keeps it forwarded.
func PortForward(ctx context.Context, cfg *rest.Config, namespace, pod string, remotePort int) (*Forward, error) {
	return forwardWith(ctx, spdyTunnel(cfg, namespace, pod, remotePort))
}

// forwardWith is PortForward with the tunnel injected, so the supervision can
// be tested without a cluster — which matters, because the bug this exists to
// fix only appears when a tunnel dies, and a healthy one never does.
func forwardWith(ctx context.Context, t tunnel) (*Forward, error) {
	bound, died, closeTunnel, err := t(0)
	if err != nil {
		return nil, err
	}

	f := &Forward{Local: net.JoinHostPort("127.0.0.1", strconv.Itoa(bound))}
	supervisorCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	f.stop = func() {
		once.Do(func() {
			cancel()
			closeTunnel()
		})
	}

	go func() {
		for {
			select {
			case <-supervisorCtx.Done():
				return
			case <-died:
			}
			closeTunnel()

			// The same local port, so every address already handed out stays
			// correct. Rebinding a listening port that has just been closed is
			// ordinarily immediate; a retry covers the case where it is not.
			// A tunnel that dies as soon as it is used counts as a failed
			// attempt, not a successful one.
			//
			// Establishing a forward succeeds whenever the pod exists; it says
			// nothing about whether anything is listening inside it. When kcp
			// is down the forwarder starts, reports "Forwarding from ...",
			// fails on the first byte, and dies — so a backoff reset on each
			// successful establish never grows, and the loop rebuilds about
			// once a second for as long as the run lasts. That produced
			// thousands of identical lines and buried the one fact that
			// mattered, which was that kcp was not listening.
			for attempt := 0; ; attempt++ {
				select {
				case <-supervisorCtx.Done():
					return
				case <-time.After(backoff(attempt)):
				}
				started := time.Now()
				_, next, nextClose, err := t(bound)
				if err != nil {
					continue
				}
				f.restarts.Add(1)
				if time.Since(started) < shortLived {
					// Give it a moment to fail before believing in it.
					select {
					case <-supervisorCtx.Done():
						nextClose()
						return
					case err := <-next:
						_ = err
						nextClose()
						continue
					case <-time.After(shortLived):
					}
				}
				died, closeTunnel = next, nextClose
				break
			}
		}
	}()

	return f, nil
}

// shortLived is how quickly a rebuilt tunnel has to fail to be treated as
// having never worked.
const shortLived = 2 * time.Second

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt, 5)) * 100 * time.Millisecond
	return min(d, 5*time.Second)
}

func spdyTunnel(cfg *rest.Config, namespace, pod string, remotePort int) tunnel {
	return func(localPort int) (int, <-chan error, func(), error) {
		clientset, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("building a clientset: %w", err)
		}
		transport, upgrader, err := spdy.RoundTripperFor(cfg)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("building the port-forward transport: %w", err)
		}
		url := clientset.CoreV1().RESTClient().Post().
			Resource("pods").Namespace(namespace).Name(pod).SubResource("portforward").URL()
		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)

		ready := make(chan struct{})
		stopCh := make(chan struct{})
		// io.Discard, deliberately. The forwarder logs a banner on every
		// establish and an error on every failed byte, and a supervised
		// forward against a server that is down produces thousands of both.
		// What a reader needs is the restart count and the reason the server
		// is down, and both are reported elsewhere.
		fw, err := portforward.New(dialer,
			[]string{fmt.Sprintf("%d:%d", localPort, remotePort)}, stopCh, ready, io.Discard, io.Discard)
		if err != nil {
			close(stopCh)
			return 0, nil, nil, fmt.Errorf("building the port forwarder: %w", err)
		}

		errCh := make(chan error, 1)
		go func() { errCh <- fw.ForwardPorts() }()

		select {
		case <-ready:
		case err := <-errCh:
			close(stopCh)
			return 0, nil, nil, fmt.Errorf("forwarding a port to %s/%s: %w", namespace, pod, err)
		case <-time.After(time.Minute):
			close(stopCh)
			return 0, nil, nil, errors.New("the port forward did not become ready within a minute")
		}

		ports, err := fw.GetPorts()
		if err != nil || len(ports) == 0 {
			close(stopCh)
			return 0, nil, nil, fmt.Errorf("reading the forwarded port: %w", err)
		}

		var closeOnce sync.Once
		return int(ports[0].Local), errCh, func() { closeOnce.Do(func() { close(stopCh) }) }, nil
	}
}

// WorkspaceConfig returns a copy of base addressing one logical cluster.
//
// # Why this exists rather than kcpclient.SetCluster
//
// SetCluster appends the cluster path to whatever host it is given:
//
//	cfg.Host += clusterPath.RequestPath()
//
// which is correct for a bare server URL and silently wrong for a config that
// already addresses a workspace. Handing it the root-scoped config produced
//
//	https://host/clusters/root/clusters/2fj3k…
//
// and every request through the resulting client failed at discovery with
// "failed to get server groups: the server could not find the requested
// resource" — an error that names neither the doubled path nor the workspace,
// and reads like the workspace is not ready yet.
//
// This normalises instead of assuming: any trailing /clusters/<path> on the
// base is replaced, so a bare, a root-scoped and an already-workspace-scoped
// base all produce the same result.
func WorkspaceConfig(base *rest.Config, cluster string) *rest.Config {
	return kcpconfig.ForCluster(base, cluster)
}

// ServerURL strips a /clusters/<path> suffix from a host, leaving the bare
// server. A host with no such suffix is returned unchanged.
func ServerURL(host string) string {
	return kcpconfig.BaseHost(host)
}

// ServerTrouble reports what is wrong with kcp, if anything.
//
// # Why a wait has to look at the server
//
// Every wait in a run is a wait for workspaces, and every one of them fails the
// same way when kcp is not serving: no workspace advances, and the wait times
// out saying so. A run of 200 clusters at 50 nodes each timed out reporting
// "50 of 50 workspaces short", which reads as 10,000 Machines that would not
// converge. What had happened was that kcp's container had gone, and the
// kubelet was refusing the forward with
//
//	failed to connect to localhost:6443 inside namespace "...": connection
//	refused
//
// The workspaces were never the finding. A wait that cannot tell "the fleet is
// slow" from "the server is dead" reports the first and means the second, and
// twenty minutes are spent proving nothing.
func ServerTrouble(ctx context.Context, cl client.Client, namespace string) string {
	pods, err := ComponentPods(ctx, cl, namespace, KcpName)
	if err != nil || len(pods) == 0 {
		return "kcp has no pod in the namespace"
	}

	for i := range pods {
		pod := &pods[i]
		for j := range pod.Status.ContainerStatuses {
			st := &pod.Status.ContainerStatuses[j]
			if term := st.LastTerminationState.Terminated; term != nil {
				reason := fmt.Sprintf("kcp last exited %d (%s) and has restarted %d time(s)",
					term.ExitCode, term.Reason, st.RestartCount)
				if term.Reason == "OOMKilled" {
					// The capacity finding this measurement exists to produce,
					// on the one component that is not a manager.
					return reason + ": it exceeded its memory limit, which is the fleet size this " +
						"shard cannot hold with the memory it was given"
				}
				return reason
			}
			if !st.Ready {
				if w := st.State.Waiting; w != nil {
					return fmt.Sprintf("kcp is not ready: %s: %s", w.Reason, w.Message)
				}
				return "kcp is running but not ready"
			}
		}
	}
	return ""
}
