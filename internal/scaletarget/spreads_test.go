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

package scaletarget

import (
	"slices"
	"strings"
	"testing"
)

// TestTuningOneKnobDoesNotBreakAnother is the defect this file exists for.
//
// The spread used to default to a fixed 1,10 — right for two hundred clusters
// and impossible for two. Somebody turning the cluster count down got a run
// refused over a knob they never touched.
func TestTuningOneKnobDoesNotBreakAnother(t *testing.T) {
	// The command that failed.
	plans, err := Fleet{Clusters: 2, NodesPerCluster: 2, ControlPlaneNodes: 1}.Plans(nil)
	if err != nil {
		t.Fatalf("CLUSTERS=2 NODES_PER_CLUSTER=2 was refused: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("two clusters gave %d spreads; there is no denser one that leaves workspaces to count", len(plans))
	}
	if plans[0].Shape.Workspaces != 2 || plans[0].Shape.ClustersPerWorkspace != 1 {
		t.Errorf("spread = %s, want 2x1", plans[0].Shape)
	}
}

func TestDefaultSpreads(t *testing.T) {
	for _, tc := range []struct {
		clusters int
		want     []int
		why      string
	}{
		{200, []int{1, 10}, "the documented default is preserved"},
		{100, []int{1, 10}, ""},
		{20, []int{1, 10}, "ten per workspace still leaves two workspaces"},
		{2, []int{1}, "the only denser spread leaves one workspace, which has no slope"},
		{1, []int{1}, ""},
		{4, []int{1, 2}, "two divides and leaves two workspaces; four would leave one"},
		{13, []int{1}, "a prime above the cap has no usable second spread"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			if got := DefaultSpreads(tc.clusters); !slices.Equal(got, tc.want) {
				t.Errorf("DefaultSpreads(%d) = %v, want %v", tc.clusters, got, tc.want)
			}
		})
	}
}

// Every derived spread must divide the fleet it was derived for, or the
// default would produce the very refusal it exists to avoid.
func TestEveryDefaultSpreadDivides(t *testing.T) {
	for clusters := 1; clusters <= 500; clusters++ {
		for _, per := range DefaultSpreads(clusters) {
			if clusters%per != 0 {
				t.Fatalf("DefaultSpreads(%d) chose %d, which does not divide", clusters, per)
			}
			if clusters/per < 1 {
				t.Fatalf("DefaultSpreads(%d) chose %d, leaving no workspaces", clusters, per)
			}
		}
		if _, err := (Fleet{Clusters: clusters, NodesPerCluster: 1, ControlPlaneNodes: 1}).Plans(nil); err != nil {
			t.Fatalf("a fleet of %d clusters was refused with the spread it chose itself: %v", clusters, err)
		}
	}
}

// An explicit spread is still honoured or refused — a default may adapt, a
// request may not be quietly changed.
func TestAnExplicitSpreadIsStillRefusedButSaysWhatWorks(t *testing.T) {
	_, err := Fleet{Clusters: 200, NodesPerCluster: 1, ControlPlaneNodes: 1, ClustersPerWorkspace: []int{3}}.Plans(nil)
	if err == nil {
		t.Fatal("an indivisible explicit spread was accepted")
	}
	for _, want := range []string{"do not divide", "Spreads that divide 200"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	// And it names one that would have worked.
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("error %q does not suggest a divisor", err)
	}
}

func TestDivisors(t *testing.T) {
	if got := Divisors(200, 10); !slices.Equal(got, []int{1, 2, 4, 5, 8, 10}) {
		t.Errorf("Divisors(200, 10) = %v", got)
	}
	if got := Divisors(13, 13); !slices.Equal(got, []int{1, 13}) {
		t.Errorf("Divisors(13, 13) = %v", got)
	}
}
