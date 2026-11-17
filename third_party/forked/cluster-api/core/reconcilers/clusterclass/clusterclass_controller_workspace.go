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

package clusterclass

import (
	"context"

	pkgerrors "github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	runtimev1 "sigs.k8s.io/cluster-api/api/runtime/v1beta2"
	runtimeclient "sigs.k8s.io/cluster-api/exp/runtime/client"
	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/cache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// SetupWithMulticlusterManager sets up the reconciler as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is the same wiring as SetupWithManager: the same For, the same watch on
// ExtensionConfig, the same map function, the same predicates and the same
// feature gate. Only the builder differs, so that the queue is keyed on a
// request that carries the cluster.
//
// Client must be scoped by the cluster in the context. Every reconcile-path use
// of it takes a context, so one field serves the whole fleet and no reconcile
// code changes.
func (r *Reconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil {
		return pkgerrors.New("Client must not be nil")
	}
	if feature.Gates.Enabled(feature.RuntimeSDK) && r.RuntimeClient == nil {
		return pkgerrors.New("RuntimeClient must not be nil")
	}

	localMgr := mgr.GetLocalManager()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "clusterclass")

	b := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		For(&clusterv1.ClusterClass{}).
		WithOptions(options)
	// Gated, where SetupWithManager registers it unconditionally.
	//
	// An ExtensionConfig event can only change a ClusterClass through variable
	// discovery, and that is itself gated on RuntimeSDK - so with the gate off
	// every event this watch delivers ends in a reconcile that does the same
	// thing it would have done anyway.
	//
	// What the gate buys is that a deployment which does not serve the type at
	// all still starts. A fleet-wide watch is one registration against the
	// shard's cache, and controller-runtime blocks a controller's startup on
	// every registered source's cache sync, including for a kind the server
	// does not serve: an unserved type does not skip the watch, it hangs it.
	if feature.Gates.Enabled(feature.RuntimeSDK) {
		b = b.Watches(
			&runtimev1.ExtensionConfig{},
			handler.EnqueueRequestsFromMapFunc(r.extensionConfigToClusterClass),
		)
	}

	err := b.
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Complete(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a multicluster manager")
	}

	r.discoverVariablesCache = cache.New[runtimeclient.CallExtensionCacheEntry](ctx, cache.DefaultTTL)
	return nil
}
