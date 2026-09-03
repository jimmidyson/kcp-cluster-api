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
	"strings"
	"testing"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func held(minutes int, goroutines int, heap uint64) deployedscale.Sample {
	return deployedscale.Sample{
		Label:      "soak",
		Workspaces: 50,
		Clusters:   50,
		Taken:      time.Date(2026, 9, 3, 12, minutes, 0, 0, time.UTC),
		Components: []deployedscale.ComponentSample{{
			Component: "capd-controller-manager",
			Process:   deployedscale.ProcessSample{Goroutines: goroutines, HeapAllocBytes: heap},
			Pod:       deployedscale.PodFacts{Ready: true},
		}},
	}
}

// TestASoakReportsDriftRatherThanAnEndpoint. Reaching a fleet and holding it are
// different questions, and a soak that reported only its last sample would
// answer the first one twice. What a reader needs is whether anything moved
// while nothing was being asked of it.
func TestASoakReportsDriftRatherThanAnEndpoint(t *testing.T) {
	// Held for an hour, heap climbing 20% with the fleet unchanged.
	drift := Drift([]deployedscale.Sample{
		held(0, 8300, 1_000_000_000),
		held(30, 8305, 1_100_000_000),
		held(60, 8310, 1_200_000_000),
	}, 50, 50)

	if drift.Duration != time.Hour {
		t.Errorf("duration = %v, want 1h", drift.Duration)
	}
	component, ok := drift.Component("capd-controller-manager")
	if !ok {
		t.Fatal("no drift for the component that was sampled")
	}
	if component.HeapGrowth < 0.19 || component.HeapGrowth > 0.21 {
		t.Errorf("heap growth = %v, want 0.20", component.HeapGrowth)
	}
	if !component.Drifted() {
		t.Error("a fifth more heap over an hour at a fixed fleet size was not called drift")
	}
	if !strings.Contains(drift.Describe(), "20") {
		t.Errorf("the summary does not carry the growth: %q", drift.Describe())
	}

	// A held fleet that did not move is the result worth having, and has to be
	// stated as one rather than as an absence of findings.
	steady := Drift([]deployedscale.Sample{
		held(0, 8300, 1_000_000_000),
		held(30, 8302, 1_004_000_000),
		held(60, 8301, 1_002_000_000),
	}, 50, 50)
	if c, _ := steady.Component("capd-controller-manager"); c.Drifted() {
		t.Errorf("a flat hour was reported as drift: %+v", c)
	}
	if !strings.Contains(steady.Describe(), "Held 50 clusters") {
		t.Errorf("a steady soak does not say so: %q", steady.Describe())
	}
}

// TestASoakThatLostClustersSaysSo. The fleet falling out of Ready while nothing
// is being asked of it is the most serious thing a soak can find, and it is not
// visible in any process metric.
func TestASoakThatLostClustersSaysSo(t *testing.T) {
	drift := Drift([]deployedscale.Sample{held(0, 8300, 1_000_000_000), held(60, 8300, 1_000_000_000)}, 50, 47)
	if drift.HeldFleet {
		t.Error("a soak that ended with 47 of 50 clusters ready reported the fleet held")
	}
	if !strings.Contains(drift.Describe(), "47 of 50") {
		t.Errorf("the summary does not say what was lost: %q", drift.Describe())
	}
}

// TestASoakNeedsMoreThanOneSample: two points make a difference, not a trend,
// but a soak is asked whether anything moved rather than how fast — so two is
// the minimum and one is not a soak at all.
func TestASoakNeedsMoreThanOneSample(t *testing.T) {
	drift := Drift([]deployedscale.Sample{held(0, 8300, 1_000_000_000)}, 50, 50)
	if _, ok := drift.Component("capd-controller-manager"); ok {
		t.Error("one sample produced a drift figure")
	}
	if !strings.Contains(drift.Describe(), "not measured") {
		t.Errorf("a soak with one sample does not say it measured nothing: %q", drift.Describe())
	}
}

// TestASoakSeesAnExcursionThatCameBack.
//
// The 1600-cluster soak reported "nothing drifted" while core's retained heap
// went 1.65 GB, 3.19, 3.46, 1.61, 1.60, 1.61 — it more than doubled in the
// middle and came home. First against last is blind to that, and a component
// that doubles under a held fleet is the most interesting thing a soak can
// find: it is the shape of a leak that gets collected, or of work arriving in
// bursts nobody asked for.
//
// So the peak is carried too, and a peak excursion counts as drift even when
// the endpoints agree.
func TestASoakSeesAnExcursionThatCameBack(t *testing.T) {
	samples := []deployedscale.Sample{
		held(0, 25138, 1_772_000_000),
		held(5, 25064, 1_793_000_000),
		held(10, 25060, 3_425_000_000),
		held(15, 25061, 3_715_000_000),
		held(20, 25061, 1_729_000_000),
		held(30, 25061, 1_729_000_000),
	}
	got := Drift(samples, 1600, 1600)
	if len(got.Components) != 1 {
		t.Fatalf("%d components, want 1", len(got.Components))
	}
	c := got.Components[0]

	// Endpoints agree, which is what made this invisible.
	if c.HeapGrowth > 0.05 || c.HeapGrowth < -0.05 {
		t.Fatalf("end-to-end heap growth = %.2f, the fixture is meant to come home", c.HeapGrowth)
	}
	if c.PeakHeapBytes != 3_715_000_000 {
		t.Errorf("peak heap = %d, want the 3.7 GB sample", c.PeakHeapBytes)
	}
	if c.PeakHeapGrowth < 1.0 || c.PeakHeapGrowth > 1.2 {
		t.Errorf("peak growth = %.2f, want about 1.1 (it more than doubled)", c.PeakHeapGrowth)
	}
	if !c.Drifted() {
		t.Error("a component whose heap doubled mid-soak was reported as not having drifted")
	}
	if d := got.Describe(); !strings.Contains(d, "peak") {
		t.Errorf("the soak's paragraph does not mention the excursion: %q", d)
	}
	if d := got.Describe(); strings.Contains(d, "Nothing drifted") {
		t.Errorf("the soak still says nothing drifted: %q", d)
	}
}

// TestASoakThatIsGenuinelyFlatStillSaysSo, so the peak does not turn ordinary
// sawtooth into a finding.
func TestASoakThatIsGenuinelyFlatStillSaysSo(t *testing.T) {
	samples := []deployedscale.Sample{
		held(0, 7065, 1_772_000_000),
		held(15, 7061, 1_815_000_000),
		held(30, 7059, 1_783_000_000),
	}
	got := Drift(samples, 400, 400)
	if got.Components[0].Drifted() {
		t.Errorf("a flat soak was reported as drifting: %+v", got.Components[0])
	}
	if d := got.Describe(); !strings.Contains(d, "Nothing drifted") {
		t.Errorf("a flat soak does not read as flat: %q", d)
	}
}
