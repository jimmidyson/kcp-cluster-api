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
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// bound is an APIBinding as the server reports it once it is serving types.
func bound(name string, groups ...string) apisv1alpha2.APIBinding {
	resources := make([]apisv1alpha2.BoundAPIResource, 0, len(groups))
	for _, g := range groups {
		resources = append(resources, apisv1alpha2.BoundAPIResource{Group: g, Resource: "things"})
	}
	return apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     apisv1alpha2.APIBindingStatus{BoundResources: resources},
	}
}

func roleNamed(t *testing.T, roles []*rbacv1.ClusterRole, name string) *rbacv1.ClusterRole {
	t.Helper()
	for _, r := range roles {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("Roles() returned no role named %s", name)
	return nil
}

// The whole point of deriving the roles: a provider the tenant enables later
// widens what they may do, with nobody editing anything.
func TestAPIGroupsFollowWhatIsBound(t *testing.T) {
	core := bound("cluster-api-core", "cluster.x-k8s.io")
	infra := bound("cluster-api-dev-infrastructure", "infrastructure.cluster.x-k8s.io")

	if got := APIGroups([]apisv1alpha2.APIBinding{core}); !slices.Equal(got, []string{"cluster.x-k8s.io"}) {
		t.Errorf("with only core bound, APIGroups() = %v", got)
	}

	want := []string{"cluster.x-k8s.io", "infrastructure.cluster.x-k8s.io"}
	if got := APIGroups([]apisv1alpha2.APIBinding{core, infra}); !slices.Equal(got, want) {
		t.Errorf("after enabling the infrastructure provider, APIGroups() = %v, want %v", got, want)
	}
}

// A workspace binds things that have nothing to do with Cluster API - this
// project's own onboarding export among them - and a role that named their
// groups would be granting the tenant something nobody asked for.
func TestAPIGroupsIgnoreEverythingOutsideClusterAPI(t *testing.T) {
	bindings := []apisv1alpha2.APIBinding{
		bound("cluster-api-workspace"),
		bound("something-else", "storage.example.com", "apis.kcp.io"),
		// A group that merely contains the string is not a Cluster API group:
		// the match is on the suffix, with the separating dot.
		bound("impostor", "cluster.x-k8s.io.evil.example.com", "notcluster.x-k8s.io"),
		bound("cluster-api-core", "cluster.x-k8s.io"),
	}

	want := []string{"cluster.x-k8s.io"}
	if got := APIGroups(bindings); !slices.Equal(got, want) {
		t.Errorf("APIGroups() = %v, want %v", got, want)
	}
}

// status, not spec: a binding that has been accepted but is not yet serving
// anything must not put its group in a role, or the role promises access to a
// type that is not there.
func TestAPIGroupsReadWhatIsServedNotWhatWasAskedFor(t *testing.T) {
	accepted := apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-api-dev-infrastructure"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: "root", Name: "cluster-api-dev-infrastructure"},
			},
		},
	}

	if got := APIGroups([]apisv1alpha2.APIBinding{accepted}); len(got) != 0 {
		t.Errorf("APIGroups() = %v for a binding that is not bound yet, want none", got)
	}
}

// An empty workspace gets roles that grant nothing rather than roles with a
// malformed rule in them. A PolicyRule naming no API group is not narrower, it
// is broken, and RBAC's answer to it is not obvious enough to rely on.
func TestRolesGrantNoClusterAPITypesUntilSomethingIsBound(t *testing.T) {
	for _, role := range Roles(nil) {
		for _, rule := range role.Rules {
			if len(rule.APIGroups) == 0 {
				t.Errorf("%s has a rule naming no API group: %v", role.Name, rule)
			}
			if slices.Contains(rule.Resources, "*") {
				t.Errorf("%s grants %v on every resource with nothing bound", role.Name, rule.Verbs)
			}
		}
	}
}

func TestAdminMayReadEveryBoundClusterAPIGroup(t *testing.T) {
	bindings := []apisv1alpha2.APIBinding{
		bound("cluster-api-core", "cluster.x-k8s.io"),
		bound("cluster-api-dev-infrastructure", "infrastructure.cluster.x-k8s.io"),
	}
	admin := roleNamed(t, Roles(bindings), AdminRoleName)

	rule := admin.Rules[0]
	if !slices.Equal(rule.APIGroups, []string{"cluster.x-k8s.io", "infrastructure.cluster.x-k8s.io"}) {
		t.Errorf("the admin role's first rule covers %v", rule.APIGroups)
	}
	if !slices.Equal(rule.Resources, []string{"*"}) {
		t.Errorf("the admin role reads %v rather than every type its groups publish", rule.Resources)
	}
	if !slices.Equal(rule.Verbs, []string{"get", "list", "watch"}) {
		t.Errorf("the admin role's read rule grants %v", rule.Verbs)
	}
}

// A tenant of a Cluster API workspace writes exactly one object, and this is
// the table that says so against every type the four in-repo exports publish.
//
// A cluster here is built from a ClusterClass: the Cluster names a class and a
// shape, and everything under it is created by the topology controller under
// the manager's identity. A tenant who could write the rest would be holding
// the grant the hand-built model needed against a system that no longer builds
// clusters that way. Scaling and version changes do not reopen it - both are
// fields of spec.topology, so write on clusters already carries them.
func TestAdminRoleWritesNothingButClusters(t *testing.T) {
	// Every type the four provider APIExports publish - see
	// internal/capiexports. A type missing from this table is a type this
	// test says nothing about.
	published := map[string][]string{
		"cluster.x-k8s.io":                {"clusters", "clusterclasses", "machines", "machinesets", "machinedeployments"},
		"bootstrap.cluster.x-k8s.io":      {"kubeadmconfigs", "kubeadmconfigtemplates"},
		"controlplane.cluster.x-k8s.io":   {"kubeadmcontrolplanes", "kubeadmcontrolplanetemplates"},
		"infrastructure.cluster.x-k8s.io": {"devclusters", "devclustertemplates", "devmachines", "devmachinetemplates"},
	}

	// Every one of those groups bound, which is what a workspace running the
	// demo's cluster has. The role is derived from the bindings, so the table
	// only means anything against a workspace that serves the types in it.
	bindings := make([]apisv1alpha2.APIBinding, 0, len(published))
	for group := range published {
		bindings = append(bindings, bound("binding-"+group, group))
	}
	admin := roleNamed(t, Roles(bindings), AdminRoleName)

	for group, resources := range published {
		for _, resource := range resources {
			// Read on everything: an owner watches what their cluster became,
			// which is most of what a tenant does with these types at all.
			for _, verb := range []string{"get", "list", "watch"} {
				if !grants(admin.Rules, group, resource, verb) {
					t.Errorf("the admin role does not grant %s on %s/%s, so an owner cannot watch what their cluster became: %+v",
						verb, group, resource, admin.Rules)
				}
			}

			writable := group == WritableGroup && resource == WritableResource
			for _, verb := range []string{"create", "update", "patch", "delete"} {
				got := grants(admin.Rules, group, resource, verb)
				if got && !writable {
					t.Errorf("the admin role grants %s on %s/%s, which the topology controller writes and a tenant does not: %+v",
						verb, group, resource, admin.Rules)
				}
				if !got && writable {
					t.Errorf("the admin role does not grant %s on %s/%s, so an owner cannot manage their own cluster: %+v",
						verb, group, resource, admin.Rules)
				}
			}
		}
	}
}

// The two writes most likely to be added back, and the reason neither belongs
// to a tenant. Both are covered by the table above; they are named here so
// that the answer is findable by whoever is about to ask the question.
func TestAdminRoleWithholdsTheTemptingWrites(t *testing.T) {
	admin := roleNamed(t, Roles([]apisv1alpha2.APIBinding{bound("core", ClusterAPIGroup)}), AdminRoleName)

	// Writing a ClusterClass is authoring the blueprint rather than using it:
	// it decides what a cluster in this installation is made of. That answer
	// is the platform's, which is why the class and its templates are seeded
	// into the workspace for the tenant and read-only once there.
	for _, verb := range []string{"create", "update", "patch", "delete"} {
		if grants(admin.Rules, ClusterAPIGroup, "clusterclasses", verb) {
			t.Errorf("the admin role grants %s on clusterclasses, so a tenant could author the blueprint: %+v", verb, admin.Rules)
		}
	}

	// Deleting a Machine to force a replacement is a real operation and a real
	// temptation. It is remediation, which is the platform's job here; a
	// tenant changes their cluster through spec.topology, and a Machine
	// deleted underneath the topology controller is a change it did not make.
	if grants(admin.Rules, ClusterAPIGroup, "machines", "delete") {
		t.Errorf("the admin role grants delete on machines, which is remediation rather than a tenant's own change: %+v", admin.Rules)
	}
}

// grants reports whether the rules allow one verb on one resource.
func grants(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	for _, rule := range rules {
		if !slices.Contains(rule.APIGroups, group) && !slices.Contains(rule.APIGroups, "*") {
			continue
		}
		if !slices.Contains(rule.Resources, resource) && !slices.Contains(rule.Resources, "*") {
			continue
		}
		if slices.Contains(rule.Verbs, verb) || slices.Contains(rule.Verbs, "*") {
			return true
		}
	}
	return false
}

// A cluster's admin kubeconfig is a Secret. Seeing that a cluster exists is
// not the same permission as being able to log into it.
func TestViewerCannotReadSecrets(t *testing.T) {
	bindings := []apisv1alpha2.APIBinding{bound("cluster-api-core", "cluster.x-k8s.io")}
	view := roleNamed(t, Roles(bindings), ViewRoleName)

	for _, rule := range view.Rules {
		if slices.Contains(rule.Resources, "secrets") {
			t.Errorf("the view role reads secrets: %v", rule)
		}
	}
}

func TestViewerOnlyReads(t *testing.T) {
	bindings := []apisv1alpha2.APIBinding{bound("cluster-api-core", "cluster.x-k8s.io")}
	view := roleNamed(t, Roles(bindings), ViewRoleName)

	for _, rule := range view.Rules {
		for _, verb := range rule.Verbs {
			if !slices.Contains([]string{"get", "list", "watch"}, verb) {
				t.Errorf("the view role grants %q on %v", verb, rule.Resources)
			}
		}
	}
}

// The roles are compared against what is in the workspace to decide whether to
// write, and the maintainer sees an event for every APIBinding in every
// workspace of the fleet. An unstable result would rewrite both roles in every
// workspace forever.
func TestRolesAreDeterministic(t *testing.T) {
	bindings := []apisv1alpha2.APIBinding{
		bound("b", "infrastructure.cluster.x-k8s.io", "cluster.x-k8s.io"),
		bound("a", "bootstrap.cluster.x-k8s.io"),
	}

	first := Roles(bindings)
	for range 5 {
		next := Roles(bindings)
		if len(first) != len(next) {
			t.Fatalf("Roles() returned %d roles, then %d", len(first), len(next))
		}
		for i := range first {
			if first[i].Name != next[i].Name {
				t.Fatalf("Roles() returned %s then %s in position %d", first[i].Name, next[i].Name, i)
			}
			if !slices.EqualFunc(first[i].Rules, next[i].Rules, sameRule) {
				t.Fatalf("%s's rules are not deterministic:\n%v\n%v", first[i].Name, first[i].Rules, next[i].Rules)
			}
		}
	}
}

// Every role this project writes has to be recognisable as one it writes: the
// reconciler refuses to overwrite a role that is not marked, and a role that
// forgot the mark could never be updated again.
func TestEveryRoleIsMarkedAsManaged(t *testing.T) {
	for _, role := range Roles(nil) {
		if role.Labels[ManagedByLabel] != ManagedByValue {
			t.Errorf("%s is not labelled %s=%s", role.Name, ManagedByLabel, ManagedByValue)
		}
	}
}

// A tenant enables a provider by creating an APIBinding, and kcp authorizes
// that as the verb "bind" on the APIExport, evaluated in the workspace the
// export lives in. Without this grant, "the tenant turns on the provider they
// want" is a description of what an administrator does for them.
func TestProviderBinderRoleLetsSomebodyEnableNamedProviders(t *testing.T) {
	role := NewProviderBinderRole([]string{"cluster-api-dev-infrastructure"})

	if len(role.Rules) != 1 {
		t.Fatalf("the provider binder role has %d rules, want the one", len(role.Rules))
	}
	rule := role.Rules[0]

	if !slices.Contains(rule.Verbs, "bind") || !slices.Contains(rule.Resources, "apiexports") {
		t.Errorf("the provider binder role does not grant bind on apiexports: %+v", rule)
	}
	// Named exports only. That workspace holds every export in the
	// installation, and a tenant does not get to bind all of them.
	if !slices.Equal(rule.ResourceNames, []string{"cluster-api-dev-infrastructure"}) {
		t.Errorf("the provider binder role names %v, want only the export it was asked for", rule.ResourceNames)
	}
	// Binding is the whole grant. It is not a licence to read the exports.
	if slices.Contains(rule.Verbs, "get") || slices.Contains(rule.Verbs, "list") {
		t.Errorf("the provider binder role grants reads on apiexports: %v", rule.Verbs)
	}
	if role.Labels[ManagedByLabel] != ManagedByValue {
		t.Errorf("the provider binder role is not labelled %s=%s", ManagedByLabel, ManagedByValue)
	}
}

// Being in a workspace is kcp's own check, decided before RBAC on any resource
// is reached, and its role is bound rather than reproduced. A grant that
// forgot it produces "access denied" on a type the workspace plainly serves.
func TestGrantRolesAlwaysBindsWorkspaceAccess(t *testing.T) {
	ctx := context.Background()
	cl := workspaceClient(t)

	if err := GrantRoles(ctx, cl, "alice", AdminRoleName); err != nil {
		t.Fatalf("GrantRoles() = %v", err)
	}

	bindings := &rbacv1.ClusterRoleBindingList{}
	if err := cl.List(ctx, bindings); err != nil {
		t.Fatalf("listing ClusterRoleBindings: %v", err)
	}
	granted := map[string]bool{}
	for i := range bindings.Items {
		granted[bindings.Items[i].RoleRef.Name] = true
	}
	for _, want := range []string{WorkspaceAccessRoleName, AdminRoleName} {
		if !granted[want] {
			t.Errorf("GrantRoles() did not bind %s; it bound %v", want, granted)
		}
	}

	// Idempotent: a demo or an operator tool re-runs against a workspace it
	// already set up.
	if err := GrantRoles(ctx, cl, "alice", AdminRoleName); err != nil {
		t.Errorf("a repeated GrantRoles() = %v", err)
	}
}
