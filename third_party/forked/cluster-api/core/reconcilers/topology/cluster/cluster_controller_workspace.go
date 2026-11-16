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

package cluster

import (
	"context"
	"time"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/exp/topology/desiredstate"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util/cache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For and the same
// predicates on it, the same watches in the same order, the same map functions
// and the same feature gates. Only the builder differs, so that the queue is
// keyed on a request that carries the cluster.
//
// # What the caller has to supply
//
// Client, APIReader and ClusterCache must be scoped by the cluster in the
// context. Every reconcile-path use of all three takes a context - the
// reconciler is handed one carrying the cluster by the builder - so a
// context-scoped implementation serves the whole fleet through the one field,
// and no reconcile code changes. That includes the desired state generator
// built below, which reads the ClusterClass and its templates through the same
// client and so resolves them in the cluster being reconciled.
//
// ClusterCache.GetClusterSource is the sole exception, which is why the source
// arrives as a parameter instead; see
// clustercache.MulticlusterClusterSourceFunc.
//
// # The For predicates are load-bearing
//
// This controller watches every Cluster in the fleet and reconciles only the
// ones with a topology. Registered per cluster that is a cheap filter; here it
// is the difference between one queue item per Cluster in the shard and one per
// Cluster that has a class. They are passed to For rather than applied inside
// Reconcile for that reason.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	clusterSource clustercache.MulticlusterClusterSourceFunc,
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.APIReader == nil || r.ClusterCache == nil {
		return pkgerrors.New("Client, APIReader and ClusterCache must not be nil")
	}
	if clusterSource == nil {
		return pkgerrors.New("clusterSource must not be nil")
	}
	if feature.Gates.Enabled(feature.RuntimeSDK) && r.RuntimeClient == nil {
		return pkgerrors.New("RuntimeClient must not be nil")
	}

	localMgr := mgr.GetLocalManager()
	scheme := localMgr.GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "topology/cluster")

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&clusterv1.Cluster{},
			// Only reconcile Cluster with topology and with changes relevant for this controller.
			predicates.ClusterHasTopology(scheme, predicateLog),
			clusterChangeIsRelevant(scheme, predicateLog),
		).
		Named("topology/cluster").
		WatchesRawSource(clusterSource("topology/cluster", func(_ context.Context, o client.Object) []ctrl.Request {
			return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		})).
		Watches(
			&clusterv1.ClusterClass{},
			handler.EnqueueRequestsFromMapFunc(r.clusterClassToCluster),
		).
		Watches(
			&clusterv1.MachineDeployment{},
			handler.EnqueueRequestsFromMapFunc(r.machineDeploymentToCluster),
			// Only trigger Cluster reconciliation if the MachineDeployment is topology owned, the resource is changed, and the change is relevant.
			predicates.ResourceIsTopologyOwned(scheme, predicateLog),
			machineDeploymentChangeIsRelevant(scheme, predicateLog),
		).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(scheme, predicateLog, r.WatchFilterValue))

	// Ungated, as SetupWithManager has it, and deliberately not put behind the
	// MachinePool gate the core Cluster reconciler's equivalent watch is behind.
	//
	// The gate would buy nothing. This reconciler's read of the current
	// topology state lists MachinePools on every reconcile whatever the gate
	// says, so a deployment serving managed topologies has to serve the type
	// regardless - and once it is served, a watch on it costs one registration
	// for the shard.
	b = b.Watches(
		&clusterv1.MachinePool{},
		handler.EnqueueRequestsFromMapFunc(r.machinePoolToCluster),
		// Only trigger Cluster reconciliation if the MachinePool is topology owned, the resource is changed.
		predicates.ResourceIsTopologyOwned(scheme, predicateLog),
	)

	c, err := b.Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	// No Cache: a fleet-wide watch resolves each cluster's cache when that
	// cluster engages, so there is no one cache to bind the tracker to.
	r.externalTracker = external.ObjectTracker{
		MultiClusterController: c,
		Scheme:                 scheme,
		PredicateLogger:        &predicateLog,
	}
	r.hookCache = cache.New[cache.HookEntry](ctx, cache.HookCacheDefaultTTL)
	r.desiredStateGenerator, err = desiredstate.NewGenerator(
		r.Client,
		r.ClusterCache,
		r.RuntimeClient,
		r.hookCache,
		// Note: We are using 10m so that we are able to relatively quickly pick up changes to the
		// upgrade plan from the extension if necessary.
		cache.New[desiredstate.GenerateUpgradePlanCacheEntry](ctx, 10*time.Minute),
	)
	if err != nil {
		return pkgerrors.Wrap(err, "failed creating desired state generator")
	}

	r.controller = c
	r.recorder = b.EventRecorderFor("topology/cluster-controller")
	r.ssaCache = ssa.NewCache("topology/cluster")
	return nil
}
