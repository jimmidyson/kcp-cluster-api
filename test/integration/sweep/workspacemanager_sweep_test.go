//go:build integration

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

package sweep_test

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
)

// What one workspace's worth of the workspace deployment watches: APIBindings,
// and one handler registration on them.
//
// One type and one handler is the whole shape, and it is the smallest of any
// deployment here for a reason worth stating rather than treating as an
// oversight. This deployment reconciles no Cluster API object at all — what it
// writes is the permission to use them — so it watches the one resource that
// answers the only question its output depends on: what has this workspace
// enabled?
const (
	workspaceReconcilerWatchedTypes  = 1
	workspaceReconcilerEventHandlers = 1
)

// TestWorkspaceDeploymentWorkspaceSweep measures what the workspace
// deployment pays per workspace: the role maintainer cmd/workspace-manager
// wires fleet-wide, and only that.
//
// # Why this shape exists
//
// Every other sweep here measures a provider serving a workload. This one
// measures the deployment that makes a workspace able to hold a workload at
// all, and it exists because without it the per-deployment figures no longer
// add up to an installation: cmd/sweeptotals refuses to print a total when a
// deployment's report is missing, and after the workspace onboarding feature
// there were five deployments and four reports.
//
// # Two of the three controllers are deliberately not measured here
//
// cmd/workspace-manager runs three. Only the role maintainer is a per-workspace
// cost:
//
//   - The permission-claim controller watches the APIExports in one workspace —
//     the one the exports are published in — and is not per tenant workspace at
//     all. It costs the same with one workspace and with a thousand.
//   - The initializer's fleet is *only* the workspaces that have not finished
//     initializing. kcp's initializing virtual workspace stops serving a
//     workspace the moment its initializer is removed, so the provider
//     disengages it and the cost is transient by construction rather than by
//     eviction. What it holds at steady state is nothing, which is a claim
//     about kcp's own behaviour and is asserted by
//     test/integration/onboarding reaching Ready rather than measured here.
//
// So the number this reports is the role maintainer's, and that is the number
// an installation pays per workspace for this deployment.
//
// # The object count is not a dimension for this shape
//
// The other shapes scale a workload: N Clusters in a workspace cost more than
// one. This deployment's work is a function of the workspace rather than of
// anything in it — the roles are recomputed from the APIBindings the workspace
// holds, and the harness's own binding is what puts it to work. So
// SWEEP_WORKSPACE_OBJECTS is accepted for symmetry with the other shapes and
// changes nothing, and activate writes nothing: binding the export *is* the
// activation, and the roles appearing is the deployment having done its work
// in that workspace.
func TestWorkspaceDeploymentWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, corev1alpha1.AddToScheme(scheme))
	must(t, tenancyv1alpha1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (the workspace deployment)",
		reportName: "sweep-report-workspace",
		exportName: "cluster-api-sweep-workspace",

		workspacesEnv:     "SWEEP_WORKSPACE_WORKSPACES",
		objectsEnv:        "SWEEP_WORKSPACE_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  workspaceReconcilerWatchedTypes,
		eventHandlers: workspaceReconcilerEventHandlers,
		facts: map[string]string{
			"deploymentName":  "workspace-manager",
			"deployment":      "workspace-manager, the fifth deployment: it reconciles permissions rather than Cluster API objects",
			"shape":           "capiworkspaces.AddMaintainerToManager: one fleet-wide controller watching APIBindings",
			"reconciledTypes": "rbac.authorization.k8s.io/clusterroles, written from apis.kcp.io/apibindings",
			"notMeasured":     "the permission-claim controller (one workspace, not per tenant) and the initializer (its fleet is the workspaces still initializing)",
			"objectDimension": "none: the work is a function of the workspace, not of a workload in it",
			"endState":        "the workspace's Cluster API roles written",
		},

		scheme: scheme,
		// None. This export publishes no types — it is the identity the role
		// maintainer acts under, and everything it touches it reaches through
		// a claim. An APIExport with no resources is legal and is exactly what
		// capiexports.Workspaces builds.
		crds: func(*testing.T) []string { return nil },

		// The claims capiexports.Workspaces declares, taken from it rather
		// than restated: a sweep measuring a narrower identity than the
		// deployment has would report a cost nobody pays.
		permissionClaims: capiexports.Workspaces().Claims(nil, nil),

		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		// Discovery by LogicalCluster, as the deployment does it. Without it
		// a workspace never disengages here: this shape claims apibindings,
		// so the provider sees kcp's own bindings in every workspace and the
		// count it disengages on never reaches zero.
		providerOptions: []providerwiring.ProviderOption{
			providerwiring.WithLogicalClusterDiscovery(),
		},

		newFleetSetup: func(t *testing.T, _ context.Context, mgr mcmanager.Manager, _ *rest.Config, _ *capicontrollerutil.WildcardRegistry) {
			t.Helper()
			must(t, capiworkspaces.AddMaintainerToManager(mgr, true))
		},

		// Nothing. See the note on the object count above.
		activate: func(*testing.T, context.Context, *tenant, int) {},

		// A workspace leaves this deployment by being deleted, not by
		// unbinding. The onboarding binding is written by the WorkspaceType
		// with defaultAPIBindingLifecycle: Maintain, so kcp recreates one a
		// tenant deletes - unbinding is not a departure here, it is a blip.
		// The workspace going away is the departure, and it is the one this
		// deployment can observe: its discovery object is the workspace's own
		// LogicalCluster, which kcp deletes with it.
		depart: func(t *testing.T, ctx context.Context, tn *tenant) {
			t.Helper()
			must(t, tn.rootClient.Delete(ctx, &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: tn.workspaceName},
			}))
		},

		// Active means this deployment did its work in this workspace and the
		// result landed here: both roles exist, written by the maintainer and
		// by nothing else.
		active: func(t *testing.T, ctx context.Context, tn *tenant, _ int) bool {
			t.Helper()
			for _, name := range []string{capiworkspaces.AdminRoleName, capiworkspaces.ViewRoleName} {
				var role rbacv1.ClusterRole
				if err := tn.directClient.Get(ctx, client.ObjectKey{Name: name}, &role); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading ClusterRole %s in workspace %s: %v", name, tn.name, err)
				}
			}
			return true
		},

		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, _ int) {
			t.Helper()

			var bindings apisv1alpha2.APIBindingList
			if err := tn.directClient.List(ctx, &bindings); err != nil {
				t.Logf("diagnose: listing APIBindings in %s: %v", tn.name, err)
			} else {
				for i := range bindings.Items {
					b := &bindings.Items[i]
					t.Logf("diagnose: APIBinding %s in %s: phase=%s claims=%d applied=%d",
						b.Name, tn.name, b.Status.Phase,
						len(b.Spec.PermissionClaims), len(b.Status.AppliedPermissionClaims))
				}
			}

			var roles rbacv1.ClusterRoleList
			if err := tn.directClient.List(ctx, &roles); err != nil {
				t.Logf("diagnose: listing ClusterRoles in %s: %v", tn.name, err)
				return
			}
			for i := range roles.Items {
				if roles.Items[i].Labels[capiworkspaces.ManagedByLabel] == capiworkspaces.ManagedByValue {
					t.Logf("diagnose: ClusterRole %s in %s: %d rules", roles.Items[i].Name, tn.name, len(roles.Items[i].Rules))
				}
			}
		},
	})
}
