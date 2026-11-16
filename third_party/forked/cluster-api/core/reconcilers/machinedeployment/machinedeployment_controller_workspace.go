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

package machinedeployment

import (
	"context"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/cache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager, on the builder that keys the queue
// on a request carrying the cluster, with the mappers and the MachineSet delete
// client built against the reconciler's cluster-aware client - see the
// MachineSet reconciler's equivalent setup for both.
//
// Unlike its siblings this one takes no cluster source: it watches nothing on
// the workload clusters, only MachineSets and Machines in the management
// cluster.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.APIReader == nil {
		return pkgerrors.New("Client and APIReader must not be nil")
	}
	if feature.Gates.Enabled(feature.InPlaceUpdates) && r.RuntimeClient == nil {
		return pkgerrors.New("RuntimeClient must not be nil when InPlaceUpdates feature gate is enabled")
	}

	localMgr := mgr.GetLocalManager()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "machinedeployment")

	clusterToMachineDeployments, err := util.ClusterToTypedObjectsMapper(r.Client, &clusterv1.MachineDeploymentList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&clusterv1.MachineDeployment{}).
		Owns(&clusterv1.MachineSet{}).
		Watches(
			&clusterv1.MachineSet{},
			handler.EnqueueRequestsFromMapFunc(r.MachineSetToDeployments),
		).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToMachineDeployments),
			predicates.ClusterPausedTransitions(localMgr.GetScheme(), predicateLog),
		)

	c, err := b.Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.msClientWithDeleteResponse = capicontrollerutil.NewClientWithDeleteResponseFromClient(r.Client)
	r.canUpdateMachineSetCache = cache.New[CanUpdateMachineSetCacheEntry](ctx, cache.HookCacheDefaultTTL)
	r.controller = c
	r.recorder = b.EventRecorderFor("machinedeployment-controller")
	r.ssaCache = ssa.NewCache("machinedeployment")
	return nil
}
