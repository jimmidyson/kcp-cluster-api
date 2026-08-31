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
	var xs, ys []float64
	for _, s := range r.Samples {
		c, ok := s.Component(component)
		if !ok || !c.Pod.Comparable() {
			continue
		}
		xs = append(xs, float64(s.Workspaces))
		ys = append(ys, measure(c))
	}
	if len(xs) < 2 {
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
			b.WriteString("- goroutines per workspace: not measured (fewer than two comparable samples, or the fit did not resolve)\n")
		}
		if slope, ok := r.PerWorkspace(component, Resident); ok {
			fmt.Fprintf(&b, "- resident bytes per workspace: **%s**\n", humanBytes(uint64(slope)))
		} else {
			b.WriteString("- resident bytes per workspace: not measured (fewer than two comparable samples, or the fit did not resolve)\n")
		}
		b.WriteString("\n")
	}

	if len(r.Reconciliations) > 0 {
		b.WriteString("## Reconciliation with the in-process instrument\n\n")
		b.WriteString("| Quantity | Deployed | In process | Ratio | Within tolerance |\n|---|--:|--:|--:|---|\n")
		for _, rec := range r.Reconciliations {
			fmt.Fprintf(&b, "| %s | %.1f | %.1f | %.2fx | %s |\n",
				rec.Quantity, rec.Deployed, rec.InProcess, rec.Ratio, yesNo(rec.WithinTolerance))
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
