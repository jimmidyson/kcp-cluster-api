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
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

// DeliveryProbe measures how long an event takes to travel from a write to the
// controller that wanted it.
//
// # Why this exists rather than timing the write
//
// The obvious measurement — how long a mutation takes — answers the wrong
// question. A write returns once the API server has accepted it, and everything
// this project is concerned about happens afterwards: the event is dispatched
// through a shared informer to every registered listener, each of which filters
// it. That fan-out is the cost that grows with workspace count, and it is
// entirely invisible to the writer.
//
// So the clock starts when the harness issues a mutation and stops when a
// controller is invoked for it. What sits between the two is the thing being
// measured.
//
// Latency is attributed per workspace, because each workspace has its own
// controller and its own registration: what a growing fleet does to one
// workspace's delivery is the question.
type DeliveryProbe struct {
	mu        sync.Mutex
	sent      map[string]time.Time
	latencies []time.Duration
	waiters   chan struct{}
}

// NewDeliveryProbe returns a probe with nothing outstanding.
func NewDeliveryProbe() *DeliveryProbe {
	return &DeliveryProbe{sent: map[string]time.Time{}}
}

// Sent starts the clock for a workspace. Called by the harness immediately
// before it issues the mutation.
//
// It reports whether the send is being timed. A workspace that already has an
// event in flight is declined rather than restarted, because a delivery is
// attributed by workspace alone: overwriting the start time would measure the
// second event's travel against the first event's arrival, which understates
// latency by exactly the amount the fleet is slow. The caller must count only
// accepted sends when deciding how many deliveries to expect — otherwise a
// profile issuing several events per workspace would report the declined ones
// as missing.
func (p *DeliveryProbe) Sent(workspace string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, inFlight := p.sent[workspace]; inFlight {
		return false
	}
	p.sent[workspace] = time.Now()
	return true
}

// Delivered stops the clock. Called by the controller when it is invoked.
//
// Two kinds of call are ignored rather than recorded, and both are ordinary
// rather than exceptional:
//
//   - A delivery for a workspace with nothing outstanding. Watches replay their
//     store when a registration is added, and controllers resync, so reconciles
//     arrive that no measurement asked for. Counting them would invent
//     latencies that answer no question.
//   - A repeat delivery for one send. A controller may reconcile an object
//     several times; only the first arrival is the one whose travel time was
//     being measured.
func (p *DeliveryProbe) Delivered(workspace string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	start, outstanding := p.sent[workspace]
	if !outstanding {
		return
	}
	delete(p.sent, workspace)
	p.latencies = append(p.latencies, time.Since(start))

	if len(p.sent) == 0 && p.waiters != nil {
		close(p.waiters)
		p.waiters = nil
	}
}

// Await blocks until every outstanding event has been delivered, and returns
// the latencies recorded since the last Reset.
//
// An event that never arrives is a finding, not something to wait out: it means
// dispatch is not keeping up, or has stopped. So the timeout returns an error
// **together with** the latencies that were collected, since a partial
// measurement is still evidence and discarding it would hide how far short the
// run fell.
func (p *DeliveryProbe) Await(ctx context.Context, timeout time.Duration) ([]time.Duration, error) {
	p.mu.Lock()
	if len(p.sent) == 0 {
		got := slices.Clone(p.latencies)
		p.mu.Unlock()
		return got, nil
	}
	if p.waiters == nil {
		p.waiters = make(chan struct{})
	}
	done := p.waiters
	p.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(timeout):
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	got := slices.Clone(p.latencies)
	if outstanding := len(p.sent); outstanding > 0 {
		return got, fmt.Errorf("%d of %d events were not delivered within %s",
			outstanding, outstanding+len(got), timeout)
	}
	return got, nil
}

// Reset clears the probe between swept points, so one point's latencies are not
// attributed to the next.
func (p *DeliveryProbe) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = map[string]time.Time{}
	p.latencies = nil
	p.waiters = nil
}

// Percentile returns the p-th percentile of ds, or zero if there are none.
//
// The tail matters more than the middle here: a median that stays flat while
// the ninety-ninth percentile climbs is exactly what a fan-out cost looks like
// before it becomes everyone's problem.
//
// Nearest-rank, and not the more obvious `int((n-1) * p)`. That formula
// truncates, so on four samples it returns the third for p99 rather than the
// fourth — it systematically reports a lower value than the percentile asked
// for, and does so worst exactly at the tail where the interesting behaviour
// is. A p99 that quietly means "p75" would understate the very cost this
// harness exists to find.
func Percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)

	rank := int(math.Ceil(p * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

// DeliverAllOutstanding marks every outstanding send as delivered.
//
// For tests that stand in for the controllers. Production callers report one
// workspace at a time through Delivered, because attributing a delivery to the
// wrong workspace would produce a latency that looks plausible and means
// nothing.
func (p *DeliveryProbe) DeliverAllOutstanding() {
	p.mu.Lock()
	workspaces := make([]string, 0, len(p.sent))
	for ws := range p.sent {
		workspaces = append(workspaces, ws)
	}
	p.mu.Unlock()

	for _, ws := range workspaces {
		p.Delivered(ws)
	}
}
