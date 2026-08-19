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
