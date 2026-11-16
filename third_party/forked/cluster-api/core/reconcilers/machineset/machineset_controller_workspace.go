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

package machineset

import (
	"context"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For, the same Owns, the
// same watches in the same order, the same map functions and predicates. Only
// the builder differs, so that the queue is keyed on a request that carries the
// cluster.
//
// The mappers are built against the reconciler's cluster-aware client rather
// than the manager's, which is what makes them list in the cluster the event
// came from; and the Machine delete client comes from that same client, for the
// reason recorded on the KubeadmControlPlane reconciler's equivalent setup.
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

	localMgr := mgr.GetLocalManager()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "machineset")

	clusterToMachineSets, err := util.ClusterToTypedObjectsMapper(r.Client, &clusterv1.MachineSetList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}
	mdToMachineSets, err := util.MachineDeploymentToObjectsMapper(r.Client, &clusterv1.MachineSetList{}, localMgr.GetScheme())
	if err != nil {
		return err
	}

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&clusterv1.MachineSet{}).
		Owns(&clusterv1.Machine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.MachineToMachineSets),
		).
		Watches(
			&clusterv1.MachineDeployment{},
			handler.EnqueueRequestsFromMapFunc(mdToMachineSets),
		).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToMachineSets),
			predicates.ClusterPausedTransitions(localMgr.GetScheme(), predicateLog),
			predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue),
		).
		WatchesRawSource(clusterSource("machineset", clusterToMachineSets))

	c, err := b.Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.machineClientWithDeleteResponse = capicontrollerutil.NewClientWithDeleteResponseFromClient(r.Client)
	r.controller = c
	r.recorder = b.EventRecorderFor("machineset-controller")
	r.ssaCache = ssa.NewCache("machineset")
	return nil
}
