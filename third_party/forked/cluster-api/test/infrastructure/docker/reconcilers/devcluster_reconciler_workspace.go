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
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers.
//
// It is SetupWithManager with the builder swapped, and one substitution that
// matters: the Cluster-to-DevCluster mapper is built against r.Client rather
// than the manager's, because that mapper lists with the context it is called
// with and r.Client is the one scoped by the cluster in that context.
//
// # What this does not make workspace-safe
//
// The in-memory workload-cluster backend this reconciler drives — InMemoryManager
// and APIServerMux — is process-wide and keys its listeners by Cluster name, so
// two logical clusters each holding a Cluster with the same namespace and name
// still collide there. That is a property of upstream's test infrastructure
// provider, which exists for development and e2e; it predates this setup and is
// not introduced by it. A real infrastructure provider holds nothing
// process-wide.
func (r *DevCluster) SetupWithMulticlusterManager(ctx context.Context, mgr mcmanager.Manager, options controller.TypedOptions[mcreconcile.Request], opts ...capicontrollerutil.MulticlusterOption) error {
	if r.Client == nil || r.InMemoryManager == nil || r.APIServerMux == nil || r.ContainerRuntime == nil {
		return pkgerrors.New("Client, InMemoryManager and APIServerMux, ContainerRuntime must not be nil")
	}

	scheme := mgr.GetLocalManager().GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "devcluster")
	err := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&infrav1.DevCluster{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(scheme, predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind("DevCluster"), r.Client, &infrav1.DevCluster{})),
			predicates.ClusterPausedTransitions(scheme, predicateLog),
		).Complete(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}
	return nil
}
