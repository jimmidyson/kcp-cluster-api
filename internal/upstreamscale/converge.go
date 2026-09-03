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
