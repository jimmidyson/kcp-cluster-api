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

// TestAWanderingHeapIsFlaggedOnTheMemoryFigures uses the dev-infrastructure
// manager's real heap series from two runs of the same twenty-five clusters:
// 26.3, 19.0, 44.5 MiB and 20.4, 27.3, 86.9 MiB. Neither climbs with the fleet.
//
// Those runs' per-cluster memory figures disagree by 76%, while their
// per-cluster goroutine figures agree to 2%. Reporting both in the same bold
// invites a reader to size a memory limit off the weaker of the two.
func TestAWanderingHeapIsFlaggedOnTheMemoryFigures(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(heaped("7", 7, 402, 26_300_000, 85_700_000))
	r.Add(heaped("13", 13, 562, 19_000_000, 94_200_000))
	r.Add(heaped("25", 25, 921, 44_500_000, 116_400_000))

	md := r.Markdown()
	if !strings.Contains(md, "weaker than the goroutine ones") {
		t.Error("a run whose heap wanders does not warn that its memory slope is fitted to GC timing")
	}
	// The figures are still reported: a wide number beats none when sizing a
	// limit, so long as it is not dressed up as the reproducible one.
	if !strings.Contains(md, "resident bytes per cluster") {
		t.Error("the caveat suppressed the figure instead of qualifying it")
	}
}

// TestAClimbingHeapIsNotFlagged: the caveat has to be a signal, not decoration.
func TestAClimbingHeapIsNotFlagged(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(heaped("7", 7, 402, 19_000_000, 85_700_000))
	r.Add(heaped("13", 13, 562, 26_300_000, 94_200_000))
	r.Add(heaped("25", 25, 921, 44_500_000, 116_400_000))

	if strings.Contains(r.Markdown(), "weaker than the goroutine ones") {
		t.Error("a run whose heap climbs with the fleet was warned about anyway")
	}
}

func heaped(label string, workspaces, goroutines int, heap, resident uint64) Sample {
	return sample(label, workspaces, ComponentDevInfrastructure, "node-1", goroutines, heap, resident, 0)
}

// TestABaselineSeparatesFixedCostFromMarginalCost covers the sample this
// harness spent its whole life without.
//
// Every slope it reports is a difference between two large numbers. kcp's
// smallest sample was 130 Machines at 1.44 GiB, which says nothing about how
// much of that is the shard merely existing — so a per-Machine figure derived
// from it was a per-Machine figure plus a share of an unmeasured intercept.
//
// What the baseline is not is a fourth point on the same line. The run that
// first took one found the idle shard 735 MB below where the loaded samples'
// line put it, so the baseline separates the two costs by being reported
// beside the fit, not by being fitted. This test is the shape of that: a
// process that is cheap idle, expensive the moment it has anything to do, and
// cheap again per unit after that.
func TestABaselineSeparatesFixedCostFromMarginalCost(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("baseline", 0, ComponentCore, "node-1", 1000, 400_000_000, 500_000_000, 0))
	r.Add(sample("10", 10, ComponentCore, "node-1", 1100, 1_216_000_000, 1_320_000_000, 0))
	r.Add(sample("20", 20, ComponentCore, "node-1", 1200, 1_232_000_000, 1_340_000_000, 0))
	r.Add(sample("30", 30, ComponentCore, "node-1", 1300, 1_248_000_000, 1_360_000_000, 0))

	slope, ok := r.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("three loaded checkpoints did not fit")
	}
	if slope < 9.5 || slope > 10.5 {
		t.Errorf("goroutines per workspace = %.1f, want 10", slope)
	}

	// The marginal cost is 1.6 MB per workspace on top of a 1.2 GB fixed cost.
	// A fit that took the idle sample as a fourth point would read 30 MB.
	mem, ok := r.PerWorkspace(ComponentCore, HeapAlloc)
	if !ok {
		t.Fatal("no heap slope")
	}
	if mem < 1_500_000 || mem > 1_700_000 {
		t.Errorf("heap per workspace = %.0f, want about 1.6e6 — the idle sample is in the fit", mem)
	}

	md := r.Markdown()
	if !strings.Contains(md, "measured idle") {
		t.Error("the idle sample was dropped rather than reported")
	}
	if !strings.Contains(md, "above the idle process") {
		t.Errorf("the report does not state the step between the idle process and the fit's own "+
			"fixed cost, which is the largest number in it:\n%s", md)
	}
}

// TestAnIdleProcessIsNotASmallFleet uses kcp's own numbers from the 50x10 run,
// the first run to sample the shard before it had anything to serve.
//
// The idle shard held 502 MB of live heap. Thirteen workspaces later it held
// 1.41 GB, and from there to fifty it climbed only another 506 MB. A line
// through all four points reads 25.5 MiB per cluster, which is nearly twice the
// 13.6 MiB the loaded points agree on among themselves, and it misses the idle
// point it was fitted through by 290 MB. That is not a cost per cluster; it is
// one line drawn across two regimes.
func TestAnIdleProcessIsNotASmallFleet(t *testing.T) {
	r := &Report{Title: "50x10"}
	r.Add(sample("baseline (no workspaces)", 0, KcpName, "node-1", 5757, 502_077_944, 769_138_688, 0))
	r.Add(sample("13 workspaces", 13, KcpName, "node-1", 6439, 1_410_078_400, 2_362_662_912, 0))
	r.Add(sample("25 workspaces", 25, KcpName, "node-1", 7061, 1_585_035_216, 3_013_373_952, 0))
	r.Add(sample("50 workspaces", 50, KcpName, "node-1", 8357, 1_916_582_032, 3_862_323_200, 0))

	slope, ok := r.PerCluster(KcpName, HeapAlloc)
	if !ok {
		t.Fatal("no heap slope: the loaded samples fit a line to within 1.4%, which is the " +
			"tightest memory fit this harness has measured")
	}
	if slope < 13.0e6 || slope > 14.3e6 {
		t.Errorf("heap per cluster = %s, want about 13.6 MiB. A fit including the idle sample "+
			"reads 25.5 MiB, so this is the arithmetic that says whether the idle sample was in it",
			humanBytes(uint64(slope)))
	}

	// The idle process is not discarded. It is the only measurement of what the
	// shard costs before it serves anything, and the gap between it and the
	// fit's own base is a quantity worth a reader's attention.
	idle, ok := r.Idle(KcpName)
	if !ok {
		t.Fatal("the report cannot say what the idle process cost")
	}
	if idle.Process.Goroutines != 5757 {
		t.Errorf("idle goroutines = %d, want 5757", idle.Process.Goroutines)
	}
	if md := r.Markdown(); !strings.Contains(md, "measured idle") {
		t.Error("the report does not state what the process cost before the run created anything, " +
			"so a reader cannot tell the fitted fixed cost from the process's own")
	}
}

// TestPointsThatDoNotLieOnALineAreNotAMeasurement is the same run's resident
// series, which does not fit.
//
// Resident memory is heap plus whatever the collector has not returned, so it
// carries GOGC's headroom as well as the fleet. kcp's three loaded samples miss
// their own least-squares line by 7% of the range they span; its live heap
// misses by 1.4%. Reporting both as "resident bytes per cluster" and "heap
// bytes per cluster" in the same list, with no way to tell them apart, is how
// the 29-78% disagreements between distributions got published.
func TestPointsThatDoNotLieOnALineAreNotAMeasurement(t *testing.T) {
	r := &Report{Title: "50x10"}
	r.Add(sample("13 workspaces", 13, KcpName, "node-1", 6439, 1_410_078_400, 2_362_662_912, 0))
	r.Add(sample("25 workspaces", 25, KcpName, "node-1", 7061, 1_585_035_216, 3_013_373_952, 0))
	r.Add(sample("50 workspaces", 50, KcpName, "node-1", 8357, 1_916_582_032, 3_862_323_200, 0))

	if slope, ok := r.PerCluster(KcpName, Resident); ok {
		t.Errorf("resident bytes per cluster = %s was reported as a measurement, "+
			"though the three samples miss the line by 7%% of their own range",
			humanBytes(uint64(slope)))
	}
	// The same run's heap does fit, and is not withheld along with it.
	if _, ok := r.PerCluster(KcpName, HeapAlloc); !ok {
		t.Error("the heap fit was refused too: the check is rejecting the component, not the series")
	}
	// And the goroutine counts, which lie on their line to within a fifth of a
	// goroutine, are untouched.
	if _, ok := r.PerCluster(KcpName, Goroutines); !ok {
		t.Error("the goroutine fit was refused")
	}

	md := r.Markdown()
	if !strings.Contains(md, "do not lie on a line") {
		t.Errorf("the report does not say why the resident figure is missing:\n%s", md)
	}
}
