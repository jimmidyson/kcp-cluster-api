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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/jimmidyson/kcp-cluster-api/internal/fleet"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/core/reconcilers/cluster"
	"sigs.k8s.io/cluster-api/core/reconcilers/machine"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

// DefaultFleetMaxConcurrentReconciles is the worker count for a controller that
// serves every workspace.
//
// # Why it is not DefaultMaxConcurrentReconciles
//
// That number is per workspace, and its whole argument is about a cost paid
// once per workspace: controller-runtime starts every worker eagerly, so a
// tenant that never creates a Cluster still pays for its idle pool, and raising
// it multiplies across the fleet. Four is a compromise forced by that
// multiplication.
//
// A fleet-wide controller has one pool for the whole process. The goroutines are
// paid once, and the pool is shared — a bursting tenant can use capacity another
// tenant is not using, which is precisely what the per-workspace partition
// cannot do and the reason evidence/reconcile-throughput.md gives for preferring
// this shape.
//
// # Why ten
//
// It is what upstream's core/main.go uses, and upstream is sizing the same thing
// this now is: one process's total worker budget. Going higher is tempting, and
// is not taken here because it would be unmeasured — the sweep confirms
// throughput is linear in workers to eight, within 9%, and says nothing beyond
// that. Raising this is cheap and reversible once the sweep is extended.
const DefaultFleetMaxConcurrentReconciles = 10

func (o SetupOptions) fleetMaxConcurrentReconciles() int {
	if o.FleetMaxConcurrentReconciles <= 0 {
		return DefaultFleetMaxConcurrentReconciles
	}
	return o.FleetMaxConcurrentReconciles
}

// SetupFleetControllers wires the core Cluster and Machine reconcilers once, as
// controllers that serve every workspace the provider engages.
//
// It MUST be called before mgr.Start, and exactly once.
//
// # What is fleet-wide and what is not
//
// Cluster and Machine are. Their reconcile paths use their Client, APIReader and
// ClusterCache with a context, so a context-scoped implementation of each serves
// the whole fleet through one field and no reconcile code changes.
//
// Everything else stays per workspace, and SetupWorkspaceComponents wires it:
// the ClusterCache itself, whose accessors are keyed with no workspace, and the
// docker/dev infrastructure provider's reconcilers, which have no fleet-wide
// setup upstream.
func SetupFleetControllers(ctx context.Context, mgr mcmanager.Manager, caches *fleet.ClusterCaches, opts SetupOptions) error {
	if mgr == nil {
		return errors.New("a multi-cluster manager is required")
	}
	if caches == nil {
		return errors.New("a fleet.ClusterCaches is required: it supplies the workspace-scoped ClusterCache and the Cluster-event source")
	}

	concurrency := opts.fleetMaxConcurrentReconciles()
	options := controller.TypedOptions[mcreconcile.Request]{MaxConcurrentReconciles: concurrency}

	// Note the absence of SkipNameValidation, which every per-workspace
	// controller needs. controller-runtime rejects a duplicate controller name
	// because two controllers reporting one metric is a reporting fault; wiring
	// per workspace collides with that by construction, and disabling the check
	// is how the per-workspace path lives with it. A fleet-wide controller
	// registers each name once, so the check can do its job — and reconcile
	// metrics become attributable again, which was P9's problem.

	clusterAwareClient := capimulticluster.NewClusterAwareClient(mgr)
	clusterAwareReader := capimulticluster.NewClusterAwareAPIReader(mgr)

	if err := (&cluster.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                caches.Scoped(),
		RemoteConnectionGracePeriod: defaultRemoteConnectionGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, caches.Source); err != nil {
		return fmt.Errorf("creating fleet-wide Cluster controller: %w", err)
	}

	if err := (&machine.Reconciler{
		Client:                      clusterAwareClient,
		APIReader:                   clusterAwareReader,
		ClusterCache:                caches.Scoped(),
		RemoteConditionsGracePeriod: defaultRemoteConditionsGracePeriod,
	}).SetupWithMulticlusterManager(ctx, mgr, options, caches.Source); err != nil {
		return fmt.Errorf("creating fleet-wide Machine controller: %w", err)
	}

	return nil
}

// SetupWorkspaceComponents returns the providerwiring.SetupFunc for the part of
// the wiring that stays per workspace once SetupFleetControllers has taken
// Cluster and Machine.
//
// Two things, for two different reasons:
//
//   - The ClusterCache, because its accessors and last-event times are keyed by
//     namespace and name with no workspace. One across the fleet would let two
//     workspaces' identically named Clusters share a connection to a workload
//     cluster. It is registered with caches so the fleet-wide controllers can
//     reach it, and deregistered when the workspace goes away.
//   - The docker/dev infrastructure provider's reconcilers, because upstream has
//     no fleet-wide setup for them. This is the shape every provider will be in
//     until it grows one, and it is why the two paths have to coexist rather
//     than one replacing the other.
//
// # What that leaves per workspace
//
// By the R16 formula — goroutines/workspace = 2 + 7×controllers + workers×controllers
// + 2×watches — this is the manager, the ClusterCache controller and the two dev
// reconcilers. Cluster and Machine were two of the five wired controllers and
// the larger share of the watches, and they leave this sum entirely: they are
// paid once for the process instead.
//
// That is the claim the sweep should be re-run against once both paths are
// startable. It is written down so the re-run has something to falsify.
func SetupWorkspaceComponents(caches *fleet.ClusterCaches, dev *DevInfrastructure, opts SetupOptions) providerwiring.SetupFunc {
	return func(ctx context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error {
		if caches == nil {
			return errors.New("a fleet.ClusterCaches is required")
		}
		if dev == nil {
			return errors.New("DevInfrastructure must not be nil: create it once per process with NewDevInfrastructure")
		}

		concurrency := opts.maxConcurrentReconciles()

		secretCachingClient, err := client.New(mgr.GetConfig(), client.Options{
			HTTPClient: mgr.GetHTTPClient(),
			Cache:      &client.CacheOptions{Reader: mgr.GetCache()},
		})
		if err != nil {
			return fmt.Errorf("creating secret caching client: %w", err)
		}

		clusterCache, err := clustercache.SetupWithManager(ctx, mgr, clustercache.Options{
			SecretClient: secretCachingClient,
			Client: clustercache.ClientOptions{
				UserAgent: remote.DefaultClusterAPIUserAgent(controllerName),
			},
		}, controllerOptions(concurrency))
		if err != nil {
			return fmt.Errorf("creating ClusterCache: %w", err)
		}

		if err := caches.Add(workspace, clusterCache); err != nil {
			return fmt.Errorf("registering ClusterCache for workspace %s: %w", workspace, err)
		}

		// Deregistration is a runnable rather than a bare goroutine so that it
		// is bound to the workspace's group: the group is what disengagement
		// cancels, and what the process waits for on shutdown. A goroutine of
		// our own would outlive both.
		if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			<-ctx.Done()
			caches.Remove(workspace)
			return nil
		})); err != nil {
			caches.Remove(workspace)
			return fmt.Errorf("registering ClusterCache deregistration for workspace %s: %w", workspace, err)
		}

		if err := (&reconcilers.DevCluster{
			Client:           mgr.GetClient(),
			ContainerRuntime: dev.containerRuntime,
			InMemoryManager:  dev.inMemoryManager,
			APIServerMux:     dev.apiServerMux,
		}).SetupWithManager(ctx, mgr, controllerOptions(concurrency)); err != nil {
			return fmt.Errorf("creating DevCluster controller: %w", err)
		}

		if err := (&reconcilers.DevMachine{
			Client:           mgr.GetClient(),
			ContainerRuntime: dev.containerRuntime,
			ClusterCache:     clusterCache,
			InMemoryManager:  dev.inMemoryManager,
			APIServerMux:     dev.apiServerMux,
		}).SetupWithManager(ctx, mgr, controllerOptions(concurrency)); err != nil {
			return fmt.Errorf("creating DevMachine controller: %w", err)
		}

		return nil
	}
}
