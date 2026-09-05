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
	"fmt"

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

// WhyItDied describes a container's last termination in the terms the next
// action differs on.
//
// # Why an exit code is not enough on its own
//
// A stock run reached its ceiling with a kube-apiserver whose status read
// `Exit Code: 137, Reason: Error`. 137 is SIGKILL, and reading it as "the
// kernel killed it for memory" is the obvious mistake and the wrong one: the
// kubelet records ReasonOOMKilled when the kernel does that, and kubeadm gives
// kube-apiserver no memory limit at all, so a cgroup OOM was not available to
// it. What actually happened was a graceful shutdown that did not finish
// inside the termination grace period — the kubelet's own SIGTERM, then its
// SIGKILL, after /livez went unanswered for long enough to fail the liveness
// probe.
//
// Those two readings send an operator to opposite places: one says buy memory,
// the other says the process stopped answering under load, which on a scale run
// is the finding rather than an obstacle to it. The facts that separate them
// are already recorded here, so the line says which it was rather than leaving
// "(Error)" for a reader to go and work out by hand — which is what it cost the
// first time.
func (f PodFacts) WhyItDied() string {
	against := "no memory limit is set on it"
	if f.MemoryLimitBytes > 0 {
		against = "its memory limit is " + humanBytes(uint64(f.MemoryLimitBytes))
	}

	switch {
	case f.OOMKilled && f.MemoryLimitBytes > 0:
		return "OOM killed: it exceeded its memory limit of " +
			humanBytes(uint64(f.MemoryLimitBytes))
	case f.OOMKilled:
		// No limit to have exceeded, so the kernel was choosing between
		// this container and the node's other work.
		return "OOM killed with no memory limit set on it, so this was node memory pressure " +
			"rather than a limit to raise"
	case f.LastExitCode == exitSIGKILL:
		return "killed with SIGKILL (137) but not flagged OOMKilled, and " + against +
			": the kubelet killed it rather than the kernel, which is what a graceful " +
			"shutdown that overran its termination grace period looks like — usually a " +
			"liveness probe that stopped being answered under load, not memory"
	case f.LastExitCode == exitSIGTERM:
		return "terminated on SIGTERM (143) and did not come back on its own, so something " +
			"outside the process asked it to stop"
	case f.LastExitCode == 1:
		return "exited 1 of its own accord, so the reason is in its own log rather than in " +
			"the kubelet's — a manager that loses its leader election exits this way"
	case f.LastReason != "":
		return fmt.Sprintf("terminated: %s (exit %d), and %s",
			f.LastReason, f.LastExitCode, against)
	default:
		return fmt.Sprintf("restarted with no termination recorded against it, and %s", against)
	}
}

// The two signal exits a container is killed with. 128+n is the shell's
// convention for "died on signal n" and the kubelet reports the same.
const (
	exitSIGTERM = 143
	exitSIGKILL = 137
)
