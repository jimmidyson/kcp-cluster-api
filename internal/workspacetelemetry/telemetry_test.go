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

package workspacetelemetry

import (
	"fmt"
	"testing"
)

// The load this package must attribute is per-workspace; the series it exports
// must not grow with workspace count. These tests hold both halves at once,
// because either alone is easy and the pair is the requirement.

func TestRecorderAttributesLoadToItsWorkspace(t *testing.T) {
	r := New(Options{TopN: 3})

	r.Engaged("alpha")
	r.Engaged("beta")
	r.RecordReconcile("alpha")
	r.RecordReconcile("alpha")
	r.RecordReconcile("beta")

	snap := r.Snapshot()

	if got := snap.Workspace("alpha").Reconciles; got != 2 {
		t.Errorf("alpha reconciles = %d, want 2", got)
	}
	if got := snap.Workspace("beta").Reconciles; got != 1 {
		t.Errorf("beta reconciles = %d, want 1", got)
	}
	if got := snap.TotalReconciles; got != 3 {
		t.Errorf("total reconciles = %d, want 3", got)
	}
	if got := snap.EngagedWorkspaces; got != 2 {
		t.Errorf("engaged = %d, want 2", got)
	}
}

// The requirement is bounded *exported* series, not bounded internal tracking:
// internal counters are bounded by capacity, and losing the long tail entirely
// would leave nothing to rank.
func TestExportedSeriesDoNotGrowWithWorkspaceCount(t *testing.T) {
	const topN = 5

	for _, workspaces := range []int{10, 100, 1000} {
		t.Run(fmt.Sprintf("workspaces=%d", workspaces), func(t *testing.T) {
			r := New(Options{TopN: topN})
			for i := range workspaces {
				ws := fmt.Sprintf("ws-%04d", i)
				r.Engaged(ws)
				// Ascending load, so the busiest are the highest-numbered.
				for range i + 1 {
					r.RecordReconcile(ws)
				}
			}

			labelled := r.Snapshot().LabelledWorkspaces()
			if len(labelled) != topN {
				t.Errorf("labelled series = %d, want %d — exported cardinality must not grow with workspace count", len(labelled), topN)
			}
		})
	}
}

func TestBusiestWorkspacesAreTheLabelledOnes(t *testing.T) {
	r := New(Options{TopN: 2})

	for _, tc := range []struct {
		ws     string
		amount int
	}{
		{"quiet", 1},
		{"loudest", 100},
		{"middling", 10},
		{"second-loudest", 50},
	} {
		r.Engaged(tc.ws)
		for range tc.amount {
			r.RecordReconcile(tc.ws)
		}
	}

	labelled := r.Snapshot().LabelledWorkspaces()
	want := map[string]bool{"loudest": true, "second-loudest": true}
	for _, ws := range labelled {
		if !want[ws] {
			t.Errorf("labelled %q, want only the busiest two %v — a hot workspace that is not labelled cannot be diagnosed", ws, want)
		}
	}
	if len(labelled) != 2 {
		t.Fatalf("labelled %d workspaces, want 2", len(labelled))
	}
}

// Everything outside the top N still has to be *accounted for*, or the totals
// silently disagree with the sum of what is exported.
func TestRemainderAccountsForWorkspacesOutsideTopN(t *testing.T) {
	r := New(Options{TopN: 2})

	for i := range 10 {
		ws := fmt.Sprintf("ws-%d", i)
		r.Engaged(ws)
		for range i + 1 {
			r.RecordReconcile(ws)
		}
	}

	snap := r.Snapshot()

	var labelledTotal uint64
	for _, ws := range snap.LabelledWorkspaces() {
		labelledTotal += snap.Workspace(ws).Reconciles
	}

	if labelledTotal+snap.RemainderReconciles != snap.TotalReconciles {
		t.Errorf("labelled (%d) + remainder (%d) = %d, want total %d — the remainder must account for the tail",
			labelledTotal, snap.RemainderReconciles, labelledTotal+snap.RemainderReconciles, snap.TotalReconciles)
	}
	if snap.RemainderWorkspaces != 8 {
		t.Errorf("remainder covers %d workspaces, want 8", snap.RemainderWorkspaces)
	}
}

// R7's recorded consequence: a series for a workspace that has dropped out of
// the top N must stop being exported, or "bounded" degrades into "bounded in
// the top N, unbounded in the residue".
func TestDisplacedWorkspaceStopsBeingLabelled(t *testing.T) {
	r := New(Options{TopN: 1})

	r.Engaged("early")
	for range 10 {
		r.RecordReconcile("early")
	}
	if got := r.Snapshot().LabelledWorkspaces(); len(got) != 1 || got[0] != "early" {
		t.Fatalf("labelled = %v, want [early]", got)
	}

	r.Engaged("later")
	for range 100 {
		r.RecordReconcile("later")
	}

	labelled := r.Snapshot().LabelledWorkspaces()
	if len(labelled) != 1 || labelled[0] != "later" {
		t.Fatalf("labelled = %v, want [later] — the displaced workspace must stop being exported", labelled)
	}
	if r.Snapshot().Released() == 0 {
		t.Error("no series were released when a workspace was displaced; stale series would accumulate")
	}
}

// FR-012: disengagement must release everything engagement acquired, telemetry
// series included, or sustained churn grows the process without bound.
func TestDisengagementReleasesWorkspaceState(t *testing.T) {
	r := New(Options{TopN: 5})

	for i := range 50 {
		ws := fmt.Sprintf("churn-%d", i)
		r.Engaged(ws)
		r.RecordReconcile(ws)
		r.Disengaged(ws)
	}

	snap := r.Snapshot()
	if got := snap.EngagedWorkspaces; got != 0 {
		t.Errorf("engaged = %d after all disengaged, want 0", got)
	}
	if got := snap.TrackedWorkspaces(); got != 0 {
		t.Errorf("tracked = %d after churn, want 0 — per-workspace state must not survive disengagement", got)
	}
	if got := len(snap.LabelledWorkspaces()); got != 0 {
		t.Errorf("labelled = %d after churn, want 0", got)
	}
}

// Engagement progress and failure are part of what an operator needs to size a
// deployment and to see a workspace that is failing to wire (FR-018, FR-007).
func TestEngagementOutcomesAreCounted(t *testing.T) {
	r := New(Options{TopN: 5})

	r.Engaged("ok")
	r.EngagementFailed("bad", "cache-sync-timeout")
	r.EngagementFailed("bad", "cache-sync-timeout")
	r.EngagementFailed("other", "discovery-error")

	snap := r.Snapshot()
	if got := snap.EngagementFailures; got != 3 {
		t.Errorf("engagement failures = %d, want 3", got)
	}
	if got := snap.EngagementFailuresByReason["cache-sync-timeout"]; got != 2 {
		t.Errorf("cache-sync-timeout failures = %d, want 2", got)
	}
	if got := snap.EngagedWorkspaces; got != 1 {
		t.Errorf("engaged = %d, want 1 — a failed engagement is not an engaged workspace", got)
	}
}

// Failure reasons are a label, so they carry the same cardinality hazard the
// workspace label does: unbounded reasons would reintroduce the problem this
// package exists to avoid.
func TestFailureReasonCardinalityIsBounded(t *testing.T) {
	r := New(Options{TopN: 5, MaxFailureReasons: 3})

	for i := range 100 {
		r.EngagementFailed("ws", fmt.Sprintf("reason-%d", i))
	}

	snap := r.Snapshot()
	if got := len(snap.EngagementFailuresByReason); got > 3 {
		t.Errorf("distinct failure reasons = %d, want at most 3", got)
	}
	if got := snap.EngagementFailures; got != 100 {
		t.Errorf("total failures = %d, want 100 — capping labels must not lose the count", got)
	}
}

// The capacity surface (FR-028/FR-032) reads totals, so a snapshot must be safe
// to take while load is being recorded.
func TestSnapshotIsSafeUnderConcurrentRecording(t *testing.T) {
	r := New(Options{TopN: 5})
	for i := range 20 {
		r.Engaged(fmt.Sprintf("ws-%d", i))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			r.RecordReconcile("ws-1")
		}
	}()
	for range 1000 {
		_ = r.Snapshot().TotalReconciles
	}
	<-done

	if got := r.Snapshot().Workspace("ws-1").Reconciles; got != 1000 {
		t.Errorf("reconciles = %d, want 1000", got)
	}
}
