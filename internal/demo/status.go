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
	"fmt"
	"io"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/api/meta"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// ClusterStatus is what the demo reports about one cluster in one workspace.
type ClusterStatus struct {
	// Workspace is the human-readable workspace path, e.g. root:capi-demo-1.
	Workspace string

	// LogicalCluster is the internal cluster name the manager engages the
	// workspace by. Reported because it is what appears in the manager's
	// logs, and matching a log line to a workspace is otherwise guesswork.
	LogicalCluster string

	// Cluster is the Cluster object's name, repeated in every workspace.
	Cluster string

	// Provisioned reports Cluster.status.initialization.infrastructureProvisioned:
	// the infrastructure provider has done its work for this cluster.
	Provisioned bool

	// Detail says what the cluster is waiting on when it is not provisioned,
	// taken from the InfrastructureReady condition.
	Detail string
}

// Summarise reads one cluster's demo status out of the two objects it spans.
//
// A missing DevCluster is reported rather than treated as an error: the
// Cluster reconciler resolves the reference asynchronously, so "not there
// yet" is an ordinary state during a demo run.
func Summarise(workspace, logicalCluster string, cluster *clusterv1.Cluster, devCluster *infrav1.DevCluster) ClusterStatus {
	status := ClusterStatus{
		Workspace:      workspace,
		LogicalCluster: logicalCluster,
		Cluster:        cluster.Name,
	}

	if p := cluster.Status.Initialization.InfrastructureProvisioned; p != nil && *p {
		status.Provisioned = true
		status.Detail = "infrastructure provisioned"
		return status
	}

	if cond := meta.FindStatusCondition(cluster.Status.Conditions, string(clusterv1.ClusterInfrastructureReadyCondition)); cond != nil {
		status.Detail = cond.Reason
		if cond.Message != "" {
			status.Detail = fmt.Sprintf("%s: %s", cond.Reason, cond.Message)
		}
		return status
	}

	if devCluster == nil {
		status.Detail = "waiting for the DevCluster to be created"
		return status
	}
	status.Detail = "waiting for the Cluster reconciler"
	return status
}

// AllProvisioned reports whether every cluster in the snapshot is provisioned,
// which is the demo's done-condition.
//
// An empty snapshot is not done: a run that created nothing has provisioned
// nothing, and reporting success for it would be the one failure mode a demo
// must not have.
func AllProvisioned(statuses []ClusterStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !s.Provisioned {
			return false
		}
	}
	return true
}

// RenderTable writes the snapshot as an aligned table.
func RenderTable(w io.Writer, statuses []ClusterStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "WORKSPACE\tLOGICAL CLUSTER\tCLUSTER\tPROVISIONED\tDETAIL"); err != nil {
		return err
	}
	for _, s := range statuses {
		provisioned := "no"
		if s.Provisioned {
			provisioned = "yes"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			s.Workspace, s.LogicalCluster, s.Cluster, provisioned, s.Detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}
