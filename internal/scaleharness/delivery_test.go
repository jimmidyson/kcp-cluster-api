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

func TestProbeMeasuresOneDelivery(t *testing.T) {
	p := NewDeliveryProbe()

	p.Sent("ws-a")
	time.Sleep(5 * time.Millisecond)
	p.Delivered("ws-a")

	got, err := p.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("recorded %d latencies, want 1", len(got))
	}
	if got[0] < 5*time.Millisecond {
		t.Errorf("latency %s is below the delay that was introduced", got[0])
	}
}

func TestProbeWaitsForEveryOutstandingDelivery(t *testing.T) {
	p := NewDeliveryProbe()

	for _, ws := range []string{"a", "b", "c"} {
		p.Sent(ws)
	}
	go func() {
		for _, ws := range []string{"a", "b", "c"} {
			time.Sleep(2 * time.Millisecond)
			p.Delivered(ws)
		}
	}()

	got, err := p.Await(t.Context(), 5*time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("recorded %d latencies, want 3", len(got))
	}
}

// An event that never arrives is the failure this exists to detect, so it must
// be reported rather than waited on forever — and reported as a shortfall, not
// as a fast delivery that happened not to be recorded.
func TestUndeliveredEventsAreReported(t *testing.T) {
	p := NewDeliveryProbe()

	p.Sent("arrives")
	p.Sent("never-arrives")
	p.Delivered("arrives")

	got, err := p.Await(t.Context(), 50*time.Millisecond)
	if err == nil {
		t.Fatal("Await succeeded with an event still outstanding")
	}
	if len(got) != 1 {
		t.Errorf("returned %d latencies, want the one that did arrive", len(got))
	}
}

// Watches replay their store on registration and controllers resync, so
// reconciles arrive for workspaces the harness is not currently timing. Counting
// those would invent deliveries that answer no question.
func TestDeliveriesWithoutASendAreIgnored(t *testing.T) {
	p := NewDeliveryProbe()

	p.Delivered("unsolicited")
	p.Sent("real")
	p.Delivered("real")

	got, err := p.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("recorded %d latencies, want 1 — an unsolicited reconcile is not a measurement", len(got))
	}
}

// Only the first delivery after a send is the one being timed; a controller may
// reconcile the same object repeatedly, and later passes would understate the
// latency by starting the clock from the wrong event.
func TestOnlyTheFirstDeliveryCounts(t *testing.T) {
	p := NewDeliveryProbe()

	p.Sent("ws")
	time.Sleep(5 * time.Millisecond)
	p.Delivered("ws")
	p.Delivered("ws")
	p.Delivered("ws")

	got, err := p.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("recorded %d latencies for one send, want 1", len(got))
	}
}

func TestProbeIsReusableAcrossPoints(t *testing.T) {
	p := NewDeliveryProbe()

	p.Sent("ws")
	p.Delivered("ws")
	if _, err := p.Await(t.Context(), time.Second); err != nil {
		t.Fatalf("first Await: %v", err)
	}

	p.Reset()

	p.Sent("ws")
	p.Delivered("ws")
	got, err := p.Await(t.Context(), time.Second)
	if err != nil {
		t.Fatalf("second Await: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("second point recorded %d latencies, want 1 — Reset must clear the previous point", len(got))
	}
}

func TestPercentilesSummariseLatencies(t *testing.T) {
	ds := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		100 * time.Millisecond,
	}

	if got := Percentile(ds, 0.5); got != 2*time.Millisecond {
		t.Errorf("p50 = %s, want 2ms", got)
	}
	if got := Percentile(ds, 0.99); got != 100*time.Millisecond {
		t.Errorf("p99 = %s, want 100ms — the tail is the point", got)
	}
	if got := Percentile(nil, 0.5); got != 0 {
		t.Errorf("p50 of nothing = %s, want 0", got)
	}
}
