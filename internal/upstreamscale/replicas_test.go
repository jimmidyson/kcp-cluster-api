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
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// TestOneReplicaKeepsItsPlainName, so every figure already recorded against a
// single-replica deployment still lines up with one taken now.
func TestOneReplicaKeepsItsPlainName(t *testing.T) {
	got := ReplicaNames("capi-controller-manager", 1)
	if len(got) != 1 || got[0] != "capi-controller-manager" {
		t.Errorf("names = %v, want the bare component name", got)
	}
}

// TestSeveralReplicasAreNamedApart.
//
// runningPodOf returned the first pod matching a deployment and its comment
// reasoned that a second would mean a rollout. That stops being true at three
// shard replicas, where it would have reported one of the three as "the shard"
// — a third of a control plane, presented as the whole of one.
func TestSeveralReplicasAreNamedApart(t *testing.T) {
	got := ReplicaNames("kcp", 3)
	if len(got) != 3 {
		t.Fatalf("names = %v, want three", got)
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n] {
			t.Errorf("two replicas share the name %q, so one overwrites the other", n)
		}
		seen[n] = true
		if !strings.HasPrefix(n, "kcp") {
			t.Errorf("%q does not say which component it is", n)
		}
	}
}

// TestTheControlPlaneIsSummedAndBrokenDown.
//
// The stock side runs three API servers behind a VIP and the kcp side three
// shard replicas, each holding its own full watch cache. One instance's figure
// is a third of a control plane, and the total is what a node budget is spent
// against — so the line carries both, and neither is left for a reader to work
// out from the samples.
func TestTheControlPlaneIsSummedAndBrokenDown(t *testing.T) {
	instances := []APIServer{
		{Process: deployedscale.ProcessSample{Goroutines: 4200, HeapAllocBytes: 7_000_000_000,
			ResidentBytes: 12_000_000_000}, StorageObjects: 33482, HeapSamples: 5},
		{Process: deployedscale.ProcessSample{Goroutines: 4100, HeapAllocBytes: 6_500_000_000,
			ResidentBytes: 11_000_000_000}, StorageObjects: 33482, HeapSamples: 5},
		{Process: deployedscale.ProcessSample{Goroutines: 4300, HeapAllocBytes: 7_200_000_000,
			ResidentBytes: 13_000_000_000}, StorageObjects: 33482, HeapSamples: 5},
	}
	got := DescribeControlPlane(instances)

	if !strings.Contains(got, "3 instances") {
		t.Errorf("the line does not say how many processes this is: %q", got)
	}
	// 12 + 11 + 13 GB resident.
	if !strings.Contains(got, "35.5 GiB") && !strings.Contains(got, "33.5 GiB") {
		t.Errorf("the line does not carry the total resident: %q", got)
	}
	// And the largest single instance, which is what bounds the node it is on.
	if !strings.Contains(got, "largest") {
		t.Errorf("the line does not name the largest instance: %q", got)
	}
	// Stored objects are the store's, not each process's, so summing them
	// would multiply the fleet by the replica count.
	if strings.Contains(got, "100446") {
		t.Errorf("stored objects were summed across instances: %q", got)
	}
	if !strings.Contains(got, "33482") {
		t.Errorf("the stored object count is missing: %q", got)
	}
}

// TestOneInstanceReadsAsItAlwaysDid, so a single-process control plane's line
// is not cluttered with a breakdown of one.
func TestOneInstanceReadsAsItAlwaysDid(t *testing.T) {
	one := []APIServer{{
		Process:        deployedscale.ProcessSample{Goroutines: 4193, HeapAllocBytes: 7_000_000_000},
		StorageObjects: 33482,
	}}
	got := DescribeControlPlane(one)
	if strings.Contains(got, "instances") || strings.Contains(got, "largest") {
		t.Errorf("a single instance was described as a set: %q", got)
	}
	if !strings.Contains(got, "4193 goroutines") {
		t.Errorf("the single instance's own line was lost: %q", got)
	}
}

// TestNoInstancesIsNotAnEmptyControlPlane. A control plane the sampler could
// not reach must not read as one that costs nothing.
func TestNoInstancesIsNotAnEmptyControlPlane(t *testing.T) {
	if got := DescribeControlPlane(nil); !strings.Contains(got, "not") {
		t.Errorf("an unreachable control plane reads as %q", got)
	}
}

// TestAFallbackReadingSaysItIsOneInstance.
//
// The pod proxy forwards a request without the caller's credentials, so a
// kube-apiserver that refuses anonymous metrics cannot be read instance by
// instance and the reading falls back to the endpoint. Every stock figure
// recorded before this existed was taken that way, so a fallback that did not
// say so would reproduce those numbers under a heading claiming the set.
func TestAFallbackReadingSaysItIsOneInstance(t *testing.T) {
	reading := ControlPlaneReading{
		Instances:   []APIServer{{Process: deployedscale.ProcessSample{Goroutines: 4193}}},
		ViaEndpoint: true,
		Why:         "403 forbidden",
	}
	got := reading.Describe()
	if !strings.Contains(got, "arbitrary") {
		t.Errorf("the fallback reads as the whole control plane: %q", got)
	}
	if !strings.Contains(got, "403 forbidden") {
		t.Errorf("the line does not say what stopped the per-instance read: %q", got)
	}
}

// TestAPartialReadingIsShortAndSaysSo. Two of three instances summed is not
// what a control plane costs, and the difference is a whole process.
func TestAPartialReadingIsShortAndSaysSo(t *testing.T) {
	reading := ControlPlaneReading{
		Instances: []APIServer{
			{Process: deployedscale.ProcessSample{Goroutines: 4193, ResidentBytes: 12_000_000_000}},
			{Process: deployedscale.ProcessSample{Goroutines: 4100, ResidentBytes: 11_000_000_000}},
		},
		Pods:    []string{"kube-apiserver-cp-0", "kube-apiserver-cp-1"},
		Missing: 1,
		Why:     "connection refused",
	}
	got := reading.Describe()
	if !strings.Contains(got, "could not be read") {
		t.Errorf("a control plane short of an instance reads as complete: %q", got)
	}
	if strings.Contains(got, "arbitrary") {
		t.Errorf("a partial reading was described as the endpoint fallback: %q", got)
	}
}

// TestEachInstanceIsAComponentOfItsOwn, named apart and carrying the pod it
// came from, so the largest can be traced to the node it is on.
func TestEachInstanceIsAComponentOfItsOwn(t *testing.T) {
	reading := ControlPlaneReading{
		Instances: []APIServer{
			{Process: deployedscale.ProcessSample{Goroutines: 4193}},
			{Process: deployedscale.ProcessSample{Goroutines: 4100}},
		},
		Pods: []string{"kube-apiserver-cp-0", "kube-apiserver-cp-1"},
	}
	got := reading.Samples("kube-apiserver")
	if len(got) != 2 {
		t.Fatalf("%d components for two instances", len(got))
	}
	if got[0].Component == got[1].Component {
		t.Errorf("both instances are called %q, so one overwrites the other", got[0].Component)
	}
	if got[0].Pod.Name != "kube-apiserver-cp-0" {
		t.Errorf("the component does not name the pod it came from: %q", got[0].Pod.Name)
	}
	if got[0].Process.Goroutines != 4193 {
		t.Errorf("the instance's own figures did not survive: %+v", got[0].Process)
	}
}
