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

package coremanager

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/kcp-dev/logicalcluster/v3"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/core/reconcilers/cluster"
	"sigs.k8s.io/cluster-api/core/reconcilers/machine"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

// DefaultFleetMaxConcurrentReconciles is the worker count for each controller.
//
// # Why it is not per workspace any more
//
// It never should have been. controller-runtime starts every worker eagerly, so
// a per-workspace pool is paid by tenants that own nothing, and it is
// *statically partitioned* — a tenant with a hundred Machines to reconcile
// cannot use the workers sitting idle in every other workspace. That partition
// is what made four a compromise rather than a choice.
//
// One pool for the process removes both problems: the goroutines are paid once,
// and burst capacity is shared.
//
// # Why ten
//
// It is what upstream's core/main.go uses, and upstream is sizing the same thing
// this now is — one process's total worker budget. Not higher: the sweep
// (evidence/reconcile-throughput.md) confirms throughput is linear in workers to
// eight, within 9%, and says nothing past that.
//
// # What sharing the pool costs, and what fixes it
//
// A shared pool has the failure the partition did not: one workspace with a
// large backlog can hold every worker, and its neighbours wait. The partition
// traded burst capacity for that guarantee.
//
// The fix is not to re-partition but to schedule — a priority queue that admits
// work fairly across workspaces, so a noisy neighbour's backlog is bounded by
// its share rather than by the size of the pool. controller-runtime's
// PriorityQueue feature gate is the hook, and mcreconcile.Request carries the
// workspace the priority function would key on. Until that exists, this is a
// shared pool with no fairness, and that is stated rather than assumed away.
const DefaultFleetMaxConcurrentReconciles = 10

func (o SetupOptions) fleetMaxConcurrentReconciles() int {
	if o.FleetMaxConcurrentReconciles <= 0 {
		return DefaultFleetMaxConcurrentReconciles
	}
	return o.FleetMaxConcurrentReconciles
}

// Fleet-wide wiring: what it is, and what it costs.
//
// Every reconciler this process runs is wired once, as a controller serving
// every workspace the provider engages, rather than once per workspace. The
// entry points are NewFleet (the shared half) and the per-provider setup
// functions below; all of them must be called before mgr.Start, and each
// exactly once.
//
// # Nothing is left per workspace
//
// The ClusterCache included: its accessors used to be keyed by namespace and
// name alone, which is why it was the last component held back, and it now keys
// on the logical cluster too.
//
// What each reconciler holds is therefore a single value that resolves the
// workspace from the context it is called with — the client, the API reader and
// the ClusterCache alike. No reconcile code knows there is more than one
// workspace.
//
// # What that is worth, measured
//
// A workspace costs **2.0 goroutines** at the margin, exactly, from two
// workspaces to a hundred while idle (evidence/fleet-wide-measured.md), and
// the same 2.0 with each workspace holding a Cluster and a DevCluster
// reconciled to provisioned (evidence/fleet-one-cache-measured.md). Work does
// not leave a per-workspace residue.
//
// It gives all of it back, too: **0.0 goroutines retained per departed
// workspace**, measured across the full departure from eight workspaces to
// none. That column is new because until the two-cache fault was fixed the
// sweep never reached the end of a departure phase to measure it.
//
// The fixed cost is one watch-list per watched type for the whole shard, no
// LISTs at all across a sweep, and three goroutines for the event broadcaster.
//
// It was 51.7 when the controllers were fleet-wide but their watches were still
// registered per cluster. Making the controllers fleet-wide collapsed the
// controller-level terms — thirty worker goroutines for the process rather than
// per workspace, one priority queue per controller rather than per controller
// per workspace — and left the per-watch term untouched, which turned out to be
// about 45 of the 51.7.
//
// Registering each watch once against the fleet-spanning cache, rather than once
// per engaged cluster, is what removed it: ten informer listeners for the whole
// shard, against 73 and climbing.
//
// Not engaging those controllers with clusters at all removed the rest of what
// the conversion had left: a controller with no per-cluster sources was still
// paying a bookkeeping goroutine per engaged cluster.
//
// What is left per workspace is not registration and not engagement. It is
// exactly two goroutines: one for the provider's scoped cluster, and one for
// this project's own engagement seam — which, with no per-workspace setup left
// to run, exists only to count engaged workspaces and could be a counter.
//
// # Heap
//
// Of order **100 KiB per workspace**, linear to a hundred workspaces with no
// departure point. Read that as an upper bound: the sweep measures the test
// process, and part of the residue is still the kcp fixture buffering the
// server's log rather than anything this wires.
//
// The active run does not improve on that figure and is not quoted for it: its
// heap is flat, steps once, and is flat again, and a line fitted through a
// single step is arithmetic rather than a measurement.
//
// An earlier figure of ~840 KiB with a departure at 32 was that buffer and
// nothing else — 48 MB of a 62 MB live heap, scaling with workspace creation
// and so indistinguishable from a per-workspace cost until it was profiled.

// Fleet is what every fleet-wide controller in this process shares: the
// clients that resolve the workspace from the context, the wildcard
// registration their watches go through, the recorder that routes events to
// the workspace their object lives in, and the one ClusterCache.
//
// It exists because there is more than one provider now. Each of Cluster API's
// providers wires its own controllers, and every one of them needs exactly
// this set - but two ClusterCaches in a process is not a duplication, it is an
// error: controller-runtime rejects the second controller registered under the
// same name, which is the check that stops two controllers reporting one
// metric. Building the shared half once and handing it to each provider is
// what lets them run in one process at all.
type Fleet struct {
	// Client and APIReader resolve the workspace from the context of the call.
	Client    client.Client
	APIReader client.Reader

	// ClusterCache connects to workload clusters, keyed by logical cluster as
	// well as by name.
	ClusterCache clustercache.ClusterCache

	// ClusterSource is how a controller watches its workload clusters:
	// ClusterCache.GetClusterSource takes no context, so it cannot resolve the
	// workspace the way the clients do and has to be passed instead.
	ClusterSource clustercache.MulticlusterClusterSourceFunc

	// Options is the controller configuration each fleet-wide controller gets.
	Options controller.TypedOptions[mcreconcile.Request]

	// builderOptions carry the wildcard registration and the event recorder
	// factory into each provider's builder.
	builderOptions []capicontrollerutil.MulticlusterOption
}

// BuilderOptions returns what a provider passes through to its own
// SetupWithMulticlusterManager.
func (f *Fleet) BuilderOptions() []capicontrollerutil.MulticlusterOption {
	return f.builderOptions
}

// NewFleet builds the shared half and the ClusterCache, in that order, and
// must be called before mgr.Start.
func NewFleet(ctx context.Context, mgr mcmanager.Manager, registry *capicontrollerutil.WildcardRegistry, opts SetupOptions) (*Fleet, error) {
	if mgr == nil {
		return nil, errors.New("a multi-cluster manager is required")
	}
	if registry == nil {
		return nil, errors.New("a wildcard registry is required: it is what joins these controllers' watches to the caches their reconcilers read through")
	}

	options := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: opts.fleetMaxConcurrentReconciles(),
	}
	if opts.SkipControllerNameValidation {
		options.SkipNameValidation = ptr.To(true)
	}

	// Every watch is one registration per shard rather than one per workspace.
	//
	// The registration does not happen here. It happens when the provider builds
	// the cache for a shard, which is after the manager starts — so what is
	// declared here is *what* to watch, and providerwiring's registry replays it
	// onto each cache as it appears. That indirection is not incidental: the
	// cache a watch goes on has to be the cache its reconciler reads through.
	//
	// It was not, and the fault was measured rather than reasoned about.
	// Pointing the watches at the provider's cache also removed the second set
	// of informers entirely: fifty-four goroutines and every LIST the sweep
	// made, with one watch-list per watched type for the whole shard where
	// there had been five or six (evidence/fleet-one-cache-measured.md).
	// Watches were registered on the local manager's cache and reads went
	// through the provider's — two informers over one endpoint with independent
	// lag. A reconcile woken by one could read a version older than the event
	// that woke it, take the wrong branch, and return without requeueing;
	// nothing woke it again, because the event was spent and the other cache
	// emits none of its own when it catches up. A DevCluster deletion routed at
	// resourceVersion 967 ran the *provisioning* path, which is only possible
	// against an object that has no deletion timestamp
	// (evidence/fleet-two-caches.md).
	//
	// The provider's cache is also the only one of the two that is kcp-aware in
	// its store keys, so it can hold two workspaces' identically named objects
	// apart. A plain controller-runtime cache keys on namespace and name alone.
	//
	// The resolver is logicalcluster.From, which reads kcp's own annotation. It
	// is supplied here because it is the one piece of this that is kcp-specific,
	// and Cluster API should not know it.
	clusterOf := func(o client.Object) (multicluster.ClusterName, bool) {
		name := logicalcluster.From(o)
		return multicluster.ClusterName(name), name != ""
	}
	wildcard := capicontrollerutil.WithWildcard(registry, clusterOf)

	// Note the absence of SkipNameValidation, which every per-workspace
	// controller needed. controller-runtime rejects a duplicate controller name
	// because two controllers reporting one metric is a reporting fault; wiring
	// per workspace collided with that by construction, and disabling the check
	// was how that path lived with it — at the cost of reconcile metrics that
	// aggregated across workspaces. Each name is now registered once, so the
	// check does its job and the metrics are attributable again.

	clusterAwareClient := capimulticluster.NewClusterAwareClient(mgr)
	clusterAwareReader := capimulticluster.NewClusterAwareAPIReader(mgr)

	// Events go to the workspace the object lives in, on the shard.
	//
	// They used to go nowhere. The recorder was the local manager's, so every
	// event Cluster API emitted was POSTed to the virtual workspace at
	// /clusters/* — which serves no core v1.Event and names no logical cluster
	// to write to — and rejected with "the server could not find the requested
	// resource (post events)".
	//
	// record.EventRecorder takes no context, so the cluster cannot travel the
	// way it does for the clients. It travels on the event instead: the
	// cluster-aware recorder marks each one with the cluster of the object it is
	// about, and the sink routes on the mark and strips it before writing.
	//
	// One broadcaster for the process, not one per workspace. Its single watcher
	// goroutine is what calls the sink, so events stay off the reconcile path
	// and a workspace still costs two goroutines. Aggregation does not merge
	// across workspaces despite the sharing, because client-go keys it on the
	// involved object's UID among other things.
	eventSink, err := NewWorkspaceEventSink(opts.ShardConfig)
	if err != nil {
		return nil, fmt.Errorf("building the workspace event sink: %w", err)
	}
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(eventSink)
	// Stopped with the context rather than left running: the broadcaster owns
	// goroutines, and a process that wired this more than once would otherwise
	// accumulate them silently.
	go func() {
		<-ctx.Done()
		broadcaster.Shutdown()
	}()

	recorderFor := capicontrollerutil.WithEventRecorderFactory(func(name string) record.EventRecorder {
		return capimulticluster.NewClusterAwareRecorder(
			broadcaster.NewRecorder(mgr.GetLocalManager().GetScheme(), corev1.EventSource{Component: name}),
			clusterOf,
		)
	})

	// The ClusterCache reads each Cluster's kubeconfig Secret through
	// SecretClient, so it has to be workspace-scoped for the same reason
	// everything else here is: reading the wrong workspace's Secret is how a
	// workload cluster gets handed to the wrong tenant.
	//
	// It is deliberately *not* the cluster-aware client every other field here
	// gets. That client reads through the APIExport's virtual workspace, which
	// serves what the export serves and nothing else — so a core v1.Secret has
	// no REST mapping there and every connection attempt fails before it
	// reaches the wire. This project wired it that way first, and no idle sweep
	// could see the fault, because an idle workspace holds no Cluster for the
	// ClusterCache to try. See NewWorkspaceSecretReader.
	secretReader, err := NewWorkspaceSecretReader(opts.ShardConfig)
	if err != nil {
		return nil, fmt.Errorf("building the workspace Secret reader: %w", err)
	}

	clusterCache, err := clustercache.SetupWithMulticlusterManager(ctx, mgr, clusterAwareClient, clustercache.Options{
		SecretClient: secretReader,
		Client: clustercache.ClientOptions{
			UserAgent: remote.DefaultClusterAPIUserAgent(controllerName),
		},
	}, options, wildcard, recorderFor)
	if err != nil {
		return nil, fmt.Errorf("creating fleet-wide ClusterCache: %w", err)
	}

	return &Fleet{
		Client:         clusterAwareClient,
		APIReader:      clusterAwareReader,
		ClusterCache:   clusterCache,
		ClusterSource:  clusterCache.GetMulticlusterClusterSource,
		Options:        options,
		builderOptions: []capicontrollerutil.MulticlusterOption{wildcard, recorderFor},
	}, nil
}

// SetupFleetControllers wires the core Cluster and Machine reconcilers, and
// the dev infrastructure provider's, as controllers that serve every workspace
// the provider engages.
//
// It MUST be called before mgr.Start, and exactly once.
func SetupFleetControllers(ctx context.Context, mgr mcmanager.Manager, registry *capicontrollerutil.WildcardRegistry, dev *DevInfrastructure, opts SetupOptions) error {
	fleet, err := NewFleet(ctx, mgr, registry, opts)
	if err != nil {
		return err
	}
	return SetupCoreControllers(ctx, mgr, fleet, dev)
}

// SetupCoreControllers wires the core reconcilers - Cluster and Machine - onto
// an already-built Fleet.
//
// The infrastructure provider's reconcilers are no longer wired here: they are
// a provider of their own, with their own APIExport and their own deployment,
// and SetupDevInfrastructureControllers is how a process wires them. Passing a
// DevInfrastructure here wires them too, which is what a co-located process
// (the demo, the sweep) wants and what a deployment does not.
func SetupCoreControllers(ctx context.Context, mgr mcmanager.Manager, fleet *Fleet, dev *DevInfrastructure) error {
	if fleet == nil {
		return errors.New("a fleet is required")
	}

	clusterAwareClient := fleet.Client
	clusterAwareReader := fleet.APIReader
	clusterCache := fleet.ClusterCache
	options := fleet.Options
	builderOpts := fleet.BuilderOptions()

	if err := (&cluster.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                clusterCache,
		RemoteConnectionGracePeriod: defaultRemoteConnectionGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, fleet.ClusterSource, builderOpts...); err != nil {
		return fmt.Errorf("creating fleet-wide Cluster controller: %w", err)
	}

	if err := (&machine.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                clusterCache,
		RemoteConditionsGracePeriod: defaultRemoteConditionsGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, fleet.ClusterSource, builderOpts...); err != nil {
		return fmt.Errorf("creating fleet-wide Machine controller: %w", err)
	}

	if dev == nil {
		return nil
	}
	return SetupDevInfrastructureControllers(ctx, mgr, fleet, dev)
}

// SetupDevInfrastructureControllers wires the docker/dev infrastructure
// provider's reconcilers onto an already-built Fleet, as one controller each
// for every workspace the manager's provider engages.
//
// It MUST be called before mgr.Start, and exactly once.
func SetupDevInfrastructureControllers(ctx context.Context, mgr mcmanager.Manager, fleet *Fleet, dev *DevInfrastructure) error {
	if fleet == nil {
		return errors.New("a fleet is required")
	}
	if dev == nil {
		return errors.New("a dev infrastructure backend is required")
	}

	clusterAwareClient := fleet.Client
	clusterCache := fleet.ClusterCache
	options := fleet.Options
	builderOpts := fleet.BuilderOptions()

	if err := (&reconcilers.DevCluster{
		Client:           clusterAwareClient,
		ContainerRuntime: dev.containerRuntime,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithMulticlusterManager(ctx, mgr, options, builderOpts...); err != nil {
		return fmt.Errorf("creating fleet-wide DevCluster controller: %w", err)
	}

	if err := (&reconcilers.DevMachine{
		Client:           clusterAwareClient,
		ContainerRuntime: dev.containerRuntime,
		ClusterCache:     clusterCache,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithMulticlusterManager(ctx, mgr, options, fleet.ClusterSource, builderOpts...); err != nil {
		return fmt.Errorf("creating fleet-wide DevMachine controller: %w", err)
	}

	return nil
}
