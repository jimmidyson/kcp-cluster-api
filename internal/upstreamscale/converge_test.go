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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func cluster(name string, controlPlaneReady bool) clusterv1.Cluster {
	c := clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "capi-scale-0000"}}
	status := metav1.ConditionFalse
	ready := int32(0)
	if controlPlaneReady {
		status = metav1.ConditionTrue
		ready = 1
	}
	c.Status.Conditions = []metav1.Condition{
		{Type: clusterv1.ClusterControlPlaneAvailableCondition, Status: status},
	}
	c.Status.ControlPlane = &clusterv1.ClusterControlPlaneStatus{
		DesiredReplicas: ptr[int32](1), ReadyReplicas: ptr(ready),
	}
	return c
}

// scalingUp is a Cluster whose control plane is Available on the replicas it
// has and is still short of the replicas it was asked for.
func scalingUp(name string, ready, desired int32) clusterv1.Cluster {
	c := cluster(name, true)
	c.Status.ControlPlane.DesiredReplicas = ptr(desired)
	c.Status.ControlPlane.ReadyReplicas = ptr(ready)
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

// TestAControlPlaneStillScalingUpIsNotReady.
//
// The flapping this is from, at a rung of 2000, twelve samples of a Cluster's
// control plane in a row:
//
//	2/1 Available=NotAvailable,EtcdClusterHealthy=NotHealthy ... Etcd member
//	does not have a corresponding Machine
//
// KubeadmControlPlane marks a one-member control plane Available the moment
// its first member answers, then takes it back while the second member joins
// and the etcd cluster is two members with one Machine. Counting Available
// alone counted every cluster at 1 of 3, lost it at 2 of 3 and counted it
// again at 3 of 3 — which is not readiness that would not hold, it is a
// control plane that had not finished being built. Full replicas, or it is
// still arriving.
func TestAControlPlaneStillScalingUpIsNotReady(t *testing.T) {
	clusters := []clusterv1.Cluster{scalingUp("c0000", 1, 3), scalingUp("c0001", 3, 3)}
	got := Converged(clusters, nil, 2, 0)
	if got.ControlPlanesReady != 1 {
		t.Errorf("%d control planes ready, want only the one at full strength", got.ControlPlanesReady)
	}
	if got.Done {
		t.Error("a fleet with a control plane still scaling up was called converged")
	}
	// A control plane that has every replica and has lost Available is not
	// ready either: the count needs both.
	lost := scalingUp("c0002", 3, 3)
	lost.Status.Conditions[0].Status = metav1.ConditionFalse
	if Converged([]clusterv1.Cluster{lost}, nil, 1, 0).ControlPlanesReady != 0 {
		t.Error("a full control plane that is not Available was counted ready")
	}
}

// TestAControlPlaneThatReportsNoReplicasIsJudgedOnAvailabilityAlone. A
// control plane provider without a replica count has nothing to be at full
// strength against, and refusing to count it would wait for ever.
func TestAControlPlaneThatReportsNoReplicasIsJudgedOnAvailabilityAlone(t *testing.T) {
	c := cluster("c0000", true)
	c.Status.ControlPlane = nil
	if Converged([]clusterv1.Cluster{c}, nil, 1, 0).ControlPlanesReady != 1 {
		t.Error("an Available control plane with no replica counts was not counted")
	}
	c.Status.ControlPlane = &clusterv1.ClusterControlPlaneStatus{}
	if Converged([]clusterv1.Cluster{c}, nil, 1, 0).ControlPlanesReady != 1 {
		t.Error("an Available control plane with empty replica counts was not counted")
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
	// A control plane is counted only at full strength, so a count that
	// falls is one that had arrived and lost Available afterwards — which is
	// KubeadmControlPlane's judgement of the cluster, not a probe timing out.
	if strings.Contains(got, "timing out") || !strings.Contains(got, "Available") {
		t.Errorf("the line still explains a fall as probes timing out: %q", got)
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

// TestTheStragglersAreNamed, with what Cluster API says about each.
//
// The rung this is from: 1999 of 2000 control planes and 19999 of 20000
// Machines, for thirty minutes, and the report could not say which cluster or
// why. The fleet was torn down at the end of the run, so the answer went with
// it. The count is the verdict; the names are the evidence.
func TestTheStragglersAreNamed(t *testing.T) {
	stuck := scalingUp("c1445", 2, 3)
	stuck.Namespace = "capi-scale-0144"
	stuck.Status.Conditions[0].Status = metav1.ConditionFalse
	stuck.Status.Conditions[0].Reason = "NotAvailable"
	stuck.Status.Conditions[0].Message = "Etcd member 1 does not have a corresponding Machine"

	m := machine("c1445-cp-x7k2p", false)
	m.Namespace = "capi-scale-0144"
	m.Status.Phase = "Provisioning"
	m.Status.Conditions[0].Reason = "BootstrapDataNotReady"
	m.Status.Conditions[0].Message = "waiting for bootstrap data"

	got := Converged([]clusterv1.Cluster{cluster("c0000", true), stuck},
		[]clusterv1.Machine{machine("m0", true), m}, 2, 2)
	if got.Done {
		t.Fatal("a fleet one short was called converged")
	}
	line := got.DescribeStragglers()
	for _, want := range []string{
		"capi-scale-0144/c1445: control plane 2 of 3 ready, Available=False (NotAvailable: Etcd member 1 does not have a corresponding Machine)",
		"capi-scale-0144/c1445-cp-x7k2p: Provisioning, Ready=False (BootstrapDataNotReady: waiting for bootstrap data)",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	if Converged([]clusterv1.Cluster{cluster("c0000", true)}, nil, 1, 0).DescribeStragglers() != "" {
		t.Error("a converged fleet has stragglers")
	}
}

// TestOnlyAFewStragglersAreNamed, because a fleet that is half arrived has a
// thousand of them and a line with a thousand names on it is not a line.
func TestOnlyAFewStragglersAreNamed(t *testing.T) {
	var clusters []clusterv1.Cluster
	for i := 0; i < 20; i++ {
		clusters = append(clusters, cluster(fmt.Sprintf("c%04d", i), false))
	}
	got := Converged(clusters, nil, 20, 0)
	if len(got.Stragglers) != maxStragglers {
		t.Errorf("%d stragglers named, want at most %d", len(got.Stragglers), maxStragglers)
	}
	if !strings.Contains(got.DescribeStragglers(), "and 15 more") {
		t.Errorf("the line does not say how many were left out: %q", got.DescribeStragglers())
	}
}

// TestAFleetThatStoppedOneShortIsStuckNotSlow.
//
// "Reconciliation did not keep up" was the sentence for 1999 of 2000 sitting
// still for thirty minutes with every component healthy. Reconciliation kept
// up for 1999 clusters; one object stopped. Those are different findings with
// different next steps, and the count alone tells them apart once it has held
// still long enough.
func TestAFleetThatStoppedOneShortIsStuckNotSlow(t *testing.T) {
	var s Steadiness
	for range stuckPolls {
		s.Observe(Convergence{ControlPlanesReady: 1999, ControlPlanesWant: 2000,
			MachinesReady: 19999, MachinesWant: 20000})
	}
	if !s.Stuck() {
		t.Fatalf("a fleet one short and motionless for %d polls is not stuck: %+v", stuckPolls, s)
	}
	if s.Flapping() {
		t.Error("a motionless fleet was called flapping")
	}
	got := timedOutBecause(s)
	if !strings.Contains(got, "stuck") || strings.Contains(got, "did not keep up") {
		t.Errorf("a stuck fleet is still described as slow: %q", got)
	}
}

// TestAFleetStillMovingIsNotStuck, and neither is one that stopped far from
// the target: the first is arriving and the second did not keep up.
func TestAFleetStillMovingIsNotStuck(t *testing.T) {
	var s Steadiness
	for i := range stuckPolls {
		s.Observe(Convergence{ControlPlanesReady: 1990 + i, ControlPlanesWant: 2000,
			MachinesReady: 19990 + i, MachinesWant: 20000})
	}
	if s.Stuck() {
		t.Error("a fleet still climbing was called stuck")
	}

	var far Steadiness
	for range stuckPolls {
		far.Observe(Convergence{ControlPlanesReady: 1000, ControlPlanesWant: 2000,
			MachinesReady: 10000, MachinesWant: 20000})
	}
	if far.Stuck() {
		t.Error("a fleet stopped halfway was called stuck rather than slow")
	}
	if !strings.Contains(timedOutBecause(far), "did not keep up") {
		t.Errorf("the ordinary timeout lost its wording: %q", timedOutBecause(far))
	}
}
