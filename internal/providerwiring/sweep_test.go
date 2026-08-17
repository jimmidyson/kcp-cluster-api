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

package providerwiring

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
)

// sweepWorkspaces is how far the unit-tier sweep goes. It is high enough that
// a per-workspace cost is a slope rather than a rounding error, and low enough
// to stay a unit test: nothing here does I/O.
const sweepWorkspaces = 16

// settleQuiet and settleTimeout bound the wait for the goroutine count to stop
// moving before a sample is taken. Everything this package starts is started
// by a `go` statement that has already run by the time Engage returns, so this
// is a guard against the scheduler rather than a wait for real work.
const (
	settleQuiet   = 300 * time.Millisecond
	settleTimeout = 10 * time.Second
)

// TestPerWorkspaceGoroutineCostIsFlatAndReclaimed is the unit tier of the
// active-workspace sweep: what this package itself costs per workspace, with
// no kcp server, no informers and no reconcilers in the measurement.
//
// It quantifies two things the specification asserts qualitatively.
// User story 2 says a workspace that unbinds "stops costing anything", and
// FR-004 says its runnables must not outlive it; both are statements about a
// curve, and neither is settled by a test that engages one workspace and
// watches a channel close. The sweep measures the slope: what one more
// workspace adds, and what is left behind once they have all gone.
//
// The expected slope is two goroutines per workspace, and they are accounted
// for rather than tolerated: one watches the engagement context for
// disengagement (Engage), and one runs the workspace's runnable
// (runnableGroup.Add). A third would mean this package had started keeping
// something per workspace that nobody had noticed.
func TestPerWorkspaceGoroutineCostIsFlatAndReclaimed(t *testing.T) {
	const goroutinesPerWorkspace = 2

	report := &sweep.Report{Title: "Per-workspace wiring cost (unit tier, fake provider)"}
	report.AddFact("workspaces", fmt.Sprint(sweepWorkspaces))
	report.AddFact("runnablesPerWorkspace", "1")
	t.Cleanup(func() { t.Logf("\n%s", report.Markdown()) })

	runnables := map[multicluster.ClusterName]*blockingRunnable{}
	w := newWiring(t, newFakeManagers(), func(_ context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error {
		return mgr.Add(runnables[workspace])
	})

	settle(t, "the baseline")
	baseline := sweep.Take(sweep.PhaseBaseline, "baseline", 0, nil)
	report.Add(baseline)

	// --- Up: one workspace at a time, sampled after each.
	disengage := make([]context.CancelFunc, 0, sweepWorkspaces)
	for i := 1; i <= sweepWorkspaces; i++ {
		workspace := multicluster.ClusterName(fmt.Sprintf("tenant-%02d", i))
		runnables[workspace] = newBlockingRunnable()

		ctx, cancel := context.WithCancel(t.Context())
		disengage = append(disengage, cancel)

		if err := w.Engage(ctx, workspace, nil); err != nil {
			t.Fatalf("Engage(%s): %v", workspace, err)
		}
		waitFor(t, runnables[workspace].started, fmt.Sprintf("%s's runnable to start", workspace))

		settle(t, fmt.Sprintf("%d workspaces engaged", i))
		report.Add(sweep.Take(sweep.PhaseActive, fmt.Sprintf("%d workspaces engaged", i), i, nil))
	}

	engaged := sweep.InPhase(report.Samples, sweep.PhaseActive)
	perWorkspace := sweep.PerWorkspace(engaged, sweep.Goroutines)
	report.AddFact("goroutinesPerWorkspace", fmt.Sprintf("%.2f", perWorkspace))

	if math.Abs(perWorkspace-goroutinesPerWorkspace) > 0.01 {
		t.Errorf("each workspace costs %.2f goroutines, want %d: one watching for disengagement and one running the workspace's runnable. "+
			"A higher number means this package now keeps something else per workspace; a lower one means a runnable is not being started",
			perWorkspace, goroutinesPerWorkspace)
	}

	// --- Down: every workspace leaves, and the process comes back to where it
	// started. The tolerance is a constant, not a per-workspace allowance:
	// anything that scales with the workspace count is the leak this asserts
	// against, however small its coefficient.
	for _, cancel := range disengage {
		cancel()
	}
	waitForEmpty(t, w)
	settle(t, "every workspace disengaged")

	after := sweep.Take(sweep.PhaseDisengaged, "every workspace disengaged", 0, nil)
	report.Add(after)

	residual := after.Goroutines - baseline.Goroutines
	report.AddFact("goroutinesLeftBehind", fmt.Sprint(residual))
	if residual > 2 {
		t.Errorf("%d goroutines outlived %d workspaces (%d at baseline, %d after they all left): "+
			"disengagement is meant to return the process to its baseline, and %.2f goroutines per workspace is a leak",
			residual, sweepWorkspaces, baseline.Goroutines, after.Goroutines, float64(residual)/sweepWorkspaces)
	}
}

// settle waits for the goroutine count to stop moving, failing the test if it
// never does: a sample taken from a process still in motion measures the
// scheduler rather than the design.
func settle(t *testing.T, what string) {
	t.Helper()
	if !sweep.Settle(settleQuiet, settleTimeout) {
		t.Fatalf("the goroutine count never settled at %s: a sample taken now would not mean anything", what)
	}
}
