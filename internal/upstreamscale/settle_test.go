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

func goroutines(pairs map[string]int) []deployedscale.ComponentSample {
	var out []deployedscale.ComponentSample
	for name, n := range pairs {
		out = append(out, deployedscale.ComponentSample{
			Component: name,
			Process:   deployedscale.ProcessSample{Goroutines: n},
		})
	}
	return out
}

// TestAStartingControllerIsNotABaseline.
//
// The 25x1 run's baseline caught the kubeadm control plane manager at 35
// goroutines. Three minutes later, with no fleet created in between, the same
// pod reported 375 — so the 35 was a manager that had not finished starting,
// not a cheap one. Every slope measured from that baseline is inflated by the
// 340 goroutines the manager was always going to open, and the run reported
// that rung's cost as half again what the settled runs did.
//
// A baseline is the zero point of every figure in the report, so it is taken
// once the numbers have stopped moving rather than as soon as the driver can
// reach a pod.
func TestAStartingControllerIsNotABaseline(t *testing.T) {
	starting := goroutines(map[string]int{"capi-controller-manager": 955, "kcp": 35})
	warmer := goroutines(map[string]int{"capi-controller-manager": 1084, "kcp": 375})
	if Settled(starting, warmer, 0.05) {
		t.Error("a manager that went from 35 to 375 goroutines was called settled")
	}

	// And the real thing: three runs agreed on the warm figure to about 1%.
	stable := goroutines(map[string]int{"capi-controller-manager": 1069, "kcp": 375})
	nearly := goroutines(map[string]int{"capi-controller-manager": 1061, "kcp": 373})
	if !Settled(stable, nearly, 0.05) {
		t.Error("two samples within 1% of each other were not called settled")
	}
}

// TestSettlingNeedsTheSameComponentsBothTimes. A component that appears in one
// sample and not the other is a controller whose pod was rolling, and calling
// that settled would take the baseline in the middle of it.
func TestSettlingNeedsTheSameComponentsBothTimes(t *testing.T) {
	before := goroutines(map[string]int{"a": 100, "b": 100})
	after := goroutines(map[string]int{"a": 100})
	if Settled(before, after, 0.05) {
		t.Error("samples covering different components were called settled")
	}
	if Settled(nil, nil, 0.05) {
		t.Error("two empty samples were called settled: nothing was measured")
	}
}

// TestTheReportSaysWhetherTheBaselineSettled, because a baseline that never
// stopped moving is a caveat on every number derived from it, and the run
// continues rather than failing — a moving baseline is still worth more than
// no run.
func TestTheReportSaysWhetherTheBaselineSettled(t *testing.T) {
	ok := SettleResult{Settled: true, Waited: 45 * time.Second}
	if d := ok.Describe(); !strings.Contains(d, "45s") || strings.Contains(d, "still") {
		t.Errorf("a settled baseline reads as %q", d)
	}
	moving := SettleResult{Waited: 3 * time.Minute, Worst: "capd-controller-manager", WorstChange: 0.42}
	d := moving.Describe()
	for _, want := range []string{"still", "capd-controller-manager", "42"} {
		if !strings.Contains(d, want) {
			t.Errorf("an unsettled baseline does not say %q: %q", want, d)
		}
	}
}
