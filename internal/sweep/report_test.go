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
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrafficOf(t *testing.T) {
	counts := Counts{
		{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"}: 3,
		{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "apis.kcp.io/apibindings"}:   1,
		{Verb: VerbWatch, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/clusters"}:        1,
		{Verb: VerbList, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"}:  2,
		{Verb: VerbDiscovery, Cluster: "2ab3c4"}:                                           7,
		{Verb: VerbCreate, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/clusters"}:       5,
	}

	got := TrafficOf(counts)
	want := Traffic{
		WatchStreams:         3,
		WildcardWatchStreams: 2,
		ScopedWatchStreams:   1,
		Lists:                2,
		Discovery:            7,
		Total:                19,
	}
	if got != want {
		t.Errorf("TrafficOf() = %+v, want %+v", got, want)
	}
}

// The slope is the number the sweep exists to produce: cost per additional
// workspace. A curve that is flat has a slope of zero however high it sits,
// which is the difference between "expensive" and "multiplying".
func TestPerWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []Sample
		measure func(Sample) float64
		want    float64
	}{
		{
			name: "a flat curve costs nothing per workspace",
			samples: []Sample{
				{Workspaces: 1, Traffic: Traffic{WildcardWatchStreams: 4}},
				{Workspaces: 2, Traffic: Traffic{WildcardWatchStreams: 4}},
				{Workspaces: 4, Traffic: Traffic{WildcardWatchStreams: 4}},
			},
			measure: func(s Sample) float64 { return float64(s.WildcardWatchStreams) },
			want:    0,
		},
		{
			name: "a linear curve reports its per-workspace cost",
			samples: []Sample{
				{Workspaces: 1, Goroutines: 110},
				{Workspaces: 2, Goroutines: 120},
				{Workspaces: 3, Goroutines: 130},
				{Workspaces: 4, Goroutines: 140},
			},
			measure: func(s Sample) float64 { return float64(s.Goroutines) },
			want:    10,
		},
		{
			name:    "one point cannot describe a curve",
			samples: []Sample{{Workspaces: 1, Goroutines: 110}},
			measure: func(s Sample) float64 { return float64(s.Goroutines) },
			want:    math.NaN(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PerWorkspace(tc.samples, tc.measure)
			switch {
			case math.IsNaN(tc.want):
				if !math.IsNaN(got) {
					t.Errorf("PerWorkspace() = %v, want NaN: a slope needs two points", got)
				}
			case math.Abs(got-tc.want) > 1e-9:
				t.Errorf("PerWorkspace() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A sweep that is flat in memory and rising in wall clock has still failed to
// scale, so every sample carries how long the step that produced it took.
func TestAddTimesEachStep(t *testing.T) {
	report := &Report{}

	report.Add(Sample{Label: "first"})
	time.Sleep(10 * time.Millisecond)
	report.Add(Sample{Label: "second"})

	if got := report.Samples[0].StepSeconds; got != 0 {
		t.Errorf("the first sample reports a step of %v, want 0: there was no previous sample to time against", got)
	}
	if got := report.Samples[1].StepSeconds; got < 0.01 {
		t.Errorf("the second sample reports a step of %v, want at least the 10ms that elapsed", got)
	}
}

func TestReportWrite(t *testing.T) {
	report := &Report{
		Title: "Active workspace sweep",
		Facts: map[string]string{"objectsPerWorkspace": "5"},
		Samples: []Sample{
			{Phase: PhaseBaseline, Label: "baseline", Workspaces: 0, Goroutines: 100, HeapBytes: 1 << 20},
			{Phase: PhaseActive, Label: "workspace 1 active", Workspaces: 1, Goroutines: 130, HeapBytes: 2 << 20,
				Traffic: Traffic{WildcardWatchStreams: 4, Discovery: 12, Total: 40}},
		},
	}

	dir := t.TempDir()
	if err := report.Write(dir, "sweep-report"); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "sweep-report.json"))
	if err != nil {
		t.Fatalf("reading the JSON report: %v", err)
	}
	var round Report
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("the JSON report does not parse: %v", err)
	}
	if len(round.Samples) != 2 || round.Samples[1].Goroutines != 130 {
		t.Errorf("round-tripped report = %+v, want the samples it was given", round.Samples)
	}

	md, err := os.ReadFile(filepath.Join(dir, "sweep-report.md"))
	if err != nil {
		t.Fatalf("reading the Markdown report: %v", err)
	}
	// The table is what a reviewer reads. Every sample has to be in it, or a
	// green run would be evidence of a measurement that was never shown.
	for _, want := range []string{"Active workspace sweep", "baseline", "workspace 1 active", "objectsPerWorkspace"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("the Markdown report does not mention %q:\n%s", want, md)
		}
	}
}
