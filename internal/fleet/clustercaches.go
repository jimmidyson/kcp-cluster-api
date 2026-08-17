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

// Package fleet holds the adapters that let one controller serve every
// workspace while the components it depends on stay per-workspace.
//
// Cluster API's fleet-wide setup functions
// (SetupWithMulticlusterManager) require exactly two things this package
// supplies, and cannot supply themselves:
//
//   - a clustercache.ClusterCache that resolves the workspace from the context,
//     because a reconciler holds one and serves many; and
//   - a clustercache.MulticlusterClusterSourceFunc, because
//     ClusterCache.GetClusterSource takes no context and so cannot resolve
//     anything.
//
// Both are backed by ClusterCaches, a registry of the per-workspace
// ClusterCaches this process runs.
package fleet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"sigs.k8s.io/cluster-api/controllers/clustercache"
)

// ErrNoWorkspaceInContext is returned when a scoped ClusterCache is called with
// a context that names no workspace.
//
// It is an error rather than a fallback for the same reason the cluster-aware
// client's is: a missing workspace is a wiring mistake, and choosing one anyway
// would answer a workload-cluster question with another tenant's connection.
var ErrNoWorkspaceInContext = fmt.Errorf("no workspace in context")

// ErrWorkspaceNotRegistered is returned when a context names a workspace this
// process holds no ClusterCache for — typically one that has disengaged.
var ErrWorkspaceNotRegistered = fmt.Errorf("no ClusterCache registered for workspace")

// ClusterCaches is the set of per-workspace ClusterCaches, and the two views of
// it that a fleet-wide controller needs.
//
// # Why the ClusterCaches stay per-workspace
//
// ClusterCache keys its accessors and its last-event times by namespace and
// name with no workspace. One instance across the fleet would let two
// workspaces' identically named Clusters share a connection to a workload
// cluster, which is a cross-tenant fault rather than a wrong answer. ADR-0003
// took containment over conversion here: the accessors never meet, so the bug
// never arises.
//
// The cost is that each workspace's ClusterCache needs its own manager to be
// built on, which is why this is a registry rather than a single value. That
// cost was priced when the decision was taken — about 15 goroutines per
// workspace, against core's 53 — and this is where it is paid.
type ClusterCaches struct {
	mu     sync.RWMutex
	caches map[multicluster.ClusterName]clustercache.ClusterCache
	// sources are the fan-ins handed to controllers. Held so that a workspace
	// engaging later is attached to each of them, which is the whole difficulty:
	// a controller registers its sources once, at build time, and workspaces
	// arrive and leave for as long as the process runs.
	sources []*clusterSourceFanIn
}

// NewClusterCaches returns an empty registry.
func NewClusterCaches() *ClusterCaches {
	return &ClusterCaches{caches: map[multicluster.ClusterName]clustercache.ClusterCache{}}
}

// Add registers a workspace's ClusterCache and attaches it to every source
// already handed out. Call it as the workspace engages.
func (c *ClusterCaches) Add(workspace multicluster.ClusterName, cc clustercache.ClusterCache) error {
	if cc == nil {
		return fmt.Errorf("ClusterCache for workspace %q must not be nil", workspace)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.caches[workspace]; ok {
		return fmt.Errorf("a ClusterCache is already registered for workspace %q", workspace)
	}
	c.caches[workspace] = cc

	for _, s := range c.sources {
		s.attach(workspace, cc)
	}
	return nil
}

// Remove drops a workspace's ClusterCache and detaches it from every source.
// Call it as the workspace disengages.
func (c *ClusterCaches) Remove(workspace multicluster.ClusterName) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.caches, workspace)
	for _, s := range c.sources {
		s.detach(workspace)
	}
}

func (c *ClusterCaches) get(workspace multicluster.ClusterName) (clustercache.ClusterCache, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cc, ok := c.caches[workspace]
	return cc, ok
}

func (c *ClusterCaches) forWorkspaceIn(ctx context.Context) (clustercache.ClusterCache, error) {
	workspace, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return nil, ErrNoWorkspaceInContext
	}
	cc, ok := c.get(workspace)
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrWorkspaceNotRegistered, workspace)
	}
	return cc, nil
}

// Scoped returns a ClusterCache that resolves, per call, to the ClusterCache of
// the workspace named in the call's context.
//
// It is what a fleet-wide reconciler holds in its ClusterCache field. Every
// method of the interface takes a context except GetClusterSource, which is why
// that one is served by Source instead and why calling it here fails loudly.
func (c *ClusterCaches) Scoped() clustercache.ClusterCache {
	return &scopedClusterCache{caches: c}
}

// Source is a clustercache.MulticlusterClusterSourceFunc over this registry.
//
// The source it returns is registered with a controller once and outlives every
// workspace: workspaces attach as they engage and detach as they leave, and the
// requests each produces carry the workspace whose ClusterCache produced them.
func (c *ClusterCaches) Source(
	controllerName string,
	mapFunc func(ctx context.Context, cluster client.Object) []ctrl.Request,
	opts ...clustercache.GetClusterSourceOption,
) source.TypedSource[mcreconcile.Request] {
	f := &clusterSourceFanIn{
		controllerName: controllerName,
		mapFunc:        mapFunc,
		opts:           opts,
		attached:       map[multicluster.ClusterName]context.CancelFunc{},
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// Seed from what is already registered. Sources are created when a
	// controller is built and workspaces engage on their own schedule, so
	// neither order is the exceptional one — a source that only listened for
	// later arrivals would silently serve nothing for every workspace already
	// running.
	f.pending = make(map[multicluster.ClusterName]clustercache.ClusterCache, len(c.caches))
	for workspace, cc := range c.caches {
		f.pending[workspace] = cc
	}
	c.sources = append(c.sources, f)
	return f
}

// scopedClusterCache delegates every context-carrying method to the workspace's
// own ClusterCache.
type scopedClusterCache struct {
	caches *ClusterCaches
}

var _ clustercache.ClusterCache = &scopedClusterCache{}

func (s *scopedClusterCache) GetClient(ctx context.Context, cluster client.ObjectKey) (client.Client, error) {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		return nil, err
	}
	return cc.GetClient(ctx, cluster)
}

func (s *scopedClusterCache) GetReader(ctx context.Context, cluster client.ObjectKey) (client.Reader, error) {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		return nil, err
	}
	return cc.GetReader(ctx, cluster)
}

func (s *scopedClusterCache) GetUncachedClient(ctx context.Context, cluster client.ObjectKey) (client.Client, error) {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		return nil, err
	}
	return cc.GetUncachedClient(ctx, cluster)
}

func (s *scopedClusterCache) GetRESTConfig(ctx context.Context, cluster client.ObjectKey) (*rest.Config, error) {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		return nil, err
	}
	return cc.GetRESTConfig(ctx, cluster)
}

func (s *scopedClusterCache) Watch(ctx context.Context, cluster client.ObjectKey, watcher clustercache.Watcher) error {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		return err
	}
	return cc.Watch(ctx, cluster, watcher)
}

// GetHealthCheckingState returns the zero state when the workspace cannot be
// resolved, because the interface gives it no way to report an error.
//
// The zero state reads as "never probed", which is what the Cluster and Machine
// reconcilers already handle for a Cluster whose connection has not been
// established — so an unresolvable workspace degrades to the same conservative
// answer rather than to a wrong one.
func (s *scopedClusterCache) GetHealthCheckingState(ctx context.Context, cluster client.ObjectKey) clustercache.HealthCheckingState {
	cc, err := s.caches.forWorkspaceIn(ctx)
	if err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Cannot report health checking state", "Cluster", cluster)
		return clustercache.HealthCheckingState{}
	}
	return cc.GetHealthCheckingState(ctx, cluster)
}

// GetClusterSource always fails.
//
// It takes no context, so there is nothing to resolve a workspace from, and
// there is no safe default: returning one workspace's source would silently
// give a fleet-wide controller one workspace's Cluster events. Callers use
// ClusterCaches.Source instead, which is why the fleet-wide setup functions take
// the source as a parameter rather than reading it off this field.
func (s *scopedClusterCache) GetClusterSource(controllerName string, _ func(ctx context.Context, cluster client.Object) []ctrl.Request, _ ...clustercache.GetClusterSourceOption) source.Source {
	return source.Func(func(context.Context, workqueue.TypedRateLimitingInterface[ctrl.Request]) error {
		return fmt.Errorf("a workspace-scoped ClusterCache cannot serve GetClusterSource for %q: "+
			"it takes no context and so cannot resolve a workspace; use fleet.ClusterCaches.Source", controllerName)
	})
}

// clusterSourceFanIn is one controller's Cluster-event source, fed by every
// registered workspace.
//
// # Why this cannot be a plain source.Channel
//
// Each workspace's ClusterCache owns its own channel and hands it out through
// GetClusterSource. There is no way to ask it to write into somebody else's. So
// this starts each workspace's source separately, against a queue that stamps
// the workspace on everything it enqueues, and against a context that names the
// workspace so the map function lists in the right one.
type clusterSourceFanIn struct {
	controllerName string
	mapFunc        func(ctx context.Context, cluster client.Object) []ctrl.Request
	opts           []clustercache.GetClusterSourceOption

	mu      sync.Mutex
	started bool
	ctx     context.Context //nolint:containedctx // the controller's context, held so workspaces engaging later can be started under it
	queue   workqueue.TypedRateLimitingInterface[mcreconcile.Request]
	// attached cancels each workspace's source. A workspace present before
	// Start is attached by Start; one arriving after is attached by Add.
	attached map[multicluster.ClusterName]context.CancelFunc
	// pending holds workspaces registered before the controller started this
	// source. Without it, every workspace engaged during startup would be lost.
	pending map[multicluster.ClusterName]clustercache.ClusterCache
}

var _ source.TypedSource[mcreconcile.Request] = &clusterSourceFanIn{}

// Start implements source.TypedSource. The controller calls it once.
func (f *clusterSourceFanIn) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started {
		return fmt.Errorf("cluster source for %q has already been started", f.controllerName)
	}
	f.started, f.ctx, f.queue = true, ctx, q

	for workspace, cc := range f.pending {
		if err := f.startLocked(workspace, cc); err != nil {
			return err
		}
	}
	f.pending = nil
	return nil
}

func (f *clusterSourceFanIn) attach(workspace multicluster.ClusterName, cc clustercache.ClusterCache) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started {
		if f.pending == nil {
			f.pending = map[multicluster.ClusterName]clustercache.ClusterCache{}
		}
		f.pending[workspace] = cc
		return
	}
	if err := f.startLocked(workspace, cc); err != nil {
		ctrl.Log.Error(err, "Failed to attach workspace to cluster source",
			"controller", f.controllerName, "workspace", workspace)
	}
}

func (f *clusterSourceFanIn) startLocked(workspace multicluster.ClusterName, cc clustercache.ClusterCache) error {
	if _, ok := f.attached[workspace]; ok {
		return nil
	}

	// The workspace goes in the context, not just on the requests: the map
	// function is handed this context and lists with it, so without it the
	// controller would enqueue correctly labelled requests for objects found in
	// the wrong workspace.
	ctx, cancel := context.WithCancel(mccontext.WithCluster(f.ctx, workspace))
	if err := cc.GetClusterSource(f.controllerName, f.mapFunc, f.opts...).
		Start(ctx, &workspaceQueue{q: f.queue, workspace: workspace}); err != nil {
		cancel()
		return fmt.Errorf("starting cluster source for workspace %q: %w", workspace, err)
	}
	f.attached[workspace] = cancel
	return nil
}

func (f *clusterSourceFanIn) detach(workspace multicluster.ClusterName) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, workspace)
	if cancel, ok := f.attached[workspace]; ok {
		cancel()
		delete(f.attached, workspace)
	}
}

// workspaceQueue stamps a workspace on everything the wrapped source enqueues.
//
// multicluster-runtime has this shape internally for lifted handlers, but does
// not export it, and a raw source does not go through a handler in any case.
type workspaceQueue struct {
	q         workqueue.TypedRateLimitingInterface[mcreconcile.Request]
	workspace multicluster.ClusterName
}

var _ workqueue.TypedRateLimitingInterface[ctrl.Request] = &workspaceQueue{}

func (w *workspaceQueue) request(item ctrl.Request) mcreconcile.Request {
	return mcreconcile.Request{Request: item, ClusterName: w.workspace}
}

func (w *workspaceQueue) Add(item ctrl.Request)          { w.q.Add(w.request(item)) }
func (w *workspaceQueue) Done(item ctrl.Request)         { w.q.Done(w.request(item)) }
func (w *workspaceQueue) Forget(item ctrl.Request)       { w.q.Forget(w.request(item)) }
func (w *workspaceQueue) AddRateLimited(i ctrl.Request)  { w.q.AddRateLimited(w.request(i)) }
func (w *workspaceQueue) NumRequeues(i ctrl.Request) int { return w.q.NumRequeues(w.request(i)) }
func (w *workspaceQueue) Len() int                       { return w.q.Len() }
func (w *workspaceQueue) ShutDown()                      { w.q.ShutDown() }
func (w *workspaceQueue) ShutDownWithDrain()             { w.q.ShutDownWithDrain() }
func (w *workspaceQueue) ShuttingDown() bool             { return w.q.ShuttingDown() }

func (w *workspaceQueue) AddAfter(item ctrl.Request, d time.Duration) {
	w.q.AddAfter(w.request(item), d)
}

// Get is on the interface but is never called on this side: the controller
// reads from the wrapped queue, and a source only ever writes.
func (w *workspaceQueue) Get() (ctrl.Request, bool) {
	item, shutdown := w.q.Get()
	return item.Request, shutdown
}
