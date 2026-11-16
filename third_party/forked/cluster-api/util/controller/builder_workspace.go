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

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"

	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/cache"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
	predicatesutil "sigs.k8s.io/cluster-api/util/predicates"
)

// MulticlusterController is what MulticlusterBuilder produces: everything a
// single-cluster Controller offers, plus the fleet-wide watch registration.
//
// MultiClusterWatch is the addition, and it is not cosmetic. Watches added at
// runtime — the contract-versioned references both core reconcilers resolve —
// have to reach every cluster the controller serves, including ones that engage
// after the watch is registered. controller-runtime's Watch registers against
// one cache and cannot do that.
type MulticlusterController interface {
	ControllerFor[mcreconcile.Request]

	// MultiClusterWatch registers a source with every engaged cluster, and with
	// any that engage later.
	MultiClusterWatch(src mcsource.TypedSource[client.Object, mcreconcile.Request]) error

	// WatchAllClusters registers a watch added at runtime, by whichever
	// mechanism this controller was built with.
	//
	// The caller supplies a type, an ordinary single-cluster handler and
	// predicates, and does not choose between per-cluster registration and one
	// shared registration — that is a property of how the controller was built,
	// and a caller that had to know would have to be changed to change it.
	WatchAllClusters(obj client.Object, h handler.TypedEventHandler[client.Object, reconcile.Request], predicates ...predicate.Predicate) error
}

// multiclusterController joins the shared wrapper — which carries the reconcile
// cache, the deferral methods and the consistency store — to the multicluster
// controller underneath it.
//
// Embedded rather than reimplemented: everything except MultiClusterWatch is
// the same code the single-cluster builder uses, so the two cannot drift.
type multiclusterController struct {
	*controllerWrapper[mcreconcile.Request]

	// mc is nil in wildcard mode: the controller is not engaged with clusters,
	// so there is no multicluster controller wrapping it. See buildWildcard.
	mc mccontroller.TypedController[mcreconcile.Request]

	// registry and clusterOf are set when the builder was given a wildcard
	// registry, and decide how WatchAllClusters registers.
	registry  *WildcardRegistry
	clusterOf capimulticluster.ClusterResolver

	// name identifies this controller in the registry's errors, which is the
	// only place a registration failure surfaces.
	name string
}

func (c *multiclusterController) MultiClusterWatch(src mcsource.TypedSource[client.Object, mcreconcile.Request]) error {
	if c.mc == nil {
		return pkgerrors.New("this controller registers its watches once against a fleet-spanning cache and is not engaged per cluster, " +
			"so it cannot register a per-cluster source; use WatchAllClusters")
	}
	return c.mc.MultiClusterWatch(src)
}

func (c *multiclusterController) WatchAllClusters(obj client.Object, h handler.TypedEventHandler[client.Object, reconcile.Request], predicates ...predicate.Predicate) error {
	if c.registry != nil {
		// Through the registry rather than against one cache, for the same
		// reason the declared watches go through it: a runtime watch registered
		// while only one shard's cache existed would see that shard for ever.
		// The contract-versioned references the core reconcilers resolve are
		// added exactly this way.
		return c.registry.add(fmt.Sprintf("%s's runtime watch on %T", c.name, obj), func(wildcard crcache.Cache) error {
			return c.TypedController.Watch(capimulticluster.WildcardSource(wildcard, obj, h, c.clusterOf, predicates...))
		})
	}
	typed := make([]predicate.TypedPredicate[client.Object], 0, len(predicates))
	for _, p := range predicates {
		typed = append(typed, p)
	}
	return c.mc.MultiClusterWatch(mcsource.TypedKind(obj, capimulticluster.LiftWithClusterInContext(h), typed...))
}

// MulticlusterBuilder builds one controller that serves every cluster a
// multicluster-runtime provider offers, rather than one controller per cluster.
//
// # What it is for
//
// Running Cluster API against a provider that exposes many logical clusters —
// kcp workspaces, or any other multicluster-runtime provider — currently means
// instantiating the whole controller set once per cluster, because
// SetupWithManager builds a controller bound to one manager and keyed on
// reconcile.Request, which carries no cluster. The per-cluster cost of that is
// a controller, a workqueue, an eagerly-started worker pool and an
// event-handler registration per watch, for every cluster.
//
// This builder produces a single controller keyed on mcreconcile.Request, which
// does carry the cluster, so those costs are paid once for the fleet.
//
// # What it deliberately does not change
//
// The reconciler. A reconciler written against reconcile.Request is passed
// through unmodified: Build adapts it with
// mccontext.ReconcilerWithClusterInContext, which puts the cluster in the
// context and calls Reconcile with the plain request. Reconcile bodies, and the
// map functions passed to Watches, are untouched — which is the whole point,
// because that is where a controller's behaviour lives.
//
// # Parity with Builder
//
// Everything Builder does around the reconciler — the reconcile cache, the
// exponential rate limiter, the metrics, the reconciliation timeout, the log
// constructor — is shared with this builder rather than reimplemented, via the
// generic reconcilerWrapper and controllerWrapper. A second copy would drift,
// and drift in rate limiting or deferral is a behaviour change nobody sees.
type MulticlusterBuilder struct {
	builder           *mcbuilder.TypedBuilder[mcreconcile.Request]
	mgr               mcmanager.Manager
	predicateLog      logr.Logger
	options           controller.TypedOptions[mcreconcile.Request]
	forObject         client.Object
	controllerName    string
	rateLimitInterval time.Duration

	// registry, when set, replaces per-cluster watch registration with one
	// registration per type per fleet-spanning cache. See WithWildcardRegistry.
	registry        *WildcardRegistry
	clusterOf       capimulticluster.ClusterResolver
	wildcardWatches []wildcardWatch
	globalPredicate predicate.Predicate

	// rawSources holds the sources given to WatchesRawSource in wildcard mode,
	// until Build has a controller to register them against. See
	// WatchesRawSource for why they cannot be left with the multicluster
	// builder.
	rawSources []source.TypedSource[mcreconcile.Request]

	// recorderFor, when set, supplies each controller's event recorder instead
	// of the local manager. See EventRecorderFor.
	recorderFor func(name string) record.EventRecorder
}

// wildcardWatch is a declared watch, held until Build so it can be registered
// against the shared cache rather than against each cluster as it engages.
type wildcardWatch struct {
	object     client.Object
	handler    handler.TypedEventHandler[client.Object, reconcile.Request]
	predicates []predicate.Predicate
	// owner is set for Owns, whose handler cannot be built until the For type
	// and the scheme are both known.
	owner bool
}

// WithWildcardCache makes every watch a single registration across the fleet,
// instead of one per engaged cluster.
//
// # What it changes
//
// Nothing a reconciler can see. The same types are watched, the same handlers
// run, the same requests are enqueued and they carry the same cluster. What
// changes is how many times the registration happens: once per type, rather than
// once per type per cluster.
//
// # Why it is worth a mode
//
// Measured on the fleet-wide wiring before this existed, per-cluster
// registration was about 45 of the 51.7 goroutines each workspace cost — the
// entire part that did not collapse when the controllers themselves stopped
// being per-workspace. It scales as watches × clusters against an informer that
// is a single object, which is the wrong shape for a provider whose clusters are
// views over one cache.
//
// With this on, and with engagement dropped, the same measurement is 2.0
// goroutines per workspace — flat across six doublings to a hundred.
//
// # What the caller has to know
//
// Two things Cluster API cannot work out for itself. The cache must span
// clusters — under kcp, one built against a /clusters/* endpoint — and objects
// out of it have to be attributable to a cluster, which is what clusterOf does.
// Both are properties of the provider, so both are supplied.
func (blder *MulticlusterBuilder) WithWildcardRegistry(r *WildcardRegistry, clusterOf capimulticluster.ClusterResolver) *MulticlusterBuilder {
	blder.registry = r
	blder.clusterOf = clusterOf
	return blder
}

// buildWildcard builds a controller that is not engaged with clusters at all.
//
// # Why it does not go through the multicluster builder
//
// That builder produces a controller the manager engages: on every engagement
// it walks the controller's per-cluster sources, binds each to the joining
// cluster, records the cluster in a map and starts a goroutine to remove it
// again when the engagement ends.
//
// In wildcard mode there are no per-cluster sources — every watch is one
// registration against the shared cache — so all of that reduces to the
// bookkeeping and its goroutine. Measured, that goroutine was three of the 8.1
// per workspace that remained after the watches were fixed: one per controller
// per engaged cluster, doing nothing for a controller with nothing to engage.
//
// A controller that never engages does not pay it: the group disappears from
// the profile, and what a workspace costs settles at exactly two goroutines
// across a sweep to a hundred — one of which is the provider's, not this. Nothing is lost, because the
// two things engagement provides are not used here: the per-cluster sources do
// not exist, and cluster resolution happens through the manager on the reconcile
// path, which never consulted the controller's map.
//
// # What has to be replicated
//
// One thing. The multicluster builder wraps the reconciler so that a request
// naming a cluster the provider does not have is dropped rather than retried
// forever. That matters more here, not less: a wildcard source sees objects from
// every cluster the endpoint serves, including ones the provider has not
// engaged, so unresolvable requests are expected rather than exceptional.
func (blder *MulticlusterBuilder) buildWildcard(
	controllerName string,
	reconciler reconcile.TypedReconciler[mcreconcile.Request],
	localMgr manager.Manager,
) (controller.TypedController[mcreconcile.Request], error) {
	options := blder.options
	options.Reconciler = &loggingClusterNotFoundWrapper{inner: reconciler}

	c, err := controller.NewTyped(controllerName, localMgr, options)
	if err != nil {
		return nil, err
	}

	if len(blder.wildcardWatches) == 0 && len(blder.rawSources) == 0 {
		return nil, pkgerrors.New("there are no watches configured, controller will never get triggered. Use For(), Owns() or Watches() to set them up")
	}

	// Straight onto the controller, not through the registry: a raw source
	// carries its own cluster and has no cache to be replayed onto, so it is
	// registered once here and started when the controller starts.
	for _, src := range blder.rawSources {
		if err := c.Watch(src); err != nil {
			return nil, pkgerrors.Wrapf(err, "registering a raw source of type %T", src)
		}
	}

	// Resolved now rather than inside the closure: the handler for Owns depends
	// on the For type and the scheme, both of which are settled here, and a
	// closure that re-derived them per cache could disagree with itself.
	watches := make([]wildcardWatch, 0, len(blder.wildcardWatches))
	for _, w := range blder.wildcardWatches {
		if w.owner {
			if blder.forObject == nil {
				return nil, pkgerrors.New("Owns() can only be used together with For()")
			}
			w.handler = handler.EnqueueRequestForOwner(localMgr.GetScheme(), localMgr.GetRESTMapper(), blder.forObject)
		}
		if blder.globalPredicate != nil {
			// Prepended, so a global filter cannot be overridden by a
			// watch-specific one that happens to come first.
			w.predicates = append([]predicate.Predicate{blder.globalPredicate}, w.predicates...)
		}
		watches = append(watches, w)
	}

	// Registered through the registry rather than against a cache, because at
	// this point there is no cache: the provider builds one per endpoint when it
	// first sees one, and that is after every controller has been wired. The
	// registry replays these onto each cache as it appears, so a fleet spanning
	// shards gets every watch on every shard.
	clusterOf := blder.clusterOf
	if err := blder.registry.add(controllerName, func(wildcard crcache.Cache) error {
		for _, w := range watches {
			if err := c.Watch(capimulticluster.WildcardSource(wildcard, w.object, w.handler, clusterOf, w.predicates...)); err != nil {
				return pkgerrors.Wrapf(err, "registering a fleet-wide watch on %T", w.object)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return c, nil
}

// loggingClusterNotFoundWrapper says when work was dropped because the cluster
// it named was not there.
//
// multicluster-runtime's wrapper turns ErrClusterNotFound into a successful
// reconcile with no requeue, which is right — a wildcard source sees objects
// from clusters the provider has not engaged, and retrying those forever would
// be worse. But it is also the one place in this wiring where work disappears
// without a trace, and the symptom of it happening to a cluster that *is*
// engaged is a reconciler that simply never runs again. That is not something
// to leave silent.
type loggingClusterNotFoundWrapper struct {
	inner reconcile.TypedReconciler[mcreconcile.Request]
}

func (r *loggingClusterNotFoundWrapper) Reconcile(ctx context.Context, req mcreconcile.Request) (reconcile.Result, error) {
	res, err := r.inner.Reconcile(ctx, req)
	if errors.Is(err, mcmulticluster.ErrClusterNotFound) {
		ctrl.LoggerFrom(ctx).V(2).Info("Dropping reconcile: the request names a cluster the provider does not have",
			"cluster", req.ClusterName, "object", req.String(), "error", err.Error())
		return reconcile.Result{}, nil
	}
	return res, err
}

// MulticlusterOption configures a MulticlusterBuilder from a reconciler's setup
// function, which builds the builder itself and so cannot be handed one.
type MulticlusterOption func(*MulticlusterBuilder)

// EventRecorderFor returns the recorder a controller should record with.
//
// The local manager's unless the caller supplied a factory, which is the
// difference between events landing wherever the management client points and
// events landing in the cluster the object came from. A fleet-wide controller
// holds one recorder for many clusters, so routing it is the caller's business:
// see capimulticluster.NewClusterAwareRecorder for the marking half.
//
// A method rather than something the builder applies, because the recorder is a
// field on the reconciler and not on the controller — setup functions assign it
// directly, and always did.
func (blder *MulticlusterBuilder) EventRecorderFor(name string) record.EventRecorder {
	if blder.recorderFor != nil {
		return blder.recorderFor(name)
	}
	return blder.mgr.GetLocalManager().GetEventRecorderFor(name)
}

// WithEventRecorderFactory supplies the recorder each controller records with.
func WithEventRecorderFactory(recorderFor func(name string) record.EventRecorder) MulticlusterOption {
	return func(b *MulticlusterBuilder) { b.recorderFor = recorderFor }
}

// WithWildcard is WithWildcardRegistry as a setup-function option.
func WithWildcard(r *WildcardRegistry, clusterOf capimulticluster.ClusterResolver) MulticlusterOption {
	return func(b *MulticlusterBuilder) { b.WithWildcardRegistry(r, clusterOf) }
}

// Apply applies options to the builder. Setup functions call it so that every
// reconciler takes the same options in the same way.
func (blder *MulticlusterBuilder) Apply(opts ...MulticlusterOption) *MulticlusterBuilder {
	for _, o := range opts {
		o(blder)
	}
	return blder
}

// NewMulticlusterControllerManagedBy returns a builder for a controller that
// serves every cluster the manager's provider offers.
func NewMulticlusterControllerManagedBy(m mcmanager.Manager, predicateLog logr.Logger) *MulticlusterBuilder {
	return &MulticlusterBuilder{
		builder:      mcbuilder.TypedControllerManagedBy[mcreconcile.Request](m),
		mgr:          m,
		predicateLog: predicateLog,
	}
}

// For defines the type of Object being reconciled, and the predicates that
// filter its events.
//
// The predicates are variadic, where Builder takes opaque ForOptions, because
// in wildcard mode there is no multicluster builder to hand an option to: the
// watch is registered against the shared cache by buildWildcard, which takes
// predicates directly. An option could only have been dropped there, silently -
// which is what happened until a reconciler with For predicates was wired this
// way. The topology controllers are all three of them: what makes them cheap is
// that they see only the Clusters that have a topology and the MachineDeployments
// and MachineSets a topology owns.
//
// Unlike Owns and Watches, no ResourceIsChanged predicate is prepended. This is
// the primary watch, and Builder does not prepend one there either.
func (blder *MulticlusterBuilder) For(object client.Object, predicates ...predicate.Predicate) *MulticlusterBuilder {
	blder.forObject = object
	if blder.registry != nil {
		blder.wildcardWatches = append(blder.wildcardWatches, wildcardWatch{
			object:     object,
			handler:    &handler.EnqueueRequestForObject{},
			predicates: predicates,
		})
		return blder
	}
	if len(predicates) > 0 {
		blder.builder.For(object, mcbuilder.WithPredicates(predicates...))
		return blder
	}
	blder.builder.For(object)
	return blder
}

// Owns defines types of Objects being generated by the controller.
func (blder *MulticlusterBuilder) Owns(object client.Object, predicates ...predicate.Predicate) *MulticlusterBuilder {
	// Note: Prepend a ResourceIsChanged predicate to all "secondary" watches, matching Builder.
	predicates = append([]predicate.Predicate{predicatesutil.ResourceIsChanged(blder.mgr.GetLocalManager().GetScheme(), blder.predicateLog)}, predicates...)
	if blder.registry != nil {
		blder.wildcardWatches = append(blder.wildcardWatches, wildcardWatch{
			object:     object,
			predicates: predicates,
			owner:      true,
		})
		return blder
	}
	blder.builder.Owns(object, mcbuilder.WithPredicates(predicates...))
	return blder
}

// Watches defines the type of Object to watch, and the handler to enqueue with.
//
// The handler is an ordinary single-cluster handler — exactly what the
// reconcilers already construct — and is lifted here rather than at the call
// site. LiftWithClusterInContext wraps it so that the requests it enqueues carry
// the cluster the event came from, which keeps two clusters' identically named
// objects apart in the queue, and so that the context it runs in carries that
// cluster too, which is what lets its map function list within the right one.
//
// That is what allows a reconciler's existing map functions to be reused
// verbatim: they still produce plain reconcile.Requests against a plain client,
// and the lift supplies the cluster around both.
func (blder *MulticlusterBuilder) Watches(object client.Object, eventHandler handler.TypedEventHandler[client.Object, reconcile.Request], predicates ...predicate.Predicate) *MulticlusterBuilder {
	predicates = append([]predicate.Predicate{predicatesutil.ResourceIsChanged(blder.mgr.GetLocalManager().GetScheme(), blder.predicateLog)}, predicates...)
	if blder.registry != nil {
		blder.wildcardWatches = append(blder.wildcardWatches, wildcardWatch{
			object:     object,
			handler:    eventHandler,
			predicates: predicates,
		})
		return blder
	}
	blder.builder.Watches(object, capimulticluster.LiftWithClusterInContext(eventHandler), mcbuilder.WithPredicates(predicates...))
	return blder
}

// WatchesRawSource adds a source already keyed on mcreconcile.Request.
//
// Unlike Watches, nothing is lifted: a raw source is registered against the
// controller rather than per cluster, so it is the source's own business to set
// the cluster on what it enqueues. See
// clustercache.MulticlusterClusterSourceFunc for the one the core reconcilers
// need.
//
// In wildcard mode it is held until Build rather than handed to the
// multicluster builder, because in that mode the multicluster builder is never
// built — buildWildcard constructs the controller directly. A source left with
// it is therefore never started, and a source that is never started is one
// nobody reads: the ClusterCache's sends to it block until they time out, and
// no probe failure ever reaches the controller that asked to hear about it.
func (blder *MulticlusterBuilder) WatchesRawSource(src source.TypedSource[mcreconcile.Request]) *MulticlusterBuilder {
	if blder.registry != nil {
		blder.rawSources = append(blder.rawSources, src)
		return blder
	}
	blder.builder.WatchesRawSource(src)
	return blder
}

// WithOptions overrides the controller options. Defaults to empty.
func (blder *MulticlusterBuilder) WithOptions(options controller.TypedOptions[mcreconcile.Request]) *MulticlusterBuilder {
	blder.options = options
	return blder
}

// WithRateLimitInterval sets the minimum interval between two reconciles of the
// same object.
func (blder *MulticlusterBuilder) WithRateLimitInterval(interval time.Duration) *MulticlusterBuilder {
	blder.rateLimitInterval = interval
	return blder
}

// WithEventFilter sets a predicate applied to all watches.
func (blder *MulticlusterBuilder) WithEventFilter(p predicate.Predicate) *MulticlusterBuilder {
	blder.globalPredicate = p
	blder.builder.WithEventFilter(p)
	return blder
}

// Named sets the controller name. Defaults to the lowercased kind of the For
// object.
func (blder *MulticlusterBuilder) Named(name string) *MulticlusterBuilder {
	blder.controllerName = name
	blder.builder.Named(name)
	return blder
}

// Complete builds the controller.
func (blder *MulticlusterBuilder) Complete(ctx context.Context, r reconcile.Reconciler) error {
	_, err := blder.Build(ctx, r)
	return err
}

// Build builds the controller and returns it.
//
// The reconciler is an ordinary single-cluster reconcile.Reconciler. It is
// adapted, not rewritten: ReconcilerWithClusterInContext unwraps the
// multicluster request, puts its cluster in the context, and calls Reconcile
// with the plain request the reconciler already expects.
func (blder *MulticlusterBuilder) Build(ctx context.Context, r reconcile.Reconciler) (MulticlusterController, error) {
	if feature.Gates.Enabled(feature.ReconcilerRateLimiting) && !feature.Gates.Enabled(feature.PriorityQueue) {
		return nil, pkgerrors.New("if feature gate ReconcilerRateLimiting is enabled, feature gate PriorityQueue must be enabled as well")
	}

	localMgr := blder.mgr.GetLocalManager()

	// Get GVK of the for object.
	var gvk schema.GroupVersionKind
	hasGVK := blder.forObject != nil
	if hasGVK {
		var err error
		gvk, err = apiutil.GVKForObject(blder.forObject, localMgr.GetScheme())
		if err != nil {
			return nil, err
		}
	}

	controllerName := blder.controllerName
	if controllerName == "" {
		controllerName = strings.ToLower(gvk.Kind)
	}

	if blder.options.ReconciliationTimeout == 0 {
		blder.options.ReconciliationTimeout = defaultReconciliationTimeout
	}

	if blder.options.LogConstructor == nil {
		log := localMgr.GetLogger().WithValues("controller", controllerName)
		if hasGVK {
			log = log.WithValues("controllerGroup", gvk.Group, "controllerKind", gvk.Kind)
		}

		blder.options.LogConstructor = func(req *mcreconcile.Request) logr.Logger {
			log := log
			if req != nil {
				// The cluster is logged as well as the object, because in a
				// fleet-wide controller the object name alone does not identify
				// what was reconciled.
				log = log.WithValues("cluster", req.ClusterName)
				if hasGVK {
					log = log.WithValues(gvk.Kind, klog.KRef(req.Namespace, req.Name))
				}
			}
			return log
		}
	}

	rateLimitInterval := time.Second
	if blder.rateLimitInterval > time.Second {
		rateLimitInterval = blder.rateLimitInterval
	}

	var queueRateLimiter *typedItemExponentialFailureRateLimiter[mcreconcile.Request]
	if feature.Gates.Enabled(feature.ReconcilerRateLimiting) {
		queueRateLimiter = newTypedItemExponentialFailureRateLimiter[mcreconcile.Request](rateLimitInterval, 5*time.Millisecond, 1000*time.Second)
		blder.options.RateLimiter = queueRateLimiter
	}

	blder.builder.WithOptions(blder.options)

	reconcileCache := cache.New[reconcileCacheEntry[mcreconcile.Request]](ctx, cache.DefaultTTL)

	// The consistency store is the local manager's.
	//
	// Correct only while no converted controller uses it: its writes are keyed
	// by namespace and name with no cluster, so a fleet-wide controller that
	// recorded writes would let one cluster's deferral apply to another's
	// reconcile. The controllers converted first — cluster and machine — never
	// call WroteAt, and EnsureReady on an owner it has no record of returns
	// nothing, so the store stays empty and inert.
	//
	// Converting machineset or machinedeployment, which do call it, MUST make
	// this cluster-aware first.
	consistencyStore := newConsistencyStore(localMgr.GetScheme(), localMgr.GetCache())

	reconciler := &reconcilerWrapper[mcreconcile.Request]{
		name:              controllerName,
		reconciler:        mccontext.ReconcilerWithClusterInContext(r),
		reconcileCache:    reconcileCache,
		rateLimitInterval: rateLimitInterval,
		queueRateLimiter:  queueRateLimiter,
		consistencyStore:  consistencyStore,
		// The embedded request is the object identity; the cluster is carried
		// alongside it and is deliberately not part of what the consistency
		// store sees, per the note above.
		namespacedName: func(req mcreconcile.Request) types.NamespacedName { return req.NamespacedName },
	}

	var (
		c   controller.TypedController[mcreconcile.Request]
		mcc mccontroller.TypedController[mcreconcile.Request]
		err error
	)
	if blder.registry != nil {
		c, err = blder.buildWildcard(controllerName, reconciler, localMgr)
	} else {
		mcc, err = blder.builder.Build(reconciler)
		c = mcc
	}
	if err != nil {
		return nil, err
	}

	mc := &multiclusterController{
		mc:        mcc,
		registry:  blder.registry,
		clusterOf: blder.clusterOf,
		name:      controllerName,
		controllerWrapper: &controllerWrapper[mcreconcile.Request]{
			TypedController:  c,
			reconcileCache:   reconcileCache,
			consistencyStore: consistencyStore,
			// DeferNextReconcileForObject is called from inside Reconcile with only
			// an object, so there is no cluster to attach here. The resulting
			// request therefore defers the object in *every* cluster.
			//
			// Harmless for the controllers converted first, which never call it,
			// and a correctness hazard for any that do — recorded alongside the
			// consistency store constraint above.
			newRequest: func(nn types.NamespacedName) mcreconcile.Request {
				return mcreconcile.Request{Request: reconcile.Request{NamespacedName: nn}}
			},
		},
	}

	reconcileTotal.WithLabelValues(controllerName, labelError).Add(0)
	reconcileTotal.WithLabelValues(controllerName, labelRequeueAfter).Add(0)
	reconcileTotal.WithLabelValues(controllerName, labelRequeue).Add(0)
	reconcileTotal.WithLabelValues(controllerName, labelSuccess).Add(0)

	return mc, nil
}
