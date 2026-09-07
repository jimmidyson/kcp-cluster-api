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

func steppedDown(name string) deployedscale.ComponentSample {
	return deployedscale.ComponentSample{
		Component: name,
		Pod: deployedscale.PodFacts{
			Name: name, StaticPod: true, RestartCount: 1, LastExitCode: 1, LastReason: "Error",
		},
	}
}

// TestAControlPlaneSheddingItselfIsNotAProcessBlip.
//
// A rung at 1500 clusters ended with kube-controller-manager exiting 1, and the
// report called it "restarted 1 time(s) — exited 1 of its own accord". Its own
// log said what that meant, in twenty-odd lines: the garbage collector, the node
// lifecycle controller, deployments, endpoints and taint eviction all shut down
// with it. A reader should not have to open a log to find that out.
func TestAControlPlaneSheddingItselfIsNotAProcessBlip(t *testing.T) {
	why := Classify([]deployedscale.ComponentSample{
		steppedDown("kube-controller-manager-capi-scale-fw9n7"),
	}, false)

	for _, want := range []string{
		"the Kubernetes control plane shed",
		"garbage collection",
		"node lifecycle",
		"taint eviction",
		"failing to manage itself",
	} {
		if !strings.Contains(why, want) {
			t.Errorf("the line does not say %q:\n%s", want, why)
		}
	}
	if strings.Contains(why, "samples are not comparable") {
		t.Errorf("it was reported with the generic restart wording:\n%s", why)
	}
}

// TestTheSchedulerSaysWhatItsAbsenceCost, which is a different sentence: a
// cluster without a scheduler is not a cluster without garbage collection.
func TestTheSchedulerSaysWhatItsAbsenceCost(t *testing.T) {
	why := Classify([]deployedscale.ComponentSample{
		steppedDown("kube-scheduler-capi-scale-nl882"),
	}, false)
	if !strings.Contains(why, "stayed Pending") {
		t.Errorf("the line does not say what a missing scheduler costs:\n%s", why)
	}
	if strings.Contains(why, "garbage collection") {
		t.Errorf("the scheduler was described as the controller manager:\n%s", why)
	}
}

// TestAClusterApiManagerIsNotTheKubernetesControlPlane.
//
// It exits 1 for the same reason and it means something else: losing it stops
// reconciliation, not the cluster's ability to run pods. The static pod marker
// is what separates them — see IsControlPlaneComponent.
func TestAClusterApiManagerIsNotTheKubernetesControlPlane(t *testing.T) {
	manager := deployedscale.ComponentSample{
		Component: "capi-controller-manager",
		Pod: deployedscale.PodFacts{
			RestartCount: 1, LastExitCode: 1, LastReason: "Error", StaticPod: false,
		},
	}
	if got := SteppedDown(manager.Component, manager.Pod); got != "" {
		t.Errorf("a Cluster API manager was described as the Kubernetes control plane: %q", got)
	}
	if why := Classify([]deployedscale.ComponentSample{manager}, false); !strings.Contains(
		why, "samples are not comparable") {
		t.Errorf("a manager lost its usual wording:\n%s", why)
	}
}

// TestAnApiServerKilledIsNotDescribedAsSteppingDown. etcd and kube-apiserver do
// not leader-elect, and a 137 is a kill rather than a choice — the existing
// wording for those is the correct one and must not be overwritten.
func TestAnApiServerKilledIsNotDescribedAsSteppingDown(t *testing.T) {
	killed := deployedscale.ComponentSample{
		Component: "kube-apiserver-capi-scale-fw9n7",
		Pod: deployedscale.PodFacts{
			StaticPod: true, RestartCount: 1, LastExitCode: 137, LastReason: "Error",
		},
	}
	if got := SteppedDown(killed.Component, killed.Pod); got != "" {
		t.Errorf("a SIGKILL was described as stepping down: %q", got)
	}
	if why := Classify([]deployedscale.ComponentSample{killed}, false); !strings.Contains(
		why, "kubelet killed it") {
		t.Errorf("the kill lost its own explanation:\n%s", why)
	}
}

// TestAnOomStillOutranksIt, because a component killed for memory wants a
// memory answer whatever else it was doing.
func TestAnOomStillOutranksIt(t *testing.T) {
	components := []deployedscale.ComponentSample{
		steppedDown("kube-controller-manager-capi-scale-fw9n7"),
		{Component: "kube-apiserver-capi-scale-nl882", Pod: deployedscale.PodFacts{
			StaticPod: true, RestartCount: 1, OOMKilled: true,
			LastReason: deployedscale.ReasonOOMKilled, MemoryLimitBytes: 8 << 30,
		}},
	}
	if why := Classify(components, false); !strings.Contains(why, "OOM killed") {
		t.Errorf("an OOM kill was outranked by a step-down:\n%s", why)
	}
}
