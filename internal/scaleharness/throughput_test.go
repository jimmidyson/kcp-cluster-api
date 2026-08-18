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

package scaleharness

import (
	"testing"
	"time"
)

func TestThroughputMeasuresCompletionRate(t *testing.T) {
	tp := NewThroughput()
	tp.Begin(4)

	go func() {
		for range 4 {
			time.Sleep(5 * time.Millisecond)
			tp.Completed()
		}
	}()

	got, err := tp.Await(t.Context(), 5*time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if got.Completions != 4 {
		t.Errorf("counted %d completions, want 4", got.Completions)
	}
	if got.Elapsed < 20*time.Millisecond {
		t.Errorf("elapsed %s is below the delay that was introduced", got.Elapsed)
	}
	if got.PerSecond <= 0 {
		t.Errorf("rate = %v, want positive", got.PerSecond)
	}
}

// A run that does not reach its target is the finding — it means the workers
// could not keep up — so it must report what was achieved rather than either
// hanging or discarding the partial result.
func TestIncompleteRunReportsWhatWasAchieved(t *testing.T) {
	tp := NewThroughput()
	tp.Begin(10)
	for range 3 {
		tp.Completed()
	}

	got, err := tp.Await(t.Context(), 50*time.Millisecond)
	if err == nil {
		t.Fatal("Await succeeded having reached 3 of 10")
	}
	if got.Completions != 3 {
		t.Errorf("reported %d completions, want the 3 that happened", got.Completions)
	}
	if got.Elapsed <= 0 {
		t.Error("no elapsed time reported for a partial run")
	}
}

// The clock must start when the load is issued, not when the counter is
// constructed: a harness that provisions for a minute and then measures would
// otherwise divide its completions by the provisioning time.
func TestBeginRestartsTheClock(t *testing.T) {
	tp := NewThroughput()
	tp.Begin(1)
	time.Sleep(20 * time.Millisecond)

	tp.Begin(1)
	tp.Completed()

	got, err := tp.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if got.Elapsed > 15*time.Millisecond {
		t.Errorf("elapsed %s includes time before the second Begin", got.Elapsed)
	}
}

// Completions arriving before Begin belong to no measurement — watches replay
// their store on registration, so reconciles happen that the harness did not
// ask for. Counting them would inflate the rate.
func TestCompletionsBeforeBeginAreNotCounted(t *testing.T) {
	tp := NewThroughput()
	tp.Completed()
	tp.Completed()

	tp.Begin(1)
	tp.Completed()

	got, err := tp.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if got.Completions != 1 {
		t.Errorf("counted %d, want 1 — reconciles before Begin are not part of the measurement", got.Completions)
	}
}

func TestAwaitWithoutBeginIsAnError(t *testing.T) {
	tp := NewThroughput()
	if _, err := tp.Await(t.Context(), 10*time.Millisecond); err == nil {
		t.Error("Await succeeded without a Begin; a rate with no start time is meaningless")
	}
}
