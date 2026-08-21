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

// Package workspacemanager runs the three controllers that onboard a workspace
// to Cluster API and keep its permissions right.
//
// Each answers a different question, and together they are the whole of
// "a tenant enables a provider and nothing else has to happen":
//
//   - The claim controller (this file) watches the APIExports in the workspace
//     the providers are published in. When a new provider export appears it
//     rewrites core's permission claims to cover the types that provider
//     publishes, so core's controllers can reach them. kcp's own Maintain
//     lifecycle then propagates acceptance of those claims into every tenant
//     workspace, with no code here.
//
//   - The initializer (internal/capiworkspaces) writes a new workspace's roles
//     before kcp lets the workspace become Ready.
//
//   - The role maintainer (internal/capiworkspaces) watches the APIBindings in
//     every Cluster API workspace and keeps those roles covering whatever the
//     tenant has enabled.
//
// The first two act in the provider's workspace and the third across the
// fleet, so they are three managers rather than three controllers on one - but
// they are one deployment, because they are one job.
package workspacemanager

import (
	"context"
	"fmt"

	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

// ClaimsControllerName is what the claim controller is called in logs and
// metrics.
const ClaimsControllerName = "cluster-api-permission-claims"

// AddClaimsToManager wires the permission-claim controller onto a manager
// scoped to the workspace the APIExports live in.
//
// skipNameValidation turns off controller-runtime's process-global check that
// no two controllers share a name. A deployment runs one of these and should
// leave it off; a test process that stands several installations up in turn
// has to, because that registry is never emptied. See
// providerwiring.SetupFunc's contract for the same rule per workspace.
//
// providers are the exports whose claim lists this deployment maintains. They
// are named rather than discovered because a claim list is a statement about
// what a *controller* does, which only the project shipping that controller
// can make; what is discovered is the other side - whose types those claims
// land on.
//
// So a third-party infrastructure provider needs no entry here. It publishes a
// labelled APIExport, and core's claim list - maintained from this
// list - grows to cover it.
func AddClaimsToManager(mgr manager.Manager, providers []capiexports.Provider, skipNameValidation bool) error {
	return builder.ControllerManagedBy(mgr).
		Named(ClaimsControllerName).
		WithOptions(controller.Options{SkipNameValidation: ptr.To(skipNameValidation)}).
		For(&apisv1alpha2.APIExport{}).
		Complete(reconcile.Func(func(ctx context.Context, _ reconcile.Request) (ctrl.Result, error) {
			return reconcileClaims(ctx, mgr.GetClient(), providers)
		}))
}

// reconcileClaims recomputes every maintained export's claim list.
//
// All of them on every event, rather than the one that changed: the claims are
// a function of the whole set of exports, so an event on any one of them can
// change any other's list. The set is a handful of objects in one workspace,
// and a reconcile that finds nothing to change writes nothing.
func reconcileClaims(ctx context.Context, cl client.Client, providers []capiexports.Provider) (ctrl.Result, error) {
	updated, err := capiexports.ReconcileClaims(ctx, cl, providers)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("reconciling permission claims: %w", err)
	}
	if len(updated) > 0 {
		log.FromContext(ctx).Info("Updated permission claims after a change to the installed providers", "exports", updated)
	}
	return reconcile.Result{}, nil
}
