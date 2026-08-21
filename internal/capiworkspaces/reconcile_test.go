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
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func workspaceClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	if err := apisv1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func readRole(t *testing.T, cl client.Client, name string) *rbacv1.ClusterRole {
	t.Helper()
	role := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: name}, role); err != nil {
		t.Fatalf("reading ClusterRole %s: %v", name, err)
	}
	return role
}

// The sequence the demo walks: a workspace onboards with core bound, and its
// tenant enables an infrastructure provider afterwards. Nobody edits a role at
// any point.
func TestReconcileRolesFollowsAProviderBeingEnabled(t *testing.T) {
	ctx := context.Background()
	core := bound("cluster-api-core", "cluster.x-k8s.io")
	cl := workspaceClient(t, &core)

	state, err := ReconcileRoles(ctx, cl)
	if err != nil {
		t.Fatalf("ReconcileRoles() = %v", err)
	}
	if !slices.Equal(state.Written, []string{AdminRoleName, ViewRoleName}) {
		t.Fatalf("the first reconcile wrote %v, want both roles", state.Written)
	}
	if groups := readRole(t, cl, AdminRoleName).Rules[0].APIGroups; !slices.Equal(groups, []string{"cluster.x-k8s.io"}) {
		t.Fatalf("the admin role covers %v before a provider is enabled", groups)
	}

	// The tenant enables the infrastructure provider.
	infra := bound("cluster-api-dev-infrastructure", "infrastructure.cluster.x-k8s.io")
	if err := cl.Create(ctx, &infra); err != nil {
		t.Fatalf("creating the provider APIBinding: %v", err)
	}

	state, err = ReconcileRoles(ctx, cl)
	if err != nil {
		t.Fatalf("ReconcileRoles() = %v", err)
	}
	if !slices.Equal(state.Written, []string{AdminRoleName, ViewRoleName}) {
		t.Fatalf("enabling a provider rewrote %v, want both roles", state.Written)
	}
	want := []string{"cluster.x-k8s.io", "infrastructure.cluster.x-k8s.io"}
	if groups := readRole(t, cl, AdminRoleName).Rules[0].APIGroups; !slices.Equal(groups, want) {
		t.Errorf("after enabling the provider the admin role covers %v, want %v", groups, want)
	}
	if groups := readRole(t, cl, ViewRoleName).Rules[0].APIGroups; !slices.Equal(groups, want) {
		t.Errorf("after enabling the provider the view role covers %v, want %v", groups, want)
	}
}

// The maintainer sees an event for every APIBinding in every workspace of the
// fleet. A reconcile that rewrote the roles each time would be a write per
// event per workspace, for nothing.
func TestReconcileRolesWritesNothingWhenNothingChanged(t *testing.T) {
	ctx := context.Background()
	core := bound("cluster-api-core", "cluster.x-k8s.io")
	cl := workspaceClient(t, &core)

	if _, err := ReconcileRoles(ctx, cl); err != nil {
		t.Fatalf("ReconcileRoles() = %v", err)
	}
	for range 3 {
		state, err := ReconcileRoles(ctx, cl)
		if err != nil {
			t.Fatalf("ReconcileRoles() = %v", err)
		}
		if len(state.Written) != 0 {
			t.Errorf("a repeated reconcile rewrote %v", state.Written)
		}
	}
}

// A name collision is somebody's deliberate role about to be replaced by a
// generated one. Refusing is the only answer that does not lose their work
// silently.
func TestReconcileRolesRefusesToOverwriteARoleItDoesNotManage(t *testing.T) {
	ctx := context.Background()
	core := bound("cluster-api-core", "cluster.x-k8s.io")
	theirs := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: AdminRoleName},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"}}},
	}
	cl := workspaceClient(t, &core, theirs)

	_, err := ReconcileRoles(ctx, cl)
	if err == nil {
		t.Fatal("ReconcileRoles() overwrote a role it does not manage")
	}
	if !strings.Contains(err.Error(), AdminRoleName) {
		t.Errorf("ReconcileRoles() = %v, want an error naming %s", err, AdminRoleName)
	}
	if got := readRole(t, cl, AdminRoleName); !slices.Equal(got.Rules[0].Resources, []string{"configmaps"}) {
		t.Errorf("the unmanaged role was changed to %v", got.Rules)
	}
}

// The roles are what a tenant is granted, so a workspace that binds nothing
// gets roles that grant nothing - not no roles at all. Something has to exist
// for the workspace's owner to bind.
func TestReconcileRolesWritesRolesForAWorkspaceWithNothingBound(t *testing.T) {
	ctx := context.Background()
	cl := workspaceClient(t)

	state, err := ReconcileRoles(ctx, cl)
	if err != nil {
		t.Fatalf("ReconcileRoles() = %v", err)
	}
	if !slices.Equal(state.Written, []string{AdminRoleName, ViewRoleName}) {
		t.Fatalf("ReconcileRoles() wrote %v, want both roles", state.Written)
	}
	if len(state.Groups) != 0 {
		t.Errorf("ReconcileRoles() reported groups %v for a workspace with nothing bound", state.Groups)
	}
}
