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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// maxStragglers bounds how many unready objects a poll names. A fleet half
// arrived has thousands, and the names matter only once there are few.
const maxStragglers = 5

// Convergence is how far a rung has got.
type Convergence struct {
	ControlPlanesReady int  `json:"controlPlanesReady"`
	ControlPlanesWant  int  `json:"controlPlanesWant"`
	MachinesReady      int  `json:"machinesReady"`
	MachinesWant       int  `json:"machinesWant"`
	Done               bool `json:"done"`

	// Stragglers names the first few Clusters and Machines that are not
	// ready, each with what Cluster API says about it. The count is the
	// verdict on a rung; these are the evidence, and the one time a rung
	// stopped at 1999 of 2000 the fleet was torn down before anybody could
	// ask which one. See DescribeStragglers.
	Stragglers []string `json:"stragglers,omitempty"`
	// MoreStragglers is how many unready objects there were beyond the ones
	// named.
	MoreStragglers int `json:"moreStragglers,omitempty"`
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
//
// # Why a control plane is ready only at full strength
//
// Available is not enough on its own. KubeadmControlPlane marks a control
// plane Available the moment one member answers, and takes it back while the
// second joins: for the length of that join the etcd cluster is two members
// with one Machine, and KCP says so —
//
//	2/1 Available=NotAvailable,EtcdClusterHealthy=NotHealthy ... Etcd member
//	does not have a corresponding Machine
//
// So a count of Available control planes rose at 1 of 3, fell at 2 of 3 and
// rose again at 3 of 3 for every cluster in a rung, and the rung read as
// readiness that would not hold when it was control planes that had not
// finished being built. A control plane counts when it is Available and every
// replica it was asked for is ready. See controlPlaneReady.
func Converged(clusters []clusterv1.Cluster, machines []clusterv1.Machine, wantClusters, wantMachines int) Convergence {
	out := Convergence{ControlPlanesWant: wantClusters, MachinesWant: wantMachines}
	for i := range clusters {
		if controlPlaneReady(&clusters[i]) {
			out.ControlPlanesReady++
		} else if len(out.Stragglers) < maxStragglers {
			out.Stragglers = append(out.Stragglers, describeUnreadyCluster(&clusters[i]))
		}
	}
	// The Machines after the Clusters, so that a line with room for five
	// names leads with the cluster and follows with the machine in it.
	unreadyMachines := 0
	for i := range machines {
		if conditionTrue(machines[i].Status.Conditions, clusterv1.MachineReadyCondition) {
			out.MachinesReady++
			continue
		}
		unreadyMachines++
		if len(out.Stragglers) < maxStragglers {
			out.Stragglers = append(out.Stragglers, describeUnreadyMachine(&machines[i]))
		}
	}
	out.MoreStragglers = (len(clusters) - out.ControlPlanesReady) + unreadyMachines - len(out.Stragglers)
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

// DescribeStragglers names what is not ready, or "" when everything is.
func (c Convergence) DescribeStragglers() string {
	if len(c.Stragglers) == 0 {
		return ""
	}
	line := "still not ready: " + strings.Join(c.Stragglers, "; ")
	if c.MoreStragglers > 0 {
		line += fmt.Sprintf("; and %d more", c.MoreStragglers)
	}
	return line
}

func describeUnreadyCluster(c *clusterv1.Cluster) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s: control plane", c.Namespace, c.Name)
	if cp := c.Status.ControlPlane; cp != nil && cp.DesiredReplicas != nil {
		ready := int32(0)
		if cp.ReadyReplicas != nil {
			ready = *cp.ReadyReplicas
		}
		fmt.Fprintf(&b, " %d of %d ready,", ready, *cp.DesiredReplicas)
	}
	b.WriteString(" " + describeCondition(c.Status.Conditions, clusterv1.ClusterControlPlaneAvailableCondition, "Available"))
	return b.String()
}

func describeUnreadyMachine(m *clusterv1.Machine) string {
	phase := m.Status.Phase
	if phase == "" {
		phase = "no phase"
	}
	return fmt.Sprintf("%s/%s: %s, %s", m.Namespace, m.Name, phase,
		describeCondition(m.Status.Conditions, clusterv1.MachineReadyCondition, "Ready"))
}

// describeCondition is Cluster API's own account of why, in the words it
// wrote: "Available=False (NotAvailable: Etcd member 1 does not have a
// corresponding Machine)". label is the short name a reader knows the
// condition by, since the type is ControlPlaneAvailable on a Cluster.
func describeCondition(conditions []metav1.Condition, name, label string) string {
	for _, c := range conditions {
		if c.Type != name {
			continue
		}
		out := fmt.Sprintf("%s=%s", label, c.Status)
		if c.Reason != "" || c.Message != "" {
			out += fmt.Sprintf(" (%s: %s)", c.Reason, c.Message)
		}
		return out
	}
	return label + " not reported"
}

// controlPlaneReady is Available with every desired replica ready.
//
// A control plane that reports no replica counts — a provider without the
// notion — is judged on Available alone, because there is nothing for it to be
// at full strength against and waiting for a number it will never publish
// would wait for ever.
func controlPlaneReady(c *clusterv1.Cluster) bool {
	if !conditionTrue(c.Status.Conditions, clusterv1.ClusterControlPlaneAvailableCondition) {
		return false
	}
	cp := c.Status.ControlPlane
	if cp == nil || cp.DesiredReplicas == nil {
		return true
	}
	ready := int32(0)
	if cp.ReadyReplicas != nil {
		ready = *cp.ReadyReplicas
	}
	return ready >= *cp.DesiredReplicas
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
// # What a fall means, and what it turned out to mean
//
// The first reading of that sequence was that Cluster API's per-cluster health
// probes were timing out, and it was wrong. Sampled twelve times in a row, the
// control planes going backwards were ones KubeadmControlPlane had marked
// Available at one member and unavailable again while the second joined —
// control planes still being built, counted too early. Converged now counts a
// control plane only once every replica it asked for is ready, so that fall no
// longer registers here at all.
//
// What can still register is a control plane that had every replica ready and
// then lost Available: KubeadmControlPlane withdraws it when a cluster's etcd
// or a control plane component stops answering it, or while a machine is
// remediated. That is Cluster API's judgement of the workload cluster rather
// than a fleet still arriving, and the two are still different findings.
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
	// Motionless is how many polls in a row have counted exactly what the
	// poll before did, control planes and Machines both. See Stuck.
	Motionless int `json:"motionless"`

	seen    bool
	last    int
	running int

	lastMachines int
	lastWant     Convergence
}

// stuckPolls is how long a fleet has to sit still, within stuckWithin of its
// target, before a timeout calls it stuck rather than slow.
//
// Eight polls is two minutes at the default interval: long enough that a
// Machine mid-provisioning has had every chance to move, short enough that a
// person reading the log can go and look at the straggler well before the
// step timeout takes the fleet away.
const (
	stuckPolls  = 8
	stuckWithin = 0.995
)

// Observe records one poll.
func (s *Steadiness) Observe(c Convergence) {
	s.Polls++
	ready := c.ControlPlanesReady

	if s.seen && ready == s.last && c.MachinesReady == s.lastMachines {
		s.Motionless++
	} else {
		s.Motionless = 0
	}
	s.lastMachines = c.MachinesReady
	s.lastWant = c

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

// Stuck reports whether the fleet all but arrived and then stopped moving.
//
// # The rung this is for
//
// 1999 of 2000 control planes and 19999 of 20000 Machines, motionless for the
// last thirty minutes of a forty-five minute wait, with every component
// healthy and the store quiet — and the verdict was "reconciliation did not
// keep up". Reconciliation kept up for 1999 clusters. One object stopped, which
// is a different finding with a different next step: not more capacity but
// one Cluster's conditions, and a MachineHealthCheck that would have
// remediated it on a production cluster.
//
// Within a fraction of a percent of the target, or one object on a rung too
// small for a fraction to mean anything, because a fleet that stopped halfway
// did not keep up, whatever it has been doing since; and motionless for
// several polls, because a fleet one short for one poll is a fleet about to
// arrive.
func (s Steadiness) Stuck() bool {
	if s.Motionless+1 < stuckPolls {
		return false
	}
	want := s.lastWant
	if want.ControlPlanesWant == 0 || want.MachinesWant == 0 {
		return false
	}
	return want.ControlPlanesWant-want.ControlPlanesReady <= stuckAllowance(want.ControlPlanesWant) &&
		want.MachinesWant-want.MachinesReady <= stuckAllowance(want.MachinesWant)
}

// stuckAllowance is how many objects may be missing from a fleet that is
// still called all but arrived: a fraction of it, and never fewer than one.
func stuckAllowance(want int) int {
	return max(1, int(float64(want)*(1-stuckWithin)))
}

// Describe says what the readiness did, or "" when it only ever climbed.
func (s Steadiness) Describe() string {
	if !s.Flapping() {
		return ""
	}
	return fmt.Sprintf("readiness did not hold: ready control planes fell from %d to %d at worst "+
		"and peaked at %d, going backwards on %d of %d polls — the fleet is not failing to arrive, "+
		"it is failing to stay ready: a control plane is counted only once every replica is "+
		"ready and Available, so each fall is one that had arrived and then lost Available, "+
		"which KubeadmControlPlane withdraws when a cluster's etcd or control plane stops "+
		"answering it or while it remediates a machine",
		s.DropFrom, s.DropTo, s.Peak, s.Regressions, s.Polls)
}
