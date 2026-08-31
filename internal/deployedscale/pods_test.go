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
