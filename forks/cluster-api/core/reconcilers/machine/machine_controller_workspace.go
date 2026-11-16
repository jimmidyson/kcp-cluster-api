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

package machine

import (
	"context"
	"time"

	pkgerrors "github.com/pkg/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/cache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers.
//
// It is SetupWithManager with the builder swapped, plus one thing the Cluster
// reconciler does not need: a fleet-wide way to watch a workload cluster's
// Nodes. See newNodeWatcher below.
//
// Client, APIReader and ClusterCache must be scoped by the cluster in the
// context — see the equivalent note on the Cluster reconciler's setup, which
// also records the event-recorder gap that applies here too.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	clusterSource clustercache.MulticlusterClusterSourceFunc,
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.APIReader == nil || r.ClusterCache == nil || r.RemoteConditionsGracePeriod < 2*time.Minute {
		return pkgerrors.New("Client, APIReader and ClusterCache must not be nil and RemoteConditionsGracePeriod must not be < 2m")
	}
	if feature.Gates.Enabled(feature.InPlaceUpdates) && r.RuntimeClient == nil {
		return pkgerrors.New("RuntimeClient must not be nil when InPlaceUpdates feature gate is enabled")
	}
	if clusterSource == nil {
		return pkgerrors.New("clusterSource must not be nil")
	}

	localMgr := mgr.GetLocalManager()
	r.predicateLog = ptr.To(ctrl.LoggerFrom(ctx).WithValues("controller", "machine"))

	// The mappers are built against the cluster-aware client rather than the
	// manager's, unlike SetupWithManager. Each one closes over the client it is
	// given and lists with the context it is called with, so this is what makes
	// them list in the cluster the event came from.
	clusterToMachines, err := util.ClusterToTypedObjectsMapper(r.Client, &clusterv1.MachineList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}
	msToMachines, err := util.MachineSetToObjectsMapper(r.Client, &clusterv1.MachineList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}
	mdToMachines, err := util.MachineDeploymentToObjectsMapper(r.Client, &clusterv1.MachineList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}

	if r.nodeDeletionRetryTimeout.Nanoseconds() == 0 {
		r.nodeDeletionRetryTimeout = 10 * time.Second
	}

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, *r.predicateLog)
	c, err := b.
		Apply(opts...).
		For(&clusterv1.Machine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), *r.predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToMachines),
			// TODO: should this wait for Cluster.Status.InfrastructureReady similar to Infra Machine resources?
			predicates.ClusterControlPlaneInitialized(localMgr.GetScheme(), *r.predicateLog),
			predicates.ResourceHasFilterLabel(localMgr.GetScheme(), *r.predicateLog, r.WatchFilterValue),
		).
		WatchesRawSource(clusterSource("machine", clusterToMachines, clustercache.WatchForProbeFailure(r.RemoteConditionsGracePeriod))).
		Watches(
			&clusterv1.MachineSet{},
			handler.EnqueueRequestsFromMapFunc(msToMachines),
		).
		Watches(
			&clusterv1.MachineDeployment{},
			handler.EnqueueRequestsFromMapFunc(mdToMachines),
		).
		Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.hookCache = cache.New[cache.HookEntry](ctx, cache.HookCacheDefaultTTL)
	r.controller = c

	// The Node watch is the one place a fleet-wide Machine controller cannot
	// reuse the single-cluster construction.
	//
	// The events come from a workload cluster's cache, and the requests they
	// produce name Machines in the management cluster being reconciled. Which
	// management cluster that is, is only known when Reconcile establishes the
	// watch — so it is taken from the context, and the handler is bound to it so
	// that both the requests and nodeToMachine's list land in the right place.
	r.newNodeWatcher = func(ctx context.Context, name string, kind client.Object, eventHandler handler.EventHandler, preds ...predicate.TypedPredicate[client.Object]) (clustercache.Watcher, error) {
		clusterName, ok := mccontext.ClusterFrom(ctx)
		if !ok {
			return nil, pkgerrors.Errorf("no cluster in context when establishing the %q watch", name)
		}
		return clustercache.NewWatcher(clustercache.TypedWatcherOptions[client.Object, mcreconcile.Request]{
			Name:         name,
			Watcher:      c,
			Kind:         kind,
			EventHandler: capimulticluster.ForClusterWithClusterInContext(eventHandler, clusterName),
			Predicates:   preds,
		}), nil
	}

	r.recorder = b.EventRecorderFor("machine-controller")
	r.externalTracker = external.ObjectTracker{
		MultiClusterController: c,
		Scheme:                 localMgr.GetScheme(),
		PredicateLogger:        r.predicateLog,
	}
	return nil
}
