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
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

func TestTheLadderDoublesAndStopsAtWhatOneProviderCanServe(t *testing.T) {
	got := Ladder(25, 400, 0)
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
	for _, rung := range Ladder(4000, 40000, 0) {
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

// TestARungRecordsHowLongItTookAndSeparatesTheDriversShare.
//
// Time to converge is the headline number for "can one management cluster hold
// this fleet" — a rung that arrives in four minutes and one that arrives in
// forty are not the same result, and the ladder recorded neither. It is also
// the number that has to be split: the spec's own risk list says the driver
// creating a rung's objects through one client may be the bottleneck, and one
// total cannot tell that apart from Cluster API being slow.
func TestARungRecordsHowLongItTookAndSeparatesTheDriversShare(t *testing.T) {
	rung := RungResult{
		Clusters: 100, Added: 100, Machines: 1000, Converged: true,
		CreatedIn: 30 * time.Second, WaitedFor: 5 * time.Minute,
	}
	if got := rung.Total(); got != 5*time.Minute+30*time.Second {
		t.Errorf("total = %s, want 5m30s", got)
	}
	// The pace that lets a reader extrapolate to the next rung.
	if got := rung.PerAddedCluster(); got != 3*time.Second {
		t.Errorf("per cluster = %s, want 3s", got)
	}
	timing := rung.Timing()
	for _, want := range []string{"30s", "5m0s", "3s each"} {
		if !strings.Contains(timing, want) {
			t.Errorf("timing does not carry %q: %q", want, timing)
		}
	}
}

// TestAFailedRungSaysHowLongItRanBeforeGivingUp. "OOM killed" reads differently
// after four minutes than after forty: the first is a fleet the component could
// not hold at all, the second is one it degraded under.
func TestAFailedRungSaysHowLongItRanBeforeGivingUp(t *testing.T) {
	rung := RungResult{
		Clusters: 200, Added: 100, Machines: 2000,
		CreatedIn: time.Minute, WaitedFor: 45 * time.Minute,
		Failure: "capd-controller-manager was OOM killed",
	}
	timing := rung.Timing()
	if !strings.Contains(timing, "45m0s") {
		t.Errorf("timing does not say how long it ran: %q", timing)
	}
	// Not "converged in": it did not.
	if strings.Contains(timing, "converged in") {
		t.Errorf("a failed rung reported a convergence time: %q", timing)
	}
	if strings.Contains(timing, "each") {
		t.Errorf("a pace was quoted for a fleet that never arrived: %q", timing)
	}
}

// TestTheCeilingSentenceCarriesTheTiming, because the sentence a report leads
// with is where a fleet size that took forty minutes has to say so.
func TestTheCeilingSentenceCarriesTheTiming(t *testing.T) {
	ceiling := Summarise([]RungResult{{
		Clusters: 50, Added: 50, Machines: 500, Converged: true,
		CreatedIn: 20 * time.Second, WaitedFor: 8 * time.Minute,
	}})
	if got := ceiling.Describe(); !strings.Contains(got, "8m0s") {
		t.Errorf("the ceiling sentence has no timing in it: %q", got)
	}
}

func TestPerClusterIsZeroWhenThereIsNothingToDivide(t *testing.T) {
	if got := (RungResult{Converged: true, WaitedFor: time.Minute}).PerAddedCluster(); got != 0 {
		t.Errorf("per cluster = %s with no clusters", got)
	}
	// And a rung that never converged has no pace, whatever its clock says.
	if got := (RungResult{Clusters: 10, Added: 10, WaitedFor: time.Minute}).PerAddedCluster(); got != 0 {
		t.Errorf("per cluster = %s for a fleet that did not arrive", got)
	}
}

// TestThePaceIsPerClusterAdded, because the ladder is incremental.
//
// A rung does not build its fleet from nothing: rung 4 of a 2,4 climb keeps
// the two clusters rung 2 left converged and adds two more, so its WaitedFor
// is the time the *increment* took. The third real run divided it by the whole
// fleet and reported "17.4s per cluster" for a rung in which two of the four
// were already Ready before it started — a figure that flatters every rung
// above the first, and worst at the top: rung 400 of a doubling ladder adds
// 200 and would divide by 400.
func TestThePaceIsPerClusterAdded(t *testing.T) {
	rung := RungResult{
		Clusters: 100, Added: 40, Machines: 1000, Converged: true,
		CreatedIn: 30 * time.Second, WaitedFor: 4 * time.Minute,
	}
	if got := rung.PerAddedCluster(); got != 6*time.Second {
		t.Errorf("pace = %s, want 6s (4m over the 40 added, not the 100 held)", got)
	}
	timing := rung.Timing()
	for _, want := range []string{"40 clusters added", "6s each", "4m0s"} {
		if !strings.Contains(timing, want) {
			t.Errorf("timing does not carry %q: %q", want, timing)
		}
	}

	// The first rung adds its whole fleet, so nothing changes there.
	first := RungResult{Clusters: 25, Added: 25, Converged: true, WaitedFor: 50 * time.Second}
	if got := first.PerAddedCluster(); got != 2*time.Second {
		t.Errorf("first rung pace = %s, want 2s", got)
	}
}

// TestNoPaceWithoutAnIncrement. A rung that added nothing — a re-run of a fleet
// that is already up — has no pace, and dividing by zero clusters to say so is
// worse than saying nothing.
func TestNoPaceWithoutAnIncrement(t *testing.T) {
	if got := (RungResult{Clusters: 10, Added: 0, Converged: true, WaitedFor: time.Minute}).PerAddedCluster(); got != 0 {
		t.Errorf("pace = %s for a rung that added no clusters", got)
	}
}

// TestAStepLadderClimbsInEvenIntervals.
//
// Doubling answers "roughly where is the ceiling"; even steps answer "where
// exactly, and what does the curve do on the way in". Once a run knows the wall
// is near — the fitted model puts one API server at the node's whole memory
// around 3,500 clusters — the rungs that matter are the ones either side of it,
// and a doubling ladder spends them elsewhere.
func TestAStepLadderClimbsInEvenIntervals(t *testing.T) {
	got := Ladder(500, 3000, 500)
	want := []int{500, 1000, 1500, 2000, 2500, 3000}
	if len(got) != len(want) {
		t.Fatalf("ladder = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ladder = %v, want %v", got, want)
		}
	}
}

// TestTheLastRungIsTheNumberThatWasAskedFor.
//
// A step that does not divide the range evenly would otherwise stop short: 375
// by 500 reaches 2875 and leaves the run 125 clusters below the size it was
// told to reach. A short final step is a smaller compromise than not answering
// the question.
func TestTheLastRungIsTheNumberThatWasAskedFor(t *testing.T) {
	got := Ladder(375, 3000, 500)
	if got[len(got)-1] != 3000 {
		t.Errorf("ladder = %v, want it to end at the 3000 it was asked for", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("ladder = %v, which does not climb", got)
		}
	}
}

// TestAStepLadderStillStopsAtWhatOneProviderCanServe, because the port range
// bounds the fleet whatever shape the climb is.
func TestAStepLadderStillStopsAtWhatOneProviderCanServe(t *testing.T) {
	for _, rung := range Ladder(4000, 40000, 4000) {
		if rung > MaxInMemoryClusters {
			t.Errorf("the ladder offers %d clusters, past the %d one provider can serve",
				rung, MaxInMemoryClusters)
		}
	}
}

// TestAStepBiggerThanTheRangeIsOneRung, rather than none.
func TestAStepBiggerThanTheRangeIsOneRung(t *testing.T) {
	got := Ladder(500, 800, 5000)
	want := []int{500, 800}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ladder = %v, want %v", got, want)
	}
}

// TestTheCulpritCanBeLookedUpInWhatElseTheSampleCarries.
//
// A manager killed while starved of CPU quota and one killed with CPU to
// spare are different findings, and the sample already holds the difference in
// its throttling map. Establishing which one it was took a cAdvisor scrape by
// hand after the run was over, and the answer — 15 throttled periods out of
// 61,053 — sent the diagnosis somewhere else entirely.
func TestTheCulpritCanBeLookedUpInWhatElseTheSampleCarries(t *testing.T) {
	components := []deployedscale.ComponentSample{
		{Component: "capi-controller-manager"},
		{Component: "capi-kubeadm-control-plane-controller-manager",
			Pod: deployedscale.PodFacts{RestartCount: 1, LastExitCode: 137, LastReason: "Error"}},
	}
	if got := Culprit(components); got != "capi-kubeadm-control-plane-controller-manager" {
		t.Errorf("culprit = %q, want the one that restarted", got)
	}
	if !strings.Contains(Classify(components, false), Culprit(components)) {
		t.Error("Culprit and Classify disagree about which component died")
	}
	if got := Culprit(nil); got != "" {
		t.Errorf("a healthy sample named %q as a culprit", got)
	}
}

// TestAnOomOutranksAPlainRestartInBothPlaces, so the component the line names
// is the component its throttling is looked up for.
func TestAnOomOutranksAPlainRestartInBothPlaces(t *testing.T) {
	components := []deployedscale.ComponentSample{
		{Component: "restarted", Pod: deployedscale.PodFacts{RestartCount: 1}},
		{Component: "killed", Pod: deployedscale.PodFacts{RestartCount: 1, OOMKilled: true,
			LastReason: deployedscale.ReasonOOMKilled}},
	}
	if got := Culprit(components); got != "killed" {
		t.Errorf("culprit = %q, want the OOM kill Classify reports", got)
	}
}
