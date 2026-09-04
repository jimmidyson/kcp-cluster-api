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

package upstreamscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// ProfilerPort is where every controller serves pprof, set by
// cmd/capiscale-prepare. Bound on all interfaces rather than localhost, because
// samples come through the API server's pod proxy, which reaches the pod IP.
const ProfilerPort = 6060

// Sampler takes one sample of every controller.
//
// Through the API server's pod proxy rather than by dialling pods: a pod IP is
// routable from inside the cluster and from nowhere else, and this driver runs
// outside it — which is what lets one measurement address a managed cluster it
// cannot be scheduled into.
type Sampler struct {
	clientset kubernetes.Interface
}

// NewSampler builds a sampler from the cluster's config.
func NewSampler(cfg *rest.Config) (*Sampler, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a clientset: %w", err)
	}
	return &Sampler{clientset: clientset}, nil
}

// Process reads one pod's goroutine count and post-collection heap through
// pprof. See ScrapeProcess for why not /metrics.
func (s *Sampler) Process(ctx context.Context, namespace, pod string) (deployedscale.ProcessSample, error) {
	port := strconv.Itoa(ProfilerPort)

	// gc=1 forces a collection before the profile is written, so HeapAlloc is
	// the retained set; debug=1 makes it the text form, which carries the
	// runtime.MemStats block this reads.
	heap, err := s.clientset.CoreV1().Pods(namespace).
		ProxyGet("http", pod, port, "/debug/pprof/heap", map[string]string{"debug": "1", "gc": "1"}).
		DoRaw(ctx)
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("reading the heap profile of %s/%s: %w "+
			"(is --profiler-address set? cmd/capiscale-prepare sets it)", namespace, pod, err)
	}
	sample, err := ParseHeapProfile(bytes.NewReader(heap))
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("%s/%s: %w", namespace, pod, err)
	}

	goroutines, err := s.clientset.CoreV1().Pods(namespace).
		ProxyGet("http", pod, port, "/debug/pprof/goroutine", map[string]string{"debug": "1"}).
		DoRaw(ctx)
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("reading the goroutine profile of %s/%s: %w",
			namespace, pod, err)
	}
	total, err := ParseGoroutineProfile(bytes.NewReader(goroutines))
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("%s/%s: %w", namespace, pod, err)
	}
	sample.Goroutines = total
	return sample, nil
}

// podMetrics is the part of metrics.k8s.io's PodMetrics this needs.
//
// Read through the REST client and decoded into this rather than by depending
// on k8s.io/metrics: one field is not worth a module, and the shape of that
// field has been stable for as long as the API has existed.
type podMetrics struct {
	Containers []struct {
		Usage map[string]string `json:"usage"`
	} `json:"containers"`
}

// Resident reads a pod's working set from metrics.k8s.io.
//
// pprof cannot give it: nothing in a Go process reports its own resident size
// to a remote scraper, and resident memory is what a container limit is set
// against. Best effort — a missing figure is a metrics-server problem rather
// than a fleet problem, and is reported as zero rather than failing a rung,
// because a rung measured without its resident numbers is still a rung. The
// report says which figures it got.
func (s *Sampler) Resident(ctx context.Context, namespace, pod string) uint64 {
	raw, err := s.clientset.CoreV1().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces", namespace, "pods", pod).DoRaw(ctx)
	if err != nil {
		return 0
	}
	var metrics podMetrics
	if err := json.Unmarshal(raw, &metrics); err != nil {
		return 0
	}
	var total int64
	for _, c := range metrics.Containers {
		quantity, err := resource.ParseQuantity(c.Usage["memory"])
		if err != nil {
			continue
		}
		total += quantity.Value()
	}
	if total < 0 {
		return 0
	}
	return uint64(total)
}

// Usage reads a pod's working set, CPU time and CFS throttling from the
// kubelet's cAdvisor exposition on the node it runs on — one scrape for all
// three. See ContainerUsage for why they come from here rather than from
// metrics.k8s.io, and Throttling for why throttling is measured at all.
func (s *Sampler) Usage(ctx context.Context, node, namespace, pod string) (ContainerUsage, error) {
	if node == "" {
		return ContainerUsage{}, fmt.Errorf("no node recorded for %s/%s", namespace, pod)
	}
	raw, err := s.clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", node, "proxy", "metrics", "cadvisor").DoRaw(ctx)
	if err != nil {
		return ContainerUsage{}, fmt.Errorf("reading cadvisor on %s: %w", node, err)
	}
	return ParseCadvisor(bytes.NewReader(raw), namespace, pod)
}

// Sample takes one sample of every controller, with its throttling beside it.
//
// A controller with no running pod is an error rather than an omission: a
// sample missing one is a fleet measured without one of its providers, and
// reporting it as a smaller fleet would be wrong in the direction nobody
// checks.
func (s *Sampler) Sample(ctx context.Context, cl client.Client, controllers []Controller,
) ([]deployedscale.ComponentSample, map[string]Throttling, error) {
	samples := make([]deployedscale.ComponentSample, 0, len(controllers))
	throttling := map[string]Throttling{}

	for _, c := range controllers {
		replicas, err := ReplicasOf(ctx, cl, c.Namespace, c.Deployment)
		if err != nil {
			return nil, nil, err
		}
		if len(replicas) == 0 {
			return nil, nil, fmt.Errorf("%s has no running pod in %s: a sample without it would "+
				"report a fleet missing a provider", c.Name, c.Namespace)
		}
		// Every replica, each under its own name. A manager runs one pod today
		// and the naming is what keeps that from being an assumption baked into
		// the instrument — see ReplicaNames.
		labels := ReplicaNames(c.Deployment, len(replicas))

		for i := range replicas {
			pod := &replicas[i]
			process, err := s.Process(ctx, c.Namespace, pod.Name)
			if err != nil {
				return nil, nil, err
			}
			// The kubelet's own accounting, which serves the resident figure,
			// the CPU time and the throttling from one scrape. metrics.k8s.io
			// is the fallback rather than the source: the cluster under test
			// carries no addon it does not need, so it has no metrics-server,
			// and the first two runs reported every controller's resident
			// memory as zero.
			if usage, err := s.Usage(ctx, pod.Spec.NodeName, c.Namespace, pod.Name); err == nil {
				process.ResidentBytes = usage.WorkingSetBytes
				process.CPUSeconds = usage.CPUSeconds
				throttling[labels[i]] = usage.Throttling
			} else {
				process.ResidentBytes = s.Resident(ctx, c.Namespace, pod.Name)
			}

			samples = append(samples, deployedscale.ComponentSample{
				Component: labels[i],
				Process:   process,
				Pod:       c.PodFacts(pod),
			})
		}
	}
	return samples, throttling, nil
}

// RunningPodsOf keeps the pods that are actually serving, in name order.
//
// Every replica rather than the first. The sampler used to take one pod per
// deployment, reasoning that a second would mean a rollout whose metrics belong
// to two different processes. That is true of a manager running one replica and
// false of a control plane running three, where taking the first reports a
// third of the cost as the whole of it.
//
// Ordered so that replica #1 is the same process from one sample to the next: a
// series whose members swap places between samples cannot be plotted.
func RunningPodsOf(pods []corev1.Pod) []corev1.Pod {
	var running []corev1.Pod
	for i := range pods {
		if pods[i].DeletionTimestamp == nil && pods[i].Status.Phase == corev1.PodRunning {
			running = append(running, pods[i])
		}
	}
	sort.Slice(running, func(i, j int) bool { return running[i].Name < running[j].Name })
	return running
}

// ReplicasOf is a Deployment's running pods, found through its own selector.
//
// By the selector rather than by the pod's name. A pod is named
// <deployment>-<replicaset hash>-<suffix>, so a name test looks sound until two
// deployments share a prefix — and clusterctl installs four managers whose
// names are built from the same stem. Matching by prefix would sum two
// managers' processes under one name, which is wrong in a direction nothing
// downstream could detect: the total would still look like a plausible
// controller.
func ReplicasOf(ctx context.Context, cl client.Client, namespace, deployment string) ([]corev1.Pod, error) {
	var d appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: deployment}, &d); err != nil {
		return nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, deployment, err)
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("the selector of %s/%s: %w", namespace, deployment, err)
	}
	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("listing the pods of %s/%s: %w", namespace, deployment, err)
	}
	return RunningPodsOf(pods.Items), nil
}

// EtcdMetricsPort is where etcd serves metrics. kubeadm points
// --listen-metrics-urls at 127.0.0.1 by default, which no scraper outside the
// node can reach; the ClusterClass patch opens it so a run can be measured.
const EtcdMetricsPort = 2381

// APIServer samples the API server: a forced collection, then its own metrics.
//
// The collection first, for the reason the kcp runs had to be rebuilt to learn:
// live heap read without one is the retained set plus whatever has not been
// swept, and three runs disagreed by a factor of four for want of it. The API
// server serves pprof on its own endpoint, so this is two calls to the same
// address the client is already talking to.
func (s *Sampler) APIServer(ctx context.Context, samples int, gap time.Duration) (APIServer, error) {
	if samples < 1 {
		samples = 1
	}
	reads := make([]APIServer, 0, samples)
	for i := range samples {
		if i > 0 {
			select {
			case <-ctx.Done():
				return APIServer{}, ctx.Err()
			case <-time.After(gap):
			}
		}
		read, err := s.apiServerOnce(ctx)
		if err != nil {
			return APIServer{}, err
		}
		reads = append(reads, read)
	}
	return LowestHeap(reads), nil
}

// apiServerOnce is one read of the API server's metrics.
func (s *Sampler) apiServerOnce(ctx context.Context) (APIServer, error) {
	rest := s.clientset.CoreV1().RESTClient()

	// Best effort: an API server with profiling disabled still has metrics
	// worth reading. Whether it landed travels with the sample, because a heap
	// read without it is a point on a sawtooth rather than the retained set,
	// and that is not the same quantity the controllers report.
	_, collectErr := rest.Get().AbsPath("/debug/pprof/heap").Param("gc", "1").Param("debug", "1").DoRaw(ctx)

	raw, err := rest.Get().AbsPath("/metrics").DoRaw(ctx)
	if err != nil {
		return APIServer{}, fmt.Errorf("reading the API server's metrics: %w", err)
	}
	sample, err := ParseAPIServer(bytes.NewReader(raw))
	if err != nil {
		return sample, err
	}
	sample.HeapCollected = collectErr == nil
	return sample, nil
}

// ControlPlanes samples every instance of a control plane, one process at a
// time.
//
// # Why not one read
//
// Both sides run the process a fleet's objects live in more than once — three
// kube-apiservers behind a VIP, three shard replicas — and each holds its own
// watch cache, so each pays for the fleet in full. Reading through the client's
// own endpoint lands on whichever one the load balancer picked: every stock
// figure recorded before this was one arbitrary instance per sample, and on a
// three-instance control plane that is a third of the answer.
//
// The heap floor is applied per instance rather than across them (see
// LowestHeap): the lowest of five reads spread over three processes is not a
// floor of anything, it is the smallest of three unrelated sawtooths.
//
// # The fallback, and why it is in the report rather than a log
//
// The per-instance read goes through the API server's pod proxy, which forwards
// the request without the caller's credentials — so a kube-apiserver that
// refuses anonymous requests to /metrics returns 403 and no instance can be
// read apart. That case falls back to the endpoint and says so in the line the
// report carries, because a run that fell back quietly would be reproducing the
// old one-arbitrary-instance figure under a heading claiming otherwise.
func (s *Sampler) ControlPlanes(ctx context.Context, cl client.Client, loc ControlPlaneLocation,
	samples int, gap time.Duration,
) (ControlPlaneReading, error) {
	if samples < 1 {
		samples = 1
	}
	pods, err := ControlPlanePods(ctx, cl, loc)
	if err != nil {
		return s.controlPlaneViaEndpoint(ctx, samples, gap, err.Error())
	}

	reads := make([][]APIServer, len(pods))
	var firstErr error
	for round := range samples {
		if round > 0 {
			select {
			case <-ctx.Done():
				return ControlPlaneReading{}, ctx.Err()
			case <-time.After(gap):
			}
		}
		for i := range pods {
			read, err := s.controlPlaneOnce(ctx, loc, pods[i].Name)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			reads[i] = append(reads[i], read)
		}
	}

	var out ControlPlaneReading
	for i := range pods {
		if len(reads[i]) == 0 {
			out.Missing++
			continue
		}
		out.Instances = append(out.Instances, LowestHeap(reads[i]))
		out.Pods = append(out.Pods, pods[i].Name)
	}
	if len(out.Instances) == 0 {
		return s.controlPlaneViaEndpoint(ctx, samples, gap, firstErr.Error())
	}
	if out.Missing > 0 {
		out.Why = firstErr.Error()
	}
	return out, nil
}

// controlPlaneOnce is one read of one instance, through the pod proxy.
func (s *Sampler) controlPlaneOnce(ctx context.Context, loc ControlPlaneLocation, pod string) (APIServer, error) {
	proxy := s.clientset.CoreV1().Pods(loc.Namespace)
	port := strconv.Itoa(loc.Port)

	// Best effort, as it is through the endpoint: a control plane with
	// profiling disabled still has metrics worth reading, and whether the
	// collection landed travels with the sample.
	_, collectErr := proxy.ProxyGet(loc.Scheme, pod, port, "/debug/pprof/heap",
		map[string]string{"gc": "1", "debug": "1"}).DoRaw(ctx)

	raw, err := proxy.ProxyGet(loc.Scheme, pod, port, "/metrics", nil).DoRaw(ctx)
	if err != nil {
		return APIServer{}, fmt.Errorf("reading %s/%s: %w", loc.Namespace, pod, err)
	}
	sample, err := ParseAPIServer(bytes.NewReader(raw))
	if err != nil {
		return sample, fmt.Errorf("%s/%s: %w", loc.Namespace, pod, err)
	}
	sample.HeapCollected = collectErr == nil
	return sample, nil
}

// controlPlaneViaEndpoint is the fallback: one arbitrary instance, marked.
func (s *Sampler) controlPlaneViaEndpoint(ctx context.Context, samples int, gap time.Duration, why string,
) (ControlPlaneReading, error) {
	one, err := s.APIServer(ctx, samples, gap)
	if err != nil {
		return ControlPlaneReading{}, err
	}
	return ControlPlaneReading{
		Instances:   []APIServer{one},
		Pods:        []string{"whichever instance the endpoint resolved to"},
		ViaEndpoint: true,
		Why:         why,
	}, nil
}

// Etcd samples one etcd member through the API server's pod proxy.
//
// The member rather than the cluster: kubeadm runs one static pod per control
// plane node and they do not aggregate. Sampling the leader would be neater and
// is not worth the round trip — the backend size and the quota are the same on
// every member, and the disk latencies are per member, which is what a reader
// wants when one node's disk is the slow one.
func (s *Sampler) Etcd(ctx context.Context, cl client.Client) (Etcd, string, error) {
	return s.EtcdAt(ctx, cl, KubeadmStore())
}

// EtcdAt samples one member of a named store. See StoreLocation: the two sides
// of the comparison keep their etcd in different places and this is how one
// sampler reads either.
func (s *Sampler) EtcdAt(ctx context.Context, cl client.Client, store StoreLocation) (Etcd, string, error) {
	members, err := StorePods(ctx, cl, store)
	if err != nil {
		return Etcd{}, "", err
	}
	// The first by name, deterministically: the backend size and the quota are
	// the same on every member, and a run that read a different one each time
	// would attribute their disk latencies to each other.
	pod := &members[0]

	sample, err := s.etcdMemberAt(ctx, store, pod.Name)
	if err != nil {
		return Etcd{}, pod.Name, fmt.Errorf("reading etcd's metrics from %s: %w "+
			"(kubeadm points --listen-metrics-urls at 127.0.0.1; the ClusterClass patch opens it, "+
			"and the deployed store is started with it open)", pod.Name, err)
	}
	return sample, pod.Name, nil
}

// etcdOf reads one named member, which is what a defragmentation needs either
// side of itself to say what it reclaimed.
func (s *Sampler) etcdMemberAt(ctx context.Context, store StoreLocation, pod string) (Etcd, error) {
	raw, err := s.clientset.CoreV1().Pods(store.Namespace).
		ProxyGet("http", pod, strconv.Itoa(store.MetricsPort), "/metrics", nil).DoRaw(ctx)
	if err != nil {
		return Etcd{}, err
	}
	return ParseEtcd(bytes.NewReader(raw))
}
