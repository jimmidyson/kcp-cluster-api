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

package kubeadmcontrolplane

import (
	"context"
	"time"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controlplane/kubeadm/pkg"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/internal/util/ssa"
	"sigs.k8s.io/cluster-api/util/cache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For, the same Owns, the
// same watch on Cluster with the same predicates, and the same management
// cluster underneath. Only the builder differs, so that the queue is keyed on a
// request that carries the cluster.
//
// # What the caller has to supply
//
// Client, APIReader, SecretCachingClient and ClusterCache must be scoped by the
// cluster in the context, as for every other fleet-wide reconciler.
//
// # The one behavioural difference, and what it costs
//
// SetupWithManager builds a REST client for Machines from the manager's own
// config, so that a delete returns the deleted object and the controller can
// defer its next reconcile until its cache has caught up. That config addresses
// no cluster in particular here - the fleet-wide manager is built against a
// wildcard endpoint - so this uses the reconciler's own cluster-aware client
// instead, through NewClientWithDeleteResponseFromClient.
//
// The cost is exactly that optimisation: a delete returns no object, so the
// controller does not defer, and a scale-down may reconcile once against a
// cache that still lists the deleted Machine. Both call sites already handle a
// nil result, and the next event corrects the view.
//
// # What is safe without changing anything
//
// The management cluster's client-certificate cache keys on the Cluster's UID
// as well as its namespace and name, so two workspaces holding identically
// named Clusters do not share an entry. That is worth stating because it is the
// kind of process-global cache that would otherwise hand one tenant's client
// certificate to another.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	clusterSource clustercache.MulticlusterClusterSourceFunc,
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil || r.SecretCachingClient == nil || r.ClusterCache == nil ||
		r.EtcdDialTimeout == time.Duration(0) || r.EtcdCallTimeout == time.Duration(0) ||
		r.RemoteConditionsGracePeriod < 2*time.Minute {
		return pkgerrors.New("Client, SecretCachingClient and ClusterCache must not be nil and " +
			"EtcdDialTimeout and EtcdCallTimeout must not be 0 and " +
			"RemoteConditionsGracePeriod must not be < 2m")
	}
	if feature.Gates.Enabled(feature.InPlaceUpdates) && r.RuntimeClient == nil {
		return pkgerrors.New("RuntimeClient must not be nil when InPlaceUpdates feature gate is enabled")
	}
	if clusterSource == nil {
		return pkgerrors.New("clusterSource must not be nil")
	}

	localMgr := mgr.GetLocalManager()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "kubeadmcontrolplane")

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&controlplanev1.KubeadmControlPlane{}).
		Owns(&clusterv1.Machine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.ClusterToKubeadmControlPlane),
			predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue),
			predicates.Any(localMgr.GetScheme(), predicateLog,
				predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(localMgr.GetScheme(), predicateLog),
				predicates.ClusterTopologyVersionChanged(localMgr.GetScheme(), predicateLog),
			),
		).
		WatchesRawSource(clusterSource("kubeadmcontrolplane", r.ClusterToKubeadmControlPlane,
			clustercache.WatchForProbeFailure(r.RemoteConditionsGracePeriod)))

	c, err := b.Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.machineClientWithDeleteResponse = capicontrollerutil.NewClientWithDeleteResponseFromClient(r.Client)
	r.controller = c
	r.recorder = b.EventRecorderFor("kubeadmcontrolplane-controller")
	r.ssaCache = ssa.NewCache("kubeadmcontrolplane")

	if r.managementCluster == nil {
		r.managementCluster = &pkg.Management{
			Client:              r.Client,
			SecretCachingClient: r.SecretCachingClient,
			ClusterCache:        r.ClusterCache,
			EtcdDialTimeout:     r.EtcdDialTimeout,
			EtcdCallTimeout:     r.EtcdCallTimeout,
			EtcdLogger:          r.EtcdLogger,
			ClientCertCache:     cache.New[pkg.ClientCertEntry](ctx, 24*time.Hour),
		}
	}

	return nil
}
