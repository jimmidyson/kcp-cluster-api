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

package scaletarget

import (
	"strings"
	"testing"
)

// TestTheAskAsStated is the request this feature started from, written the way
// it was asked for rather than as a shape and a machine split.
func TestTheAskAsStated(t *testing.T) {
	fleet := Fleet{
		Clusters:             200,
		NodesPerCluster:      50,
		ControlPlaneNodes:    3,
		ClustersPerWorkspace: []int{1, 10},
	}

	if got := fleet.Nodes(); got != 10_000 {
		t.Errorf("nodes = %d, want 10000", got)
	}
	// The control plane is part of the fifty, not on top of it.
	if m := fleet.Machines(); m.ControlPlane != 3 || m.Workers != 47 || m.PerCluster() != 50 {
		t.Errorf("machines = %+v, want 3 control plane and 47 workers making 50", m)
	}

	plans, err := fleet.Plans([]int{50})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans, want one per spread", len(plans))
	}
	if plans[0].Shape.Workspaces != 200 || plans[0].Shape.ClustersPerWorkspace != 1 {
		t.Errorf("first spread = %s, want 200x1", plans[0].Shape)
	}
	if plans[1].Shape.Workspaces != 20 || plans[1].Shape.ClustersPerWorkspace != 10 {
		t.Errorf("second spread = %s, want 20x10", plans[1].Shape)
	}
	// Both spreads hold the same fleet, which is what makes the pair a
	// comparison rather than two unrelated runs.
	for _, p := range plans {
		if p.Shape.Clusters() != 200 || p.Nodes() != 10_000 {
			t.Errorf("spread %s holds %d clusters and %d nodes", p.Shape, p.Shape.Clusters(), p.Nodes())
		}
	}
}

func TestASingleSpreadIsOnePlan(t *testing.T) {
	plans, err := Fleet{Clusters: 8, NodesPerCluster: 1, ControlPlaneNodes: 1, ClustersPerWorkspace: []int{1}}.Plans(nil)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plans) != 1 || plans[0].Shape.Workspaces != 8 {
		t.Fatalf("plans = %v", plans)
	}
	if m := plans[0].Machines; m.ControlPlane != 1 || m.Workers != 0 {
		t.Errorf("a one node cluster should be one control plane machine and no workers, got %+v", m)
	}
}

// TestAnIndivisibleSpreadIsRefused. Rounding up would give a run holding more
// clusters than was asked for while reporting the number that was asked for.
func TestAnIndivisibleSpreadIsRefused(t *testing.T) {
	_, err := Fleet{Clusters: 200, NodesPerCluster: 50, ControlPlaneNodes: 3, ClustersPerWorkspace: []int{3}}.Plans(nil)
	if err == nil {
		t.Fatal("200 clusters were spread 3 per workspace")
	}
	if !strings.Contains(err.Error(), "do not divide") {
		t.Errorf("error %q does not say why", err)
	}
}

// TestControlPlaneIsInsideTheNodeCount is the arithmetic somebody would most
// easily get wrong the other way round.
func TestControlPlaneIsInsideTheNodeCount(t *testing.T) {
	_, err := Fleet{Clusters: 1, NodesPerCluster: 3, ControlPlaneNodes: 5, ClustersPerWorkspace: []int{1}}.Plans(nil)
	if err == nil {
		t.Fatal("a 3 node cluster accepted 5 control plane nodes")
	}
	if !strings.Contains(err.Error(), "not on top of it") {
		t.Errorf("error %q does not explain the convention", err)
	}
}

// An unstated spread is derived rather than refused: see DefaultSpreads. It is
// the one field with a sensible answer the fleet can work out for itself, and
// refusing it made tuning the cluster count break a knob nobody touched.
func TestAnUnstatedSpreadIsDerived(t *testing.T) {
	plans, err := Fleet{Clusters: 200, NodesPerCluster: 50, ControlPlaneNodes: 3}.Plans(nil)
	if err != nil {
		t.Fatalf("an unstated spread was refused: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d spreads, want the pair that separates the two terms", len(plans))
	}
	if plans[0].Shape.ClustersPerWorkspace != 1 || plans[1].Shape.ClustersPerWorkspace != 10 {
		t.Errorf("spreads = %s and %s, want one and ten per workspace", plans[0].Shape, plans[1].Shape)
	}
}

func TestFleetRejectsWhatCannotBeBuilt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet Fleet
		wants string
	}{
		{"no clusters", Fleet{Clusters: 0, NodesPerCluster: 1, ControlPlaneNodes: 1, ClustersPerWorkspace: []int{1}}, "at least one"},
		{"no nodes", Fleet{Clusters: 1, NodesPerCluster: 0, ControlPlaneNodes: 0, ClustersPerWorkspace: []int{1}}, "not nodes"},
		{"zero per workspace", Fleet{Clusters: 1, NodesPerCluster: 1, ControlPlaneNodes: 1, ClustersPerWorkspace: []int{0}}, "at least one"},
		// Workers with no control plane never converge; the machine split
		// catches it wherever it is expressed.
		{"workers with no control plane", Fleet{Clusters: 1, NodesPerCluster: 5, ControlPlaneNodes: 0, ClustersPerWorkspace: []int{1}}, "never converges"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.fleet.Plans(nil)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not mention %q", err, tc.wants)
			}
		})
	}
}
