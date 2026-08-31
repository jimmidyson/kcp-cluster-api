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
	"slices"
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
)

func TestParseShapes(t *testing.T) {
	shapes, err := ParseShapes("200x1, 20x10")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	want := []Shape{{Workspaces: 200, ClustersPerWorkspace: 1}, {Workspaces: 20, ClustersPerWorkspace: 10}}
	if !slices.Equal(shapes, want) {
		t.Fatalf("shapes = %v, want %v", shapes, want)
	}
	// Both spreads hold the same fleet, which is the pair this test exists to
	// keep comparable: a per-workspace figure and a per-cluster figure can
	// only be separated by measuring one cluster count at two spreads.
	if shapes[0].Clusters() != shapes[1].Clusters() {
		t.Errorf("200x1 holds %d clusters and 20x10 holds %d", shapes[0].Clusters(), shapes[1].Clusters())
	}
}

func TestShapeStringRoundTrips(t *testing.T) {
	want := Shape{Workspaces: 200, ClustersPerWorkspace: 1}
	got, err := ParseShapes(want.String())
	if err != nil {
		t.Fatalf("parsing %q: %v", want.String(), err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("round trip of %q gave %v", want.String(), got)
	}
}

func TestParseShapesRejectsWhatCannotBeBuilt(t *testing.T) {
	for _, tc := range []struct{ name, input, wants string }{
		{"empty", "", "no shapes"},
		{"no multiplication", "200", "<workspaces>x<clustersPerWorkspace>"},
		{"unparseable workspaces", "manyx1", "workspace count"},
		{"unparseable clusters", "200xmany", "clusters per workspace"},
		{"no workspaces", "0x1", "at least one workspace"},
		{"no clusters", "200x0", "at least one cluster"},
		{"negative", "-4x1", "at least one workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseShapes(tc.input)
			if err == nil {
				t.Fatalf("parsing %q succeeded", tc.input)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not mention %q", err, tc.wants)
			}
		})
	}
}

func TestMachinesValidate(t *testing.T) {
	if err := (Machines{ControlPlane: 3, Workers: 47}).Validate(); err != nil {
		t.Errorf("3 control plane and 47 workers rejected: %v", err)
	}
	if got := (Machines{ControlPlane: 3, Workers: 47}).PerCluster(); got != 50 {
		t.Errorf("per cluster = %d, want 50", got)
	}

	// Workers with no control plane is the combination that produces a run
	// that never converges rather than a cheaper one, so it is rejected
	// before a run rather than diagnosed after one.
	err := (Machines{ControlPlane: 0, Workers: 47}).Validate()
	if err == nil {
		t.Fatal("workers with no control plane were accepted")
	}
	if !strings.Contains(err.Error(), "never converges") {
		t.Errorf("error %q does not say why", err)
	}

	if err := (Machines{}).Validate(); err == nil {
		t.Error("a cluster with no machines at all was accepted")
	}
}

func TestCheckpointsAlwaysEndAtTheTarget(t *testing.T) {
	got, err := Checkpoints(200, []int{25, 50})
	if err != nil {
		t.Fatalf("checkpoints: %v", err)
	}
	want := []int{50, 100, 200}
	if !slices.Equal(got, want) {
		t.Fatalf("checkpoints = %v, want %v", got, want)
	}
}

func TestCheckpointsDeduplicateAndSort(t *testing.T) {
	// 100% is already the target, and an unsorted input must not produce an
	// unsorted sample order: the samples are read as a series.
	got, err := Checkpoints(20, []int{100, 50, 50, 10})
	if err != nil {
		t.Fatalf("checkpoints: %v", err)
	}
	want := []int{2, 10, 20}
	if !slices.Equal(got, want) {
		t.Fatalf("checkpoints = %v, want %v", got, want)
	}
}

func TestCheckpointsRoundUpSoNoneSamplesNothing(t *testing.T) {
	// 10% of 4 is 0.4. Rounded down it is a checkpoint at zero workspaces,
	// which is the baseline under another name.
	got, err := Checkpoints(4, []int{10})
	if err != nil {
		t.Fatalf("checkpoints: %v", err)
	}
	want := []int{1, 4}
	if !slices.Equal(got, want) {
		t.Fatalf("checkpoints = %v, want %v", got, want)
	}
}

func TestCheckpointsRejectNonsense(t *testing.T) {
	if _, err := Checkpoints(0, []int{50}); err == nil {
		t.Error("checkpoints over no workspaces were accepted")
	}
	if _, err := Checkpoints(200, []int{0}); err == nil {
		t.Error("a 0% checkpoint was accepted")
	}
	if _, err := Checkpoints(200, []int{101}); err == nil {
		t.Error("a 101% checkpoint was accepted")
	}
}

func TestParsePercents(t *testing.T) {
	got, err := ParsePercents(" 25, 50 ,100")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if !slices.Equal(got, []int{25, 50, 100}) {
		t.Fatalf("percents = %v", got)
	}
	if got, err := ParsePercents(""); err != nil || got != nil {
		t.Errorf("empty gave %v, %v; want nil, nil", got, err)
	}
	if _, err := ParsePercents("half"); err == nil {
		t.Error("an unparseable percentage was accepted")
	}
}

func TestNewPlan(t *testing.T) {
	plan, err := NewPlan(Shape{Workspaces: 200, ClustersPerWorkspace: 1}, Machines{ControlPlane: 3, Workers: 47}, []int{50})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if got := plan.Nodes(); got != 10_000 {
		t.Errorf("nodes = %d, want 10000", got)
	}
	if !slices.Equal(plan.Checkpoints, []int{100, 200}) {
		t.Errorf("checkpoints = %v", plan.Checkpoints)
	}

	// The two spreads of one fleet reach the same node count, which is what
	// makes the pair a comparison rather than two unrelated runs.
	dense, err := NewPlan(Shape{Workspaces: 20, ClustersPerWorkspace: 10}, Machines{ControlPlane: 3, Workers: 47}, nil)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if dense.Nodes() != plan.Nodes() {
		t.Errorf("20x10 reaches %d nodes and 200x1 reaches %d", dense.Nodes(), plan.Nodes())
	}
}

func TestNewPlanRejectsAnUnbuildableTarget(t *testing.T) {
	if _, err := NewPlan(Shape{Workspaces: 0, ClustersPerWorkspace: 1}, Machines{ControlPlane: 1}, nil); err == nil {
		t.Error("a shape with no workspaces was planned")
	}
	if _, err := NewPlan(Shape{Workspaces: 4, ClustersPerWorkspace: 1}, Machines{Workers: 1}, nil); err == nil {
		t.Error("workers with no control plane were planned")
	}
	if _, err := NewPlan(Shape{Workspaces: 4, ClustersPerWorkspace: 1}, Machines{ControlPlane: 1}, []int{200}); err == nil {
		t.Error("a 200% checkpoint was planned")
	}
}

func TestClassify(t *testing.T) {
	// Reaching nothing is the one outcome that is not a measurement, and it
	// must not be reported as a pass. AGENTS.md, "Done is a command".
	if v := Classify(0, 200, "wall-clock budget"); v.Outcome != verify.OutcomeCouldNotRun {
		t.Errorf("reaching zero gave %s, want could not run", v.Outcome)
	}

	short := Classify(140, 200, "wall-clock budget 60m")
	if short.Outcome != verify.OutcomePass {
		t.Errorf("falling short gave %s, want pass: the number is the deliverable", short.Outcome)
	}
	if !strings.Contains(short.Note, "extrapolation") {
		t.Errorf("a short run's note %q does not warn that figures above it are extrapolated", short.Note)
	}

	full := Classify(200, 200, "reached the target")
	if full.Outcome != verify.OutcomePass {
		t.Errorf("reaching the target gave %s", full.Outcome)
	}
	if full.Note != "" {
		t.Errorf("a complete run carries the note %q", full.Note)
	}
}
