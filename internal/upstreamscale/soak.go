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
	"sort"
	"strings"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// driftThreshold is the growth over a soak above which a component is said to
// have drifted.
//
// A judgement. A held fleet is doing steady-state work — resyncs, watch
// traffic, renewals — so its heap is not expected to be flat to the byte, and
// the samples are post-collection so they are the retained set rather than
// whatever had not been swept. Ten percent more retained heap over a soak, with
// the fleet unchanged, is something growing rather than something breathing.
const driftThreshold = 0.10

// ComponentDrift is what one component did while nothing was asked of it.
type ComponentDrift struct {
	Component       string  `json:"component"`
	FirstGoroutines int     `json:"firstGoroutines"`
	LastGoroutines  int     `json:"lastGoroutines"`
	FirstHeapBytes  uint64  `json:"firstHeapBytes"`
	LastHeapBytes   uint64  `json:"lastHeapBytes"`
	HeapGrowth      float64 `json:"heapGrowth"`
	GoroutineGrowth float64 `json:"goroutineGrowth"`
}

// Drifted reports whether either quantity moved enough to be worth a reader's
// attention.
func (d ComponentDrift) Drifted() bool {
	return d.HeapGrowth > driftThreshold || d.GoroutineGrowth > driftThreshold
}

// SoakResult is the held rung, measured over time.
type SoakResult struct {
	Duration   time.Duration    `json:"duration"`
	Clusters   int              `json:"clusters"`
	ReadyAtEnd int              `json:"readyAtEnd"`
	HeldFleet  bool             `json:"heldFleet"`
	Components []ComponentDrift `json:"components,omitempty"`
	// Measured is false when the soak had too few samples to say anything.
	Measured bool `json:"measured"`
}

// Component returns one component's drift.
func (s SoakResult) Component(name string) (ComponentDrift, bool) {
	for _, c := range s.Components {
		if c.Component == name {
			return c, true
		}
	}
	return ComponentDrift{}, false
}

// Drift reduces a soak's samples to what moved.
//
// # Why a soak is asked a different question from a climb
//
// A climb answers whether a fleet can be reached. Holding it is a separate
// question with a separate failure: a management cluster that converges and
// then leaks, or lets clusters fall out of Ready while nothing is being asked
// of it, has not reached that scale in any sense an operator cares about. So
// the soak reports the difference between its first and last samples rather
// than its last, and reports the fleet's readiness at the end alongside them,
// because clusters falling out is not visible in any process metric.
func Drift(samples []deployedscale.Sample, clusters, readyAtEnd int) SoakResult {
	out := SoakResult{Clusters: clusters, ReadyAtEnd: readyAtEnd, HeldFleet: readyAtEnd >= clusters}
	if len(samples) < 2 {
		return out
	}
	out.Measured = true
	first, last := samples[0], samples[len(samples)-1]
	out.Duration = last.Taken.Sub(first.Taken)

	names := map[string]struct{}{}
	for _, c := range first.Components {
		names[c.Component] = struct{}{}
	}
	var ordered []string
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		a, okA := first.Component(name)
		b, okB := last.Component(name)
		if !okA || !okB || !a.Pod.Comparable() || !b.Pod.Comparable() {
			// A restart resets every process metric, so a component that
			// restarted during the soak has no drift to report — which is
			// itself worse news than drift, and the report's own restart
			// warning is where it is said.
			continue
		}
		out.Components = append(out.Components, ComponentDrift{
			Component:       name,
			FirstGoroutines: a.Process.Goroutines,
			LastGoroutines:  b.Process.Goroutines,
			FirstHeapBytes:  a.Process.HeapAllocBytes,
			LastHeapBytes:   b.Process.HeapAllocBytes,
			HeapGrowth:      growth(float64(a.Process.HeapAllocBytes), float64(b.Process.HeapAllocBytes)),
			GoroutineGrowth: growth(float64(a.Process.Goroutines), float64(b.Process.Goroutines)),
		})
	}
	return out
}

func growth(from, to float64) float64 {
	if from == 0 {
		return 0
	}
	return (to - from) / from
}

// Describe is the soak's paragraph in the report.
func (s SoakResult) Describe() string {
	if !s.Measured {
		return "The soak was not measured: it has fewer than two samples, and drift is a difference."
	}

	var b strings.Builder
	if s.HeldFleet {
		fmt.Fprintf(&b, "Held %d clusters for %s.", s.Clusters, s.Duration.Round(time.Minute))
	} else {
		fmt.Fprintf(&b, "**Did not hold**: %d of %d clusters were still Ready after %s.",
			s.ReadyAtEnd, s.Clusters, s.Duration.Round(time.Minute))
	}

	var drifted []string
	for _, c := range s.Components {
		if c.Drifted() {
			drifted = append(drifted, fmt.Sprintf("%s (heap %+.0f%%, goroutines %+.0f%%)",
				c.Component, 100*c.HeapGrowth, 100*c.GoroutineGrowth))
		}
	}
	if len(drifted) == 0 {
		b.WriteString(" Nothing drifted: every component's retained heap and goroutine count " +
			"ended within 10% of where it started, with the fleet unchanged.")
		return b.String()
	}
	fmt.Fprintf(&b, " Drifted: %s. The fleet did not change during this, so what grew grew on its own.",
		strings.Join(drifted, "; "))
	return b.String()
}
