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

import "sort"

// Retention is what the workspaces that have left are still costing, and the
// two samples that say so.
type Retention struct {
	// PerDeparture is the cost that did not come back, divided by the number
	// of workspaces that left before Down was taken.
	PerDeparture float64
	// Departed is how many had left by then.
	Departed int
	// Up and Down are the two ends of the comparison, kept so that a caller
	// reporting a failure can subtract their profiles and name the goroutines
	// rather than quoting the number again.
	Up, Down Sample
}

// Retained reports what the workspaces that have left are still costing, per
// departure.
//
// It compares a teardown sample with the sample taken at the same workspace
// count on the way up, because that is the only comparison that isolates the
// answer: both describe a process serving k workspaces, so any difference is
// what the departed workspaces did not give back.
//
// # Every comparable pair, not one
//
// This used to compare at one workspace only, which made the figure a single
// subtraction of two integers. On a shape sweeping two workspaces that is one
// departure and one difference, so any transient at either end was reported
// verbatim as retention — and it was, three times, at 2.0 and 3.0 against a
// budget of zero.
//
// Every workspace count that has both an active and a teardown sample gives an
// independent estimate. The lowest of them is taken, for the same reason the
// teardown sample is itself the lowest of several reads: a goroutine that has
// not gone yet inflates an estimate and a transient inflates it, while nothing
// pushes it below what is genuinely still held. A shape that really retains
// per departure retains it in every pair, so the minimum keeps a true finding
// and drops a one-off.
//
// The last departure is excluded: it also shuts the shared wildcard cache
// down, because kcp empties the APIExportEndpointSlice when the last APIBinding
// goes, and that is a fixed cost coming off rather than a workspace's share.
//
// ok is false when the sweep cannot answer: fewer than two workspaces, or no
// workspace count with both ends of a comparison.
func Retained(samples []Sample, measure func(Sample) float64) (perDeparture float64, departed int, ok bool) {
	r, ok := RetainedDetail(samples, measure)
	return r.PerDeparture, r.Departed, ok
}

// RetainedDetail is [Retained] with the samples it compared.
func RetainedDetail(samples []Sample, measure func(Sample) float64) (Retention, bool) {
	up := map[int]*Sample{}
	down := map[int]*Sample{}
	peak := 0
	for i := range samples {
		s := &samples[i]
		switch s.Phase {
		case PhaseActive:
			if s.Workspaces > peak {
				peak = s.Workspaces
			}
			up[s.Workspaces] = s
		case PhaseDisengaged:
			down[s.Workspaces] = s
		case PhaseBaseline, PhaseEngaged:
		}
	}
	if peak < 2 {
		return Retention{}, false
	}

	// Sorted, and ties broken towards the pair with the most departures: the
	// estimates are divided by different denominators, so two that agree are
	// better represented by the one averaged over more departures. Ranging a
	// map here would make the reported pair differ between runs.
	counts := make([]int, 0, len(down))
	for k := range down {
		counts = append(counts, k)
	}
	sort.Ints(counts)

	var best Retention
	found := false
	for _, k := range counts {
		d := down[k]
		// At the peak nothing has departed, and at zero the wildcard cache has
		// gone with the last one. Neither is a departure this can price.
		if k < 1 || k >= peak {
			continue
		}
		u, hasUp := up[k]
		if !hasUp {
			continue
		}
		departed := peak - k
		candidate := Retention{
			PerDeparture: (measure(*d) - measure(*u)) / float64(departed),
			Departed:     departed,
			Up:           *u,
			Down:         *d,
		}
		switch {
		case !found,
			candidate.PerDeparture < best.PerDeparture,
			candidate.PerDeparture == best.PerDeparture && candidate.Departed > best.Departed:
			best, found = candidate, true
		}
	}
	if !found {
		return Retention{}, false
	}
	return best, true
}
