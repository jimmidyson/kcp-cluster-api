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

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func provisionedCluster() *clusterv1.Cluster {
	cluster := NewCluster(ClusterName(0), BackendInMemory)
	cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
	return cluster
}

func TestSummariseProvisioned(t *testing.T) {
	got := Summarise("root:demo-1", "abcdef", provisionedCluster(), NewDevCluster(ClusterName(0), BackendInMemory))
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
	cluster := NewCluster(ClusterName(0), BackendInMemory)
	cluster.Status.Conditions = []metav1.Condition{{
		Type:    string(clusterv1.ClusterInfrastructureReadyCondition),
		Status:  metav1.ConditionFalse,
		Reason:  "NotReady",
		Message: "waiting for the load balancer",
	}}

	got := Summarise("root:demo-1", "abcdef", cluster, NewDevCluster(ClusterName(0), BackendInMemory))
	if got.Provisioned {
		t.Error("Summarise(...).Provisioned = true for a cluster whose infrastructure is not ready")
	}
	if !strings.Contains(got.Detail, "NotReady") || !strings.Contains(got.Detail, "load balancer") {
		t.Errorf("Summarise(...).Detail = %q, want the condition's reason and message", got.Detail)
	}
}

func TestSummariseMissingDevCluster(t *testing.T) {
	got := Summarise("root:demo-1", "abcdef", NewCluster(ClusterName(0), BackendInMemory), nil)
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
