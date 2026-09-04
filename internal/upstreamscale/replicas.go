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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// # Why replicas are named and summed rather than picked from
//
// Both sides of the comparison run their control plane more than once: three
// kube-apiservers behind a VIP on the stock side, three shard replicas on the
// kcp side. Each holds its own watch cache, so each pays for the fleet in full
// and one instance's heap is a third of what the control plane costs.
//
// The sampler used to take the first pod matching a deployment, reasoning that
// a second would mean a rollout. That holds for a single-replica manager and
// stops holding here, so a set is sampled as a set: every replica named apart
// so one does not overwrite another in the report, and the line that summarises
// them carrying both the total and the largest single instance — the total
// because that is what the nodes pay, the largest because that is what the node
// it lands on has to fit.

// ReplicaNames is what the report calls each replica of a component.
//
// One replica keeps the bare component name, so every figure already recorded
// against a single-replica deployment still lines up with one taken now; a run
// that renamed them would make its own history incomparable for no gain.
func ReplicaNames(component string, n int) []string {
	switch {
	case n < 1:
		return nil
	case n == 1:
		return []string{component}
	}
	out := make([]string, 0, n)
	for i := range n {
		// An ordinal rather than the pod name: pod names change on every
		// restart, and a series that renames itself mid-run cannot be plotted.
		out = append(out, fmt.Sprintf("%s#%d", component, i+1))
	}
	return out
}

// SumControlPlane adds up what a set of instances costs.
//
// Everything a process owns is summed. StorageObjects and its two breakdowns
// are not: they count what the store holds, which every instance reports in
// full, so summing them would multiply the fleet by the replica count. The
// highest is taken instead, since a replica whose watch cache is behind reports
// fewer.
func SumControlPlane(instances []APIServer) APIServer {
	var out APIServer
	for _, in := range instances {
		out.Process.Goroutines += in.Process.Goroutines
		out.Process.HeapAllocBytes += in.Process.HeapAllocBytes
		out.Process.HeapSysBytes += in.Process.HeapSysBytes
		out.Process.ResidentBytes += in.Process.ResidentBytes
		out.Process.CPUSeconds += in.Process.CPUSeconds
		out.InflightRequests += in.InflightRequests
		out.RejectedRequests += in.RejectedRequests
		out.EtcdRequestSum += in.EtcdRequestSum
		out.EtcdRequestCount += in.EtcdRequestCount

		out.StorageObjects = max(out.StorageObjects, in.StorageObjects)
		out.ClusterAPIObjects = max(out.ClusterAPIObjects, in.ClusterAPIObjects)
		out.EventObjects = max(out.EventObjects, in.EventObjects)

		// A collection that landed on some instances and not others is not a
		// collected total, so the weaker claim wins.
		out.HeapSamples = max(out.HeapSamples, in.HeapSamples)
	}
	out.HeapCollected = len(instances) > 0
	for _, in := range instances {
		if !in.HeapCollected {
			out.HeapCollected = false
		}
	}
	return out
}

// LargestControlPlane is the instance that costs the most, which is the one
// bounding the node it runs on.
//
// By resident memory, because that is what a limit is set against, falling back
// to heap for a run whose resident figures did not arrive.
func LargestControlPlane(instances []APIServer) APIServer {
	var out APIServer
	for _, in := range instances {
		switch {
		case in.Process.ResidentBytes > out.Process.ResidentBytes:
			out = in
		case in.Process.ResidentBytes == out.Process.ResidentBytes &&
			in.Process.HeapAllocBytes > out.Process.HeapAllocBytes:
			out = in
		}
	}
	return out
}

// DescribeControlPlane is the line a rung carries about the control plane,
// however many processes it is.
func DescribeControlPlane(instances []APIServer) string {
	switch len(instances) {
	case 0:
		return "the control plane was **not sampled**: no instance answered, so this rung has no " +
			"figure for the process the fleet's objects live in"
	case 1:
		// A set of one is not a set, and describing it as one would make every
		// single-instance run read differently from the ones already recorded.
		return instances[0].Describe()
	}

	total := SumControlPlane(instances)
	largest := LargestControlPlane(instances)

	var b strings.Builder
	fmt.Fprintf(&b, "%d instances, summed: %d goroutines, %s heap, %s resident, %d requests in flight, "+
		"etcd calls %.1fms",
		len(instances), total.Process.Goroutines, humanBytes(total.Process.HeapAllocBytes),
		humanBytes(total.Process.ResidentBytes), total.InflightRequests, total.EtcdRequestMeanMillis())
	fmt.Fprintf(&b, "; %d objects stored (%d Cluster API, %d event) — the store's, which every instance "+
		"reports in full, so not summed",
		total.StorageObjects, total.ClusterAPIObjects, total.EventObjects)
	if total.SheddingLoad() {
		fmt.Fprintf(&b, "; **shedding load**: %d request(s) rejected by priority and fairness",
			total.RejectedRequests)
	}
	fmt.Fprintf(&b, "; largest instance: %s", largest.Describe())
	return b.String()
}

// ControlPlaneLocation is where a side's control-plane processes are and how
// their metrics are reached.
//
// The same idea as StoreLocation, and for the same reason: the two sides keep
// the process a fleet's objects live in in different places — kubeadm's static
// pods on one, a Deployment in the run's own namespace on the other — and one
// sampler has to read either without the two readings being taken differently.
type ControlPlaneLocation struct {
	Namespace string
	Labels    map[string]string
	// Component is what the report calls one of these processes, before the
	// replica ordinal is added.
	Component string
	// Scheme and Port are what the pod proxy is pointed at. https for a
	// kube-apiserver, which serves nothing in the clear.
	Scheme string
	Port   int
}

// KubeAPIServers is the stock side's: kubeadm's static pods, one per control
// plane node.
func KubeAPIServers() ControlPlaneLocation {
	return ControlPlaneLocation{
		Namespace: "kube-system",
		Labels:    map[string]string{"component": "kube-apiserver"},
		Component: "kube-apiserver",
		Scheme:    "https",
		Port:      6443,
	}
}

// ControlPlanePods is the running instances of a control plane, in name order.
//
// Ordered so that instance #1 is the same process from one sample to the next.
func ControlPlanePods(ctx context.Context, cl client.Client, loc ControlPlaneLocation) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace(loc.Namespace),
		client.MatchingLabels(loc.Labels)); err != nil {
		return nil, fmt.Errorf("listing control plane pods in %s: %w", loc.Namespace, err)
	}
	running := RunningPodsOf(pods.Items)
	if len(running) == 0 {
		return nil, fmt.Errorf("no running control plane pods matching %v in %s: on a managed control "+
			"plane they run where no pod list shows them, which is a cluster this measurement can still "+
			"be pointed at — but not one whose instances it can read apart",
			loc.Labels, loc.Namespace)
	}
	return running, nil
}

// ControlPlaneReading is a control plane as one sample saw it.
type ControlPlaneReading struct {
	// Instances is one per process, in the order ControlPlanePods returned
	// them, each already reduced by LowestHeap over its own reads.
	Instances []APIServer
	// Pods names them, for a reader working out which node the largest is on.
	Pods []string

	// ViaEndpoint says the instances could not be read apart and this is one
	// arbitrary process reached through the client's own endpoint — a third of
	// a control plane on a three-instance cluster. Why says what stopped it.
	//
	// This is not a detail to leave in a log: every stock figure recorded
	// before this existed was taken that way, and a run that silently fell
	// back would be producing those numbers again under a heading that claims
	// otherwise.
	ViaEndpoint bool
	Why         string

	// Missing is how many instances answered nothing, when some did. A control
	// plane summed over two of its three processes is not the control plane's
	// cost, and the line says so rather than reporting a smaller one.
	Missing int
}

// Samples are the reading as report components, one per instance.
//
// The ordinal is the instance's place among those that answered, and the pod
// name is what says which process it was. Those come apart when a replica is
// unreachable for one sample: the ordinals close up, so #2 in that sample is
// the pod that was #3 in the last one. The pod name in each sample is the
// reliable identity, and the summed figures are what the rung is read on.
func (r ControlPlaneReading) Samples(component string) []deployedscale.ComponentSample {
	labels := ReplicaNames(component, len(r.Instances))
	out := make([]deployedscale.ComponentSample, 0, len(r.Instances))
	for i, in := range r.Instances {
		name := r.podName(i)
		out = append(out, deployedscale.ComponentSample{
			Component: labels[i],
			Process:   in.Process,
			Pod:       deployedscale.PodFacts{Name: name, Ready: true},
		})
	}
	return out
}

func (r ControlPlaneReading) podName(i int) string {
	if i < len(r.Pods) {
		return r.Pods[i]
	}
	return "unknown"
}

// Describe is the line the report carries, with the caveat attached when there
// is one.
func (r ControlPlaneReading) Describe() string {
	line := DescribeControlPlane(r.Instances)
	if r.Missing > 0 && !r.ViaEndpoint {
		line += fmt.Sprintf(" — **%d further instance(s) could not be read** (%s), so the summed "+
			"figures are that much short of what this control plane costs", r.Missing, r.Why)
	}
	if r.ViaEndpoint {
		line += " — **one arbitrary instance**, not the set: it was read through the client's own " +
			"endpoint because the instances could not be read apart (" + r.Why + "), so a control " +
			"plane running more than one process costs more than this line says"
	}
	return line
}
