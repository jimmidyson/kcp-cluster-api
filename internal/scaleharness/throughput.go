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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Throughput measures how fast a fleet retires reconcile work.
//
// # Why this is separate from DeliveryProbe
//
// The probe answers "how long did one event take to arrive", which is a
// latency question and is measured with one event in flight per workspace. This
// answers "how much work can the fleet retire per second", which needs the
// opposite — a queue deep enough that workers are the constraint rather than
// the arrival rate.
//
// The distinction matters because the two are limited by different things. A
// controller's `MaxConcurrentReconciles` does not affect the latency of an
// event arriving into an empty queue at all; it caps the rate at which a
// backlog drains. Only the second measurement can say whether a worker count is
// too low.
type Throughput struct {
	mu      sync.Mutex
	started time.Time
	running bool
	count   int
	target  int
	done    chan struct{}
}

// Result is one throughput measurement.
type Result struct {
	// Completions is how many reconciles finished within the window.
	Completions int `json:"completions"`
	// Elapsed is wall-clock from Begin to the last counted completion, or to
	// the timeout if the target was not reached.
	Elapsed time.Duration `json:"elapsed"`
	// PerSecond is Completions over Elapsed — the figure a worker count is
	// judged by.
	PerSecond float64 `json:"perSecond"`
}

// NewThroughput returns a counter that is not yet measuring.
func NewThroughput() *Throughput {
	return &Throughput{}
}

// Begin starts the clock and sets how many completions constitute the run.
//
// Called immediately before the load is issued, never at construction: a
// harness that provisions workspaces for a minute and then measures would
// otherwise divide its completions by the provisioning time and report a rate
// several times too low.
func (t *Throughput) Begin(target int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = time.Now()
	t.running = true
	t.count = 0
	t.target = target
	t.done = make(chan struct{})
}

// Completed records one finished reconcile. Called from the reconciler, so it
// must stay cheap — this is on the path being measured.
//
// Completions arriving before Begin are ignored. Watches replay their store on
// registration and controllers resync, so reconciles happen that no measurement
// asked for; counting them would inflate the rate with work the harness did not
// issue.
func (t *Throughput) Completed() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return
	}
	t.count++
	if t.count >= t.target && t.done != nil {
		close(t.done)
		t.done = nil
	}
}

// Await blocks until the target is reached and returns the rate.
//
// A run that falls short returns an error **together with** the partial result,
// because falling short is the finding this exists to detect: it means the
// workers could not retire the backlog in the time allowed, which is exactly
// the condition a worker count is being tested for. Discarding the partial
// numbers would throw away the answer.
func (t *Throughput) Await(ctx context.Context, timeout time.Duration) (Result, error) {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return Result{}, errors.New("throughput was never begun: a rate with no start time is meaningless")
	}
	done := t.done
	t.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(timeout):
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	elapsed := time.Since(t.started)
	res := Result{Completions: t.count, Elapsed: elapsed}
	if elapsed > 0 {
		res.PerSecond = float64(t.count) / elapsed.Seconds()
	}
	if t.count < t.target {
		return res, fmt.Errorf("retired %d of %d reconciles in %s", t.count, t.target, timeout)
	}
	return res, nil
}
