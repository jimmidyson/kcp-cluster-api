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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// Throttled reads how much CPU the kernel took away from a pod, from the
// kubelet's cAdvisor exposition on the node it runs on. See Throttling for why
// this is measured at all. Best effort, for the same reason as Resident.
func (s *Sampler) Throttled(ctx context.Context, node, namespace, pod string) (Throttling, error) {
	if node == "" {
		return Throttling{}, fmt.Errorf("no node recorded for %s/%s", namespace, pod)
	}
	raw, err := s.clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", node, "proxy", "metrics", "cadvisor").DoRaw(ctx)
	if err != nil {
		return Throttling{}, fmt.Errorf("reading cadvisor on %s: %w", node, err)
	}
	return ParseThrottling(bytes.NewReader(raw), namespace, pod)
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
		var pods corev1.PodList
		if err := cl.List(ctx, &pods, client.InNamespace(c.Namespace)); err != nil {
			return nil, nil, fmt.Errorf("listing pods in %s: %w", c.Namespace, err)
		}
		pod := runningPodOf(pods.Items, c.Deployment)
		if pod == nil {
			return nil, nil, fmt.Errorf("%s has no running pod in %s: a sample without it would "+
				"report a fleet missing a provider", c.Name, c.Namespace)
		}

		process, err := s.Process(ctx, c.Namespace, pod.Name)
		if err != nil {
			return nil, nil, err
		}
		process.ResidentBytes = s.Resident(ctx, c.Namespace, pod.Name)

		samples = append(samples, deployedscale.ComponentSample{
			Component: c.Deployment,
			Process:   process,
			Pod:       deployedscale.PodFactsFrom(pod, c.Deployment),
		})
		if t, err := s.Throttled(ctx, pod.Spec.NodeName, c.Namespace, pod.Name); err == nil {
			throttling[c.Deployment] = t
		}
	}
	return samples, throttling, nil
}

// runningPodOf picks the deployment's pod, and only one: a second would mean
// the deployment was rolling, whose metrics belong to two different processes.
func runningPodOf(pods []corev1.Pod, deployment string) *corev1.Pod {
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if len(pod.Name) > len(deployment) && pod.Name[:len(deployment)] == deployment {
			return pod
		}
	}
	return nil
}
