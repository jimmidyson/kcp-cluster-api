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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sample(label string, workspaces int, component, node string, goroutines int, heap, resident uint64, restarts int32) Sample {
	return Sample{
		Label:      label,
		Workspaces: workspaces,
		Clusters:   workspaces,
		Components: []ComponentSample{{
			Component: component,
			Process: ProcessSample{
				Goroutines: goroutines, HeapAllocBytes: heap, ResidentBytes: resident, CPUSeconds: 1,
			},
			Pod: PodFacts{Name: component + "-1", Node: node, Ready: true, RestartCount: restarts},
		}},
	}
}

func TestPerWorkspaceFitsASlope(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))
	r.Add(sample("32", 32, ComponentCore, "node-1", 1700, 34_000_000, 102_000_000, 0))

	slope, ok := r.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no slope from three samples")
	}
	if slope < 49 || slope > 51 {
		t.Errorf("goroutines per workspace = %v, want about 50", slope)
	}
}

// TestARestartedSampleIsExcludedFromTheFit is the property that keeps a
// restart from being read as a cheaper fleet: the process metrics reset, so
// the sample is not comparable with the others.
func TestARestartedSampleIsExcludedFromTheFit(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))
	// A restarted container reporting a fresh process's small numbers at the
	// widest point would drag the slope negative.
	r.Add(sample("32", 32, ComponentCore, "node-1", 40, 1_000_000, 8_000_000, 1))

	slope, ok := r.PerWorkspace(ComponentCore, Goroutines)
	if !ok {
		t.Fatal("no slope")
	}
	if slope <= 0 {
		t.Errorf("slope = %v: the restarted sample was included", slope)
	}

	if len(r.Disturbed()) != 1 {
		t.Errorf("the restart was not reported: %v", r.Disturbed())
	}
	if !strings.Contains(r.Markdown(), "A container restarted during this run") {
		t.Error("the report does not warn that a container restarted")
	}
}

// TestANegativeFitIsNotAMeasurement. Least squares returns a negative slope
// whenever noise exceeds signal, and a negative cost per workspace is not a
// cheaper fleet.
func TestANegativeFitIsNotAMeasurement(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 900, 20_000_000, 60_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))

	if slope, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Errorf("a negative fit was reported as a measurement: %v", slope)
	}
	if !strings.Contains(r.Markdown(), "not measured") {
		t.Error("the report does not say the figure was not measured")
	}
}

func TestOneSampleIsNotASlope(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	if _, ok := r.PerWorkspace(ComponentCore, Goroutines); ok {
		t.Error("one sample produced a slope")
	}
}

// TestCoLocationIsStatedInTheReport is the most misleading thing this harness
// could omit: a run where everything landed on one node measured a co-located
// deployment whatever the manifests asked for.
func TestCoLocationIsStatedInTheReport(t *testing.T) {
	r := &Report{Title: "t"}
	s := sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0)
	s.Components = append(s.Components, ComponentSample{
		Component: ComponentBootstrap,
		Pod:       PodFacts{Node: "node-1", Ready: true},
	})
	r.Add(s)

	nodes, coLocated := r.Placement()
	if len(nodes) != 1 || !coLocated {
		t.Fatalf("placement = %v, co-located = %v", nodes, coLocated)
	}
	if !strings.Contains(r.Markdown(), "not a multi-node figure") {
		t.Error("a co-located run does not say so in its report")
	}
}

func TestSpreadIsStatedToo(t *testing.T) {
	r := &Report{Title: "t"}
	s := sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0)
	s.Components = append(s.Components, ComponentSample{
		Component: ComponentBootstrap,
		Pod:       PodFacts{Node: "node-2", Ready: true},
	})
	r.Add(s)

	nodes, coLocated := r.Placement()
	if len(nodes) != 2 || coLocated {
		t.Fatalf("placement = %v, co-located = %v", nodes, coLocated)
	}
	md := r.Markdown()
	if !strings.Contains(md, "spread across 2 nodes") {
		t.Error("a spread run does not say so")
	}
	if strings.Contains(md, "not a multi-node figure") {
		t.Error("a spread run was labelled co-located")
	}
}

// A single component on a single node is not "co-located": there is nothing
// for it to be co-located with, and warning about it would train a reader to
// ignore the warning.
func TestOneComponentIsNotCoLocated(t *testing.T) {
	r := &Report{Title: "t"}
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	if _, coLocated := r.Placement(); coLocated {
		t.Error("a single-component run was reported as co-located")
	}
}

func TestComponentsAreInCanonicalOrder(t *testing.T) {
	r := &Report{Title: "t"}
	s := Sample{Label: "x", Components: []ComponentSample{
		{Component: ComponentDevInfrastructure},
		{Component: ComponentCore},
	}}
	r.Add(s)

	got := r.Components()
	if len(got) != 2 || got[0] != ComponentCore {
		t.Errorf("components = %v, want core first whatever order they were sampled in", got)
	}
}

func TestWriteProducesBothForms(t *testing.T) {
	r := &Report{Title: "Deployed fleet"}
	r.AddFact("spread", "32x1")
	r.Add(sample("8", 8, ComponentCore, "node-1", 500, 10_000_000, 30_000_000, 0))
	r.Add(sample("16", 16, ComponentCore, "node-1", 900, 18_000_000, 54_000_000, 0))

	dir := t.TempDir()
	if err := r.Write(dir, "deployed"); err != nil {
		t.Fatalf("writing: %v", err)
	}

	md, err := os.ReadFile(filepath.Join(dir, "deployed.md"))
	if err != nil {
		t.Fatalf("reading markdown: %v", err)
	}
	if !strings.Contains(string(md), "Deployed fleet") || !strings.Contains(string(md), ComponentCore) {
		t.Error("the markdown does not carry the title and the component")
	}
	// The per-deployment heading is what FR-010 asks for: cost is never
	// reported only as a total.
	if !strings.Contains(string(md), "## "+ComponentCore) {
		t.Error("the report has no per-deployment section")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "deployed.json"))
	if err != nil {
		t.Fatalf("reading json: %v", err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(back.Samples) != 2 || back.Facts["spread"] != "32x1" {
		t.Errorf("the json did not round trip: %+v", back)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{{512, "512 B"}, {2048, "2.0 KiB"}, {5 * 1024 * 1024, "5.0 MiB"}} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
