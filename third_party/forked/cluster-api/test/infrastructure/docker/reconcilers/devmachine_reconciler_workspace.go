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

package reconcilers

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
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	dockerbackend "sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends/docker"
	"sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers.
//
// Three things differ from SetupWithManager, and all three are forced.
//
// The mappers are built against r.Client rather than the manager's, because they
// list with the context they are called with and r.Client is the one scoped by
// the cluster in it.
//
// The ClusterCache source comes from the parameter rather than from
// r.ClusterCache.GetClusterSource, which takes no context and so cannot say
// which cluster an event came from.
//
// The async task manager's source is its fleet-wide one. Its tasks are keyed by
// the logical cluster as well as the DockerMachine, so two tenants' identically
// named DockerMachines no longer share a task — which would have let one
// tenant's provisioning cancel or report on the other's.
//
// # What this does not make workspace-safe
//
// The in-memory workload-cluster backend keys its listeners by Cluster name and
// is process-wide, so identically named Clusters in two logical clusters still
// collide there. That is upstream's test infrastructure provider, and it
// predates this setup.
func (r *DevMachine) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	clusterSource clustercache.MulticlusterClusterSourceFunc,
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.InMemoryManager == nil || r.APIServerMux == nil || r.ContainerRuntime == nil || r.ClusterCache == nil {
		return pkgerrors.New("Client, InMemoryManager and APIServerMux, ContainerRuntime and ClusterCache must not be nil")
	}
	if clusterSource == nil {
		return pkgerrors.New("clusterSource must not be nil")
	}

	scheme := mgr.GetLocalManager().GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "devmachine")
	clusterToDevMachines, err := util.ClusterToTypedObjectsMapper(r.Client, &infrav1.DevMachineList{}, scheme)
	if err != nil {
		return err
	}

	r.DockerMachineTaskManager = dockerbackend.NewTaskManager()
	c, err := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&infrav1.DevMachine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(scheme, predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("DevMachine"))),
		).
		Watches(
			&infrav1.DevCluster{},
			handler.EnqueueRequestsFromMapFunc(r.DevClusterToDevMachines),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToDevMachines),
			predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(scheme, predicateLog),
		).
		WatchesRawSource(clusterSource("devmachine", clusterToDevMachines)).
		WatchesRawSource(r.DockerMachineTaskManager.GetMulticlusterSource()).
		Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.controller = c
	return nil
}
