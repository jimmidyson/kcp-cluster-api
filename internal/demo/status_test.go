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

package demo

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// devClusterFor stands in for the infrastructure cluster the topology
// controller stamps from the class, which is what the status table reads. Its
// contents do not matter here; that it exists, and is named after its Cluster,
// does.
func devClusterFor(cluster string) *infrav1.DevCluster {
	return &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{Name: InfraClusterName(cluster), Namespace: Namespace},
	}
}

func provisionedCluster() *clusterv1.Cluster {
	cluster := NewCluster(ClusterName(0), 1, 1, DefaultKubernetesVersion)
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	return cluster
}

func TestSummariseProvisioned(t *testing.T) {
	got := Summarise("root:demo-1", "abcdef", provisionedCluster(), devClusterFor(ClusterName(0)))
	if !got.Provisioned {
		t.Errorf("Summarise(...).Provisioned = false, want true")
	}
	if got.Workspace != "root:demo-1" || got.LogicalCluster != "abcdef" {
		t.Errorf("Summarise(...) lost its workspace identity: %+v", got)
	}
}

// What a cluster is waiting on is the whole value of the table while a demo
// is still running, so the condition's reason has to reach the row.
func TestSummariseReportsWhatItIsWaitingOn(t *testing.T) {
	cluster := NewCluster(ClusterName(0), 1, 1, DefaultKubernetesVersion)
	cluster.Status.Conditions = []metav1.Condition{{
		Type:    string(clusterv1.ClusterInfrastructureReadyCondition),
		Status:  metav1.ConditionFalse,
		Reason:  "NotReady",
		Message: "waiting for the load balancer",
	}}

	got := Summarise("root:demo-1", "abcdef", cluster, devClusterFor(ClusterName(0)))
	if got.Provisioned {
		t.Error("Summarise(...).Provisioned = true for a cluster whose infrastructure is not ready")
	}
	if !strings.Contains(got.Detail, "NotReady") || !strings.Contains(got.Detail, "load balancer") {
		t.Errorf("Summarise(...).Detail = %q, want the condition's reason and message", got.Detail)
	}
}

func TestSummariseMissingDevCluster(t *testing.T) {
	got := Summarise("root:demo-1", "abcdef", NewCluster(ClusterName(0), 1, 1, DefaultKubernetesVersion), nil)
	if got.Provisioned {
		t.Error("Summarise(...).Provisioned = true with no DevCluster")
	}
	if got.Detail == "" {
		t.Error("Summarise(...).Detail is empty, so the table would say nothing about a cluster that has not started")
	}
}

// A run that created nothing has provisioned nothing. Reporting it as done is
// the one failure a demo must not have, because it looks exactly like success.
func TestAllProvisionedEmptyIsNotDone(t *testing.T) {
	if AllProvisioned(nil) {
		t.Error("AllProvisioned(nil) = true, want false")
	}
}

func TestAllProvisioned(t *testing.T) {
	all := []ClusterStatus{{Provisioned: true}, {Provisioned: true}}
	if !AllProvisioned(all) {
		t.Error("AllProvisioned(all provisioned) = false")
	}
	some := []ClusterStatus{{Provisioned: true}, {Provisioned: false}}
	if AllProvisioned(some) {
		t.Error("AllProvisioned(one outstanding) = true")
	}
}

func TestRenderTable(t *testing.T) {
	var sb strings.Builder
	statuses := []ClusterStatus{
		{Workspace: "root:demo-1", LogicalCluster: "aaaa", Cluster: "demo-00", Provisioned: true, Detail: "infrastructure provisioned"},
		{Workspace: "root:demo-2", LogicalCluster: "bbbb", Cluster: "demo-00", Detail: "waiting for the Cluster reconciler"},
	}
	if err := RenderTable(&sb, statuses); err != nil {
		t.Fatalf("RenderTable: %v", err)
	}

	out := sb.String()
	for _, want := range []string{"WORKSPACE", "root:demo-1", "root:demo-2", "aaaa", "bbbb", "yes", "no"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderTable output missing %q:\n%s", want, out)
		}
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n"); lines != 2 {
		t.Errorf("RenderTable wrote %d rows after the header, want 2:\n%s", lines, out)
	}
}

// A cluster is ready when Cluster API says it is Available, and the demo's
// whole done-condition rests on reading that one condition correctly.
func TestSummariseReady(t *testing.T) {
	cluster := provisionedCluster()
	cluster.Status.Conditions = []metav1.Condition{{
		Type:   clusterv1.ClusterAvailableCondition,
		Status: metav1.ConditionTrue,
		Reason: clusterv1.ClusterAvailableReason,
	}}

	got := Summarise("root:demo-1", "abcdef", cluster, devClusterFor(ClusterName(0)))
	if !got.Ready {
		t.Errorf("Summarise(...).Ready = false for an Available cluster, detail %q", got.Detail)
	}
}

// Provisioned but not available is the state every bug in this wiring has
// produced, so it must not read as ready - and the row has to say what is
// outstanding rather than repeating "infrastructure provisioned".
func TestSummariseProvisionedIsNotReady(t *testing.T) {
	cluster := provisionedCluster()
	cluster.Status.Conditions = []metav1.Condition{{
		Type:    clusterv1.ClusterAvailableCondition,
		Status:  metav1.ConditionFalse,
		Reason:  "NotAvailable",
		Message: "control plane is not available",
	}}

	got := Summarise("root:demo-1", "abcdef", cluster, devClusterFor(ClusterName(0)))
	if got.Ready {
		t.Error("Summarise(...).Ready = true for a cluster whose Available condition is false")
	}
	if !got.Provisioned {
		t.Error("Summarise(...).Provisioned = false, want the milestone still reported")
	}
	if !strings.Contains(got.Detail, "control plane is not available") {
		t.Errorf("Summarise(...).Detail = %q, want the Available condition's message", got.Detail)
	}
}

func TestAllClustersReadyEmptyIsNotDone(t *testing.T) {
	if AllClustersReady(nil) {
		t.Error("AllClustersReady(nil) = true, want false")
	}
}

func TestAllClustersReady(t *testing.T) {
	if !AllClustersReady([]ClusterStatus{{Ready: true}, {Ready: true}}) {
		t.Error("AllClustersReady(all ready) = false")
	}
	if AllClustersReady([]ClusterStatus{{Ready: true}, {Provisioned: true}}) {
		t.Error("AllClustersReady(one provisioned but not ready) = true")
	}
}

// A control plane is ready when it has every replica it was asked for, not
// when it has its first one - which is what Initialized already says.
func TestSummariseControlPlaneReadyNeedsEveryReplica(t *testing.T) {
	kcp := &controlplanev1.KubeadmControlPlane{}
	kcp.Name = "demo-00-cp"
	kcp.Spec.Replicas = ptr.To(int32(3))
	kcp.Status.Initialization.ControlPlaneInitialized = ptr.To(true)
	kcp.Status.ReadyReplicas = ptr.To(int32(1))

	got := SummariseControlPlane("root:demo-1", "abcdef", kcp)
	if !got.Initialized {
		t.Error("SummariseControlPlane(...).Initialized = false for an initialized control plane")
	}
	if got.Ready {
		t.Error("SummariseControlPlane(...).Ready = true with 1 of 3 replicas ready")
	}

	kcp.Status.ReadyReplicas = ptr.To(int32(3))
	if got := SummariseControlPlane("root:demo-1", "abcdef", kcp); !got.Ready {
		t.Errorf("SummariseControlPlane(...).Ready = false with 3 of 3 replicas ready, detail %q", got.Detail)
	}
}

// Zero desired replicas is not readiness. Without this a control plane that
// never scaled up would satisfy "every replica is ready" with none.
func TestSummariseControlPlaneWithNoReplicasIsNotReady(t *testing.T) {
	kcp := &controlplanev1.KubeadmControlPlane{}
	kcp.Name = "demo-00-cp"

	if got := SummariseControlPlane("root:demo-1", "abcdef", kcp); got.Ready {
		t.Error("SummariseControlPlane(...).Ready = true for a control plane asked for no replicas")
	}
}

func TestAllControlPlanesReadyEmptyIsVacuouslyTrue(t *testing.T) {
	if !AllControlPlanesReady(nil) {
		t.Error("AllControlPlanesReady(nil) = false, but a run that asked for no control plane is not waiting for one")
	}
	if AllControlPlanesReady([]ControlPlaneStatus{{Ready: true}, {Initialized: true}}) {
		t.Error("AllControlPlanesReady(one initialized but not ready) = true")
	}
}

// Bootstrapped is not ready: the data secret exists long before the Node does.
func TestSummariseMachineReady(t *testing.T) {
	machine := &clusterv1.Machine{}
	machine.Name = "demo-00-cp-abcde"
	machine.Spec.Bootstrap.DataSecretName = ptr.To("demo-00-cp-abcde")
	machine.Status.Phase = "Provisioned"
	machine.Status.Conditions = []metav1.Condition{{
		Type:    clusterv1.MachineReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  "NotReady",
		Message: "Node is not yet available",
	}}

	got := SummariseMachine("root:demo-1", "abcdef", machine, nil)
	if !got.Bootstrapped {
		t.Error("SummariseMachine(...).Bootstrapped = false for a machine with its data secret")
	}
	if got.Ready {
		t.Error("SummariseMachine(...).Ready = true for a machine whose Ready condition is false")
	}
	if !strings.Contains(got.Detail, "Node is not yet available") {
		t.Errorf("SummariseMachine(...).Detail = %q, want the Ready condition's message", got.Detail)
	}

	machine.Status.Conditions[0].Status = metav1.ConditionTrue
	if got := SummariseMachine("root:demo-1", "abcdef", machine, nil); !got.Ready {
		t.Errorf("SummariseMachine(...).Ready = false for a Ready machine, detail %q", got.Detail)
	}
}

func TestAllMachinesReady(t *testing.T) {
	if !AllMachinesReady(nil) {
		t.Error("AllMachinesReady(nil) = false, but a run that asked for no machines is not waiting for any")
	}
	if AllMachinesReady([]MachineStatus{{Ready: true}, {Bootstrapped: true}}) {
		t.Error("AllMachinesReady(one bootstrapped but not ready) = true")
	}
}
