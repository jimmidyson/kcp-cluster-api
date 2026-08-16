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

package scaleharness

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
)

func sampleRun() SweepRun {
	return SweepRun{
		Service: "configmaps",
		Profile: IdleHeavy(),
		Mode:    ModeSynthetic,
		Points: []Point{
			{Workspaces: 8, Value: 80},
			{Workspaces: 16, Value: 160},
			{Workspaces: 32, Value: 320},
			{Workspaces: 64, Value: 1280},
		},
		Departure: FindDeparture([]Point{
			{Workspaces: 8, Value: 80},
			{Workspaces: 16, Value: 160},
			{Workspaces: 32, Value: 320},
			{Workspaces: 64, Value: 1280},
		}, DepartureOptions{Tolerance: 0.25}),
	}
}

// FR-039 and SC-020. Synthetic load can under-measure — generated objects may
// fail validation or take cheap error paths — and an under-measured memory
// figure becomes an under-provisioned limit. So the mode travels with the
// number rather than being metadata somebody may drop.
func TestEveryRunCarriesItsLoadMode(t *testing.T) {
	run := sampleRun()
	if run.Mode == "" {
		t.Fatal("run has no load mode")
	}

	blob, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), string(ModeSynthetic)) {
		t.Errorf("serialised run does not carry its mode: %s", blob)
	}
}

func TestRunWithoutAModeIsRejected(t *testing.T) {
	run := sampleRun()
	run.Mode = ""

	if err := run.Validate(); err == nil {
		t.Error("Validate accepted a run with no load mode; an unlabelled figure must not be usable for sizing")
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	run := sampleRun()
	run.Mode = "vibes"

	if err := run.Validate(); err == nil {
		t.Error("Validate accepted an unrecognised load mode")
	}
}

// The three outcomes come from internal/verify rather than a second
// convention, so `task verify` and `task test:scale` are read the same way.
func TestOutcomeUsesTheExistingContract(t *testing.T) {
	run := sampleRun()
	if got := run.Outcome(); got != verify.OutcomePass {
		t.Errorf("a completed sweep with a departure point reported %v, want pass", got)
	}

	noDeparture := sampleRun()
	noDeparture.Points = noDeparture.Points[:2]
	noDeparture.Departure = FindDeparture(noDeparture.Points, DepartureOptions{Tolerance: 0.25})
	if got := noDeparture.Outcome(); got != verify.OutcomeCouldNotRun {
		t.Errorf("a sweep too short to establish a departure point reported %v, want could-not-run", got)
	}
	if got := noDeparture.Outcome().String(); got != "could not run" {
		t.Errorf("outcome renders as %q, want the existing wording", got)
	}
}

// A sweep that ran and found cost linear is a real result, not a failure and
// not an inability to measure: it says capacity is above the swept range.
func TestLinearSweepIsAPass(t *testing.T) {
	run := sampleRun()
	run.Points = []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 320},
		{Workspaces: 64, Value: 640},
	}
	run.Departure = FindDeparture(run.Points, DepartureOptions{Tolerance: 0.25})

	if run.Departure.Found {
		t.Fatal("test setup is wrong: this series is linear")
	}
	if got := run.Outcome(); got != verify.OutcomePass {
		t.Errorf("a linear sweep reported %v, want pass", got)
	}
}

// An extrapolated figure has to say so, or a projection is read as a
// measurement — which is how a sizing table acquires numbers nobody took.
func TestExtrapolationIsFlagged(t *testing.T) {
	run := sampleRun()
	if run.Extrapolated(64) {
		t.Error("a workspace count inside the swept range was reported as extrapolated")
	}
	if !run.Extrapolated(1024) {
		t.Error("a workspace count far above the swept range was not reported as extrapolated")
	}
}

func TestSummaryNamesServiceProfileAndMode(t *testing.T) {
	got := sampleRun().Summary()
	for _, want := range []string{"configmaps", "idle-heavy", string(ModeSynthetic)} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q does not mention %q", got, want)
		}
	}
}
