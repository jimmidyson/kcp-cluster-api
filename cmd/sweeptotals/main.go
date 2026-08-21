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

// Command sweeptotals adds up what an installation pays per workspace, one
// provider deployment at a time.
//
// Cluster API runs one process per provider, so a workspace is engaged by each
// of them and the cost of serving it is the sum. A single aggregate number
// cannot say that: it hides which deployment a regression is in, and it hides
// a deployment that is not scaling behind three that are. This reads the
// per-deployment sweep reports and prints them side by side, with the total
// underneath.
//
// It is arithmetic over evidence, not a measurement. It needs no kcp and runs
// in milliseconds, so a figure can be re-derived or corrected without
// re-running a sweep — the same separation `task scale:model` keeps.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
)

// deploymentNameFact is the fact a sweep sets to say which deployment it
// measured. A report without it is some other shape — the single-type floor,
// or the whole fleet co-located — and is not part of a total.
const deploymentNameFact = "deploymentName"

func main() {
	dir := flag.String("reports", "bin", "Directory holding the sweep report JSON files.")
	out := flag.String("out", "", "Where to write the totals report. Empty writes <reports>/sweep-report-total.md.")
	require := flag.String("require", "core-manager,kubeadm-bootstrap-manager,kubeadm-control-plane-manager,dev-infrastructure-manager,workspace-manager",
		"Comma-separated deployments that must be present. A total missing one of them is not a total.")
	flag.Parse()

	if err := run(*dir, *out, splitAndTrim(*require)); err != nil {
		fmt.Fprintf(os.Stderr, "sweeptotals: %v\n", err)
		os.Exit(1)
	}
}

// deployment is one provider's measured per-workspace cost.
type deployment struct {
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Measured time.Time `json:"measured"`
	Source   string    `json:"source"`

	Workspaces int `json:"workspaces"`

	// FixedWatchStreams is what this deployment holds open on the shard
	// regardless of how many workspaces it serves. It is per deployment rather
	// than per workspace, which is exactly why it belongs in a total: four
	// deployments watch Cluster four times where one export would have watched
	// it once.
	FixedWatchStreams int `json:"fixedWatchStreams"`

	GoroutinesPerWorkspace   float64 `json:"goroutinesPerWorkspace"`
	WatchStreamsPerWorkspace float64 `json:"watchStreamsPerWorkspace"`
	DiscoveryPerWorkspace    float64 `json:"discoveryRequestsPerWorkspace"`
	RequestsPerWorkspace     float64 `json:"requestsPerWorkspace"`
	HeapBytesPerWorkspace    float64 `json:"heapBytesPerWorkspace"`

	// RetainedPerDeparture is what a departed workspace does not give back.
	// Absent when the sweep was too narrow to ask.
	RetainedPerDeparture  float64 `json:"retainedGoroutinesPerDepartedWorkspace"`
	RetainedMeasured      bool    `json:"retainedMeasured"`
	GoMaxProcs, GoVersion string  `json:"-"`
}

type totals struct {
	Deployments []deployment      `json:"deployments"`
	Total       deployment        `json:"total"`
	Missing     []string          `json:"missing,omitempty"`
	Warnings    []string          `json:"warnings,omitempty"`
	Conditions  map[string]string `json:"conditions,omitempty"`
}

func run(dir, out string, required []string) error {
	found, err := load(dir)
	if err != nil {
		return err
	}

	t := totals{Conditions: map[string]string{}}
	for _, d := range found {
		t.Deployments = append(t.Deployments, d)
	}
	sort.Slice(t.Deployments, func(i, j int) bool { return t.Deployments[i].Name < t.Deployments[j].Name })

	byName := map[string]bool{}
	for _, d := range t.Deployments {
		byName[d.Name] = true
	}
	for _, name := range required {
		if !byName[name] {
			t.Missing = append(t.Missing, name)
		}
	}

	// A total whose parts were measured under different conditions is an
	// addition of things that are not comparable. Say so rather than print it
	// as though it were one run.
	procs, versions := map[string]bool{}, map[string]bool{}
	for _, d := range t.Deployments {
		procs[d.GoMaxProcs] = true
		versions[d.GoVersion] = true
	}
	if len(procs) > 1 {
		t.Warnings = append(t.Warnings, "the reports were measured at different GOMAXPROCS, so these figures are not directly addable")
	}
	if len(versions) > 1 {
		t.Warnings = append(t.Warnings, "the reports were measured on different Go versions, so these figures are not directly addable")
	}
	if len(t.Deployments) > 0 {
		t.Conditions["goMaxProcs"] = t.Deployments[0].GoMaxProcs
		t.Conditions["goVersion"] = t.Deployments[0].GoVersion
	}
	if spread := measurementSpread(t.Deployments); spread > 24*time.Hour {
		t.Warnings = append(t.Warnings, fmt.Sprintf(
			"the oldest and newest report are %s apart: one of them may describe wiring the others do not have",
			spread.Round(time.Hour)))
	}

	t.Total = sum(t.Deployments)

	path := out
	if path == "" {
		path = filepath.Join(dir, "sweep-report-total.md")
	}
	if err := os.WriteFile(path, []byte(markdown(t)), 0o644); err != nil { //nolint:gosec // a report is meant to be readable.
		return fmt.Errorf("writing %s: %w", path, err)
	}
	jsonPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	encoded, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the totals: %w", err)
	}
	if err := os.WriteFile(jsonPath, append(encoded, '\n'), 0o644); err != nil { //nolint:gosec // a report is meant to be readable.
		return fmt.Errorf("writing %s: %w", jsonPath, err)
	}

	fmt.Print(markdown(t))
	fmt.Fprintf(os.Stderr, "\nwritten to %s and %s\n", path, jsonPath)

	if len(t.Missing) > 0 {
		return fmt.Errorf("no report for %s: a total missing a deployment is not a total, so nothing here should be quoted as one",
			strings.Join(t.Missing, ", "))
	}
	return nil
}

// load reads every sweep report in dir that names the deployment it measured.
func load(dir string) ([]deployment, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "sweep-report*.json"))
	if err != nil {
		return nil, fmt.Errorf("looking for reports in %s: %w", dir, err)
	}

	deployments := make([]deployment, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // the path came from a glob of a directory the caller named.
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var report sweep.Report
		if err := json.Unmarshal(raw, &report); err != nil {
			// Not every sweep-report*.json is a report — this tool writes one
			// itself — so a file that does not parse as one is skipped rather
			// than failed on.
			continue
		}
		name := report.Facts[deploymentNameFact]
		if name == "" {
			continue
		}

		d, err := measure(name, report)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		d.Source = filepath.Base(path)
		if info, err := os.Stat(path); err == nil {
			d.Measured = info.ModTime()
		}
		deployments = append(deployments, d)
	}
	return deployments, nil
}

// measure derives one deployment's per-workspace figures from its samples.
//
// From the samples rather than from the facts the report also carries: the
// samples are the measurement and the facts are a rendering of it, and a total
// built by re-parsing rendered strings would drift from the thing it claims to
// add up.
func measure(name string, report sweep.Report) (deployment, error) {
	active := sweep.InPhase(report.Samples, sweep.PhaseActive)
	if len(active) == 0 {
		return deployment{}, fmt.Errorf("the report for %s has no active samples", name)
	}
	peak := active[len(active)-1]

	d := deployment{
		Name:              name,
		Title:             report.Title,
		Workspaces:        peak.Workspaces,
		FixedWatchStreams: peak.WatchStreams,
		GoMaxProcs:        report.Facts["goMaxProcs"],
		GoVersion:         report.Facts["goVersion"],

		GoroutinesPerWorkspace:   sweep.PerWorkspace(active, sweep.Goroutines),
		WatchStreamsPerWorkspace: sweep.PerWorkspace(active, func(s sweep.Sample) float64 { return float64(s.WatchStreams) }),
		DiscoveryPerWorkspace:    sweep.PerWorkspace(active, func(s sweep.Sample) float64 { return float64(s.Discovery) }),
		RequestsPerWorkspace:     sweep.PerWorkspace(active, func(s sweep.Sample) float64 { return float64(s.Total) }),
		HeapBytesPerWorkspace:    sweep.PerWorkspace(active, sweep.Heap),
	}
	if retained, _, ok := sweep.Retained(report.Samples, sweep.Goroutines); ok {
		d.RetainedPerDeparture, d.RetainedMeasured = retained, true
	}
	return d, nil
}

// sum adds the deployments up. What an installation pays per workspace is the
// sum of what each process it runs pays, because each of them engages the
// workspace separately.
func sum(deployments []deployment) deployment {
	total := deployment{Name: "TOTAL", RetainedMeasured: true}
	for _, d := range deployments {
		total.FixedWatchStreams += d.FixedWatchStreams
		total.GoroutinesPerWorkspace += d.GoroutinesPerWorkspace
		total.WatchStreamsPerWorkspace += d.WatchStreamsPerWorkspace
		total.DiscoveryPerWorkspace += d.DiscoveryPerWorkspace
		total.RequestsPerWorkspace += d.RequestsPerWorkspace
		total.HeapBytesPerWorkspace += d.HeapBytesPerWorkspace
		total.RetainedPerDeparture += d.RetainedPerDeparture
		if !d.RetainedMeasured {
			total.RetainedMeasured = false
		}
	}
	return total
}

func measurementSpread(deployments []deployment) time.Duration {
	var oldest, newest time.Time
	for _, d := range deployments {
		if d.Measured.IsZero() {
			continue
		}
		if oldest.IsZero() || d.Measured.Before(oldest) {
			oldest = d.Measured
		}
		if newest.IsZero() || d.Measured.After(newest) {
			newest = d.Measured
		}
	}
	if oldest.IsZero() || newest.IsZero() {
		return 0
	}
	return newest.Sub(oldest)
}

func markdown(t totals) string {
	var b strings.Builder

	b.WriteString("# What an installation pays per workspace\n\n")
	b.WriteString("One process per provider, so a workspace is engaged by each of them and the\n")
	b.WriteString("cost of serving it is the sum. Each row is one deployment's own sweep; the\n")
	b.WriteString("total is what a workspace costs the installation.\n\n")

	for _, w := range t.Warnings {
		fmt.Fprintf(&b, "> **Warning:** %s\n\n", w)
	}
	if len(t.Missing) > 0 {
		fmt.Fprintf(&b, "> **Incomplete:** no report for %s. The total below is a sum of what was\n"+
			"> found and is not an installation's cost.\n\n", strings.Join(t.Missing, ", "))
	}

	b.WriteString("| Deployment | Workspaces swept | Goroutines/ws | Watch streams/ws | Discovery/ws | Requests/ws | Heap/ws | Streams held | Retained/departure |\n")
	b.WriteString("|---|--:|--:|--:|--:|--:|--:|--:|--:|\n")
	for _, d := range t.Deployments {
		b.WriteString(row(d, fmt.Sprint(d.Workspaces)))
	}
	b.WriteString(row(t.Total, "—"))
	b.WriteString("\n")

	b.WriteString("**Streams held** is per deployment rather than per workspace: it is what that\n")
	b.WriteString("process holds open on the shard whether it serves one workspace or twenty, so\n")
	b.WriteString("the total is what the shard sees from the installation at rest.\n\n")
	b.WriteString("**Heap/ws** is the one column to read with care. It is a least-squares fit, and\n")
	b.WriteString("a process whose live heap grows in steps reports a slope that is the step\n")
	b.WriteString("divided by the swept range rather than a per-workspace cost. Read a heap figure\n")
	b.WriteString("against its own report's step table before quoting it.\n\n")

	if len(t.Conditions) > 0 {
		b.WriteString("| Condition | Value |\n|---|---|\n")
		keys := make([]string, 0, len(t.Conditions))
		for k := range t.Conditions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "| %s | %s |\n", k, t.Conditions[k])
		}
		b.WriteString("\n")
	}

	b.WriteString("Measured by:\n\n")
	for _, d := range t.Deployments {
		when := "unknown"
		if !d.Measured.IsZero() {
			when = d.Measured.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "- `%s` — %s (%s)\n", d.Source, d.Title, when)
	}
	return b.String()
}

func row(d deployment, workspaces string) string {
	retained := "not measured"
	if d.RetainedMeasured {
		retained = fmt.Sprintf("%.1f", d.RetainedPerDeparture)
	}
	return fmt.Sprintf("| %s | %s | %.1f | %.2f | %.1f | %.1f | %.0f KiB | %d | %s |\n",
		d.Name, workspaces,
		d.GoroutinesPerWorkspace, d.WatchStreamsPerWorkspace, d.DiscoveryPerWorkspace,
		d.RequestsPerWorkspace, d.HeapBytesPerWorkspace/1024, d.FixedWatchStreams, retained)
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
