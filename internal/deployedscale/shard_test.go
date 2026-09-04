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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestTheShardIsAsManyProcessesAsTheStockControlPlane.
//
// The stock side is three kube-apiservers behind a VIP, active/active, each
// holding its own full watch cache. A single-replica shard against three API
// servers compares one process with three and gets the cost of a control plane
// wrong by about that factor — so the comparison gives kcp the same shape.
//
// Verified rather than predicted: test/integration/deployed's
// TestAThreeReplicaShardServesOneStore starts three of these over one etcd and
// checks that a workspace created through one is served by the others.
func TestTheShardIsAsManyProcessesAsTheStockControlPlane(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3
	d := o.KcpDeployment()

	if got := *d.Spec.Replicas; got != 3 {
		t.Errorf("replicas = %d, want the stock control plane's 3", got)
	}
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--enable-leader-election") {
		t.Errorf("three replicas without leader election is three copies of kcp's controllers "+
			"reconciling one store: %q", args)
	}
	// One per node, as the stock side's are. Three on one node is one machine's
	// memory and one machine's disk reported as a three-node control plane.
	affinity := d.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.PodAntiAffinity == nil ||
		len(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) == 0 {
		t.Error("nothing stops the three replicas landing on one node")
	}
}

// TestOneReplicaIsTheLocalShapeAndStaysIt.
//
// Every deployed run so far has been one replica, and the figures from them are
// the ones a new run is read against. Leader election on a single replica would
// add a lease and a failure mode to the shape those numbers came from, and
// required anti-affinity would make a single-node kind cluster unschedulable.
func TestOneReplicaIsTheLocalShapeAndStaysIt(t *testing.T) {
	o := testOptions()
	d := o.KcpDeployment()

	if got := *d.Spec.Replicas; got != 1 {
		t.Errorf("replicas = %d, want 1 by default", got)
	}
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if strings.Contains(args, "leader-election") {
		t.Errorf("a single replica was given leader election: %q", args)
	}
	if a := d.Spec.Template.Spec.Affinity; a != nil && a.PodAntiAffinity != nil &&
		len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
		t.Error("a single replica was given anti-affinity against itself, which no cluster can satisfy twice")
	}
}

// TestTheShardCanBePinnedToTheNodesTheComparisonGivesIt.
//
// R5: the control plane under test gets the same budget either way — the same
// three nodes, with the shard and its etcd on them and nothing else. A shard
// scheduled onto a worker node is measured sharing a machine with the fleet's
// own load.
func TestTheShardCanBePinnedToTheNodesTheComparisonGivesIt(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3
	o.KcpNodeSelector = map[string]string{"scale-role": "control-plane"}

	spec := o.KcpDeployment().Spec.Template.Spec
	if spec.NodeSelector["scale-role"] != "control-plane" {
		t.Errorf("the shard is not pinned: %v", spec.NodeSelector)
	}
}

// TestTheShardIsStillRestartedRatherThanRolled.
//
// A rolling update would run two generations of the shard at once, and the
// samples either side of it would be two different processes reported as one
// series. Nothing updates a shard mid-run, so this is about what happens when
// something does.
func TestTheShardIsStillRestartedRatherThanRolled(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3
	if got := o.KcpDeployment().Spec.Strategy.Type; got != "Recreate" {
		t.Errorf("strategy = %s, want Recreate", got)
	}
}

// TestEveryShardReplicaGetsItsOwnRootDirectory.
//
// kcp generates its PKI into the root directory unless it is given one, so the
// replicas must not share that volume — and an emptyDir per pod is what a
// Deployment gives them. The state that matters is in etcd.
func TestEveryShardReplicaGetsItsOwnRootDirectory(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3
	spec := o.KcpDeployment().Spec.Template.Spec

	for _, v := range spec.Volumes {
		if v.Name == "data" {
			if v.EmptyDir == nil {
				t.Errorf("the shard's data volume is %+v, want an emptyDir per replica", v.VolumeSource)
			}
			return
		}
	}
	t.Error("the shard has no data volume")
}

// TestTheShardServesItsOwnMetricsPort, because a control plane that cannot be
// scraped instance by instance is one arbitrary process per sample — which is
// what the stock side's figures were until it was fixed.
func TestTheShardIsScrapable(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3
	container := o.KcpDeployment().Spec.Template.Spec.Containers[0]

	var served bool
	for _, p := range container.Ports {
		if p.ContainerPort == KcpPort && p.Protocol == corev1.ProtocolTCP {
			served = true
		}
	}
	if !served {
		t.Errorf("the shard does not name the port its metrics are on: %+v", container.Ports)
	}
}

// TestSeveralReplicasWithoutAStoreIsRefused.
//
// kcp's default store is an etcd embedded in its own process. Three replicas
// of that are three servers with three stores behind one Service — a fleet
// created through one of them would be invisible through the other two, and
// the run would report a control plane that keeps losing a third of its
// objects rather than a configuration mistake.
func TestSeveralReplicasWithoutAStoreIsRefused(t *testing.T) {
	o := testOptions()
	o.ShardReplicas = 3

	err := o.validate()
	if err == nil {
		t.Fatal("three replicas over three embedded stores was accepted")
	}
	if !strings.Contains(err.Error(), "etcd") {
		t.Errorf("the error does not name what is missing: %v", err)
	}

	o.Etcd = EtcdOptions{Members: 3, QuotaBytes: 1}
	if err := o.validate(); err != nil {
		t.Errorf("three replicas over an external store was refused: %v", err)
	}
}

// TestTheManagersCanBeReadWithTheSameInstrumentAsTheStockOnes.
//
// The stock side's managers are sampled through pprof with gc=1, which forces a
// collection and so reports the retained set. This side's are scraped from
// /metrics, where the same field is a point on the collector's sawtooth. Those
// are different quantities, and subtracting one from the other is the mistake
// this whole comparison exists to stop — so the managers can be told to serve
// pprof, on the port the stock side's serve it on.
func TestTheManagersCanBeReadWithTheSameInstrumentAsTheStockOnes(t *testing.T) {
	o := testOptions()
	o.ProfilerPort = ProfilerPort

	for _, c := range o.components() {
		container := o.ManagerDeployment(c).Spec.Template.Spec.Containers[0]
		args := strings.Join(container.Args, " ")
		if !strings.Contains(args, fmt.Sprintf("--profiler-address=:%d", ProfilerPort)) {
			t.Errorf("%s cannot be read the way the stock side's managers are: %q", c.Name, args)
		}
		var named bool
		for _, p := range container.Ports {
			if p.ContainerPort == ProfilerPort {
				named = true
			}
		}
		if !named {
			t.Errorf("%s does not name the port it serves pprof on: %+v", c.Name, container.Ports)
		}
	}
}

// TestProfilingIsOffUnlessAsked, so that a run taken without it is the shape
// every recorded deployed run was taken in.
func TestProfilingIsOffUnlessAsked(t *testing.T) {
	o := testOptions()
	for _, c := range o.components() {
		args := strings.Join(o.ManagerDeployment(c).Spec.Template.Spec.Containers[0].Args, " ")
		if strings.Contains(args, "profiler") {
			t.Errorf("%s serves pprof without being asked: %q", c.Name, args)
		}
	}
}

// TestTheManagersCanBeKeptOffTheControlPlanesNodes.
//
// R5 gives the control plane under test its own nodes — three of them, one
// shard replica and one etcd member each, which is the shape kubeadm gives the
// stock side. That only holds if nothing else is scheduled there, and the four
// managers are the something else: they are the fleet's own load, and a
// manager sharing a node with the shard it is driving makes the shard's
// figures a measurement of both.
func TestTheManagersCanBeKeptOffTheControlPlanesNodes(t *testing.T) {
	o := testOptions()
	o.ManagerNodeSelector = map[string]string{"scale-role": "managers"}

	for _, c := range o.components() {
		spec := o.ManagerDeployment(c).Spec.Template.Spec
		if spec.NodeSelector["scale-role"] != "managers" {
			t.Errorf("%s is not pinned: %v", c.Name, spec.NodeSelector)
		}
	}
	// And the control plane keeps its own pool, which is a different one.
	o.KcpNodeSelector = map[string]string{"scale-role": "control-plane"}
	if got := o.KcpDeployment().Spec.Template.Spec.NodeSelector["scale-role"]; got != "control-plane" {
		t.Errorf("the shard followed the managers to %q", got)
	}
}
