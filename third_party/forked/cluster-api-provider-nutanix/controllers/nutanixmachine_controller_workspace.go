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
// It is SetupWithManager with the builder swapped, and the mappers and scheme
// resolved the way the NutanixCluster counterpart resolves them.
//
// # APIReader is required here, where SetupWithManager defaults it
//
// SetupWithManager falls back to mgr.GetAPIReader() when APIReader is nil. That
// fallback is wrong for a fleet-wide controller and is deliberately not
// reproduced: the reader is used by metro VM placement to enumerate the sibling
// NutanixMachines of the one being reconciled, and the local manager's reader
// addresses no cluster in particular. Defaulting to it would enumerate siblings
// across every cluster the manager serves, so one tenant's placement decision
// would be computed from another tenant's machines. The caller passes a
// cluster-aware reader or this returns an error; there is no default that is
// safe to pick here.
func (r *NutanixMachineReconciler) SetupWithMulticlusterManager(
	ctx context.Context,
	mgr mcmanager.Manager,
	options controller.TypedOptions[mcreconcile.Request],
	opts ...capicontrollerutil.MulticlusterOption,
) error {
	if r.Client == nil {
		return fmt.Errorf("Client must not be nil")
	}
	if r.APIReader == nil {
		return fmt.Errorf("APIReader must not be nil: a fleet-wide controller needs a cluster-aware reader, and the manager's reads across every cluster it serves")
	}
	if r.CredentialReader == nil {
		return fmt.Errorf("CredentialReader must not be nil: a fleet-wide controller resolves Prism Central credentials per workspace, and the informers address one API server")
	}

	scheme := mgr.GetLocalManager().GetScheme()
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "nutanixmachine")

	clusterToObjectFunc, err := capiutil.ClusterToTypedObjectsMapper(r.Client, &infrav1.NutanixMachineList{}, scheme)
	if err != nil {
		return fmt.Errorf("failed to create mapper for Cluster to NutanixMachine: %w", err)
	}

	if err := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		Named("nutanixmachine-controller").
		For(&infrav1.NutanixMachine{}).
		WithOptions(options).
		Watches(
			&capiv1beta2.Machine{},
			handler.EnqueueRequestsFromMapFunc(
				capiutil.MachineToInfrastructureMapFunc(
					infrav1.GroupVersion.WithKind("NutanixMachine"),
				),
			),
		).
		Watches(
			&infrav1.NutanixCluster{},
			handler.EnqueueRequestsFromMapFunc(r.mapNutanixClusterToNutanixMachines()),
		).
		Watches(
			&capiv1beta2.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToObjectFunc),
			predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(scheme, predicateLog),
		).
		Complete(ctx, r); err != nil {
		return fmt.Errorf("failed setting up the NutanixMachine controller with a multicluster manager: %w", err)
	}

	return nil
}
