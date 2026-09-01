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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ComponentSample is one deployment at one moment: what its process says about
// itself, and what the cluster says about its container.
type ComponentSample struct {
	Component string        `json:"component"`
	Process   ProcessSample `json:"process"`
	Pod       PodFacts      `json:"pod"`
}

// Sample is the whole fleet at one moment.
type Sample struct {
	Label      string            `json:"label"`
	Workspaces int               `json:"workspaces"`
	Clusters   int               `json:"clusters"`
	Nodes      int               `json:"nodes"`
	Taken      time.Time         `json:"taken"`
	Components []ComponentSample `json:"components"`
}

// Component returns one component's part of the sample.
func (s Sample) Component(name string) (ComponentSample, bool) {
	for _, c := range s.Components {
		if c.Component == name {
			return c, true
		}
	}
	return ComponentSample{}, false
}

// Report is what a deployed run produces.
type Report struct {
	Title string            `json:"title"`
	Facts map[string]string `json:"facts,omitempty"`
	// Samples in the order they were taken.
	Samples []Sample `json:"samples"`
	// Reconciliations record how this run compares with an in-process run of
	// the same shape. Empty when none was asked for.
	Reconciliations []Reconciliation `json:"reconciliations,omitempty"`
}

// AddFact records one condition the numbers only mean anything under.
func (r *Report) AddFact(key, value string) {
	if r.Facts == nil {
		r.Facts = map[string]string{}
	}
	r.Facts[key] = value
}

// Add appends a sample.
func (r *Report) Add(s Sample) {
	if s.Taken.IsZero() {
		s.Taken = time.Now()
	}
	r.Samples = append(r.Samples, s)
}

// Placement describes where the run's components ran.
//
// It is a fact rather than a footnote: a run in which everything landed on one
// node measured a co-located deployment, whatever the manifests asked for, and
// reporting it as a multi-node figure would be the single most misleading
// thing this harness could do.
func (r *Report) Placement() (nodes []string, coLocated bool) {
	seen := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			if c.Pod.Node != "" {
				seen[c.Pod.Node] = true
			}
		}
	}
	for n := range seen {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	components := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			components[c.Component] = true
		}
	}
	// Co-located when more than one component shared the single node they all
	// ran on. One component cannot be spread, so it is not co-located either.
	return nodes, len(nodes) == 1 && len(components) > 1
}

// Disturbed reports components whose containers restarted during the run.
//
// A restart resets every process metric, so a run containing one has samples
// that are not comparable with each other however reasonable they look. This
// is reported rather than corrected: there is no honest correction.
func (r *Report) Disturbed() []ComponentSample {
	var out []ComponentSample
	for _, s := range r.Samples {
		for _, c := range s.Components {
			if !c.Pod.Comparable() {
				out = append(out, c)
			}
		}
	}
	return out
}

// PerWorkspace fits a least-squares slope of one measure against the workspace
// count, for one component.
//
// A negative slope is refused for the reason the in-process target run refuses
// it: least squares returns one whenever the noise in a quantity exceeds its
// signal across the swept range, and a negative cost per workspace is not a
// cheaper fleet.
func (r *Report) PerWorkspace(component string, measure func(ComponentSample) float64) (float64, bool) {
	return r.slope(component, measure, func(s Sample) float64 { return float64(s.Workspaces) })
}

// PerCluster is the same fit against cluster count, and on the evidence so far
// it is the one that predicts cost.
//
// # Why both are reported
//
// Twenty-five clusters were measured twice, as twenty-five workspaces of one
// and as five workspaces of five. Per workspace the two disagree wildly —
// core-manager at 17.0 against 77.0 — and a reader comparing those two reports
// would conclude that packing clusters into fewer workspaces is four times
// more expensive. Per cluster they agree: 17.0 against 15.4, and the control
// plane manager at 46.0 against 46.1.
//
// So the workspace grouping is close to free and the cluster count is what
// costs, which is exactly the question a fleet target asks: whether 200
// clusters in 200 workspaces differs from 200 in 20. Reporting only the
// per-workspace figure hides that behind an artefact of how the fleet was
// arranged.
func (r *Report) PerCluster(component string, measure func(ComponentSample) float64) (float64, bool) {
	return r.slope(component, measure, func(s Sample) float64 { return float64(s.Clusters) })
}

func (r *Report) slope(component string, measure func(ComponentSample) float64, x func(Sample) float64) (float64, bool) {
	var xs, ys []float64
	for _, s := range r.Samples {
		c, ok := s.Component(component)
		if !ok || !c.Pod.Comparable() {
			continue
		}
		xs = append(xs, x(s))
		ys = append(ys, measure(c))
	}
	// Three distinct sample points, not two.
	//
	// A two-point fit passes exactly through both points. Its residual is
	// identically zero whatever the data, so it offers no way at all to tell a
	// slope from the difference between two noisy samples — and goroutine counts
	// are noisy: in-flight requests, a watch reconnecting, a worker pool that
	// happens to be busy. A run at 1 and 2 workspaces reported 17.0 goroutines
	// per workspace from 416 and 433, a 4% swing on a 400-goroutine process,
	// and that number then disagreed 8.5x with a 61-sample in-process sweep. The
	// disagreement was about the fit, not about the fleet.
	//
	// So a run too small to resolve a slope reports no slope. Publishing one it
	// cannot support is the thing this repository has already decided is worse
	// than publishing nothing.
	if distinct(xs) < 3 {
		return 0, false
	}

	var sumX, sumY, sumXY, sumXX float64
	n := float64(len(xs))
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
		sumXY += xs[i] * ys[i]
		sumXX += xs[i] * xs[i]
	}
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		return 0, false
	}
	slope := (n*sumXY - sumX*sumY) / denominator
	if slope < 0 {
		return 0, false
	}
	return slope, true
}

// Goroutines and Resident are the measures PerWorkspace is usually asked for.
func Goroutines(c ComponentSample) float64 { return float64(c.Process.Goroutines) }
func Resident(c ComponentSample) float64   { return float64(c.Process.ResidentBytes) }
func HeapAlloc(c ComponentSample) float64  { return float64(c.Process.HeapAllocBytes) }

// Components lists every component that appears in the report, in the
// canonical order.
func (r *Report) Components() []string {
	seen := map[string]bool{}
	for _, s := range r.Samples {
		for _, c := range s.Components {
			seen[c.Component] = true
		}
	}
	var out []string
	for _, c := range Components() {
		if seen[c.Name] {
			out = append(out, c.Name)
			delete(seen, c.Name)
		}
	}
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// Write renders the report to dir as name.md and name.json, as the sweeps do.
func (r *Report) Write(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(r.Markdown()), 0o600); err != nil {
		return fmt.Errorf("writing the markdown report: %w", err)
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the json report: %w", err)
	}
	return nil
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", r.Title)

	nodes, coLocated := r.Placement()
	fmt.Fprintf(&b, "| Condition | Value |\n|---|---|\n")
	keys := make([]string, 0, len(r.Facts))
	for k := range r.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "| %s | %s |\n", k, r.Facts[k])
	}
	fmt.Fprintf(&b, "| nodes | %s |\n", strings.Join(nodes, ", "))
	// Stated in the report itself rather than left for a reader to work out
	// from the node list.
	if coLocated {
		fmt.Fprintf(&b, "| placement | **co-located: every component ran on one node, so this is not a multi-node figure** |\n")
	} else if len(nodes) > 1 {
		fmt.Fprintf(&b, "| placement | spread across %d nodes |\n", len(nodes))
	}
	b.WriteString("\n")

	if disturbed := r.Disturbed(); len(disturbed) > 0 {
		b.WriteString("> **A container restarted during this run.** Every process metric resets when the\n")
		b.WriteString("> process does, so the samples below are not comparable with each other:\n>\n")
		seen := map[string]bool{}
		for _, c := range disturbed {
			if seen[c.Component] {
				continue
			}
			seen[c.Component] = true
			reason := c.Pod.LastReason
			if reason == "" {
				reason = "unknown reason"
			}
			fmt.Fprintf(&b, "> - `%s` restarted %d time(s), last: %s\n", c.Component, c.Pod.RestartCount, reason)
		}
		b.WriteString("\n")
	}

	for _, component := range r.Components() {
		fmt.Fprintf(&b, "## %s\n\n", component)
		b.WriteString("| Step | Workspaces | Clusters | Goroutines | Heap | Resident | RSS/heap | CPU | Node |\n")
		b.WriteString("|---|--:|--:|--:|--:|--:|--:|--:|---|\n")
		for _, s := range r.Samples {
			c, ok := s.Component(component)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %.2fx | %.1fs | %s |\n",
				s.Label, s.Workspaces, s.Clusters,
				c.Process.Goroutines, humanBytes(c.Process.HeapAllocBytes), humanBytes(c.Process.ResidentBytes),
				c.Process.ResidentToHeapRatio(), c.Process.CPUSeconds, c.Pod.Node)
		}
		b.WriteString("\n")

		if slope, ok := r.PerWorkspace(component, Goroutines); ok {
			fmt.Fprintf(&b, "- goroutines per workspace: **%.1f**\n", slope)
		} else {
			b.WriteString("- goroutines per workspace: " + notMeasured + "\n")
		}
		if slope, ok := r.PerWorkspace(component, Resident); ok {
			fmt.Fprintf(&b, "- resident bytes per workspace: **%s**\n", humanBytes(uint64(slope)))
		} else {
			b.WriteString("- resident bytes per workspace: " + notMeasured + "\n")
		}
		// Per cluster as well as per workspace. See PerCluster: the two agree
		// only when a workspace holds one cluster, and it is the per-cluster
		// figure that has held across every distribution measured so far.
		if slope, ok := r.PerCluster(component, Goroutines); ok {
			fmt.Fprintf(&b, "- goroutines per cluster: **%.1f**\n", slope)
		}
		if slope, ok := r.PerCluster(component, Resident); ok {
			fmt.Fprintf(&b, "- resident bytes per cluster: **%s**\n", humanBytes(uint64(slope)))
		}
		if !monotonic(r, component, HeapAlloc) {
			b.WriteString("- " + heapWobble + "\n")
		}
		b.WriteString("\n")
	}

	if len(r.Reconciliations) > 0 {
		b.WriteString("## Reconciliation with the in-process instrument\n\n")
		b.WriteString("| Quantity | Deployed | In process | Ratio | Within tolerance |\n|---|--:|--:|--:|---|\n")
		for _, rec := range r.Reconciliations {
			verdict := yesNo(rec.WithinTolerance)
			if !rec.Comparable {
				verdict = "**not a like-for-like comparison**"
			}
			fmt.Fprintf(&b, "| %s | %.1f | %.1f | %.2fx | %s |\n",
				rec.Quantity, rec.Deployed, rec.InProcess, rec.Ratio, verdict)
		}
		b.WriteString("\n")
		for _, rec := range r.Reconciliations {
			if !rec.Comparable && rec.Why != "" {
				b.WriteString("- " + rec.Why + "\n")
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "**no**"
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// SortedNodes is the set of nodes a sample's components ran on.
func (s Sample) SortedNodes() []string {
	seen := map[string]bool{}
	for _, c := range s.Components {
		if c.Pod.Node != "" {
			seen[c.Pod.Node] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// notMeasured is what a report says instead of a per-workspace figure it cannot
// support. See PerWorkspace for why three points rather than two.
const notMeasured = "not measured (a slope needs at least three distinct workspace counts; " +
	"a fit through two points has no residual and so cannot be told from noise)"

// distinct counts how many different values appear in xs.
func distinct(xs []float64) int {
	seen := make(map[float64]struct{}, len(xs))
	for _, x := range xs {
		seen[x] = struct{}{}
	}
	return len(seen)
}

// heapWobble is the caveat on the memory figures, printed when a run's own heap
// series says they cannot be trusted as a cost per unit of fleet.
//
// # Why the memory slopes get a caveat the goroutine ones do not
//
// Goroutine counts rise monotonically with fleet size in every run measured so
// far, and their slopes reproduce: twenty-five clusters arranged as 25x1 and as
// 5x5 agree to 1.6% in total, and the control plane manager to 0.3%.
//
// The memory slopes do not. The same two runs disagree by 29%, 15%, 78% and 76%
// per component. The reason is in each run's own heap column: it does not climb
// with the fleet, it wanders — the dev infrastructure manager sampled 26.3, 19.0
// and 44.5 MiB in one sweep and 20.4, 27.3 and 86.9 MiB in the other. A sample
// is taken whenever a checkpoint is reached, which is whenever it is reached
// relative to a garbage collection, and a line fitted through that measures when
// the collector last ran as much as it measures the fleet.
//
// The figures stay: resident memory is what a limit is set against, and a wide
// figure is more use than none. What they do not get is the same standing as a
// count that reproduces.
const heapWobble = "**The memory figures above are weaker than the goroutine ones.** This run's heap did not " +
	"climb monotonically with the fleet (see the Heap column), so a slope through it is fitted partly to " +
	"garbage collection timing. Per-cluster memory has not reproduced across fleet distributions; " +
	"per-cluster goroutines has."

// monotonic reports whether a component's measure is non-decreasing across the
// run's comparable samples, which is the cheapest available signal that a slope
// through it describes the fleet rather than the runtime's timing.
func monotonic(r *Report, component string, measure func(ComponentSample) float64) bool {
	var last float64
	first := true
	for _, s := range r.Samples {
		c, ok := s.Component(component)
		if !ok || !c.Pod.Comparable() {
			continue
		}
		v := measure(c)
		if !first && v < last {
			return false
		}
		last, first = v, false
	}
	return true
}
