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
	"sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For and the same
// predicates on it, the same watch on Cluster, the same predicates and the same
// map function. Only the builder differs, so that the queue is keyed on a
// request that carries the cluster - and the mapper is built against the
// reconciler's cluster-aware client rather than the local manager's, which
// addresses no cluster in particular. A mapper built on the latter would list
// MachineDeployment{}s through the wrong endpoint on every Cluster event.
//
// Like the MachineDeployment reconciler's, this setup takes no cluster source:
// it watches nothing on the workload clusters.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.APIReader == nil {
		return pkgerrors.New("Client and APIReader must not be nil")
	}

	localMgr := mgr.GetLocalManager()
	scheme := localMgr.GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "topology/machinedeployment")

	clusterToMachineDeployments, err := util.ClusterToTypedObjectsMapper(r.Client, &clusterv1.MachineDeploymentList{}, scheme)
	if err != nil {
		return err
	}

	err = capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&clusterv1.MachineDeployment{},
			predicates.ResourceIsTopologyOwned(scheme, predicateLog),
			predicates.ResourceNotPaused(scheme, predicateLog),
		).
		Named("topology/machinedeployment").
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(scheme, predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToMachineDeployments),
			predicates.ClusterUnpaused(scheme, predicateLog),
			predicates.ClusterHasTopology(scheme, predicateLog),
		).
		Complete(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	return nil
}
