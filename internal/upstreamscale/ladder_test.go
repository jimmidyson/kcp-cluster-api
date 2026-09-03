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

package upstreamscale

import (
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func TestTheLadderDoublesAndStopsAtWhatOneProviderCanServe(t *testing.T) {
	got := Ladder(25, 400)
	want := []int{25, 50, 100, 200, 400}
	if len(got) != len(want) {
		t.Fatalf("ladder = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ladder = %v, want %v", got, want)
		}
	}

	// The port range is a ceiling on the fleet, so it is a ceiling on the
	// ladder: no rung is offered that the provider is known in advance to
	// refuse.
	for _, rung := range Ladder(4000, 40000) {
		if rung > MaxInMemoryClusters {
			t.Errorf("the ladder offers %d clusters, past the %d one provider can serve",
				rung, MaxInMemoryClusters)
		}
	}
}

// TestAFailedRungIsClassified. "It broke" is not a result: the three ways this
// run can end tell an operator three different things — buy memory, raise a
// limit, or look at why reconciliation stopped keeping up.
func TestAFailedRungIsClassified(t *testing.T) {
	oom := []deployedscale.ComponentSample{
		{Component: "capd-controller-manager", Pod: deployedscale.PodFacts{OOMKilled: true, RestartCount: 1}},
	}
	if got := Classify(oom, false); !strings.Contains(got, "OOM") {
		t.Errorf("an OOM kill was classified as %q", got)
	}
	if got := Classify(oom, false); !strings.Contains(got, "capd-controller-manager") {
		t.Errorf("the classification does not name the component: %q", got)
	}

	restarted := []deployedscale.ComponentSample{
		{Component: "capi-controller-manager", Pod: deployedscale.PodFacts{RestartCount: 2, LastReason: "Error"}},
	}
	if got := Classify(restarted, false); !strings.Contains(got, "restarted") {
		t.Errorf("a restart was classified as %q", got)
	}

	// Nothing died and the fleet still did not get there: that is the third
	// answer, and it is the interesting one, because it is the only failure
	// mode that is about Cluster API keeping up rather than about a limit.
	healthy := []deployedscale.ComponentSample{
		{Component: "capi-controller-manager", Pod: deployedscale.PodFacts{Ready: true}},
	}
	got := Classify(healthy, true)
	if !strings.Contains(got, "did not reach") {
		t.Errorf("a timeout with every component healthy was classified as %q", got)
	}
	if strings.Contains(got, "OOM") || strings.Contains(got, "restarted") {
		t.Errorf("a timeout was reported as a death: %q", got)
	}

	if got := Classify(healthy, false); got != "" {
		t.Errorf("a converged rung was classified as a failure: %q", got)
	}
}

// TestTheCeilingIsTheLastRungThatConverged, and the run says both numbers: the
// largest fleet it held and the one it could not. A ceiling reported as a
// single number leaves a reader unable to tell a measured limit from an
// untested guess at the next step.
func TestTheCeilingIsTheLastRungThatConverged(t *testing.T) {
	ceiling := Summarise([]RungResult{
		{Clusters: 25, Machines: 250, Converged: true},
		{Clusters: 50, Machines: 500, Converged: true},
		{Clusters: 100, Machines: 1000, Failure: "capd-controller-manager was OOM killed"},
	})
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 50 {
		t.Fatalf("last good rung = %+v, want 50 clusters", ceiling.LastGood)
	}
	if ceiling.Failed == nil || ceiling.Failed.Clusters != 100 {
		t.Fatalf("failed rung = %+v, want 100 clusters", ceiling.Failed)
	}
	if !strings.Contains(ceiling.Describe(), "50 clusters") ||
		!strings.Contains(ceiling.Describe(), "OOM") {
		t.Errorf("the summary does not carry both halves: %q", ceiling.Describe())
	}

	// A climb that never failed has no ceiling in it, and must not imply one:
	// the largest rung converged, which is a floor under the answer and not
	// the answer.
	all := Summarise([]RungResult{
		{Clusters: 25, Machines: 250, Converged: true},
		{Clusters: 50, Machines: 500, Converged: true},
	})
	if all.Failed != nil {
		t.Error("a climb with no failure reported one")
	}
	if !strings.Contains(all.Describe(), "not a ceiling") {
		t.Errorf("a climb that never failed does not say so: %q", all.Describe())
	}

	// And a climb whose very first rung failed measured nothing at all.
	none := Summarise([]RungResult{{Clusters: 25, Machines: 250, Failure: "did not reach the end state"}})
	if none.LastGood != nil {
		t.Errorf("a climb that never converged reported a good rung: %+v", none.LastGood)
	}
	if !strings.Contains(none.Describe(), "nothing") {
		t.Errorf("a climb that converged nowhere does not say so: %q", none.Describe())
	}
}
