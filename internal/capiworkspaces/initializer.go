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

package capiworkspaces

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/multicluster-provider/initializingworkspaces"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// InitializerControllerName is what the initializing controller is called in
// logs and metrics.
const InitializerControllerName = "cluster-api-workspace-initializer"

// ClaimRetryInterval is how often a workspace still waiting on kcp's own
// APIBinding initializer is looked at again - and, if kcp has stopped trying
// to apply a permission claim, poked into trying once more. See
// NudgeUnappliedClaims.
const ClaimRetryInterval = 5 * time.Second

// NewInitializerProvider builds the multicluster provider that yields every
// workspace waiting on this project's initializer.
//
// kcp exposes those workspaces - and only those - through the WorkspaceType's
// own initializing virtual workspace, whose URL kcp publishes on the type's
// status. A workspace appears there when it is created and disappears the
// moment the initializer is removed from it, so the "fleet" this manager
// serves is precisely the set of workspaces that are not finished yet. Nothing
// here has to filter for that, and nothing here holds a cache of workspaces it
// has already dealt with.
func NewInitializerProvider(cfg *rest.Config, scheme *runtime.Scheme) (*initializingworkspaces.Provider, error) {
	return initializingworkspaces.New(cfg, string(WorkspaceTypeName), initializingworkspaces.Options{Scheme: scheme})
}

// AddInitializerToManager wires the initializing controller onto mgr.
//
// initializer is the name kcp gave this type - read it off the live
// WorkspaceType with initialization.InitializerForType rather than
// reconstructing it, because kcp builds it from the type's *logical cluster*
// name, which is only equal to its path in `root`.
func AddInitializerToManager(mgr mcmanager.Manager, initializer corev1alpha1.LogicalClusterInitializer, skipNameValidation bool) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named(InitializerControllerName).
		WithOptions(controller.TypedOptions[mcreconcile.Request]{SkipNameValidation: ptr.To(skipNameValidation)}).
		For(&corev1alpha1.LogicalCluster{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return initializeWorkspace(ctx, mgr, req, initializer)
		}))
}

// initializeWorkspace writes one workspace's roles and then lets it become
// Ready.
//
// The order is the whole point of using an initializer rather than doing this
// once the workspace is up: kcp holds the workspace out of Ready until the
// initializer is gone, so a tenant is never handed a workspace that serves
// Cluster API and grants nobody the use of it.
func initializeWorkspace(ctx context.Context, mgr mcmanager.Manager, req mcreconcile.Request, initializer corev1alpha1.LogicalClusterInitializer) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("workspace", req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("getting the cluster for %s: %w", req.ClusterName, err)
	}
	cli := cl.GetClient()

	logical := &corev1alpha1.LogicalCluster{}
	if err := cli.Get(ctx, req.NamespacedName, logical); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !slices.Contains(logical.Status.Initializers, initializer) {
		// Already done. The provider disengages a workspace once its
		// initializer is gone, but the call is documented as racy and an event
		// in flight arrives after the work is finished.
		return reconcile.Result{}, nil
	}

	// The APIBindings this type declares are created by kcp's own
	// `system:apibindings` initializer, which runs independently of this one
	// and may not have run yet. Waiting for it is what makes the roles right
	// on the first write rather than on a later reconcile that may not come:
	// once this initializer is removed the workspace leaves this manager's
	// fleet, and the fleet-wide maintainer only sees workspaces that have
	// finished binding.
	if slices.Contains(logical.Status.Initializers, tenancyv1alpha1.WorkspaceAPIBindingsInitializer) {
		logger.V(4).Info("Waiting for kcp to create the workspace's default APIBindings")
		nudged, err := NudgeUnappliedClaims(ctx, cli)
		if err != nil {
			return reconcile.Result{}, err
		}
		if len(nudged) > 0 {
			logger.Info("Retried permission claims that kcp gave up applying", "apiBindings", nudged)
		}
		return reconcile.Result{RequeueAfter: ClaimRetryInterval}, nil
	}

	state, err := ReconcileRoles(ctx, cli)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(state.Written) > 0 {
		logger.Info("Wrote the workspace's Cluster API roles", "roles", state.Written, "apiGroups", state.Groups)
	}

	patch := client.MergeFrom(logical.DeepCopy())
	logical.Status.Initializers = initialization.EnsureInitializerAbsent(initializer, logical.Status.Initializers)
	if err := cli.Status().Patch(ctx, logical, patch); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing the %s initializer from %s: %w", initializer, req.ClusterName, err)
	}
	logger.Info("Workspace initialized for Cluster API")

	return reconcile.Result{}, nil
}

// ClaimRetryAnnotation is written on an APIBinding to make kcp look at it
// again. Its value is a counter; only the fact that it changed matters.
const ClaimRetryAnnotation = "cluster-api.kcp.io/permission-claim-retry"

// MaxClaimRetries bounds the poking. The race this works around is decided in
// seconds, so a binding that has not applied its claims after this many tries
// is not losing a race - something else is wrong, and writing to it every few
// seconds for as long as the workspace exists would turn one stuck workspace
// into a write loop nobody asked for.
const MaxClaimRetries = 12

// NudgeUnappliedClaims makes kcp retry the permission claims it has given up
// applying, and reports the APIBindings it poked.
//
// # Why this exists
//
// A Cluster API workspace binds core's APIExport, and core claims the types of
// every installed provider - including providers this workspace has not
// enabled. kcp is built for that: `permissionClaimMaterialiserReconciler`
// creates a bound CRD for a claimed resource whose producing export nobody in
// this workspace has bound, so the claim can be applied anyway.
//
// The two halves race. kcp's `permissionclaimlabel` controller starts trying
// to apply the claims immediately, fails with "unable to find informer for
// <group>.<resource>" while the bound CRD does not exist yet, and exhausts its
// workqueue retries in about thirteen seconds. The materialiser usually wins;
// when it does not, nothing re-enqueues the APIBinding, so
// `PermissionClaimsApplied` stays False - and kcp's `system:apibindings`
// initializer waits for exactly that condition before letting the workspace
// become Ready. Measured on kcp v0.32.3: the workspace is Ready in about ten
// seconds when the materialiser wins, and had not become Ready two minutes
// later when it did not, on roughly half of the runs.
//
// A metadata patch is all it takes: any update re-enqueues the binding with a
// fresh retry budget, and by then the bound CRD is there. It touches nothing
// kcp owns - `Maintain` rewrites `spec`, never annotations - so the two do not
// fight.
//
// This is a workaround for a kcp defect and is meant to be deleted rather than
// maintained. What retires it is kcp re-enqueueing the APIBinding when the
// bound CRD it was waiting for appears.
func NudgeUnappliedClaims(ctx context.Context, cl client.Client) ([]string, error) {
	bindings := &apisv1alpha2.APIBindingList{}
	if err := cl.List(ctx, bindings); err != nil {
		return nil, fmt.Errorf("listing APIBindings: %w", err)
	}

	var nudged []string
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if !claimsPending(binding) {
			continue
		}

		patch := client.MergeFrom(binding.DeepCopy())
		annotations := binding.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		retries, _ := strconv.Atoi(annotations[ClaimRetryAnnotation])
		if retries >= MaxClaimRetries {
			continue
		}
		annotations[ClaimRetryAnnotation] = strconv.Itoa(retries + 1)
		binding.SetAnnotations(annotations)

		if err := cl.Patch(ctx, binding, patch); err != nil {
			return nudged, fmt.Errorf("retrying the permission claims on APIBinding %s: %w", binding.Name, err)
		}
		nudged = append(nudged, binding.Name)
	}
	return nudged, nil
}

// claimsPending reports whether a binding has claims it has not applied and
// kcp is no longer working on it.
//
// Both halves matter. A binding that accepts no claims has nothing to wait
// for, and one that is still Binding is being worked on - poking that one
// would fight the controller rather than restart it.
func claimsPending(binding *apisv1alpha2.APIBinding) bool {
	if len(binding.Spec.PermissionClaims) == 0 {
		return false
	}
	if binding.Status.Phase != apisv1alpha2.APIBindingPhaseBound {
		return false
	}
	return len(binding.Status.AppliedPermissionClaims) < len(binding.Spec.PermissionClaims)
}
