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
	"reflect"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

// WorkspaceTypeName is the type a tenant creates a Cluster API workspace with.
//
//	kubectl create -f - <<'YAML'
//	apiVersion: tenancy.kcp.io/v1alpha1
//	kind: Workspace
//	metadata:
//	  name: my-clusters
//	spec:
//	  type:
//	    name: cluster-api
//	    path: root
//	YAML
//
// That is the whole of onboarding. What it buys is in NewWorkspaceType.
const WorkspaceTypeName tenancyv1alpha1.WorkspaceTypeName = "cluster-api"

// UniversalType is kcp's own type, which a Cluster API workspace extends so
// that it remains an ordinary workspace: it can hold children, it can live
// wherever a universal workspace can, and it keeps whatever kcp's own
// bootstrap grants a universal workspace.
var UniversalType = tenancyv1alpha1.WorkspaceTypeReference{
	Name: tenancyv1alpha1.WorkspaceTypeName("universal"),
	Path: "root",
}

// TypeReference is how a Workspace names this type, given the path of the
// workspace the type was created in.
func TypeReference(providerPath string) tenancyv1alpha1.WorkspaceTypeReference {
	return tenancyv1alpha1.WorkspaceTypeReference{Name: WorkspaceTypeName, Path: providerPath}
}

// DefaultExports are the APIExports every Cluster API workspace binds, in the
// order they are bound.
//
// The onboarding export is first, and the order is load-bearing rather than
// tidy. kcp labels an object with the permission claims the workspace has
// accepted at the moment the object is written, and it is the onboarding
// export's binding that accepts the claim on APIBindings. Bind it second and
// the core binding created just before it carries no claim label, so the
// controller that maintains this workspace's roles cannot see the very binding
// that tells it Cluster API is here - until something next rewrites it.
//
// The provider exports are deliberately not in this list. Which infrastructure
// provider a workspace uses is the tenant's decision, made by creating an
// APIBinding of their own, and a WorkspaceType that bound one for them would
// be making it for them.
func DefaultExports() []string {
	return []string{capiexports.WorkspaceExport, capiexports.CoreExport}
}

// NewWorkspaceType builds the WorkspaceType a tenant onboards to Cluster API
// with. providerPath is the workspace holding the APIExports.
//
// Three things are being asked of kcp here, and each replaces a manual step:
//
//   - defaultAPIBindings binds Cluster API's core APIExport in every workspace
//     of this type, so a tenant never writes an APIBinding to get `Cluster`.
//
//   - defaultAPIBindingLifecycle: Maintain makes that binding *keep* accepting
//     whatever core's export claims, not only what it claimed on the day the
//     workspace was made. kcp rebuilds the binding's accepted-claim list from
//     the export's on every export update, so a provider onboarded next month
//     becomes reachable by core's controllers in every existing workspace with
//     nobody accepting anything. This is the half of ADR-0001's decision 3
//     that needs no code from this project at all.
//
//   - initializer makes the workspace wait. A workspace of this type is not
//     Ready until this project's initializer has written its roles, so a
//     tenant is never handed a workspace that serves Cluster API and grants
//     nobody the use of it.
//
// The trade-off Maintain carries is real and is documented for tenants rather
// than discovered by them: a hand-edit to the managed binding's
// spec.permissionClaims is reverted on the next reconcile, and a tenant who
// wants to refuse one of core's claims has to leave the managed binding behind
// and maintain their own.
func NewWorkspaceType(providerPath string, exports []string) *tenancyv1alpha1.WorkspaceType {
	bindings := make([]tenancyv1alpha1.APIExportReference, 0, len(exports))
	for _, export := range exports {
		bindings = append(bindings, tenancyv1alpha1.APIExportReference{
			Path:   providerPath,
			Export: export,
		})
	}

	return &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{Name: string(WorkspaceTypeName)},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			Initializer:                true,
			Extend:                     tenancyv1alpha1.WorkspaceTypeExtension{With: []tenancyv1alpha1.WorkspaceTypeReference{UniversalType}},
			DefaultAPIBindings:         bindings,
			DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
			// What the initializer may do inside a workspace it is
			// initializing. Left unset, kcp's content proxy impersonates the
			// workspace owner, which is cluster-admin - far more than writing
			// two ClusterRoles needs, and a privilege worth not holding across
			// every tenant workspace in the installation.
			//
			// The LogicalCluster rules are how an initializer finishes: it
			// removes its own name from status.initializers, and a workspace
			// whose initializer cannot do that never becomes Ready.
			InitializerPermissions: initializerPermissions(),
		},
	}
}

// initializerPermissions is the least the initializer can do its job with.
func initializerPermissions() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		// Discovery. A typed client asks the workspace what it serves before
		// it reads anything, and the content proxy evaluates these rules for
		// non-resource URLs as well as for objects: without this every request
		// fails with `no rule allows get on /api` before RBAC on the object is
		// ever consulted, which reads like a missing type rather than a
		// missing permission.
		{
			NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/version", "/openapi", "/openapi/*"},
			Verbs:           []string{"get"},
		},
		{
			APIGroups: []string{"rbac.authorization.k8s.io"},
			Resources: []string{"clusterroles"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch"},
		},
		{
			APIGroups: []string{"apis.kcp.io"},
			Resources: []string{"apibindings"},
			// patch as well as read: an APIBinding whose permission claims kcp
			// has stopped trying to apply is poked into being retried. See
			// NudgeUnappliedClaims for the race that makes that necessary.
			Verbs: []string{"get", "list", "watch", "patch"},
		},
		{
			APIGroups: []string{"core.kcp.io"},
			Resources: []string{"logicalclusters"},
			Verbs:     []string{"get", "list", "watch", "update", "patch"},
		},
		{
			APIGroups: []string{"core.kcp.io"},
			Resources: []string{"logicalclusters/status"},
			Verbs:     []string{"update", "patch"},
		},
	}
}

// EnsureWorkspaceType creates or updates the type in the workspace cl is
// scoped to and waits for kcp to give it a virtual workspace URL.
//
// The wait is not decoration: the URL is what the initializer controller
// connects to, and it appears only once kcp's WorkspaceType controller has
// seen a shard. A process that started its own kcp server moments earlier will
// otherwise fail on a condition that resolves itself in seconds.
func EnsureWorkspaceType(ctx context.Context, cl client.Client, want *tenancyv1alpha1.WorkspaceType, timeout time.Duration) error {
	if timeout == 0 {
		timeout = time.Minute
	}

	if err := cl.Create(ctx, want.DeepCopy()); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating WorkspaceType %s: %w", want.Name, err)
		}
		got := &tenancyv1alpha1.WorkspaceType{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil {
			return fmt.Errorf("reading existing WorkspaceType %s: %w", want.Name, err)
		}
		if !reflect.DeepEqual(got.Spec, want.Spec) {
			// Brought to the requested shape rather than left alone. A type
			// that exists but binds fewer exports than asked for is not the
			// type that was asked for, and the difference shows up much later
			// as a workspace missing an API nobody can explain.
			got.Spec = want.Spec
			if err := cl.Update(ctx, got); err != nil {
				return fmt.Errorf("updating WorkspaceType %s: %w", want.Name, err)
			}
		}
	}

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		got := &tenancyv1alpha1.WorkspaceType{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(want), got); err != nil {
			return false, nil //nolint:nilerr // transient; keep polling until timeout.
		}
		for _, vw := range got.Status.VirtualWorkspaces {
			if vw.Type == tenancyv1alpha1.VirtualWorkspaceTypeInitializing && vw.URL != "" {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("WorkspaceType %s never got an initializing virtual workspace URL: %w", want.Name, err)
	}
	return nil
}
