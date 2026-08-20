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

package sweep_test

import (
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
)

func TestRetained(t *testing.T) {
	// A sweep up to three workspaces and back down. Two workspaces departed
	// between the two samples taken at one workspace, and the process is
	// holding six goroutines it did not hold the first time it served one.
	samples := []sweep.Sample{
		{Phase: sweep.PhaseBaseline, Workspaces: 0, Goroutines: 10},
		{Phase: sweep.PhaseActive, Workspaces: 1, Goroutines: 20},
		{Phase: sweep.PhaseActive, Workspaces: 2, Goroutines: 30},
		{Phase: sweep.PhaseActive, Workspaces: 3, Goroutines: 40},
		{Phase: sweep.PhaseDisengaged, Workspaces: 2, Goroutines: 33},
		{Phase: sweep.PhaseDisengaged, Workspaces: 1, Goroutines: 26},
		{Phase: sweep.PhaseDisengaged, Workspaces: 0, Goroutines: 11},
	}

	perDeparture, departed, ok := sweep.Retained(samples, sweep.Goroutines)
	if !ok {
		t.Fatal("Retained reported it could not answer from a sweep that goes up to three and back down")
	}
	if departed != 2 {
		t.Errorf("departed = %d, want 2: three workspaces were served and one remains at the comparison point", departed)
	}
	if want := 3.0; perDeparture != want {
		t.Errorf("perDeparture = %v, want %v: 26 goroutines serving one workspace against the 20 it took the first time", perDeparture, want)
	}
}

// The zero-workspace sample is deliberately not the comparison point: the last
// departure also stops the shared wildcard cache, which is a fixed cost coming
// off rather than a workspace's share. Reading it there would report a
// reclaim where there is a retention.
func TestRetainedIgnoresTheLastDeparture(t *testing.T) {
	samples := []sweep.Sample{
		{Phase: sweep.PhaseActive, Workspaces: 1, Goroutines: 100},
		{Phase: sweep.PhaseActive, Workspaces: 2, Goroutines: 110},
		{Phase: sweep.PhaseDisengaged, Workspaces: 1, Goroutines: 104},
		{Phase: sweep.PhaseDisengaged, Workspaces: 0, Goroutines: 20},
	}

	perDeparture, departed, ok := sweep.Retained(samples, sweep.Goroutines)
	if !ok {
		t.Fatal("Retained could not answer from a two-workspace sweep")
	}
	if departed != 1 || perDeparture != 4 {
		t.Errorf("Retained = (%v, %d), want (4, 1): the comparison is at one workspace, not at none", perDeparture, departed)
	}
}

func TestRetainedNeedsMoreThanOneWorkspace(t *testing.T) {
	// One workspace up and down. There is no point at which some workspaces
	// have departed and one remains, so the question cannot be asked.
	samples := []sweep.Sample{
		{Phase: sweep.PhaseActive, Workspaces: 1, Goroutines: 100},
		{Phase: sweep.PhaseDisengaged, Workspaces: 0, Goroutines: 20},
	}

	if _, _, ok := sweep.Retained(samples, sweep.Goroutines); ok {
		t.Error("Retained answered from a one-workspace sweep, which has no comparison to make")
	}
}

// The shape that failed in CI three times: a transient at one end of one
// comparison, reported as retention because there was only one comparison to
// make.
//
// The numbers are the real ones from the run that reported 3.0 — a fleet sweep
// of two workspaces, 488 goroutines serving one on the way up and 491 serving
// one on the way down. Sweeping a third workspace adds a second, independent
// pair, and taking the lowest estimate stops a transient in either pair from
// being the answer.
func TestRetainedIsNotDecidedByOneNoisyPair(t *testing.T) {
	samples := []sweep.Sample{
		{Phase: sweep.PhaseActive, Workspaces: 1, Goroutines: 488},
		{Phase: sweep.PhaseActive, Workspaces: 2, Goroutines: 554},
		{Phase: sweep.PhaseActive, Workspaces: 3, Goroutines: 620},
		// Clean: 554 serving two on the way down, exactly as on the way up.
		{Phase: sweep.PhaseDisengaged, Workspaces: 2, Goroutines: 554},
		// Noisy: three goroutines that are not retention.
		{Phase: sweep.PhaseDisengaged, Workspaces: 1, Goroutines: 491},
		{Phase: sweep.PhaseDisengaged, Workspaces: 0, Goroutines: 227},
	}

	perDeparture, _, ok := sweep.Retained(samples, sweep.Goroutines)
	if !ok {
		t.Fatal("Retained could not answer from a three-workspace sweep")
	}
	if perDeparture != 0 {
		t.Errorf("perDeparture = %v, want 0: the pair at two workspaces gave every goroutine back, so the three at one workspace are a transient rather than what every departure costs", perDeparture)
	}
}

// A real retention shows up in every pair, so the lowest estimate keeps it.
func TestRetainedKeepsACostEveryPairAgreesOn(t *testing.T) {
	samples := []sweep.Sample{
		{Phase: sweep.PhaseActive, Workspaces: 1, Goroutines: 100},
		{Phase: sweep.PhaseActive, Workspaces: 2, Goroutines: 120},
		{Phase: sweep.PhaseActive, Workspaces: 3, Goroutines: 140},
		{Phase: sweep.PhaseDisengaged, Workspaces: 2, Goroutines: 125}, // +5 over one departure
		{Phase: sweep.PhaseDisengaged, Workspaces: 1, Goroutines: 110}, // +10 over two
		{Phase: sweep.PhaseDisengaged, Workspaces: 0, Goroutines: 30},
	}

	perDeparture, _, ok := sweep.Retained(samples, sweep.Goroutines)
	if !ok {
		t.Fatal("Retained could not answer")
	}
	if want := 5.0; perDeparture != want {
		t.Errorf("perDeparture = %v, want %v: both pairs agree on five per departure", perDeparture, want)
	}
}

// The pair the figure came from is reported, so a failure can subtract their
// profiles rather than describing the end of the sweep.
func TestRetainedDetailNamesThePairItCompared(t *testing.T) {
	samples := []sweep.Sample{
		{Phase: sweep.PhaseActive, Workspaces: 1, Label: "1 active", Goroutines: 100},
		{Phase: sweep.PhaseActive, Workspaces: 2, Label: "2 active", Goroutines: 120},
		{Phase: sweep.PhaseActive, Workspaces: 3, Label: "3 active", Goroutines: 140},
		{Phase: sweep.PhaseDisengaged, Workspaces: 2, Label: "2 left", Goroutines: 121},
		{Phase: sweep.PhaseDisengaged, Workspaces: 1, Label: "1 left", Goroutines: 110},
	}

	r, ok := sweep.RetainedDetail(samples, sweep.Goroutines)
	if !ok {
		t.Fatal("RetainedDetail could not answer")
	}
	if r.Up.Label != "2 active" || r.Down.Label != "2 left" {
		t.Errorf("compared %q with %q, want the pair at two workspaces, which gave the lowest estimate", r.Up.Label, r.Down.Label)
	}
	if r.Departed != 1 {
		t.Errorf("Departed = %d, want 1", r.Departed)
	}
}
