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
	corev1 "k8s.io/api/core/v1"
)

// ReasonOOMKilled is what the kubelet records when the kernel killed a
// container for exceeding its memory limit.
const ReasonOOMKilled = "OOMKilled"

// PodFacts is what the cluster knows about one component that its own process
// cannot know about itself.
//
// The two that matter are the node and the OOMKill. The node is what makes a
// figure attributable to a spread deployment rather than a co-located one, and
// an OOMKill is the capacity finding: a process cannot report the moment it
// was killed, so a measurement that only scraped processes would record the
// fleet getting cheaper as containers died.
type PodFacts struct {
	Name  string `json:"name"`
	Node  string `json:"node"`
	PodIP string `json:"podIP"`
	Ready bool   `json:"ready"`

	// RestartCount and OOMKilled describe what has already happened to this
	// container. A restart resets every process metric to a fresh process's
	// values, so a sample taken after one is not comparable with a sample
	// taken before it, whatever the numbers look like.
	RestartCount int32  `json:"restartCount"`
	OOMKilled    bool   `json:"oomKilled"`
	LastExitCode int32  `json:"lastExitCode,omitempty"`
	LastReason   string `json:"lastReason,omitempty"`

	// MemoryLimitBytes is what the OOMKill would be against. Recorded with
	// every sample because a figure read against no limit cannot be turned
	// into a sizing decision.
	MemoryLimitBytes int64 `json:"memoryLimitBytes"`
}

// PodFactsFrom reads one container's facts out of a pod.
//
// The container is named rather than assumed to be the only one: a pod that
// gained a sidecar would otherwise silently start reporting the sidecar's
// restarts as the manager's.
func PodFactsFrom(pod *corev1.Pod, container string) PodFacts {
	facts := PodFacts{
		Name:  pod.Name,
		Node:  pod.Spec.NodeName,
		PodIP: pod.Status.PodIP,
	}

	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		if c.Name != container {
			continue
		}
		if limit := c.Resources.Limits.Memory(); limit != nil {
			facts.MemoryLimitBytes = limit.Value()
		}
	}

	for i := range pod.Status.ContainerStatuses {
		s := &pod.Status.ContainerStatuses[i]
		if s.Name != container {
			continue
		}
		facts.Ready = s.Ready
		facts.RestartCount = s.RestartCount
		if term := s.LastTerminationState.Terminated; term != nil {
			facts.LastReason = term.Reason
			facts.LastExitCode = term.ExitCode
			facts.OOMKilled = term.Reason == ReasonOOMKilled
		}
	}

	return facts
}

// Comparable reports whether a sample taken from this container can be read
// alongside earlier ones.
//
// A restart is disqualifying rather than noteworthy. Every process metric
// resets when the process does, so a container that has restarted reports a
// small heap and a low goroutine count that look like a cheaper fleet.
func (f PodFacts) Comparable() bool { return f.RestartCount == 0 }
