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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func cluster(name string, controlPlaneReady bool) clusterv1.Cluster {
	c := clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "capi-scale-0000"}}
	status := metav1.ConditionFalse
	if controlPlaneReady {
		status = metav1.ConditionTrue
	}
	c.Status.Conditions = []metav1.Condition{
		{Type: clusterv1.ClusterControlPlaneAvailableCondition, Status: status},
	}
	return c
}

func machine(name string, ready bool) clusterv1.Machine {
	m := clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "capi-scale-0000"}}
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	m.Status.Conditions = []metav1.Condition{{Type: clusterv1.MachineReadyCondition, Status: status}}
	return m
}

// TestConvergenceIsBothHalves. The end state this run waits for is every
// control plane ready AND every Machine Ready — the same end state the kcp runs
// used, so the two are answering the same question. A count of one half would
// call a fleet converged while half of it was still coming up.
func TestConvergenceIsBothHalves(t *testing.T) {
	clusters := []clusterv1.Cluster{cluster("c0000", true), cluster("c0001", true)}
	machines := []clusterv1.Machine{machine("m0", true), machine("m1", true), machine("m2", true)}

	got := Converged(clusters, machines, 2, 3)
	if !got.Done {
		t.Fatalf("a fully ready fleet was not converged: %+v", got)
	}
	if got.ControlPlanesReady != 2 || got.MachinesReady != 3 {
		t.Errorf("counts = %+v", got)
	}

	// Every control plane up, one Machine short.
	partial := Converged(clusters, []clusterv1.Machine{machine("m0", true), machine("m1", false)}, 2, 3)
	if partial.Done {
		t.Error("a fleet with an unready Machine was called converged")
	}
	if !strings.Contains(partial.Describe(), "1 of 3 Machines") {
		t.Errorf("the progress line does not say what is outstanding: %q", partial.Describe())
	}

	// The objects have not all been created yet, which is different from
	// created and not ready: a count taken while the topology controller is
	// still stamping objects would otherwise read as a fleet going backwards.
	early := Converged(clusters[:1], machines[:1], 2, 3)
	if early.Done {
		t.Error("a fleet whose objects do not all exist yet was called converged")
	}
	if !strings.Contains(early.Describe(), "1 of 2 control planes") {
		t.Errorf("progress = %q", early.Describe())
	}
}

// TestReadinessThatGoesBackwardsIsNotAFleetStillArriving.
//
// The polls this is from, at a rung of 500:
//
//	291 ready, then 269, then 259, then 327
//
// Machines climbed throughout. Reporting the last count as what the rung was
// stuck at describes a fleet that never arrived, and this one did.
func TestReadinessThatGoesBackwardsIsNotAFleetStillArriving(t *testing.T) {
	var s Steadiness
	for _, ready := range []int{291, 269, 259, 327} {
		s.Observe(Convergence{ControlPlanesReady: ready, ControlPlanesWant: 500})
	}
	if !s.Flapping() {
		t.Fatalf("readiness that fell twice reads as steady: %+v", s)
	}
	if s.Drawdown() != 32 {
		t.Errorf("drawdown = %d, want the 291-to-259 fall a later peak must not erase", s.Drawdown())
	}
	got := s.Describe()
	if !strings.Contains(got, "291") || !strings.Contains(got, "259") || !strings.Contains(got, "327") {
		t.Errorf("the fall and the peak are not both in the line: %q", got)
	}
	if !strings.Contains(got, "failing to arrive") {
		t.Errorf("the line does not separate the two findings: %q", got)
	}
	if !strings.Contains(timedOutBecause(s), "did not fail to arrive") {
		t.Errorf("a timeout on a flapping fleet still blames reconciliation: %q", timedOutBecause(s))
	}
}

// TestAFleetStillClimbingIsNotCalledUnsteady, so the ordinary case — a rung
// filling up — keeps the sentence it always had.
func TestAFleetStillClimbingIsNotCalledUnsteady(t *testing.T) {
	var s Steadiness
	for _, ready := range []int{0, 120, 310, 480} {
		s.Observe(Convergence{ControlPlanesReady: ready, ControlPlanesWant: 500})
	}
	if s.Flapping() {
		t.Errorf("a fleet that only climbed was called unsteady: %+v", s)
	}
	if s.Describe() != "" {
		t.Errorf("a steady climb was given a caveat: %q", s.Describe())
	}
	if !strings.Contains(timedOutBecause(s), "reconciliation did not keep up") {
		t.Errorf("the ordinary timeout lost its wording: %q", timedOutBecause(s))
	}
}

// TestOneDipIsAnEventAndNotAPattern. A single cluster restarting, or one probe
// that missed, should not re-explain a whole rung.
func TestOneDipIsAnEventAndNotAPattern(t *testing.T) {
	var s Steadiness
	for _, ready := range []int{100, 200, 197, 300, 420} {
		s.Observe(Convergence{ControlPlanesReady: ready, ControlPlanesWant: 500})
	}
	if s.Flapping() {
		t.Errorf("one dip was reported as flapping: %+v", s)
	}
}

// TestTheTroughIsMeasuredAfterThePeak. A rung that climbs from nothing has its
// lowest count at the start, and reporting that as a fall would turn every
// ordinary climb into a collapse.
func TestTheTroughIsMeasuredAfterThePeak(t *testing.T) {
	var s Steadiness
	for _, ready := range []int{5, 400, 380, 360, 395} {
		s.Observe(Convergence{ControlPlanesReady: ready, ControlPlanesWant: 500})
	}
	if s.Peak != 400 {
		t.Errorf("peak = %d, want 400", s.Peak)
	}
	if s.DropFrom != 400 || s.DropTo != 360 {
		t.Errorf("fall = %d to %d, want the drop after the peak rather than the climb from 5",
			s.DropFrom, s.DropTo)
	}
}
