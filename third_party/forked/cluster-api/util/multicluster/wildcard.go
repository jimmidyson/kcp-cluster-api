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

package multicluster

import (
	"context"
	"fmt"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ClusterResolver says which logical cluster an object belongs to.
//
// The object knows: a provider that serves many logical clusters through one
// endpoint has to mark them apart somehow, and under kcp that is the
// kcp.io/cluster annotation. What the mark is stays out of Cluster API, which is
// why this is supplied rather than assumed.
//
// Returning false drops the event. That is the right default for an object that
// names no cluster: it cannot be turned into a request that reaches a
// reconciler, and guessing would send it to the wrong tenant.
type ClusterResolver func(client.Object) (mcmulticluster.ClusterName, bool)

// WildcardSource registers one event handler for a type across every logical
// cluster, rather than one per cluster.
//
// # Why this exists
//
// multicluster-runtime's own source registers per engaged cluster: as each
// cluster joins, it adds an event handler to that cluster's cache. Under a
// provider whose clusters are views over one shared informer — which is what
// kcp's virtual workspace gives you — the informer is shared but the
// *registrations* are not, so the cost is one client-go processorListener (two
// goroutines and a 1024-slot ring buffer) per cluster per watched type, plus two
// more goroutines that multicluster-runtime adds around it.
//
// Measured, that was about 45 of the 51.7 goroutines a workspace cost after the
// controllers themselves had been made fleet-wide — the whole of what did not
// collapse. It also does not scale in the shape a kcp shard needs: watches per
// workspace times workspaces, for a fleet where the informer behind them is one
// object.
//
// This registers once. Events arrive carrying every cluster's objects, and each
// is demultiplexed by asking the object which cluster it came from. The
// per-workspace registration cost becomes zero, and the per-type cost is what it
// always was.
//
// Measured on the same wiring afterwards, swept to a hundred workspaces: ten
// informer listeners for the whole shard, where per-cluster registration would
// have been about nine hundred. With the per-cluster engagement this makes
// unnecessary also dropped (see MulticlusterBuilder.buildWildcard), a workspace
// costs exactly two goroutines, against 51.7 before either change.
//
// # What it gives the handler
//
// Exactly what a per-cluster registration would have: a context naming the
// cluster, and a queue that stamps requests with it. A handler written for one
// cluster — EnqueueRequestForObject, EnqueueRequestForOwner, or a Cluster API
// map function listing through a context-scoped client — works unchanged.
//
// # What it requires of the cache
//
// That it spans clusters. A cache scoped to one logical cluster will return only
// that cluster's objects, and the source will silently watch a fraction of the
// fleet. Under kcp this is a cache built against a /clusters/* endpoint.
func WildcardSource(
	wildcard cache.Cache,
	obj client.Object,
	h handler.TypedEventHandler[client.Object, reconcile.Request],
	clusterOf ClusterResolver,
	predicates ...predicate.Predicate,
) source.TypedSource[mcreconcile.Request] {
	typed := make([]predicate.TypedPredicate[client.Object], 0, len(predicates))
	for _, p := range predicates {
		typed = append(typed, p)
	}
	return source.TypedKind(wildcard, obj,
		handler.TypedEventHandler[client.Object, mcreconcile.Request](&demultiplexingHandler{h: h, clusterOf: clusterOf}),
		typed...)
}

// demultiplexingHandler routes each event to the cluster its object names.
//
// This is the counterpart of LiftWithClusterInContext for a source that is not
// per cluster. The lift knows its cluster at registration time; this one learns
// it per event, which is the only difference and the whole point.
type demultiplexingHandler struct {
	h         handler.TypedEventHandler[client.Object, reconcile.Request]
	clusterOf ClusterResolver
}

func (d *demultiplexingHandler) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	if ctx, q, ok := d.route(ctx, evt.Object, q); ok {
		d.h.Create(ctx, evt, q)
	}
}

func (d *demultiplexingHandler) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	// ObjectNew, because an update that changed the cluster is not an update —
	// it is a different object — and the new state is what a reconcile acts on.
	if ctx, q, ok := d.route(ctx, evt.ObjectNew, q); ok {
		d.h.Update(ctx, evt, q)
	}
}

func (d *demultiplexingHandler) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	if ctx, q, ok := d.route(ctx, evt.Object, q); ok {
		d.h.Delete(ctx, evt, q)
	}
}

func (d *demultiplexingHandler) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	if ctx, q, ok := d.route(ctx, evt.Object, q); ok {
		d.h.Generic(ctx, evt, q)
	}
}

func (d *demultiplexingHandler) route(ctx context.Context, obj client.Object, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) (context.Context, workqueue.TypedRateLimitingInterface[reconcile.Request], bool) {
	if obj == nil {
		return ctx, nil, false
	}
	cluster, ok := d.clusterOf(obj)
	if !ok {
		// Logged, because this is a dropped event and a dropped event is
		// indistinguishable from a controller that decided to do nothing. If
		// the resolver is wrong about a type, the symptom is a reconciler that
		// never runs, and nothing else says so.
		ctrl.LoggerFrom(ctx).V(2).Info("Dropping event: its object names no cluster",
			"object", klog.KObj(obj), "type", fmt.Sprintf("%T", obj))
		return ctx, nil, false
	}
	// The resource version is the useful part. A reconcile woken by this event
	// reads through whatever client its reconciler holds, and if that client's
	// cache is a different informer from this one it can answer with a version
	// older than the event that caused the wake — which looks exactly like a
	// reconciler that decided to do nothing. Logging what was routed makes that
	// comparison possible against the reconcile that follows.
	if log := ctrl.LoggerFrom(ctx); log.V(4).Enabled() {
		log.V(4).Info("Routing event", "cluster", cluster, "object", klog.KObj(obj),
			"type", fmt.Sprintf("%T", obj), "resourceVersion", obj.GetResourceVersion())
	}
	return mccontext.WithCluster(ctx, cluster), &clusterStampingQueue{q: q, cluster: cluster}, true
}

// clusterStampingQueue puts the cluster on everything the handler enqueues.
//
// multicluster-runtime has this shape for its lifted handlers and does not
// export it, and its version binds one cluster for the lifetime of a
// registration. This one is built per event, which is what a single shared
// registration needs.
type clusterStampingQueue struct {
	q       workqueue.TypedRateLimitingInterface[mcreconcile.Request]
	cluster mcmulticluster.ClusterName
}

var _ workqueue.TypedRateLimitingInterface[reconcile.Request] = &clusterStampingQueue{}

func (c *clusterStampingQueue) request(item reconcile.Request) mcreconcile.Request {
	return mcreconcile.Request{Request: item, ClusterName: c.cluster}
}

func (c *clusterStampingQueue) Add(item reconcile.Request)    { c.q.Add(c.request(item)) }
func (c *clusterStampingQueue) Done(item reconcile.Request)   { c.q.Done(c.request(item)) }
func (c *clusterStampingQueue) Forget(item reconcile.Request) { c.q.Forget(c.request(item)) }
func (c *clusterStampingQueue) Len() int                      { return c.q.Len() }
func (c *clusterStampingQueue) ShutDown()                     { c.q.ShutDown() }
func (c *clusterStampingQueue) ShutDownWithDrain()            { c.q.ShutDownWithDrain() }
func (c *clusterStampingQueue) ShuttingDown() bool            { return c.q.ShuttingDown() }

func (c *clusterStampingQueue) AddRateLimited(item reconcile.Request) {
	c.q.AddRateLimited(c.request(item))
}

func (c *clusterStampingQueue) NumRequeues(item reconcile.Request) int {
	return c.q.NumRequeues(c.request(item))
}

func (c *clusterStampingQueue) AddAfter(item reconcile.Request, d time.Duration) {
	c.q.AddAfter(c.request(item), d)
}

// Get is never called on this side: a source writes, and the controller reads
// from the queue underneath.
func (c *clusterStampingQueue) Get() (reconcile.Request, bool) {
	item, shutdown := c.q.Get()
	return item.Request, shutdown
}
