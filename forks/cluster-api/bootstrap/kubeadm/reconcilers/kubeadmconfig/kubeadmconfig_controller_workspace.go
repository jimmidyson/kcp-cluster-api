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

package kubeadmconfig

import (
	"context"
	"time"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/bootstrap/kubeadm/pkg/locking"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/feature"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For, the same watches in
// the same order, the same map functions, the same predicates and the same
// feature gate. Only the builder differs, so that the queue is keyed on a
// request that carries the cluster.
//
// # What the caller has to supply
//
// Client, SecretCachingClient and ClusterCache must be scoped by the cluster in
// the context. Every reconcile-path use of all three takes a context, so a
// context-scoped implementation serves the whole fleet through the one field
// and no reconcile code changes. ClusterCache.GetClusterSource is the sole
// exception, which is why the source arrives as a parameter; see
// clustercache.MulticlusterClusterSourceFunc.
//
// This reconciler carries more Secret traffic than any other - the bootstrap
// data Secret it produces, and the cluster certificates it generates for the
// first control plane machine - so a caller whose cluster-aware client cannot
// serve core v1 Secrets will find every one of those calls failing rather than
// only some. That is a property of the caller's client, not of this wiring.
//
// # The init lock
//
// KubeadmInitLock defaults to a mutex over r.Client rather than over the
// manager's client, which is the one substantive difference from
// SetupWithManager. The lock is a ConfigMap in the cluster being reconciled,
// and the manager's client addresses no cluster in particular: defaulting it
// the single-cluster way would serialize control plane initialization across
// every cluster in the fleet through one object, or fail to find it at all.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	clusterSource clustercache.MulticlusterClusterSourceFunc,
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.SecretCachingClient == nil || r.ClusterCache == nil || r.TokenTTL == time.Duration(0) {
		return pkgerrors.New("Client, SecretCachingClient and ClusterCache must not be nil and TokenTTL must not be 0")
	}
	if clusterSource == nil {
		return pkgerrors.New("clusterSource must not be nil")
	}

	if r.KubeadmInitLock == nil {
		r.KubeadmInitLock = locking.NewControlPlaneInitMutex(r.Client)
	}

	localMgr := mgr.GetLocalManager()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "kubeadmconfig")

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&bootstrapv1.KubeadmConfig{}).
		WithOptions(options).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(r.MachineToBootstrapMapFunc),
		).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue))

	if feature.Gates.Enabled(feature.MachinePool) {
		b = b.Watches(
			&clusterv1.MachinePool{},
			handler.EnqueueRequestsFromMapFunc(r.MachinePoolToBootstrapMapFunc),
		)
	}

	b = b.Watches(
		&clusterv1.Cluster{},
		handler.EnqueueRequestsFromMapFunc(r.ClusterToKubeadmConfigs),
		predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(localMgr.GetScheme(), predicateLog),
		predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue),
	).WatchesRawSource(clusterSource("kubeadmconfig", r.ClusterToKubeadmConfigs))

	if _, err := b.Build(ctx, r); err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}
	return nil
}
