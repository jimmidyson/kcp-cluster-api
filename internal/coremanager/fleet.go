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

	"sigs.k8s.io/controller-runtime/pkg/controller"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/core/reconcilers/cluster"
	"sigs.k8s.io/cluster-api/core/reconcilers/machine"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers"
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

// SetupFleetControllers wires every reconciler this binary runs, once, as
// controllers that serve every workspace the provider engages.
//
// It MUST be called before mgr.Start, and exactly once.
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
// Less than it looks, and the measurement is in evidence/fleet-wide-measured.md.
// A workspace still costs **51.7 goroutines** at the margin.
//
// The controller-level terms did collapse and are visible as constants in the
// profile: thirty worker goroutines for the process rather than thirty per
// workspace, and one priority queue per controller rather than one per
// controller per workspace.
//
// The per-*watch* term did not, and it is the larger one. Every engaged
// workspace still gets an event-handler registration per watched type, and
// multicluster-runtime charges four to five goroutines for one where
// controller-runtime charges two. At the nine watches this set registers, that
// is about 45 of the 51.7.
//
// So this conversion is necessary and not sufficient. Everything that still
// scales with workspace count is a registration on a shared informer, which is
// exactly and only what an interposed cache can remove.
//
// # The dev infrastructure provider is optional
//
// A nil dev wires the core reconcilers and nothing else. That is not a test
// affordance: the docker/dev provider is upstream's *test* infrastructure, and a
// real deployment runs its own infrastructure provider instead of it. It also
// needs a container runtime, so requiring it would make the core wiring
// unmeasurable anywhere without one.
//
// When it is wired, it is the one thing here that is still process-wide: its
// in-memory backend binds a fixed port and keys its listeners by Cluster name,
// so two workspaces each holding a Cluster with the same namespace and name
// collide inside it. See DevInfrastructure.
func SetupFleetControllers(ctx context.Context, mgr mcmanager.Manager, dev *DevInfrastructure, opts SetupOptions) error {
	if mgr == nil {
		return errors.New("a multi-cluster manager is required")
	}

	options := controller.TypedOptions[mcreconcile.Request]{
		MaxConcurrentReconciles: opts.fleetMaxConcurrentReconciles(),
	}

	// Note the absence of SkipNameValidation, which every per-workspace
	// controller needed. controller-runtime rejects a duplicate controller name
	// because two controllers reporting one metric is a reporting fault; wiring
	// per workspace collided with that by construction, and disabling the check
	// was how that path lived with it — at the cost of reconcile metrics that
	// aggregated across workspaces. Each name is now registered once, so the
	// check does its job and the metrics are attributable again.

	clusterAwareClient := capimulticluster.NewClusterAwareClient(mgr)
	clusterAwareReader := capimulticluster.NewClusterAwareAPIReader(mgr)

	// The ClusterCache reads each Cluster's kubeconfig Secret through
	// SecretClient, so it has to be workspace-scoped for the same reason
	// everything else here is: reading the wrong workspace's Secret is how a
	// workload cluster gets handed to the wrong tenant.
	//
	// It is the same client rather than the separate caching one the
	// per-workspace path built. That one existed because the manager's cache is
	// configured not to cache Secrets; here each engaged workspace's own cache
	// answers, and giving the fleet a second Secret-caching layer would hold
	// every tenant's kubeconfigs in memory at once.
	clusterCache, err := clustercache.SetupWithMulticlusterManager(ctx, mgr, clusterAwareClient, clustercache.Options{
		SecretClient: clusterAwareClient,
		Client: clustercache.ClientOptions{
			UserAgent: remote.DefaultClusterAPIUserAgent(controllerName),
		},
	}, options)
	if err != nil {
		return fmt.Errorf("creating fleet-wide ClusterCache: %w", err)
	}

	if err := (&cluster.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                clusterCache,
		RemoteConnectionGracePeriod: defaultRemoteConnectionGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, clusterCache.GetMulticlusterClusterSource); err != nil {
		return fmt.Errorf("creating fleet-wide Cluster controller: %w", err)
	}

	if err := (&machine.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                clusterCache,
		RemoteConditionsGracePeriod: defaultRemoteConditionsGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, clusterCache.GetMulticlusterClusterSource); err != nil {
		return fmt.Errorf("creating fleet-wide Machine controller: %w", err)
	}

	if dev == nil {
		return nil
	}

	if err := (&reconcilers.DevCluster{
		Client:           clusterAwareClient,
		ContainerRuntime: dev.containerRuntime,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithMulticlusterManager(ctx, mgr, options); err != nil {
		return fmt.Errorf("creating fleet-wide DevCluster controller: %w", err)
	}

	if err := (&reconcilers.DevMachine{
		Client:           clusterAwareClient,
		ContainerRuntime: dev.containerRuntime,
		ClusterCache:     clusterCache,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithMulticlusterManager(ctx, mgr, options, clusterCache.GetMulticlusterClusterSource); err != nil {
		return fmt.Errorf("creating fleet-wide DevMachine controller: %w", err)
	}

	return nil
}
