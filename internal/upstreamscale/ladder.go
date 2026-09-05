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
	"fmt"
	"strings"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Ladder is the fleet sizes a run climbs, never past what one in-memory
// provider can serve.
//
// # Doubling or stepping, and when each is right
//
// A step of zero doubles. That is the shape for a first run: the question is
// where a ceiling is rather than exactly which cluster crosses it, each rung
// costs a full convergence, and a linear climb from nothing spends most of a
// run re-measuring fleets it has already priced.
//
// A step is the shape for the run after it. Once the ceiling's neighbourhood is
// known — the model fitted to the first run puts one API server at its node's
// whole memory around 3,500 clusters — the rungs worth spending are the ones
// either side of that, and doubling puts them everywhere else. Even steps also
// give the next fit its points where the curve is most likely to stop being a
// line.
//
// Stepping, the last rung is always the size that was asked for: a step that
// does not divide the range evenly would otherwise stop short — 375 by 500
// reaches 2875 — and a short final step is a smaller compromise than not
// answering the question the run was given. Doubling, max stays what it always
// was, a cap rather than a target, so the shape the recorded runs were climbed
// in is unchanged.
func Ladder(start, max, step int) []int {
	if start < 1 {
		start = 1
	}
	if max > MaxInMemoryClusters {
		max = MaxInMemoryClusters
	}
	if start > max {
		return nil
	}

	if step <= 0 {
		// Unchanged, deliberately: max is a cap here rather than a target, and
		// the recorded runs were climbed this way.
		var out []int
		for n := start; n <= max; n *= 2 {
			out = append(out, n)
		}
		return out
	}

	var out []int
	for n := start; n <= max; n += step {
		out = append(out, n)
	}
	if out[len(out)-1] != max {
		out = append(out, max)
	}
	return out
}

// RungResult is one fleet size, climbed.
type RungResult struct {
	Clusters  int    `json:"clusters"`
	Machines  int    `json:"machines"`
	Converged bool   `json:"converged"`
	Failure   string `json:"failure,omitempty"`

	// Added is how many clusters this rung created, which is not how many it
	// holds. The ladder is incremental: a rung keeps the fleet the rung below
	// it left converged and adds to it, so its WaitedFor is the time the
	// increment took and the pace is per added cluster.
	//
	// The distinction is not cosmetic. Rung 4 of a 2,4 climb waited for two
	// new clusters with two already Ready, and dividing by four reported a
	// pace twice as good as the truth. The error grows with the ladder: rung
	// 400 adds 200.
	Added int `json:"added"`

	// CreatedIn is how long the driver took to apply this rung's objects, and
	// WaitedFor is how long the fleet then took to reach the end state — or
	// how long it ran before it was given up on.
	//
	// # Why the two are separate
	//
	// Time to converge is the headline answer to "can one management cluster
	// hold this fleet": a rung that arrives in four minutes and one that
	// arrives in forty are not the same result. But a single total cannot be
	// read, because the spec's own risk list names the driver as a candidate
	// bottleneck — creating a rung's worth of objects through one client is
	// work — and a run that cannot separate its own object creation from
	// Cluster API's reconciliation is not measuring Cluster API.
	//
	// WaitedFor is also worth having on a failure. "OOM killed" reads
	// differently after four minutes than after forty: the first is a fleet
	// the component could not hold at all, the second one it degraded under.
	CreatedIn time.Duration `json:"createdIn"`
	WaitedFor time.Duration `json:"waitedFor"`
}

// Total is the whole cost of the rung in wall time, the driver's share
// included.
func (r RungResult) Total() time.Duration { return r.CreatedIn + r.WaitedFor }

// PerAddedCluster is the pace that lets a reader extrapolate to the next rung:
// the wait divided by the clusters this rung actually brought up.
//
// Zero for a rung that did not converge, whatever its clock says — dividing the
// time a fleet failed to arrive in by the fleet it did not reach produces a
// number that looks like a rate and is not one — and zero for a rung that added
// nothing, since there is no increment to have taken any time.
func (r RungResult) PerAddedCluster() time.Duration {
	if !r.Converged || r.Added == 0 {
		return 0
	}
	return r.WaitedFor / time.Duration(r.Added)
}

// Timing is what the report carries beside each rung.
func (r RungResult) Timing() string {
	created := r.CreatedIn.Round(time.Second)
	waited := r.WaitedFor.Round(time.Second)
	if !r.Converged {
		return fmt.Sprintf("created in %s, then gave up after %s", created, waited)
	}
	return fmt.Sprintf("created in %s, converged in %s (%d clusters added, %s each)",
		created, waited, r.Added, r.PerAddedCluster().Round(time.Millisecond))
}

// Classify says why a rung did not converge, in the terms an operator acts on.
//
// The three answers mean different things and a run that collapses them into
// "it broke" is not worth taking: a kill says raise a limit or buy memory, a
// restart says look at why the process died, and a fleet that simply did not
// arrive with every component healthy says Cluster API stopped keeping up —
// which is the only one of the three that is about Cluster API rather than
// about the box it was given.
//
// A death outranks a timeout, because a component that died is why the fleet
// did not arrive rather than a second thing that happened to go wrong.
func Classify(components []deployedscale.ComponentSample, timedOut bool) string {
	for _, c := range components {
		if c.Pod.OOMKilled {
			return fmt.Sprintf("%s was OOM killed: it exceeded its memory limit", c.Component)
		}
	}
	for _, c := range components {
		if c.Pod.RestartCount > 0 {
			reason := c.Pod.LastReason
			if reason == "" {
				reason = "unknown reason"
			}
			return fmt.Sprintf("%s restarted %d time(s) (%s), so its samples are not comparable "+
				"with the rungs below it", c.Component, c.Pod.RestartCount, reason)
		}
	}
	if timedOut {
		return "the fleet did not reach the end state in time, with every component still healthy: " +
			"nothing ran out, reconciliation did not keep up"
	}
	return ""
}

// Ceiling is what a climb found.
type Ceiling struct {
	// LastGood is the largest fleet that fully converged, or nil when none did.
	LastGood *RungResult `json:"lastGood,omitempty"`
	// Failed is the rung that stopped the climb, or nil when none did.
	Failed *RungResult `json:"failed,omitempty"`
}

// Summarise reduces a climb to the two numbers worth quoting.
func Summarise(rungs []RungResult) Ceiling {
	var c Ceiling
	for i := range rungs {
		r := rungs[i]
		switch {
		case r.Converged:
			good := r
			c.LastGood = &good
		default:
			failed := r
			c.Failed = &failed
			return c
		}
	}
	return c
}

// Describe is the sentence a report leads with.
//
// Both halves, always. A ceiling given as one number leaves a reader unable to
// tell a measured limit from an untested guess at what comes next, and a climb
// that never failed has not found a ceiling at all — it has put a floor under
// one, which is a different claim and is made as one.
func (c Ceiling) Describe() string {
	var b strings.Builder
	if c.LastGood == nil {
		b.WriteString("This run measured nothing: the smallest fleet it tried did not converge")
		if c.Failed != nil && c.Failed.Failure != "" {
			fmt.Fprintf(&b, " (%s)", c.Failed.Failure)
		}
		b.WriteString(".")
		return b.String()
	}

	fmt.Fprintf(&b, "Held %d clusters and %d Machines, every control plane ready and every Machine "+
		"Ready, %s.", c.LastGood.Clusters, c.LastGood.Machines, c.LastGood.Timing())
	if c.Failed == nil {
		b.WriteString(" **That is a floor, not a ceiling**: no rung failed, so the largest fleet " +
			"tried is the largest measured and not the largest possible.")
		return b.String()
	}
	fmt.Fprintf(&b, " The next rung, %d clusters and %d Machines, did not: %s (%s).",
		c.Failed.Clusters, c.Failed.Machines, c.Failed.Failure, c.Failed.Timing())
	return b.String()
}
