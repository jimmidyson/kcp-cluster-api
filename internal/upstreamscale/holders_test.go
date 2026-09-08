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
	"context"
	"strings"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func lease(name, holder string) *coordinationv1.Lease {
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: ptr(holder)},
	}
}

func etcdMemberPod(node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "etcd-" + node, Namespace: "kube-system",
			Labels: map[string]string{"component": "etcd"},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// TestPlacementNamesWhereTheSinglePointsOfFailureSit.
//
// The incident this is from: a control plane node died under a 2000-cluster
// fleet, and it was the node holding the raft leader, the controller
// manager's lease and the kube-vip VIP at once. Nothing in the report said
// so; it took three kubectl commands by hand afterwards. Where those three
// sit at each rung decides what one node's loss costs, so the rung records
// it.
func TestPlacementNamesWhereTheSinglePointsOfFailureSit(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		lease("kube-controller-manager", "cp-1_2f1c6a1e-9a5a-4f2b-9d1a-0b2f7c1e2d3f"),
		lease("kube-scheduler", "cp-0_7b0e3a2c-1c4d-4a1e-8f2b-3d4e5f6a7b8c"),
		lease("plndr-cp-lock", "cp-1"),
		etcdMemberPod("cp-0"), etcdMemberPod("cp-1"), etcdMemberPod("cp-2"),
	).Build()

	members := map[string]Etcd{
		"etcd-cp-0": {HasLeader: true},
		"etcd-cp-1": {HasLeader: true, IsLeader: true},
		"etcd-cp-2": {HasLeader: true},
	}
	got := ReadPlacement(context.Background(), cl, KubeadmStore(), members)

	if got.EtcdLeader != "etcd-cp-1" || got.EtcdLeaderNode != "cp-1" {
		t.Errorf("etcd leader = %q on %q, want etcd-cp-1 on cp-1", got.EtcdLeader, got.EtcdLeaderNode)
	}
	// The lease holder identity is <node>_<uuid> for the Kubernetes
	// components and the bare node for kube-vip; both come back as the node.
	for lease, want := range map[string]string{
		"kube-controller-manager": "cp-1", "kube-scheduler": "cp-0", "plndr-cp-lock": "cp-1",
	} {
		if got.Leases[lease] != want {
			t.Errorf("%s held by %q, want %q", lease, got.Leases[lease], want)
		}
	}

	line := got.Describe()
	for _, want := range []string{
		"etcd leader etcd-cp-1 on cp-1",
		"kube-controller-manager lease on cp-1",
		"kube-scheduler lease on cp-0",
		"kube-vip (plndr-cp-lock) on cp-1",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	if !strings.Contains(line, "the raft leader, the controller manager and the VIP share cp-1") {
		t.Errorf("the line does not say that one node holds three of them: %q", line)
	}
}

// TestPlacementSaysNothingItDoesNotKnow: no leases and no leader is an empty
// line rather than a line full of blanks, so a host without these leases —
// or a store that is not kubeadm's — adds no fact.
func TestPlacementSaysNothingItDoesNotKnow(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).Build()

	got := ReadPlacement(context.Background(), cl, KubeadmStore(), map[string]Etcd{"etcd-0": {HasLeader: true}})
	if got.Describe() != "" {
		t.Errorf("a placement with nothing in it described itself: %q", got.Describe())
	}

	// A leader whose pod is not in the store's namespace is still named,
	// without a node.
	got = ReadPlacement(context.Background(), cl, KubeadmStore(), map[string]Etcd{"etcd-0": {IsLeader: true}})
	if got.EtcdLeader != "etcd-0" || got.EtcdLeaderNode != "" {
		t.Errorf("leader = %q on %q", got.EtcdLeader, got.EtcdLeaderNode)
	}
	if !strings.Contains(got.Describe(), "etcd leader etcd-0") || strings.Contains(got.Describe(), " on ") {
		t.Errorf("a leader with no known node was described with one: %q", got.Describe())
	}
}

// TestAManagerOnAControlPlaneNodeIsNamed, because that is where the DevCluster
// provider was for a whole run and nothing said so: every sample carried the
// node name, and the reader had to know which names were control plane nodes.
func TestAManagerOnAControlPlaneNodeIsNamed(t *testing.T) {
	components := []deployedscale.ComponentSample{
		{Component: "capi-controller-manager", Pod: deployedscale.PodFacts{Node: "md-0-a"}},
		{Component: "capd-controller-manager", Pod: deployedscale.PodFacts{Node: "cp-1"}},
		{Component: "capi-kubeadm-bootstrap-controller-manager", Pod: deployedscale.PodFacts{Node: "md-0-b"}},
	}
	got := OnControlPlane(components, []string{"cp-0", "cp-1", "cp-2"})
	if !strings.Contains(got, "capd-controller-manager on cp-1") {
		t.Errorf("the provider on a control plane node is not named: %q", got)
	}
	if strings.Contains(got, "capi-controller-manager") {
		t.Errorf("a manager on a worker was named: %q", got)
	}
	if !strings.Contains(got, "page cache") {
		t.Errorf("the line does not say why it matters: %q", got)
	}
	if OnControlPlane(components[:1], []string{"cp-0"}) != "" {
		t.Error("managers all on workers produced a warning")
	}
}
