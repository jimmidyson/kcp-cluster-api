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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Convergence is how far a rung has got.
type Convergence struct {
	ControlPlanesReady int  `json:"controlPlanesReady"`
	ControlPlanesWant  int  `json:"controlPlanesWant"`
	MachinesReady      int  `json:"machinesReady"`
	MachinesWant       int  `json:"machinesWant"`
	Done               bool `json:"done"`
}

// Converged counts a rung against the end state the run waits for: every
// control plane ready and every Machine Ready.
//
// # Why both halves
//
// This is the same end state the kcp runs measured, deliberately, so that the
// two instruments are answering the same question — and it is the end state
// that costs, because a ready cluster is one the core manager holds a live
// ClusterCache for, which an engagement-only run never opens.
//
// Counting only control planes would call a fleet converged with half its
// Machines still coming up. Counting only Machines would call it converged
// before a control plane had a chance to fail. Both, or the number means less
// than it appears to.
func Converged(clusters []clusterv1.Cluster, machines []clusterv1.Machine, wantClusters, wantMachines int) Convergence {
	out := Convergence{ControlPlanesWant: wantClusters, MachinesWant: wantMachines}
	for i := range clusters {
		if conditionTrue(clusters[i].Status.Conditions, clusterv1.ClusterControlPlaneAvailableCondition) {
			out.ControlPlanesReady++
		}
	}
	for i := range machines {
		if conditionTrue(machines[i].Status.Conditions, clusterv1.MachineReadyCondition) {
			out.MachinesReady++
		}
	}
	// The counts are against what was asked for rather than against what
	// exists. A fleet whose objects the topology controller has not finished
	// stamping has fewer Clusters than the rung asked for, and comparing ready
	// against existing would call that converged.
	out.Done = out.ControlPlanesReady >= wantClusters && out.MachinesReady >= wantMachines
	return out
}

// Describe is the progress line a waiting run logs, and the one a timeout
// reports as what it was still waiting for.
func (c Convergence) Describe() string {
	return fmt.Sprintf("%d of %d control planes ready, %d of %d Machines ready",
		c.ControlPlanesReady, c.ControlPlanesWant, c.MachinesReady, c.MachinesWant)
}

func conditionTrue(conditions []metav1.Condition, name string) bool {
	for _, c := range conditions {
		if c.Type == name {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

// Steadiness watches a rung's readiness across every poll, so that a timeout
// can say which of two very different things happened.
//
// # The observation this is for
//
// A rung climbing to 500 clusters logged this:
//
//	291 of 500 control planes ready, 1458 of 5000 Machines ready
//	269 of 500 control planes ready, 1588 of 5000 Machines ready
//	259 of 500 control planes ready, 1828 of 5000 Machines ready
//	327 of 500 control planes ready, 2189 of 5000 Machines ready
//
// The Machines climb. The control planes go backwards and forwards. Those are
// different failures wearing one number, and the run kept only the last poll,
// so a rung that ran out of time reported the final count as though the fleet
// had been stuck there.
//
// A control plane's availability is not derived from objects the way a
// Machine's readiness is: Cluster API probes each workload cluster through its
// ClusterCache and marks the control plane unavailable when the probe fails.
// So a count that oscillates says the probes are failing intermittently, not
// that the fleet is failing to arrive.
//
// # Why the difference decides what the rung means
//
// The rung waits for every control plane to be ready at one instant. If a
// tenth of them are flapping at any moment, that instant never comes, however
// long it waits and however complete the fleet is. A ceiling recorded from it
// is a ceiling on simultaneity rather than on capacity, and the two are not
// interchangeable — the second is a fact about how many clusters a management
// cluster can hold, and the first is a fact about how well it can prove it.
//
// Flapping is also its own load: each flip writes a condition to the
// management cluster's store, so a fleet that cannot hold its readiness is
// generating the writes that stop it holding its readiness.
type Steadiness struct {
	Polls int `json:"polls"`
	// Peak is the highest count of ready control planes seen at any poll.
	Peak int `json:"peak"`
	// DropFrom and DropTo are the worst fall: the running peak readiness had
	// reached, and the lowest it went before recovering.
	//
	// The worst fall rather than the last one, and measured against the peak
	// at the time rather than the peak overall. A rung that climbs to 291,
	// falls to 259 and then reaches 327 has a fall of 32 in the middle of it;
	// resetting the low each time a new peak arrives would report that rung as
	// having never fallen at all, which is the poll sequence this was built
	// from.
	DropFrom int `json:"dropFrom"`
	DropTo   int `json:"dropTo"`
	// Regressions is how many polls counted fewer ready control planes than
	// the poll before.
	Regressions int `json:"regressions"`

	seen    bool
	last    int
	running int
}

// Observe records one poll.
func (s *Steadiness) Observe(c Convergence) {
	s.Polls++
	ready := c.ControlPlanesReady

	if s.seen && ready < s.last {
		s.Regressions++
	}
	if ready > s.running || !s.seen {
		s.running = ready
	}
	if ready > s.Peak || !s.seen {
		s.Peak = ready
	}
	if fall := s.running - ready; fall > s.Drawdown() {
		s.DropFrom, s.DropTo = s.running, ready
	}
	s.seen, s.last = true, ready
}

// Drawdown is how far readiness fell at its worst.
func (s Steadiness) Drawdown() int { return s.DropFrom - s.DropTo }

// Flapping reports whether readiness went backwards repeatedly.
//
// Twice rather than once, because a single dip is an event — a cluster
// genuinely restarting, a probe that missed — and a rung should not be
// re-explained on the strength of one. Repeated dips are a pattern, and the
// pattern is the finding.
func (s Steadiness) Flapping() bool { return s.Regressions > 1 && s.Drawdown() > 0 }

// Describe says what the readiness did, or "" when it only ever climbed.
func (s Steadiness) Describe() string {
	if !s.Flapping() {
		return ""
	}
	return fmt.Sprintf("readiness did not hold: ready control planes fell from %d to %d at worst "+
		"and peaked at %d, going backwards on %d of %d polls — the fleet is not failing to arrive, "+
		"it is failing to be ready all at once, which is what Cluster API's per-cluster health "+
		"probes report when they start timing out",
		s.DropFrom, s.DropTo, s.Peak, s.Regressions, s.Polls)
}
