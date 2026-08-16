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
	"cmp"
	"maps"
	"slices"
	"sync"
)

const (
	// defaultTopN is how many workspaces are exported with a workspace label.
	// Small on purpose: this set exists to name outliers, and an operator
	// reading more than a handful of series is not diagnosing, they are
	// browsing. The long tail is covered by the remainder.
	defaultTopN = 20

	// defaultMaxFailureReasons bounds the distinct engagement-failure reasons
	// that get their own series. A reason is a label, so it carries the same
	// cardinality hazard the workspace label does; a reason derived from an
	// error string could otherwise be unbounded.
	defaultMaxFailureReasons = 20
)

// Options configures a Recorder.
type Options struct {
	// TopN is how many of the busiest workspaces are exported with a
	// workspace label. Zero means defaultTopN.
	TopN int

	// MaxFailureReasons bounds distinct engagement-failure reasons retained
	// as labels. Zero means defaultMaxFailureReasons.
	MaxFailureReasons int
}

// WorkspaceLoad is one workspace's contribution.
type WorkspaceLoad struct {
	Reconciles uint64
}

// Recorder attributes load to workspaces while bounding what is exported.
//
// Counters are kept for every engaged workspace, which is bounded by the
// process's capacity. What is capped is the *exported* set: aggregates always,
// the busiest TopN with a workspace label, and one remainder for the rest.
type Recorder struct {
	topN              int
	maxFailureReasons int

	mu       sync.RWMutex
	load     map[string]*WorkspaceLoad
	engaged  map[string]struct{}
	released uint64

	totalReconciles    uint64
	engagementFailures uint64
	failuresByReason   map[string]uint64

	// labelled is the workspace set currently exported with a label. It is
	// tracked rather than derived on demand so that displacement can be
	// observed and stale series released — see R7.
	labelled map[string]struct{}
}

// New returns a Recorder.
func New(opts Options) *Recorder {
	topN := opts.TopN
	if topN <= 0 {
		topN = defaultTopN
	}
	maxReasons := opts.MaxFailureReasons
	if maxReasons <= 0 {
		maxReasons = defaultMaxFailureReasons
	}
	return &Recorder{
		topN:              topN,
		maxFailureReasons: maxReasons,
		load:              map[string]*WorkspaceLoad{},
		engaged:           map[string]struct{}{},
		failuresByReason:  map[string]uint64{},
		labelled:          map[string]struct{}{},
	}
}

// Engaged records that a workspace has been wired.
func (r *Recorder) Engaged(workspace string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engaged[workspace] = struct{}{}
	r.ensureLoadLocked(workspace)
}

// Disengaged releases everything held for a workspace.
//
// This is FR-012 as it applies to telemetry: state that outlives the workspace
// it describes turns sustained bind/unbind churn into unbounded growth, and
// does so invisibly, because each individual leak is small.
func (r *Recorder) Disengaged(workspace string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.engaged, workspace)
	delete(r.load, workspace)
	if _, ok := r.labelled[workspace]; ok {
		delete(r.labelled, workspace)
		r.released++
	}
}

// EngagementFailed records a failed engagement attempt and its reason.
//
// A failed engagement is not an engaged workspace: the count an operator sizes
// against must not include workspaces that never wired.
func (r *Recorder) EngagementFailed(workspace, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.engagementFailures++

	// Retain the reason only while there is room. The total is always
	// counted, so capping labels loses attribution, never magnitude.
	if _, known := r.failuresByReason[reason]; known || len(r.failuresByReason) < r.maxFailureReasons {
		r.failuresByReason[reason]++
	}
}

// RecordReconcile attributes one reconcile to a workspace.
func (r *Recorder) RecordReconcile(workspace string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureLoadLocked(workspace).Reconciles++
	r.totalReconciles++
}

func (r *Recorder) ensureLoadLocked(workspace string) *WorkspaceLoad {
	l, ok := r.load[workspace]
	if !ok {
		l = &WorkspaceLoad{}
		r.load[workspace] = l
	}
	return l
}

// Snapshot returns a consistent view, recomputing which workspaces are
// exported with a label.
//
// The ranking is recomputed here rather than on every reconcile: attribution
// must not put per-event cost into the path this project is trying to make
// cheaper.
func (r *Recorder) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	ranked := slices.SortedFunc(maps.Keys(r.load), func(a, b string) int {
		// Compared rather than subtracted: these are uint64, and a difference
		// taken through int would wrap on large counters.
		if c := cmp.Compare(r.load[b].Reconciles, r.load[a].Reconciles); c != 0 {
			return c
		}
		// Ties broken by name so a snapshot is deterministic; otherwise map
		// ordering would make the exported set flap between calls.
		return cmp.Compare(a, b)
	})

	top := min(r.topN, len(ranked))
	nowLabelled := make(map[string]struct{}, top)
	for _, ws := range ranked[:top] {
		nowLabelled[ws] = struct{}{}
	}

	// Release series for workspaces that have dropped out. Without this the
	// bound holds only for the top N and the residue grows without limit.
	for ws := range r.labelled {
		if _, still := nowLabelled[ws]; !still {
			r.released++
		}
	}
	r.labelled = nowLabelled

	loads := make(map[string]WorkspaceLoad, len(r.load))
	for ws, l := range r.load {
		loads[ws] = *l
	}

	var labelledReconciles uint64
	for ws := range nowLabelled {
		labelledReconciles += loads[ws].Reconciles
	}

	return Snapshot{
		EngagedWorkspaces:          len(r.engaged),
		TotalReconciles:            r.totalReconciles,
		EngagementFailures:         r.engagementFailures,
		EngagementFailuresByReason: maps.Clone(r.failuresByReason),
		RemainderReconciles:        r.totalReconciles - labelledReconciles,
		RemainderWorkspaces:        len(loads) - len(nowLabelled),

		loads:    loads,
		labelled: slices.Sorted(maps.Keys(nowLabelled)),
		released: r.released,
	}
}

// Snapshot is a consistent view of attributed load at one instant.
type Snapshot struct {
	// EngagedWorkspaces is how many workspaces are currently wired.
	EngagedWorkspaces int
	// TotalReconciles is the process-wide count, carrying no workspace label.
	TotalReconciles uint64
	// EngagementFailures counts every failed engagement attempt.
	EngagementFailures uint64
	// EngagementFailuresByReason breaks failures down by a bounded reason set.
	EngagementFailuresByReason map[string]uint64
	// RemainderReconciles is load from workspaces outside the labelled set.
	RemainderReconciles uint64
	// RemainderWorkspaces is how many workspaces that remainder covers.
	RemainderWorkspaces int

	loads    map[string]WorkspaceLoad
	labelled []string
	released uint64
}

// Workspace returns one workspace's load. Absent workspaces report zero.
func (s Snapshot) Workspace(name string) WorkspaceLoad { return s.loads[name] }

// LabelledWorkspaces returns the workspaces exported with a workspace label,
// sorted. Its length is the exported cardinality, and is capped.
func (s Snapshot) LabelledWorkspaces() []string { return s.labelled }

// TrackedWorkspaces is how many workspaces have internal counters. Bounded by
// capacity rather than by TopN, and expected to reach zero under churn.
func (s Snapshot) TrackedWorkspaces() int { return len(s.loads) }

// Released counts series retired because a workspace was displaced from the
// labelled set or disengaged.
func (s Snapshot) Released() uint64 { return s.released }
