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

package sweep_test

import (
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
)

func TestStacksGrownNamesOnlyWhatGrew(t *testing.T) {
	before := sweep.Sample{Label: "1 active", Goroutines: 488, Stacks: sweep.Stacks{
		"a.Parked": 90,
		"b.Source": 6,
		"c.Serve":  4,
	}}
	after := sweep.Sample{Label: "1 left", Goroutines: 491, Stacks: sweep.Stacks{
		"a.Parked": 92, // grew by two
		"b.Source": 6,  // unchanged
		"c.Serve":  1,  // gave three back
		"d.New":    1,  // appeared
	}}

	grown := sweep.StacksGrown(before.Stacks, after.Stacks)
	if len(grown) != 2 {
		t.Fatalf("StacksGrown returned %d stacks, want 2: only a.Parked and d.New hold more than before", len(grown))
	}
	if grown[0].Stack != "a.Parked" || grown[0].Growth() != 2 {
		t.Errorf("first = %q +%d, want a.Parked +2: the largest growth comes first", grown[0].Stack, grown[0].Growth())
	}
	if grown[1].Stack != "d.New" || grown[1].Before != 0 {
		t.Errorf("second = %q (before %d), want d.New starting from none", grown[1].Stack, grown[1].Before)
	}
}

// The whole point of a diff rather than a profile: a stack that holds hundreds
// of goroutines at both ends is not what a departure left behind, and must not
// drown out the one that is.
func TestStacksGrownIgnoresTheLargeAndUnchanged(t *testing.T) {
	before := sweep.Stacks{"workers": 900, "leaked": 0}
	after := sweep.Stacks{"workers": 900, "leaked": 3}

	grown := sweep.StacksGrown(before, after)
	if len(grown) != 1 || grown[0].Stack != "leaked" || grown[0].Growth() != 3 {
		t.Errorf("StacksGrown = %+v, want only leaked +3", grown)
	}
}

func TestFormatStacksGrownSaysWhenNothingGrew(t *testing.T) {
	before := sweep.Sample{Label: "2 active", Goroutines: 554, Stacks: sweep.Stacks{"a": 1}}
	after := sweep.Sample{Label: "2 left", Goroutines: 554, Stacks: sweep.Stacks{"a": 1}}

	out := sweep.FormatStacksGrown(before, after, 0)
	if !strings.Contains(out, "no stack grew") {
		t.Errorf("FormatStacksGrown = %q, want it to say no stack grew rather than printing nothing", out)
	}
}

// A sample taken before profiles were captured, or one where the profile could
// not be written, must say so rather than reading as "nothing grew".
func TestFormatStacksGrownSaysWhenThereIsNoProfile(t *testing.T) {
	out := sweep.FormatStacksGrown(
		sweep.Sample{Label: "1 active"},
		sweep.Sample{Label: "1 left"},
		0,
	)
	if !strings.Contains(out, "no profile was captured") {
		t.Errorf("FormatStacksGrown = %q, want it to distinguish a missing profile from an empty diff", out)
	}
}

func TestFormatStacksGrownCapsWhatItPrints(t *testing.T) {
	before, after := sweep.Stacks{}, sweep.Stacks{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		after[name] = 1
	}
	out := sweep.FormatStacksGrown(
		sweep.Sample{Label: "up", Stacks: before},
		sweep.Sample{Label: "down", Stacks: after},
		2,
	)
	if !strings.Contains(out, "and 3 more stack(s), 3 goroutine(s)") {
		t.Errorf("FormatStacksGrown = %q, want the three it did not print summarised rather than dropped", out)
	}
}

// Parsing is exercised through a captured sample, because that is the only way
// the profile format reaches the diff.
func TestTakeCapturesStacksThatSumToTheGoroutineCount(t *testing.T) {
	s := sweep.Take(sweep.PhaseBaseline, "baseline", 0, nil)
	if len(s.Stacks) == 0 {
		t.Fatal("Take captured no stacks, so a retention failure would have nothing to subtract")
	}

	total := 0
	for _, n := range s.Stacks {
		total += n
	}
	// Not equality: the count and the profile are taken microseconds apart, and
	// the test binary's own goroutines move. Close is what makes the diff
	// meaningful; exact would be a flake.
	if diff := total - s.Goroutines; diff < -5 || diff > 5 {
		t.Errorf("stacks account for %d goroutines against a count of %d, which is too far apart to subtract meaningfully", total, s.Goroutines)
	}

	for stack := range s.Stacks {
		if strings.Contains(stack, "0x") {
			t.Errorf("stack %q kept an address, which differs between builds and would make two profiles unsubtractable", stack)
			break
		}
	}
}
