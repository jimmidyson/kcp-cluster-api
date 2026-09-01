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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample(label string, workspaces int, component, node string, goroutines int, heap, resident uint64, restarts int32) Sample {
	return Sample{
		Label:      label,
		Workspaces: workspaces,
		Clusters:   workspaces,
		Components: []ComponentSample{{
			Component: component,
			Process: ProcessSample{
				Goroutines: goroutines, HeapAllocBytes: heap, ResidentBytes: resident, CPUSeconds: 1,
			},
			Pod: PodFacts{Name: component + "-1", Node: node, Ready: true, RestartCount: restarts},
		}},
	}
}

// packed builds a sample where a workspace holds several clusters, which
// sample() cannot express: it sets one cluster per workspace.
func packed(label string, workspaces, clusters int, component string, goroutines int) Sample {
	s := sample(label, workspaces, component, "node-1", goroutines, 10_000_000, 30_000_000, 0)
	s.Clusters = clusters
	return s
}

// TestPerClusterIsTheFigureThatHoldsAcrossDistributions is built from two real
// runs of the same twenty-five clusters, arranged differently.
//
// Per workspace they look four times apart, which reads as a large cost to
// packing clusters into fewer workspaces. Per cluster they agree, and the
// control plane manager agrees to within 0.1 — so the packing is close to free
// and the cluster count is what costs. A report showing only the per-workspace
// column invites exactly the wrong conclusion from its own numbers.
func TestPerClusterIsTheFigureThatHoldsAcrossDistributions(t *testing.T) {
	// 25x1: 7, 13 and 25 workspaces, one cluster each. Measured 463/565/769.
	spread := &Report{Title: "25x1"}
	spread.Add(packed("7", 7, 7, ComponentCore, 463))
	spread.Add(packed("13", 13, 13, ComponentCore, 565))
	spread.Add(packed("25", 25, 25, ComponentCore, 769))

	// 5x5: 2, 3 and 5 workspaces, five clusters each. Measured 498/575/729.
	dense := &Report{Title: "5x5"}
	dense.Add(packed("2", 2, 10, ComponentCore, 498))
	dense.Add(packed("3", 3, 15, ComponentCore, 575))
	dense.Add(packed("5", 5, 25, ComponentCore, 729))

	spreadWS, ok := spread.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no per-workspace slope for 25x1")
	}
	denseWS, ok := dense.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no per-workspace slope for 5x5")
	}
	if denseWS/spreadWS < 3 {
		t.Fatalf("the per-workspace figures were expected to look far apart (%.1f against %.1f); "+
			"this test is not exercising what it thinks", spreadWS, denseWS)
	}

	spreadC, ok := spread.PerCluster(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no per-cluster slope for 25x1")
	}
	denseC, ok := dense.PerCluster(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no per-cluster slope for 5x5")
	}
	if ratio := denseC / spreadC; ratio < 0.8 || ratio > 1.25 {
		t.Errorf("per cluster the two distributions disagree: %.1f for 25x1 against %.1f for 5x5 (%.2fx). "+
			"The whole reason to report a per-cluster figure is that this is the quantity that holds.",
			spreadC, denseC, ratio)
	}

	if md := dense.Markdown(); !strings.Contains(md, "goroutines per cluster") {
		t.Error("the report does not carry a per-cluster figure, so a reader comparing two " +
			"distributions sees only the column that misleads")
	}
}

func TestPerWorkspaceFitsASlope(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))
	r.Add(sample("32", 32, ComponentCore, "node-1", 1700, 34_000_000, 102_000_000, 0))

	slope, ok := r.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no slope from three samples")
	}
	if slope < 49 || slope > 51 {
		t.Errorf("goroutines per workspace = %v, want about 50", slope)
	}
}

// TestARestartedSampleIsExcludedFromTheFit is the property that keeps a
// restart from being read as a cheaper fleet: the process metrics reset, so
// the sample is not comparable with the others.
func TestARestartedSampleIsExcludedFromTheFit(t *testing.T) {
	r := &Report{Title: "t"}
	// Four samples, so that dropping the restarted one still leaves the three
	// distinct workspace counts a slope needs. That is the real interaction:
	// excluding a sample can take a run below what it can support, and then the
	// honest answer is no figure rather than a figure from what is left.
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))
	r.Add(sample("24", 24, ComponentCore, "node-1", 1300, 26_000_000, 78_000_000, 0))
	// A restarted container reporting a fresh process's small numbers at the
	// widest point would drag the slope negative.
	r.Add(sample("32", 32, ComponentCore, "node-1", 40, 1_000_000, 8_000_000, 1))

	slope, ok := r.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no slope")
	}
	if slope <= 0 {
		t.Errorf("slope = %v: the restarted sample was included", slope)
	}

	if len(r.Disturbed()) != 1 {
		t.Errorf("the restart was not reported: %v", r.Disturbed())
	}
	if !strings.Contains(r.Markdown(), "A container restarted during this run") {
		t.Error("the report does not warn that a container restarted")
	}
}

// TestANegativeFitIsNotAMeasurement. Least squares returns a negative slope
// whenever noise exceeds signal, and a negative cost per workspace is not a
// cheaper fleet.
func TestANegativeFitIsNotAMeasurement(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 900, 20_000_000, 60_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))

	if slope, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Errorf("a negative fit was reported as a measurement: %v", slope)
	}
	if !strings.Contains(r.Markdown(), "not measured") {
		t.Error("the report does not say the figure was not measured")
	}
}

func TestOneSampleIsNotASlope(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	if _, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Error("one sample produced a slope")
	}
}

// TestCoLocationIsStatedInTheReport is the most misleading thing this harness
// could omit: a run where everything landed on one node measured a co-located
// deployment whatever the manifests asked for.
func TestCoLocationIsStatedInTheReport(t *testing.T) {
	r := &Report{Title: "t"}
	s := sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0)
	s.Components = append(s.Components, ComponentSample{
		Component: ComponentBootstrap,
		Pod:       PodFacts{Node: "node-1", Ready: true},
	})
	r.Add(s)

	nodes, coLocated := r.Placement()
	if len(nodes) != 1 || !coLocated {
		t.Fatalf("placement = %v, co-located = %v", nodes, coLocated)
	}
	if !strings.Contains(r.Markdown(), "not a multi-node figure") {
		t.Error("a co-located run does not say so in its report")
	}
}

func TestSpreadIsStatedToo(t *testing.T) {
	r := &Report{Title: "t"}
	s := sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0)
	s.Components = append(s.Components, ComponentSample{
		Component: ComponentBootstrap,
		Pod:       PodFacts{Node: "node-2", Ready: true},
	})
	r.Add(s)

	nodes, coLocated := r.Placement()
	if len(nodes) != 2 || coLocated {
		t.Fatalf("placement = %v, co-located = %v", nodes, coLocated)
	}
	md := r.Markdown()
	if !strings.Contains(md, "spread across 2 nodes") {
		t.Error("a spread run does not say so")
	}
	if strings.Contains(md, "not a multi-node figure") {
		t.Error("a spread run was labelled co-located")
	}
}

// A single component on a single node is not "co-located": there is nothing
// for it to be co-located with, and warning about it would train a reader to
// ignore the warning.
func TestOneComponentIsNotCoLocated(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	if _, coLocated := r.Placement(); coLocated {
		t.Error("a single-component run was reported as co-located")
	}
}

func TestComponentsAreInCanonicalOrder(t *testing.T) {
	r := &Report{Title: "t"}
	s := Sample{Label: "x", Components: []ComponentSample{
		{Component: ComponentDevInfrastructure},
		{Component: ComponentCore},
	}}
	r.Add(s)

	got := r.Components()
	if len(got) != 2 || got[0] != ComponentCore {
		t.Errorf("components = %v, want core first whatever order they were sampled in", got)
	}
}

func TestWriteProducesBothForms(t *testing.T) {
	r := &Report{Title: "Deployed fleet"}
	r.AddFact("spread", "32x1")
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))

	dir := t.TempDir()
	if err := r.Write(dir, "deployed"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(dir, "deployed.md"))
	if err != nil {
		t.Fatalf("reading markdown: %v", err)
	}
	if !strings.Contains(string(md), "Deployed fleet") || !strings.Contains(string(md), ComponentCore) {
		t.Error("the markdown does not carry the title and the component")
	}
	// The per-deployment heading is what FR-010 asks for: cost is never
	// reported only as a total.
	if !strings.Contains(string(md), "## "+ComponentCore) {
		t.Error("the report has no per-deployment section")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "deployed.json"))
	if err != nil {
		t.Fatalf("reading json: %v", err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(back.Samples) != 2 || back.Facts["spread"] != "32x1" {
		t.Errorf("the json did not round trip: %+v", back)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{{512, "512 B"}, {2048, "2.0 KiB"}, {5 * 1024 * 1024, "5.0 MiB"}} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTwoSamplesAreNotASlope is the regression test for a figure this harness
// published and should not have.
//
// A run at 1 and 2 workspaces reported "17.0 goroutines per workspace" from
// counts of 416 and 433 — a 4% swing on a 400-goroutine process, taken as a
// marginal cost. It then disagreed 8.5x with a 61-sample in-process sweep, and
// the run failed on a disagreement that was about the fit rather than the fleet.
//
// A line through two points passes exactly through both. Its residual is zero
// whatever the data, so nothing in it distinguishes a slope from noise.
func TestTwoSamplesAreNotASlope(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("1", 1, ComponentCore, "node-1", 416, 15_000_000, 79_000_000, 0))
	r.Add(sample("2", 2, ComponentCore, "node-1", 433, 17_000_000, 81_000_000, 0))

	if slope, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Errorf("two points produced a per-workspace figure of %v, which the data cannot support", slope)
	}
	if !strings.Contains(r.Markdown(), "at least three distinct workspace counts") {
		t.Error("the report does not say why the figure was not measured")
	}
}

// TestRepeatedWorkspaceCountsAreNotThreePoints: sampling the same size three
// times says nothing about how cost varies with size, however many samples it
// produces.
func TestRepeatedWorkspaceCountsAreNotThreePoints(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("4", 4, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("4", 4, ComponentCore, "node-1", 505, 10_100_000, 30_100_000, 0))
	r.Add(sample("8", 8, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))

	if _, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Error("three samples at two distinct sizes produced a slope")
	}
}

// TestExcludingARestartCanLeaveTooFewPoints pins the other half of the
// interaction above: when dropping a restarted sample takes the run below three
// distinct workspace counts, the answer is no figure, not a figure fitted to
// whatever survived.
func TestExcludingARestartCanLeaveTooFewPoints(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))
	r.Add(sample("32", 32, ComponentCore, "node-1", 40, 1_000_000, 8_000_000, 1))

	if slope, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Errorf("a slope of %v was fitted to the two samples left after excluding a restart", slope)
	}
	if len(r.Disturbed()) != 1 {
		t.Errorf("the restart was not reported: %v", r.Disturbed())
	}
}
