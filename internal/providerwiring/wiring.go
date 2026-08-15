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
	"maps"
	"net/http"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ErrDisengaged is returned when a runnable is registered against a workspace
// that has already gone away. A SetupFunc racing a disengagement sees it, and
// should treat it as a reason to stop rather than as a failure to report: the
// workspace it was setting up no longer exists.
var ErrDisengaged = errors.New("workspace has been disengaged")

// Options configures Wiring.
type Options struct {
	// Log is where per-workspace lifecycle events and the failures of
	// per-workspace runnables are reported. Runnable failures have nowhere
	// else to go: a runnable outlives the call that registered it, so there
	// is no caller left to return an error to.
	Log logr.Logger
}

// Wiring runs a SetupFunc once for each workspace a provider engages, and
// stops that workspace's work when it is disengaged.
//
// It implements multicluster-runtime's Runnable, so it is registered with a
// multi-cluster manager via AddToManager and receives engagements from there.
// See this package's documentation for the lifecycle contract it exists to
// keep.
type Wiring struct {
	managers ManagerGetter
	setup    SetupFunc
	log      logr.Logger

	mu      sync.Mutex
	started bool
	engaged map[multicluster.ClusterName]*runnableGroup
}

var _ mcmanager.Runnable = (*Wiring)(nil)

// New returns Wiring that runs setup for each workspace managers yields.
//
// Prefer AddToManager, which pairs construction with registration; New is for
// tests and for callers that hold a ManagerGetter rather than a manager.
func New(managers ManagerGetter, setup SetupFunc, opts Options) (*Wiring, error) {
	if managers == nil {
		return nil, errors.New("a ManagerGetter is required")
	}
	if setup == nil {
		return nil, errors.New("a SetupFunc is required")
	}
	return &Wiring{
		managers: managers,
		setup:    setup,
		log:      opts.Log,
		engaged:  map[multicluster.ClusterName]*runnableGroup{},
	}, nil
}

// AddToManager registers per-workspace wiring with mgr.
//
// It MUST be called before mgr.Start. multicluster-runtime's coordinator
// hands each engagement to the components registered at that moment and never
// replays earlier ones, so wiring registered after the manager is running
// misses every workspace that engaged before it — silently, since neither the
// coordinator nor the manager treats it as an error. This function cannot
// observe the manager's state to enforce that, so it is stated as a caller
// obligation; what it does enforce is that one Wiring is used once, which
// catches the reuse that would otherwise produce the same symptom.
func AddToManager(mgr mcmanager.Manager, setup SetupFunc, opts Options) (*Wiring, error) {
	w, err := New(mgr, setup, opts)
	if err != nil {
		return nil, err
	}
	if err := mgr.Add(w); err != nil {
		return nil, fmt.Errorf("registering per-workspace wiring: %w", err)
	}
	return w, nil
}

// Engage sets up one workspace. It is called by the multi-cluster manager
// when a workspace joins, with a context that is cancelled when it leaves.
func (w *Wiring) Engage(ctx context.Context, workspace multicluster.ClusterName, _ cluster.Cluster) error {
	log := w.log.WithValues("workspace", workspace)

	w.mu.Lock()
	if _, ok := w.engaged[workspace]; ok {
		// Already set up. The provider de-duplicates engagements itself,
		// but documents the check as racy, so this is not unreachable.
		w.mu.Unlock()
		return nil
	}
	group := newRunnableGroup(ctx, log)
	w.engaged[workspace] = group
	w.mu.Unlock()

	// Watch for disengagement from here rather than after setup completes: a
	// workspace can go away while its setup is still running, and that has to
	// stop the runnables the partial setup already registered.
	go func() {
		<-ctx.Done()
		w.disengage(workspace)
		log.Info("Disengaged workspace")
	}()

	mgr, err := w.managers.GetManager(ctx, workspace)
	if err != nil {
		w.disengage(workspace)
		return fmt.Errorf("getting manager for workspace %s: %w", workspace, err)
	}

	if err := w.setup(ctx, workspace, &workspaceManager{Manager: mgr, group: group}); err != nil {
		w.disengage(workspace)
		return fmt.Errorf("setting up workspace %s: %w", workspace, err)
	}

	log.Info("Engaged workspace")
	return nil
}

// Start blocks until ctx is cancelled, then waits for every engaged
// workspace's runnables to stop. It satisfies manager.Runnable.
func (w *Wiring) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return ErrStarted
	}
	w.started = true
	w.mu.Unlock()

	<-ctx.Done()

	w.mu.Lock()
	groups := slices.Collect(maps.Values(w.engaged))
	clear(w.engaged)
	w.mu.Unlock()

	for _, group := range groups {
		group.stop()
	}
	return nil
}

// Engaged returns the workspaces currently set up, in sorted order.
func (w *Wiring) Engaged() []multicluster.ClusterName {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Sorted(maps.Keys(w.engaged))
}

// disengage stops one workspace's runnables and forgets it. It is safe to
// call for a workspace that is not engaged, and safe to call twice: setup
// failure and disengagement can race, and both end here.
func (w *Wiring) disengage(workspace multicluster.ClusterName) {
	w.mu.Lock()
	group, ok := w.engaged[workspace]
	delete(w.engaged, workspace)
	w.mu.Unlock()

	if ok {
		group.stop()
	}
}

// runnableGroup runs the runnables registered for one workspace, under a
// context of its own so they can be stopped without waiting for the process
// to end.
type runnableGroup struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    logr.Logger

	wg sync.WaitGroup

	mu      sync.Mutex
	stopped bool
}

func newRunnableGroup(parent context.Context, log logr.Logger) *runnableGroup {
	ctx, cancel := context.WithCancel(parent)
	return &runnableGroup{ctx: ctx, cancel: cancel, log: log}
}

// Add starts r under the group's context.
//
// Runnables are started as they are registered rather than being collected
// and started later, which is safe here and is not in a controller-runtime
// manager: a workspace is engaged only after its cache has synced, so there
// is no cache-then-everything-else ordering left to observe.
func (g *runnableGroup) Add(r manager.Runnable) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return ErrDisengaged
	}

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := r.Start(g.ctx); err != nil && !errors.Is(err, context.Canceled) {
			g.log.Error(err, "Per-workspace runnable failed")
		}
	}()
	return nil
}

// stop cancels the group's runnables and waits for them to return.
func (g *runnableGroup) stop() {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()

	g.cancel()
	g.wg.Wait()
}

// workspaceManager is the manager a SetupFunc is handed: the workspace-scoped
// manager from multicluster-runtime, with the two methods that would
// otherwise cross a workspace boundary replaced.
type workspaceManager struct {
	manager.Manager
	group *runnableGroup
}

// Add binds the runnable to this workspace instead of to the process.
//
// The manager this embeds delegates Add to the host manager, which means a
// controller registered for a workspace keeps running after that workspace is
// gone — the context supplied to Engage is cancelled and nobody is listening.
// Registering with the workspace's own group is what makes disengagement mean
// something.
func (m *workspaceManager) Add(r manager.Runnable) error {
	return m.group.Add(r)
}

// GetWebhookServer returns a server that refuses registration.
//
// Webhook handlers registered here would be registered on the process-wide
// server shared by every workspace, and controller-runtime's webhook builder
// skips a path that is already taken rather than rejecting it — so the first
// workspace's handlers, holding the first workspace's client, would go on
// serving every workspace's admission requests with nothing logged. Refusing
// loudly is the point; see ErrWebhooksAlreadyWired and the conversion plan's
// G4.
func (m *workspaceManager) GetWebhookServer() webhook.Server {
	return refusingWebhookServer{Server: m.Manager.GetWebhookServer()}
}

// refusingWebhookServer is a webhook.Server that will not accept handlers.
type refusingWebhookServer struct {
	webhook.Server
}

// Register panics. It cannot return an error — controller-runtime's interface
// has no error to return — and the alternative to panicking is to accept a
// registration that silently serves the wrong tenant. This is reachable only
// from a SetupFunc doing something the seam's contract forbids, at startup,
// deterministically.
func (refusingWebhookServer) Register(path string, _ http.Handler) {
	panic(fmt.Errorf("per-workspace setup registered a webhook at %q: %w", path, ErrWebhooksAlreadyWired))
}
