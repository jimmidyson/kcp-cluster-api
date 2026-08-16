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
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/workspacetelemetry"
)

// waitFor fails the test if c is not closed promptly. Everything this package
// does is asynchronous, so the alternative to a bounded wait is a test that
// hangs rather than one that fails.
func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// stillOpen fails the test if c has been closed. Used to assert that one
// workspace's shutdown left another workspace alone.
func stillOpen(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("%s happened and should not have", what)
	default:
	}
}

// fakeManager is a manager.Manager that panics for everything this package
// does not use, so a test fails loudly if the implementation starts reaching
// somewhere new rather than passing on a zero value.
type fakeManager struct {
	manager.Manager
	webhookServer webhook.Server
}

func (f *fakeManager) GetWebhookServer() webhook.Server { return f.webhookServer }

type fakeWebhookServer struct{ webhook.Server }

// fakeManagers hands out one fakeManager per workspace and records the
// workspaces it was asked about.
type fakeManagers struct {
	mu       sync.Mutex
	asked    []multicluster.ClusterName
	managers map[multicluster.ClusterName]*fakeManager
	err      error
}

func newFakeManagers() *fakeManagers {
	return &fakeManagers{managers: map[multicluster.ClusterName]*fakeManager{}}
}

func (f *fakeManagers) GetManager(_ context.Context, workspace multicluster.ClusterName) (manager.Manager, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, workspace)
	if f.err != nil {
		return nil, f.err
	}
	if _, ok := f.managers[workspace]; !ok {
		f.managers[workspace] = &fakeManager{webhookServer: fakeWebhookServer{}}
	}
	return f.managers[workspace], nil
}

func (f *fakeManagers) askedFor() []multicluster.ClusterName {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// blockingRunnable runs until its context is cancelled, and reports both
// events. It is the stand-in for a controller: the thing whose continuing to
// run after its workspace has gone is the defect being tested for.
type blockingRunnable struct {
	started  chan struct{}
	stopped  chan struct{}
	startErr error
}

func newBlockingRunnable() *blockingRunnable {
	return &blockingRunnable{started: make(chan struct{}), stopped: make(chan struct{})}
}

func (r *blockingRunnable) Start(ctx context.Context) error {
	close(r.started)
	defer close(r.stopped)
	if r.startErr != nil {
		return r.startErr
	}
	<-ctx.Done()
	return nil
}

// newWiring builds Wiring with a discarding logger, failing the test if
// construction does.
func newWiring(t *testing.T, managers ManagerGetter, setup SetupFunc) *Wiring {
	t.Helper()
	w, err := New(managers, setup, Options{Log: logr.Discard()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return w
}

// TestEngageRunsSetupPerWorkspace covers FR-001 and FR-007: every engaged
// workspace is set up, none is named anywhere, and each gets its own manager.
func TestEngageRunsSetupPerWorkspace(t *testing.T) {
	managers := newFakeManagers()

	var mu sync.Mutex
	var setupFor []multicluster.ClusterName
	w := newWiring(t, managers, func(_ context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error {
		if mgr == nil {
			t.Error("setup was given a nil manager")
		}
		mu.Lock()
		defer mu.Unlock()
		setupFor = append(setupFor, workspace)
		return nil
	})

	ctx := t.Context()
	for _, workspace := range []multicluster.ClusterName{"tenant-a", "tenant-b"} {
		if err := w.Engage(ctx, workspace, nil); err != nil {
			t.Fatalf("Engage(%s): %v", workspace, err)
		}
	}

	mu.Lock()
	got := slices.Clone(setupFor)
	mu.Unlock()
	slices.Sort(got)

	want := []multicluster.ClusterName{"tenant-a", "tenant-b"}
	if !slices.Equal(got, want) {
		t.Errorf("setup ran for %v, want %v", got, want)
	}
	if asked := managers.askedFor(); !slices.Equal(asked, []multicluster.ClusterName{"tenant-a", "tenant-b"}) {
		t.Errorf("asked for managers %v, want one per workspace", asked)
	}
	if engaged := w.Engaged(); !slices.Equal(engaged, want) {
		t.Errorf("Engaged() = %v, want %v", engaged, want)
	}
}

// TestEngageIsIdempotent covers FR-002. The provider de-duplicates
// engagements itself but documents that check as racy, so a repeat call is
// reachable and must not wire a second copy of everything.
func TestEngageIsIdempotent(t *testing.T) {
	var calls int
	w := newWiring(t, newFakeManagers(), func(context.Context, multicluster.ClusterName, manager.Manager) error {
		calls++
		return nil
	})

	ctx := t.Context()
	for range 3 {
		if err := w.Engage(ctx, "tenant-a", nil); err != nil {
			t.Fatalf("Engage: %v", err)
		}
	}

	if calls != 1 {
		t.Errorf("setup ran %d times for one workspace, want 1", calls)
	}
}

// TestRunnablesStopOnDisengage covers FR-004, and is the reason this package
// interposes a manager at all: multicluster-runtime's own per-workspace
// manager delegates Add to the host manager, so a controller registered
// through it would still be running here.
func TestRunnablesStopOnDisengage(t *testing.T) {
	runnable := newBlockingRunnable()
	w := newWiring(t, newFakeManagers(), func(_ context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
		return mgr.Add(runnable)
	})

	ctx, disengage := context.WithCancel(t.Context())
	if err := w.Engage(ctx, "tenant-a", nil); err != nil {
		t.Fatalf("Engage: %v", err)
	}
	waitFor(t, runnable.started, "the runnable to start")

	disengage()

	waitFor(t, runnable.stopped, "the runnable to stop after disengagement")
	waitForEmpty(t, w)
}

// TestDisengageLeavesOtherWorkspacesRunning covers FR-007 at this layer:
// workspaces share a process, and one leaving must not stop another's work.
func TestDisengageLeavesOtherWorkspacesRunning(t *testing.T) {
	runnables := map[multicluster.ClusterName]*blockingRunnable{
		"tenant-a": newBlockingRunnable(),
		"tenant-b": newBlockingRunnable(),
	}
	w := newWiring(t, newFakeManagers(), func(_ context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error {
		return mgr.Add(runnables[workspace])
	})

	ctxA, disengageA := context.WithCancel(t.Context())
	if err := w.Engage(ctxA, "tenant-a", nil); err != nil {
		t.Fatalf("Engage(tenant-a): %v", err)
	}
	if err := w.Engage(t.Context(), "tenant-b", nil); err != nil {
		t.Fatalf("Engage(tenant-b): %v", err)
	}
	waitFor(t, runnables["tenant-a"].started, "tenant-a's runnable to start")
	waitFor(t, runnables["tenant-b"].started, "tenant-b's runnable to start")

	disengageA()
	waitFor(t, runnables["tenant-a"].stopped, "tenant-a's runnable to stop")

	stillOpen(t, runnables["tenant-b"].stopped, "tenant-b's runnable stopping")
	waitForEngaged(t, w, "tenant-b")
}

// TestReEngageAfterDisengage covers FR-005. A workspace that unbinds and
// rebinds has to work the second time; this is the case that fails outright
// if per-engagement state accumulates.
func TestReEngageAfterDisengage(t *testing.T) {
	var mu sync.Mutex
	var calls int
	w := newWiring(t, newFakeManagers(), func(context.Context, multicluster.ClusterName, manager.Manager) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return nil
	})

	ctx, disengage := context.WithCancel(t.Context())
	if err := w.Engage(ctx, "tenant-a", nil); err != nil {
		t.Fatalf("first Engage: %v", err)
	}
	disengage()
	waitForEmpty(t, w)

	if err := w.Engage(t.Context(), "tenant-a", nil); err != nil {
		t.Fatalf("second Engage: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("setup ran %d times across two engagements, want 2", calls)
	}
}

// TestSetupFailureIsReportedAndIsolated covers FR-006: the failure surfaces to
// the caller, the failed workspace is not left half-engaged, and the next
// workspace is unaffected.
func TestSetupFailureIsReportedAndIsolated(t *testing.T) {
	boom := errors.New("boom")
	w := newWiring(t, newFakeManagers(), func(_ context.Context, workspace multicluster.ClusterName, _ manager.Manager) error {
		if workspace == "tenant-a" {
			return boom
		}
		return nil
	})

	ctx := t.Context()
	if err := w.Engage(ctx, "tenant-a", nil); !errors.Is(err, boom) {
		t.Errorf("Engage(tenant-a) = %v, want it to wrap %v", err, boom)
	}
	if err := w.Engage(ctx, "tenant-b", nil); err != nil {
		t.Errorf("Engage(tenant-b) = %v, want success", err)
	}

	if engaged := w.Engaged(); !slices.Equal(engaged, []multicluster.ClusterName{"tenant-b"}) {
		t.Errorf("Engaged() = %v, want only tenant-b: a failed setup must not stay engaged", engaged)
	}
}

// TestSetupFailureStopsPartialWiring covers the half-wired case: setup that
// registers a runnable and then fails must not leave that runnable running.
func TestSetupFailureStopsPartialWiring(t *testing.T) {
	runnable := newBlockingRunnable()
	boom := errors.New("boom")
	w := newWiring(t, newFakeManagers(), func(_ context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
		if err := mgr.Add(runnable); err != nil {
			return err
		}
		return boom
	})

	if err := w.Engage(t.Context(), "tenant-a", nil); !errors.Is(err, boom) {
		t.Fatalf("Engage = %v, want it to wrap %v", err, boom)
	}

	waitFor(t, runnable.stopped, "the runnable registered before the failure to stop")
}

// TestGetManagerFailureIsReported covers the other setup-path failure: the
// workspace's manager cannot be obtained.
func TestGetManagerFailureIsReported(t *testing.T) {
	managers := newFakeManagers()
	managers.err = errors.New("not engaged yet")

	var called bool
	w := newWiring(t, managers, func(context.Context, multicluster.ClusterName, manager.Manager) error {
		called = true
		return nil
	})

	if err := w.Engage(t.Context(), "tenant-a", nil); !errors.Is(err, managers.err) {
		t.Errorf("Engage = %v, want it to wrap %v", err, managers.err)
	}
	if called {
		t.Error("setup ran despite the workspace's manager being unavailable")
	}
	if engaged := w.Engaged(); len(engaged) != 0 {
		t.Errorf("Engaged() = %v, want none", engaged)
	}
}

// TestStartWaitsForRunnables covers graceful shutdown: Start returns only once
// the work it is responsible for has actually stopped.
func TestStartWaitsForRunnables(t *testing.T) {
	runnable := newBlockingRunnable()
	w := newWiring(t, newFakeManagers(), func(_ context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
		return mgr.Add(runnable)
	})

	ctx, stop := context.WithCancel(t.Context())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		if err := w.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
	}()

	if err := w.Engage(t.Context(), "tenant-a", nil); err != nil {
		t.Fatalf("Engage: %v", err)
	}
	waitFor(t, runnable.started, "the runnable to start")
	stillOpen(t, returned, "Start returning before its context was cancelled")

	stop()
	waitFor(t, returned, "Start to return")

	select {
	case <-runnable.stopped:
	default:
		t.Error("Start returned before the workspace's runnable had stopped")
	}
}

// TestStartTwiceIsRejected covers the enforceable half of FR-003: one Wiring
// belongs to one manager, so a second Start means it was reused.
func TestStartTwiceIsRejected(t *testing.T) {
	w := newWiring(t, newFakeManagers(), func(context.Context, multicluster.ClusterName, manager.Manager) error {
		return nil
	})

	// Run the first Start to completion, by handing it a context that is
	// already cancelled, rather than racing it from a goroutine: which of two
	// concurrent Starts wins is not the property under test.
	ctx, stop := context.WithCancel(t.Context())
	stop()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	if err := w.Start(t.Context()); !errors.Is(err, ErrStarted) {
		t.Errorf("second Start = %v, want %v", err, ErrStarted)
	}
}

// TestAddAfterDisengageIsRejected covers a SetupFunc racing a disengagement:
// registering against a workspace that has gone must fail rather than start
// work nothing will ever stop.
func TestAddAfterDisengageIsRejected(t *testing.T) {
	var mgr manager.Manager
	w := newWiring(t, newFakeManagers(), func(_ context.Context, _ multicluster.ClusterName, m manager.Manager) error {
		mgr = m
		return nil
	})

	ctx, disengage := context.WithCancel(t.Context())
	if err := w.Engage(ctx, "tenant-a", nil); err != nil {
		t.Fatalf("Engage: %v", err)
	}
	disengage()
	waitForEmpty(t, w)

	if err := mgr.Add(newBlockingRunnable()); !errors.Is(err, ErrDisengaged) {
		t.Errorf("Add after disengagement = %v, want %v", err, ErrDisengaged)
	}
}

// TestWebhookRegistrationIsRefused covers FR-008's structural half. Wiring
// webhooks per workspace does not fail on its own — controller-runtime skips
// a path that is already registered — so the first workspace's handlers, and
// its client, would serve every workspace. The seam refuses instead.
func TestWebhookRegistrationIsRefused(t *testing.T) {
	var recovered any
	w := newWiring(t, newFakeManagers(), func(_ context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
		defer func() { recovered = recover() }()
		mgr.GetWebhookServer().Register("/validate-cluster", http.NotFoundHandler())
		return nil
	})

	if err := w.Engage(t.Context(), "tenant-a", nil); err != nil {
		t.Fatalf("Engage: %v", err)
	}

	err, ok := recovered.(error)
	if !ok {
		t.Fatalf("registering a per-workspace webhook did not panic, it returned %v", recovered)
	}
	if !errors.Is(err, ErrWebhooksAlreadyWired) {
		t.Errorf("panic = %v, want it to wrap %v", err, ErrWebhooksAlreadyWired)
	}
}

// waitForEngaged waits until exactly want is engaged.
//
// Disengagement is observed asynchronously — the workspace's context is
// cancelled, and the wiring notices — so a test that reads Engaged()
// immediately after cancelling races that. Waiting for the expected set is not
// a weaker assertion than reading it once: the set still has to be exactly
// want, and the test still fails if it never gets there.
// TestTelemetryRecordsEngagementOutcomes covers FR-018.
//
// The harness cannot report load the process does not expose, and engagement
// is where a workspace's existence becomes observable at all. Reasons are fixed
// categories rather than error text: they are a metric label, so deriving them
// from an error string would make their cardinality unbounded — the problem the
// telemetry package exists to avoid.
func TestTelemetryRecordsEngagementOutcomes(t *testing.T) {
	recorder := workspacetelemetry.New(workspacetelemetry.Options{})

	managers := newFakeManagers()
	var failNext bool
	w, err := New(managers, func(context.Context, multicluster.ClusterName, manager.Manager) error {
		if failNext {
			return errors.New("setup exploded")
		}
		return nil
	}, Options{Log: logr.Discard(), Telemetry: recorder})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, disengage := context.WithCancel(t.Context())
	if err := w.Engage(ctx, "good", nil); err != nil {
		t.Fatalf("Engage(good): %v", err)
	}
	if got := recorder.Snapshot().EngagedWorkspaces; got != 1 {
		t.Errorf("engaged workspaces = %d after one success, want 1", got)
	}

	failNext = true
	if err := w.Engage(t.Context(), "bad", nil); err == nil {
		t.Fatal("Engage(bad) succeeded; the test needs it to fail")
	}

	snap := recorder.Snapshot()
	if snap.EngagementFailures != 1 {
		t.Errorf("engagement failures = %d, want 1", snap.EngagementFailures)
	}
	if snap.EngagedWorkspaces != 1 {
		t.Errorf("engaged = %d after one success and one failure, want 1: a failed engagement is not an engaged workspace", snap.EngagedWorkspaces)
	}
	for reason := range snap.EngagementFailuresByReason {
		if strings.Contains(reason, "exploded") {
			t.Errorf("failure reason %q carries the error text: reasons are labels, and unbounded labels are the cardinality problem this avoids", reason)
		}
	}

	disengage()
	waitForEmpty(t, w)
	if got := recorder.Snapshot().EngagedWorkspaces; got != 0 {
		t.Errorf("engaged = %d after disengagement, want 0", got)
	}
}

// Telemetry is optional, and nothing about wiring may depend on it being
// present: a nil recorder is the configuration every existing caller uses.
func TestWiringWorksWithoutTelemetry(t *testing.T) {
	w := newWiring(t, newFakeManagers(), func(context.Context, multicluster.ClusterName, manager.Manager) error {
		return nil
	})

	if err := w.Engage(t.Context(), "tenant-a", nil); err != nil {
		t.Fatalf("Engage with no recorder configured: %v", err)
	}
}

// TestSustainedChurnLeavesNoResidue covers FR-012 at the scale that makes it
// matter.
//
// TestRunnablesStopOnDisengage already shows that one workspace's runnables
// stop. This asserts the property the fleet target depends on: that repeating
// engage and disengage many times leaves nothing behind. A per-workspace leak
// is individually small and therefore invisible in a two-workspace test, and
// the appliance model's whole premise is that a shard's cost is bounded — which
// a leak under churn quietly falsifies.
//
// What this discriminates, established by mutating the implementation rather
// than assumed:
//
//   - Residue in the engaged map is caught: dropping the delete in disengage
//     fails this test and passes the rest of the suite.
//   - A runnable that never terminates is caught, since every cycle's runnable
//     is waited on.
//
// What it does *not* catch, recorded so the next reader does not over-trust
// it: removing group.stop() entirely still passes, because a group's context
// descends from the engagement's, so cancelling the engagement stops the
// runnables by propagation regardless. group.stop()'s distinct value is the
// synchronous wait, which matters on Start's shutdown path rather than here.
//
// The goroutine count is a backstop for leaks that are anchored to neither of
// the above, not the primary assertion.
func TestSustainedChurnLeavesNoResidue(t *testing.T) {
	const cycles = 50

	managers := newFakeManagers()
	var mu sync.Mutex
	var runnables []*blockingRunnable

	w := newWiring(t, managers, func(_ context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
		r := newBlockingRunnable()
		mu.Lock()
		runnables = append(runnables, r)
		mu.Unlock()
		return mgr.Add(r)
	})

	settle(t)
	baseline := runtime.NumGoroutine()

	for i := range cycles {
		ctx, cancel := context.WithCancel(t.Context())
		workspace := multicluster.ClusterName(fmt.Sprintf("churn-%d", i))
		if err := w.Engage(ctx, workspace, nil); err != nil {
			cancel()
			t.Fatalf("Engage %s: %v", workspace, err)
		}
		waitForEngaged(t, w, workspace)
		cancel()
		waitForEmpty(t, w)
	}

	mu.Lock()
	started := runnables
	mu.Unlock()

	if len(started) != cycles {
		t.Fatalf("started %d runnables, want %d", len(started), cycles)
	}
	for i, r := range started {
		select {
		case <-r.stopped:
		case <-time.After(10 * time.Second):
			t.Fatalf("runnable %d never stopped: work for a departed workspace outlives it", i)
		}
	}

	settle(t)
	// A small allowance absorbs runtime bookkeeping; the failure this guards
	// against is growth proportional to cycles, not a handful of stragglers.
	if grew := runtime.NumGoroutine() - baseline; grew > cycles/10 {
		t.Errorf("goroutines grew by %d over %d engage/disengage cycles (baseline %d): per-workspace state is surviving disengagement",
			grew, cycles, baseline)
	}
}

// settle gives stopping goroutines a chance to finish before they are counted.
// Without it the check races teardown and reports leaks that are not leaks.
func settle(t *testing.T) {
	t.Helper()
	for range 20 {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForEngaged(t *testing.T, w *Wiring, want ...multicluster.ClusterName) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := w.Engaged()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for engaged workspaces to be %v; they are %v", want, got)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForEmpty waits until no workspace is engaged.
func waitForEmpty(t *testing.T, w *Wiring) {
	t.Helper()
	waitForEngaged(t, w)
}
