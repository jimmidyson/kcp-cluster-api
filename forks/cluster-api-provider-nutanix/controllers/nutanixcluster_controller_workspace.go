/*
Copyright 2026 Nutanix

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

package controllers

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capiutil "sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"

	infrav1 "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
)

// SetupWithMulticlusterManager sets the reconciler up as one controller serving
// every cluster the manager's provider offers, rather than one controller per
// cluster.
//
// It is SetupWithManager with the builder swapped, and two substitutions:
//
//   - The Cluster-to-NutanixCluster mapper is built against r.Client rather than
//     the manager's. The mapper lists with the context it is called with, and
//     r.Client is the one that resolves to the cluster named in that context;
//     the manager's client addresses no cluster in particular.
//   - The scheme comes from the local manager, which is the one holding the
//     registered types.
//
// Controller options are the caller's rather than r.controllerConfig's. The
// config's rate limiter is typed on reconcile.Request and a fleet-wide
// controller's queue is typed on mcreconcile.Request, so the two cannot be the
// same value; the fleet's owner supplies the queue it wants.
func (r *NutanixClusterReconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil {
		return fmt.Errorf("Client must not be nil")
	}
	if r.CredentialReader == nil {
		return fmt.Errorf("CredentialReader must not be nil: a fleet-wide controller resolves Prism Central credentials per workspace, and the informers address one API server")
	}

	scheme := mgr.GetLocalManager().GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "nutanixcluster")

	if err := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		Named("nutanixcluster-controller").
		For(&infrav1.NutanixCluster{}).
		WithOptions(options).
		Watches(
			&capiv1beta2.Cluster{},
			handler.EnqueueRequestsFromMapFunc(
				capiutil.ClusterToInfrastructureMapFunc(
					ctx,
					infrav1.GroupVersion.WithKind(infrav1.NutanixClusterKind),
					r.Client,
					&infrav1.NutanixCluster{},
				),
			),
			predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(scheme, predicateLog),
		).
		Watches(
			&infrav1.NutanixFailureDomain{},
			handler.EnqueueRequestsFromMapFunc(r.mapNutanixFailureDomainToNutanixCluster()),
		).
		Complete(ctx, r); err != nil {
		return fmt.Errorf("failed setting up the NutanixCluster controller with a multicluster manager: %w", err)
	}

	return nil
}
