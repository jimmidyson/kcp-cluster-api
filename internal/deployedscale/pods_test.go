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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pod(container string, status corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "core-manager-abc", Namespace: "scale"},
		Spec: corev1.PodSpec{
			NodeName: "node-2",
			Containers: []corev1.Container{{
				Name: container,
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
				},
			}},
		},
		Status: corev1.PodStatus{PodIP: "10.244.2.9", ContainerStatuses: []corev1.ContainerStatus{status}},
	}
}

func TestPodFactsFrom(t *testing.T) {
	got := PodFactsFrom(pod(ComponentCore, corev1.ContainerStatus{Name: ComponentCore, Ready: true}), ComponentCore)

	if got.Node != "node-2" {
		t.Errorf("node = %q; without it a figure cannot be attributed to a spread deployment", got.Node)
	}
	if got.PodIP != "10.244.2.9" {
		t.Errorf("podIP = %q, and the harness scrapes that address", got.PodIP)
	}
	if !got.Ready {
		t.Error("ready = false")
	}
	if got.MemoryLimitBytes != 2*1024*1024*1024 {
		t.Errorf("memory limit = %d, want 2Gi: a figure read against no limit is not a sizing input", got.MemoryLimitBytes)
	}
	if !got.Comparable() {
		t.Error("a container that has not restarted was reported as not comparable")
	}
}

// TestOOMKillIsVisible is the capacity finding this measurement exists to
// produce. A process cannot report the moment it was killed, so without this a
// run would record the fleet getting cheaper as its containers died.
func TestOOMKillIsVisible(t *testing.T) {
	got := PodFactsFrom(pod(ComponentCore, corev1.ContainerStatus{
		Name:         ComponentCore,
		RestartCount: 1,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: ReasonOOMKilled, ExitCode: 137,
		}},
	}), ComponentCore)

	if !got.OOMKilled {
		t.Error("an OOMKilled container was not reported as one")
	}
	if got.LastExitCode != 137 {
		t.Errorf("exit code = %d, want 137", got.LastExitCode)
	}
	if got.Comparable() {
		t.Error("a restarted container was reported as comparable: its metrics are a fresh process's")
	}
}

// TestARestartWithoutAnOOMKillIsStillDisqualifying: any restart resets the
// process metrics, whatever caused it.
func TestARestartWithoutAnOOMKillIsStillDisqualifying(t *testing.T) {
	got := PodFactsFrom(pod(ComponentCore, corev1.ContainerStatus{
		Name:         ComponentCore,
		Ready:        true,
		RestartCount: 2,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "Error", ExitCode: 1,
		}},
	}), ComponentCore)

	if got.OOMKilled {
		t.Error("a non-OOM restart was reported as an OOMKill")
	}
	if got.Comparable() {
		t.Error("a restarted container was reported as comparable")
	}
	if got.LastReason != "Error" {
		t.Errorf("last reason = %q", got.LastReason)
	}
}

// TestFactsAreReadFromTheNamedContainer guards a pod that gained a sidecar
// from silently reporting the sidecar's restarts as the manager's.
func TestFactsAreReadFromTheNamedContainer(t *testing.T) {
	p := pod(ComponentCore, corev1.ContainerStatus{Name: ComponentCore, Ready: true})
	p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
		Name:         "sidecar",
		RestartCount: 9,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: ReasonOOMKilled,
		}},
	})

	got := PodFactsFrom(p, ComponentCore)
	if got.RestartCount != 0 || got.OOMKilled {
		t.Errorf("the sidecar's restarts were read as the manager's: %+v", got)
	}
}

func TestPodFactsFromAnUnscheduledPod(t *testing.T) {
	got := PodFactsFrom(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending"}}, ComponentCore)
	if got.Node != "" || got.Ready {
		t.Errorf("an unscheduled pod reported placement or readiness: %+v", got)
	}
}

// TestASigkillWithoutTheOomFlagIsNotCalledAnOom.
//
// This is the case a stock run actually produced: a kube-apiserver whose last
// state read `Exit Code: 137, Reason: Error`, on a container kubeadm gives no
// memory limit. Reading 137 as an OOM sends an operator to buy memory for a
// process that ran out of responsiveness, not out of memory — and the run whose
// ceiling that was is the one that would have been mis-explained.
func TestASigkillWithoutTheOomFlagIsNotCalledAnOom(t *testing.T) {
	facts := PodFacts{
		Name:         "kube-apiserver-capi-scale-nl882",
		RestartCount: 1,
		LastExitCode: 137,
		LastReason:   "Error",
	}
	got := facts.WhyItDied()

	if strings.Contains(got, "OOM") && !strings.Contains(got, "not flagged OOMKilled") {
		t.Errorf("a kubelet kill was described as an OOM: %q", got)
	}
	if !strings.Contains(got, "kubelet") {
		t.Errorf("the line does not say who did the killing: %q", got)
	}
	if !strings.Contains(got, "no memory limit is set on it") {
		t.Errorf("the line does not say there was no limit to have exceeded: %q", got)
	}
}

// TestARealOomSaysWhatItWasAgainst, because "it exceeded its memory limit" is
// not actionable without the limit — the next run has to be given a number.
func TestARealOomSaysWhatItWasAgainst(t *testing.T) {
	facts := PodFacts{
		Name:             "kcp-0",
		RestartCount:     1,
		LastExitCode:     137,
		LastReason:       ReasonOOMKilled,
		OOMKilled:        true,
		MemoryLimitBytes: 4 << 30,
	}
	got := facts.WhyItDied()
	if !strings.Contains(got, "OOM killed") {
		t.Errorf("a real OOM does not read as one: %q", got)
	}
	if !strings.Contains(got, "4.0 GiB") {
		t.Errorf("the limit it was killed against is missing: %q", got)
	}
}

// TestAnOomWithNoLimitIsNodePressure. There is no limit to raise, so a line
// telling an operator to raise one would send them looking for a field that
// does not exist.
func TestAnOomWithNoLimitIsNodePressure(t *testing.T) {
	facts := PodFacts{RestartCount: 1, LastReason: ReasonOOMKilled, OOMKilled: true}
	if got := facts.WhyItDied(); !strings.Contains(got, "node memory pressure") {
		t.Errorf("an OOM against no limit reads as a limit to raise: %q", got)
	}
}

// TestAnExitOfOneIsSentToTheProcessOwnLog. A manager that loses its leader
// election exits 1, and the kubelet knows nothing about why.
func TestAnExitOfOneIsSentToTheProcessOwnLog(t *testing.T) {
	facts := PodFacts{RestartCount: 1, LastExitCode: 1, LastReason: "Error"}
	if got := facts.WhyItDied(); !strings.Contains(got, "own log") {
		t.Errorf("an exit 1 does not point at the process's own log: %q", got)
	}
}
