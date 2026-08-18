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

package sweep

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Phase says what a sample was taken after, so a curve that goes up as
// workspaces are added can be read against the curve that should come back
// down as they leave.
type Phase string

const (
	// PhaseBaseline is the process before any workspace has engaged.
	PhaseBaseline Phase = "baseline"
	// PhaseEngaged is a workspace bound and wired, holding no objects: the
	// cost of merely being served.
	PhaseEngaged Phase = "engaged"
	// PhaseActive is a workspace whose objects are being reconciled.
	PhaseActive Phase = "active"
	// PhaseDisengaged is after a workspace has unbound.
	PhaseDisengaged Phase = "disengaged"
)

// Traffic is the derived view of [Counts] that the report shows: the
// quantities the conversion plan makes claims about.
type Traffic struct {
	// WatchStreams is the number of distinct watches opened — the O(types)
	// question. Re-establishing one does not increase it.
	WatchStreams int `json:"watchStreams"`
	// WildcardWatchStreams are watches spanning every workspace at once. The
	// design intends these to be the only ones.
	WildcardWatchStreams int `json:"wildcardWatchStreams"`
	// ScopedWatchStreams are watches addressed to a single workspace. The
	// design intends this to stay at zero: a per-workspace watch is the
	// multiplication onto the shard the wildcard cache exists to avoid.
	ScopedWatchStreams int `json:"scopedWatchStreams"`
	// Lists is every collection read, including an informer's initial one.
	Lists int `json:"lists"`
	// Discovery is every request for the API surface itself. A RESTMapper
	// built per workspace shows up here and nowhere else.
	Discovery int `json:"discovery"`
	// Total is every request of any kind.
	Total int `json:"total"`
}

// TrafficOf derives the reported quantities from raw counts.
func TrafficOf(counts Counts) Traffic {
	return Traffic{
		WatchStreams:         counts.DistinctStreams(IsWatch),
		WildcardWatchStreams: counts.DistinctStreams(And(IsWatch, IsWildcard)),
		ScopedWatchStreams:   counts.DistinctStreams(And(IsWatch, IsWorkspaceScoped)),
		Lists:                counts.Total(IsList),
		Discovery:            counts.Total(IsDiscovery),
		Total:                counts.Total(Any),
	}
}

// Sample is one observation: what the process cost at a point in the sweep.
//
// Traffic figures are cumulative since the counter was installed, because
// that is what they mean — a watch opened at the first workspace is still
// open at the fourth, and a discovery request paid once is paid.
type Sample struct {
	Phase Phase `json:"phase"`
	// Label describes the step this sample was taken after.
	Label string `json:"label"`
	// Workspaces is how many were engaged when it was taken.
	Workspaces int `json:"workspaces"`
	// Goroutines and HeapBytes are process state, sampled after a settling
	// period and a garbage collection — see [Take].
	Goroutines int    `json:"goroutines"`
	HeapBytes  uint64 `json:"heapBytes"`
	Traffic    `json:"traffic"`

	// StepSeconds is the wall clock from the previous sample to this one,
	// filled in by [Report.Add]. It is not a benchmark — it includes the
	// settling wait, and the work between two samples is whatever the sweep
	// did — but it answers a question the other figures cannot: whether
	// engaging the hundredth workspace takes longer than engaging the first.
	// A cost that is flat in memory and rising in time still fails to scale.
	StepSeconds float64 `json:"stepSeconds"`

	// Counts is the classification the traffic figures were derived from. It
	// is kept for assertions that need more detail than the summary, and is
	// not serialised: its keys are logical cluster names, which differ on
	// every run and would make two reports incomparable.
	Counts Counts `json:"-"`
}

// Take samples the process now.
//
// It runs the garbage collector first, twice: heap figures taken without one
// measure when a collection last happened rather than what is retained, and
// finalisers mean one cycle can leave reachable-but-dead memory behind. This
// makes the sample cost tens of milliseconds, which is irrelevant next to the
// seconds a workspace takes to engage.
func Take(phase Phase, label string, workspaces int, counter *Counter) Sample {
	runtime.GC()
	runtime.GC()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	counts := Counts{}
	if counter != nil {
		counts = counter.Snapshot()
	}

	return Sample{
		Phase:      phase,
		Label:      label,
		Workspaces: workspaces,
		Goroutines: runtime.NumGoroutine(),
		HeapBytes:  mem.HeapAlloc,
		Traffic:    TrafficOf(counts),
		Counts:     counts,
	}
}

// Settle waits for the goroutine count to stop moving, so that a sample
// describes a process that has finished reacting rather than one in the
// middle of it.
//
// Engaging a workspace is asynchronous well past the point where the wiring
// reports it done: informers start, a workqueue drains, connections are
// established. Sampling immediately would attribute whatever had not happened
// yet to the next workspace instead.
//
// It returns whether the count actually settled. A caller that gets false has
// measured a process still in motion and should say so rather than quietly
// reporting the number.
func Settle(quiet time.Duration, timeout time.Duration) bool {
	const interval = 100 * time.Millisecond

	deadline := time.Now().Add(timeout)
	stableFor := time.Duration(0)
	last := runtime.NumGoroutine()

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		current := runtime.NumGoroutine()
		if current == last {
			stableFor += interval
			if stableFor >= quiet {
				return true
			}
			continue
		}
		last = current
		stableFor = 0
	}
	return false
}

// PerWorkspace is the slope of measure against workspace count, by least
// squares: the marginal cost of one more workspace.
//
// It returns NaN when the samples cannot describe a slope — fewer than two
// points, or every point at the same workspace count — rather than a zero
// that would read as "costs nothing".
func PerWorkspace(samples []Sample, measure func(Sample) float64) float64 {
	n := float64(len(samples))
	if n < 2 {
		return math.NaN()
	}

	var sumX, sumY, sumXY, sumXX float64
	for _, s := range samples {
		x, y := float64(s.Workspaces), measure(s)
		sumX += x
		sumY += y
		sumXY += x * y
		sumXX += x * x
	}

	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return math.NaN()
	}
	return (n*sumXY - sumX*sumY) / denominator
}

// Goroutines and Heap are the measures a caller passes to [PerWorkspace].
func Goroutines(s Sample) float64 { return float64(s.Goroutines) }

// Heap is the retained heap in bytes.
func Heap(s Sample) float64 { return float64(s.HeapBytes) }

// InPhase returns the samples taken in the given phase, so a slope is
// computed over comparable points rather than across a teardown.
func InPhase(samples []Sample, phase Phase) []Sample {
	var out []Sample
	for _, s := range samples {
		if s.Phase == phase {
			out = append(out, s)
		}
	}
	return out
}

// Stream is one distinct request the process made, and how often it was
// repeated. An inventory of these is what turns "watches do not multiply" from
// a number into something a reviewer can check: every open stream, named.
type Stream struct {
	Verb     Verb   `json:"verb"`
	Cluster  string `json:"cluster"`
	Resource string `json:"resource"`
	Count    int    `json:"count"`
}

// Inventory lists the distinct requests matching p, in a stable order.
//
// rename maps logical cluster names to something readable and comparable
// between runs — kcp generates them, so a report that quoted them raw would
// differ from run to run in the one place a reader most wants to compare. Pass
// nil to leave them alone.
func Inventory(counts Counts, p Predicate, rename func(string) string) []Stream {
	var out []Stream
	for req, count := range counts {
		if !p(req) {
			continue
		}
		cluster := req.Cluster
		if rename != nil {
			cluster = rename(cluster)
		}
		out = append(out, Stream{Verb: req.Verb, Cluster: cluster, Resource: req.Resource, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Verb < out[j].Verb
	})
	return out
}

// Report is a whole sweep: what was measured, under what conditions.
type Report struct {
	Title string `json:"title"`
	// Facts record the conditions the numbers only mean anything under —
	// object counts, workspace counts, the kcp version, GOMAXPROCS. A figure
	// without them is not reproducible and should not be quoted.
	Facts   map[string]string `json:"facts,omitempty"`
	Samples []Sample          `json:"samples"`
	// Streams is the inventory of what was open at the end of the sweep, when
	// the process was serving the most workspaces it ever served. It is the
	// evidence behind the headline number, so that "watches did not multiply"
	// can be checked rather than believed.
	Streams []Stream `json:"streams,omitempty"`

	// lastAdd is when the previous sample was recorded, so each one can carry
	// how long the step that produced it took. Unexported: it is bookkeeping,
	// not a measurement, and has no place in the serialised report.
	lastAdd time.Time
}

// AddFact records one condition of the run.
func (r *Report) AddFact(key, value string) {
	if r.Facts == nil {
		r.Facts = map[string]string{}
	}
	r.Facts[key] = value
}

// Add appends a sample, timing it against the previous one.
//
// The first sample has no step to time and reports zero, rather than the age
// of the report, which would be the harness's own startup.
func (r *Report) Add(s Sample) {
	now := time.Now()
	if !r.lastAdd.IsZero() {
		s.StepSeconds = now.Sub(r.lastAdd).Seconds()
	}
	r.lastAdd = now
	r.Samples = append(r.Samples, s)
}

// Write renders the report to dir as name.md and name.json.
//
// Both, not one: the Markdown is what a reviewer reads in a pull request and
// what the design documentation quotes, and the JSON is what a later run is
// compared against without re-parsing prose.
func (r *Report) Write(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating the report directory %s: %w", dir, err)
	}

	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the report: %w", err)
	}
	jsonPath := filepath.Join(dir, name+".json")
	if err := os.WriteFile(jsonPath, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", jsonPath, err)
	}

	mdPath := filepath.Join(dir, name+".md")
	if err := os.WriteFile(mdPath, []byte(r.Markdown()), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", mdPath, err)
	}
	return nil
}

// Markdown renders the sweep as a table.
// Departure reports whether a measured cost stopped being linear within the
// swept range.
//
// Only samples taken while workspaces were being added are considered: the
// baseline is taken with the manager stopped and a phase that activates
// existing workspaces adds none, so including either would put two points at
// the same workspace count and make the projection meaningless.
func (r *Report) Departure(measure func(Sample) float64, opts DepartureOptions) Departure {
	scaling := make([]Sample, 0, len(r.Samples))
	seen := map[int]bool{}
	for _, s := range r.Samples {
		if s.Phase == PhaseBaseline || seen[s.Workspaces] {
			continue
		}
		seen[s.Workspaces] = true
		scaling = append(scaling, s)
	}
	return FindDeparture(PointsOf(scaling, measure), opts)
}

func (r *Report) Markdown() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", r.Title)

	if len(r.Facts) > 0 {
		// Sorted, because Go's map iteration is not: two runs of the same
		// sweep should differ in their numbers and nowhere else.
		keys := make([]string, 0, len(r.Facts))
		for key := range r.Facts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("| Condition | Value |\n|---|---|\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "| %s | %s |\n", key, r.Facts[key])
		}
		b.WriteString("\n")
	}

	b.WriteString("| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |\n")
	b.WriteString("|---|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, s := range r.Samples {
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %d (%d/%d) | %d | %d | %d | %.1fs |\n",
			s.Label, s.Workspaces, s.Goroutines, humanBytes(s.HeapBytes),
			s.WatchStreams, s.WildcardWatchStreams, s.ScopedWatchStreams,
			s.Lists, s.Discovery, s.Total, s.StepSeconds)
	}

	if len(r.Streams) > 0 {
		b.WriteString("\nEvery stream open at the widest point of the sweep:\n\n")
		b.WriteString("| Verb | Logical cluster | Resource | Requests |\n|---|---|---|--:|\n")
		for _, s := range r.Streams {
			fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", s.Verb, s.Cluster, s.Resource, s.Count)
		}
	}

	return b.String()
}

func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
