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

package demo

import (
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/rest"
)

func TestPlanWorkspacesWithoutUsers(t *testing.T) {
	plans := PlanWorkspaces("root", "capi-demo", nil, 2)

	want := []WorkspacePlan{
		{Name: "capi-demo-1", Parent: "root", Path: "root:capi-demo-1"},
		{Name: "capi-demo-2", Parent: "root", Path: "root:capi-demo-2"},
	}
	if !slices.Equal(plans, want) {
		t.Errorf("PlanWorkspaces(no users) = %+v, want %+v", plans, want)
	}
}

// A user's workspaces live under that user's own home, and the nth workspace
// of every user has the same name. Identical names across users are the point:
// they are what makes a leak visible rather than plausible, exactly as
// identical Cluster names are within a workspace.
func TestPlanWorkspacesGivesEachUserTheSameNames(t *testing.T) {
	plans := PlanWorkspaces("root", "capi-demo", []string{"alice", "bob"}, 4)

	want := []WorkspacePlan{
		{Name: "capi-demo-1", Parent: "root:capi-demo:alice", Path: "root:capi-demo:alice:capi-demo-1", Owner: "alice"},
		{Name: "capi-demo-1", Parent: "root:capi-demo:bob", Path: "root:capi-demo:bob:capi-demo-1", Owner: "bob"},
		{Name: "capi-demo-2", Parent: "root:capi-demo:alice", Path: "root:capi-demo:alice:capi-demo-2", Owner: "alice"},
		{Name: "capi-demo-2", Parent: "root:capi-demo:bob", Path: "root:capi-demo:bob:capi-demo-2", Owner: "bob"},
	}
	if !slices.Equal(plans, want) {
		t.Errorf("PlanWorkspaces(2 users, 4 workspaces) = %+v, want %+v", plans, want)
	}
}

// Round-robin rather than contiguous blocks, so an odd count leaves the extra
// workspace with the first user rather than leaving the last user with none.
func TestPlanWorkspacesRoundRobinsTheRemainder(t *testing.T) {
	plans := PlanWorkspaces("root", "capi-demo", []string{"alice", "bob"}, 3)

	owners := make([]string, 0, len(plans))
	for _, p := range plans {
		owners = append(owners, p.Owner)
	}
	if want := []string{"alice", "bob", "alice"}; !slices.Equal(owners, want) {
		t.Errorf("owners = %v, want %v", owners, want)
	}
	if got := plans[2].Name; got != "capi-demo-2" {
		t.Errorf("alice's second workspace is named %q, want capi-demo-2", got)
	}
}

func TestOrgAndHomePaths(t *testing.T) {
	if got := OrgPath("root", "capi-demo"); got != "root:capi-demo" {
		t.Errorf("OrgPath = %q, want root:capi-demo", got)
	}
	if got := HomePath("root", "capi-demo", "alice"); got != "root:capi-demo:alice" {
		t.Errorf("HomePath = %q, want root:capi-demo:alice", got)
	}
}

// The home role is what lets a user list their own workspaces, and the reason
// they can list nobody else's is that nothing grants it anywhere else.
func TestHomeRoleGrantsWorkspaceReads(t *testing.T) {
	role := NewHomeRole()

	if !grants(role.Rules, "tenancy.kcp.io", "workspaces", "list") {
		t.Errorf("home role does not grant list on workspaces: %+v", role.Rules)
	}
	// A tenant reading their own workspaces is the whole grant. Creating them
	// is the demo's job, and a role that could do it would be a wider claim
	// than anything here demonstrates.
	if grants(role.Rules, "tenancy.kcp.io", "workspaces", "create") {
		t.Errorf("home role grants create on workspaces, want read-only: %+v", role.Rules)
	}
}

func TestWorkspaceRoleGrantsClusterAPIButNotTenancy(t *testing.T) {
	role := NewWorkspaceRole()

	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		if !grants(role.Rules, "cluster.x-k8s.io", "clusters", verb) {
			t.Errorf("workspace role does not grant %s on clusters: %+v", verb, role.Rules)
		}
	}
	if !grants(role.Rules, "", "secrets", "get") {
		t.Errorf("workspace role does not grant get on secrets, so the owner cannot reach their kubeconfig: %+v", role.Rules)
	}
	// A cluster workspace is a leaf. Nothing a tenant does there involves
	// creating more of them.
	if grants(role.Rules, "tenancy.kcp.io", "workspaces", "list") {
		t.Errorf("workspace role grants workspace reads, which belongs to the home: %+v", role.Rules)
	}
}

// Entry into a workspace is kcp's built-in role, bound rather than reproduced.
// Writing the rule out - verb "access" on the non-resource URL "/" - would work
// today and would be a copy of kcp's own policy, drifting silently the day kcp
// changes it. The demo's roles must therefore not carry that rule, and the
// binding must name kcp's role.
func TestWorkspaceAccessIsKcpsOwnRole(t *testing.T) {
	if got := WorkspaceAccessRoleName; got != "system:kcp:workspace:access" {
		t.Errorf("WorkspaceAccessRoleName = %q, want kcp's own role name", got)
	}
	for name, role := range map[string]*rbacv1.ClusterRole{
		"home":      NewHomeRole(),
		"workspace": NewWorkspaceRole(),
	} {
		if hasAccessRule(role.Rules) {
			t.Errorf("%s role writes kcp's workspace-access rule out instead of binding %s: %+v",
				name, WorkspaceAccessRoleName, role.Rules)
		}
	}
	if got := NewOwnerBinding(WorkspaceAccessRoleName, "alice").RoleRef.Name; got != WorkspaceAccessRoleName {
		t.Errorf("access binding roleRef = %q, want %q", got, WorkspaceAccessRoleName)
	}
}

func TestNewOwnerBindingNamesTheUser(t *testing.T) {
	binding := NewOwnerBinding(WorkspaceRoleName, "alice")

	if binding.RoleRef.Name != WorkspaceRoleName {
		t.Errorf("binding roleRef = %q, want %q", binding.RoleRef.Name, WorkspaceRoleName)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "User" || binding.Subjects[0].Name != "alice" {
		t.Errorf("binding subjects = %+v, want the single user alice", binding.Subjects)
	}
	if !strings.Contains(binding.Name, "alice") {
		t.Errorf("binding name %q does not name the user it binds", binding.Name)
	}
}

// The demo authenticates as the kcp admin and asks the server to evaluate the
// request as the tenant. What must not happen is that the admin's own
// credentials leak into the client as an identity as well as a carrier: kcp
// authorizes the impersonated user, so the config has to say who that is and
// nothing else.
func TestConfigForUserImpersonatesAndScopes(t *testing.T) {
	base := &rest.Config{Host: "https://localhost:6443", BearerToken: "admin-token"}

	cfg := ConfigForUser(base, "alice", "root:capi-demo:alice")

	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("Impersonate.UserName = %q, want alice", cfg.Impersonate.UserName)
	}
	if !strings.HasSuffix(cfg.Host, "/clusters/root:capi-demo:alice") {
		t.Errorf("Host = %q, want it scoped to root:capi-demo:alice", cfg.Host)
	}
	if base.Impersonate.UserName != "" || base.Host != "https://localhost:6443" {
		t.Errorf("ConfigForUser mutated the base config: %+v", base)
	}
}

func TestAccessCheckExpectation(t *testing.T) {
	for _, tc := range []struct {
		check AccessCheck
		want  bool
	}{
		{AccessCheck{User: "alice", Owner: "alice"}, true},
		{AccessCheck{User: "alice", Owner: "bob"}, false},
		// The org workspace holds every tenant's home and belongs to none of
		// them, so no tenant is expected to read it.
		{AccessCheck{User: "alice", Owner: OwnerNobody}, false},
		// The parent workspace is kcp's: it lists its direct children to any
		// authenticated user.
		{AccessCheck{User: "alice", Owner: OwnerEveryone}, true},
	} {
		if got := tc.check.Expected(); got != tc.want {
			t.Errorf("AccessCheck{User:%q, Owner:%q}.Expected() = %v, want %v",
				tc.check.User, tc.check.Owner, got, tc.want)
		}
	}
}

// The parent workspace row says what a tenant can see from the top of the
// tree. That is the shard's policy rather than this demo's, so a shard that
// has narrowed it must not be reported as a demo that leaked or broke.
func TestParentWorkspaceCheckIsReportedNotAsserted(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		check := AccessCheck{User: "alice", Owner: OwnerEveryone, Allowed: allowed}
		if !check.Reported() {
			t.Errorf("AccessCheck{Owner:%q}.Reported() = false", OwnerEveryone)
		}
		if !check.AsIntended() {
			t.Errorf("AccessCheck{Owner:%q, Allowed:%v}.AsIntended() = false, want true either way",
				OwnerEveryone, allowed)
		}
	}
	tenantOwned := AccessCheck{User: "alice", Owner: "bob"}
	if tenantOwned.Reported() {
		t.Error("a tenant-owned check reports as Reported(), so a leak would not fail the run")
	}
}

func TestIsolated(t *testing.T) {
	allowedOwn := AccessCheck{User: "alice", Owner: "alice", Allowed: true}
	deniedOther := AccessCheck{User: "alice", Owner: "bob", Allowed: false}
	leaked := AccessCheck{User: "alice", Owner: "bob", Allowed: true}
	lockedOut := AccessCheck{User: "bob", Owner: "bob", Allowed: false}

	if !Isolated([]AccessCheck{allowedOwn, deniedOther}) {
		t.Error("Isolated() = false for checks that all went as intended")
	}
	if Isolated([]AccessCheck{allowedOwn, leaked}) {
		t.Error("Isolated() = true even though one user read another's workspace")
	}
	if Isolated([]AccessCheck{allowedOwn, lockedOut}) {
		t.Error("Isolated() = true even though a user could not read their own workspace")
	}
	// A run that checked nothing proved nothing, which is the one answer a
	// tenancy assertion must not give.
	if Isolated(nil) {
		t.Error("Isolated(nil) = true, want false: no checks is not isolation")
	}
}

func TestRenderAccessTable(t *testing.T) {
	var sb strings.Builder
	err := RenderAccessTable(&sb, []AccessCheck{
		{User: "alice", Workspace: "root:capi-demo:alice", Owner: "alice", Resource: "workspaces", Allowed: true, Detail: "1 workspace"},
		{User: "alice", Workspace: "root:capi-demo:bob", Owner: "bob", Resource: "workspaces", Allowed: false, Detail: "forbidden"},
	})
	if err != nil {
		t.Fatalf("RenderAccessTable: %v", err)
	}

	out := sb.String()
	for _, want := range []string{"USER", "WORKSPACE", "OWNER", "RESOURCE", "ALLOWED", "alice", "root:capi-demo:bob", "forbidden"} {
		if !strings.Contains(out, want) {
			t.Errorf("access table does not mention %q:\n%s", want, out)
		}
	}
}

// A table nobody reads carefully is the failure mode a tenancy report has, so
// a check that did not go as intended says which way round it went rather than
// leaving the reader to compare two columns.
func TestRenderAccessTableCallsOutBothFailures(t *testing.T) {
	var sb strings.Builder
	err := RenderAccessTable(&sb, []AccessCheck{
		{User: "alice", Workspace: "root:capi-demo:bob", Owner: "bob", Resource: "workspaces", Allowed: true, Detail: "1 workspace: capi-demo-1"},
		{User: "bob", Workspace: "root:capi-demo:bob", Owner: "bob", Resource: "workspaces", Allowed: false, Detail: "forbidden"},
	})
	if err != nil {
		t.Fatalf("RenderAccessTable: %v", err)
	}

	out := sb.String()
	if !strings.Contains(out, "LEAK:") {
		t.Errorf("a user reading another's workspaces is not called out:\n%s", out)
	}
	if !strings.Contains(out, "LOCKED OUT:") {
		t.Errorf("a user refused their own workspaces is not called out:\n%s", out)
	}
}

func TestValidateUsers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		users   []string
		wantErr bool
	}{
		{"none", nil, false},
		{"two", []string{"alice", "bob"}, false},
		{"duplicate", []string{"alice", "alice"}, true},
		{"empty name", []string{"alice", ""}, true},
		// The home workspace is named after the user, so a name kcp cannot
		// name a workspace fails here rather than halfway through a run.
		{"not a workspace name", []string{"Alice"}, true},
		// The access table spells two owners that are not users. A tenant
		// called one of them would make a row that reads as a fact about kcp.
		{"reserved owner", []string{OwnerEveryone}, true},
		{"reserved owner nobody", []string{"alice", OwnerNobody}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateUsers(tc.users); (err != nil) != tc.wantErr {
				t.Errorf("validateUsers(%v) = %v, wantErr %v", tc.users, err, tc.wantErr)
			}
		})
	}
}

// hasAccessRule reports whether the rules carry kcp's workspace-content
// grant: verb "access" on the non-resource URL "/".
func hasAccessRule(rules []rbacv1.PolicyRule) bool {
	for _, rule := range rules {
		if slices.Contains(rule.NonResourceURLs, "/") && slices.Contains(rule.Verbs, "access") {
			return true
		}
	}
	return false
}

// grants reports whether the rules allow verb on group/resource.
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
