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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A 32 GiB node's allocatable and a 64 GiB node's, as measured.
var (
	gib32 = uint64(309) << 30 / 10
	gib64 = uint64(619) << 30 / 10
)

// TestTheModelReproducesTheMeasuredRuns: it has to agree with what was
// climbed, or it is not a model of it. 32 GiB held 1000 and failed 2000;
// 64 GiB held 3500 with the busiest node at 87%.
func TestTheModelReproducesTheMeasuredRuns(t *testing.T) {
	c := MeasuredCapacity()
	if got := c.MachinesForNode(gib32); got < 10000 || got >= 20000 {
		t.Errorf("a 32 GiB node is expected to hold %d Machines, want between 10000 (held) and 20000 (failed)", got)
	}
	// 64 GiB held 45000 Machines cleanly at 89% and reached 50000.
	if got := c.MachinesForNode(gib64); got < 45000 || got >= 55000 {
		t.Errorf("a 64 GiB node is expected to hold %d Machines, want the 45000 held cleanly at 89%% and under 55000", got)
	}
	// The busiest 64 GiB node read 54 GiB at 35000 Machines and 55 at 45000.
	if got := c.ControlPlaneNodeBytes(35000) / float64(1<<30); got < 50 || got > 58 {
		t.Errorf("the model puts a node at %.1f GiB for 35000 Machines, measured 54", got)
	}
	if got := c.ControlPlaneNodeBytes(45000) / float64(1<<30); got < 52 || got > 58 {
		t.Errorf("the model puts a node at %.1f GiB for 45000 Machines, measured 55", got)
	}
	// Past the last measured point the curve keeps climbing rather than
	// promising a plateau nobody has seen.
	if c.ControlPlaneNodeBytes(80000) <= c.ControlPlaneNodeBytes(50000) {
		t.Error("the curve stops climbing past the last measured point")
	}
	// The core manager's 8 GiB limit was at 93% at 3500 clusters and the
	// control plane manager's 6 GiB at 96%: both at the line, neither over.
	if got := c.ClustersForManager("core", 8<<30); got < 3000 || got > 4500 {
		t.Errorf("an 8 GiB core manager is expected to hold %d clusters, want about 3500", got)
	}
	if got := c.ClustersForManager("kubeadm-control-plane", 6<<30); got < 3000 || got > 5000 {
		t.Errorf("a 6 GiB control plane manager is expected to hold %d clusters, want about 3500 to 4500", got)
	}
	if got := c.ClustersForManager("something-else", 6<<30); got != -1 {
		t.Errorf("a manager the model does not know was judged: %d", got)
	}
}

// TestALadderPastTheClusterIsWarnedAboutAndOneWithinItIsNot, in the words an
// operator acts on: which node, which manager, and what the top rung wanted.
func TestALadderPastTheClusterIsWarnedAboutAndOneWithinItIsNot(t *testing.T) {
	c := MeasuredCapacity()
	nodes := []NodeMemory{{Name: "cp-a", Allocatable: gib64}, {Name: "cp-b", Allocatable: gib32}}
	managers := []ManagerLimit{{Name: "core", Limit: 8 << 30}, {Name: "kubeadm-control-plane", Limit: 6 << 30}}

	past := c.Expect(6000, 60000, nodes, managers)
	short := past.Short()
	if !strings.Contains(short, "cp-b") || !strings.Contains(short, "60000") {
		t.Errorf("the smallest node is not the one named as short: %s", short)
	}
	if strings.Contains(short, "cp-a") {
		t.Errorf("the larger node was named as short: %s", short)
	}
	if !strings.Contains(past.Describe(), "past that") {
		t.Errorf("the fact does not say the ladder is past the cluster: %s", past.Describe())
	}

	within := c.Expect(1000, 10000, []NodeMemory{{Name: "cp-a", Allocatable: gib64}}, managers)
	if got := within.Short(); got != "" {
		t.Errorf("a ladder within the cluster was warned about: %s", got)
	}
	if !strings.Contains(within.Describe(), "within that") {
		t.Errorf("the fact does not say the ladder is within the cluster: %s", within.Describe())
	}

	// A manager can be the short one on its own, with the node fine.
	manager := c.Expect(6000, 60000, []NodeMemory{{Name: "cp-a", Allocatable: 8 * gib64}}, managers)
	if got := manager.Short(); !strings.Contains(got, "core manager") || strings.Contains(got, "cp-a") {
		t.Errorf("a manager at the edge with a large node was not the one named: %s", got)
	}
}

// TestTheLimitJudgedIsTheDeployedOne, not the table's: a limit raised by flag
// is the limit the run is climbing under.
func TestTheLimitJudgedIsTheDeployedOne(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	core := Controllers()[0]
	raised := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: core.Deployment, Namespace: core.Namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "other"}, {
				Name: core.Container,
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				}},
			}},
		}}},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(raised).Build()

	limits, err := ManagerLimits(context.Background(), cl, []Controller{core})
	if err != nil {
		t.Fatalf("ManagerLimits: %v", err)
	}
	if len(limits) != 1 || limits[0].Limit != 16<<30 {
		t.Errorf("limits = %+v, want the deployed 16Gi", limits)
	}

	// A controller that is not deployed is an error, not a zero: a run with
	// no manager to read has a bigger problem than its capacity.
	if _, err := ManagerLimits(context.Background(), cl, Controllers()[:2]); err == nil {
		t.Error("a missing deployment was not reported")
	}
	var nodes []NodeMemory
	nodes, err = ReadNodeMemory(context.Background(), cl)
	if err != nil || len(nodes) != 0 {
		t.Errorf("ReadNodeMemory on a cluster with no control plane nodes = %v, %v", nodes, err)
	}
}
