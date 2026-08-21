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

// Package capiworkspaces describes a Cluster API workspace: the WorkspaceType
// a tenant onboards with, and the roles that say what may be done inside one.
//
// # Why a workspace needs anything beyond an APIBinding
//
// Binding an APIExport makes a type *served* in a workspace. It says nothing
// about who may use it: that is ordinary RBAC, evaluated inside the workspace,
// and a workspace that serves `Cluster` but grants nobody anything is a
// workspace where nobody can create one. Somebody has to write those roles,
// and "somebody" was this project's demo, by hand, once per workspace.
//
// # Why they cannot be written once and left
//
// The interesting rule in a Cluster API role is the list of API groups it
// covers, and that list is not knowable when the workspace is created. A
// tenant enables an infrastructure provider by binding its APIExport, which
// may happen a week later and may be a provider nobody here has heard of. A
// role written at creation time either has to guess - naming groups that are
// not served - or has to be edited by hand every time a provider is enabled,
// which is the manual step this package exists to remove.
//
// So the roles are *derived* from the APIBindings a workspace holds, by a
// function with no server in it (Roles), and applied by two callers that
// differ only in when they run: the WorkspaceType's initializer, once, before
// the workspace is ready, and a fleet-wide controller, whenever a binding
// appears or changes.
package capiworkspaces

import (
	"context"
	"fmt"
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

// Role names. Two, because the two answer different questions: who runs the
// clusters in this workspace, and who may look.
//
// Named for what they grant rather than for who holds them - Kubernetes'
// `admin`/`edit`/`view` convention - because a role is bound to whoever the
// workspace's owner decides, and a name like "owner" would be wrong the first
// time it is bound to a team.
const (
	// AdminRoleName is full use of every Cluster API type the workspace
	// serves, and the right to enable another provider.
	AdminRoleName = "cluster-api-admin"

	// ViewRoleName is read-only over the same set, minus Secrets: a cluster's
	// admin kubeconfig is a Secret, and being able to see that a cluster
	// exists is not being able to log into it.
	ViewRoleName = "cluster-api-view"

	// WorkspaceAccessRoleName is kcp's own ClusterRole for the right to be in
	// a workspace at all. It is bound rather than reproduced: kcp bootstraps
	// it into the local admin logical cluster and merges that cluster's roles
	// when it resolves the check, so a binding in any workspace resolves it.
	//
	// The check it satisfies is kcp's rather than Kubernetes': before RBAC on
	// the resource is consulted, the workspace content authorizer asks for the
	// verb "access" on the non-resource URL "/". Granting only the roles below
	// is the mistake that produces "access denied" on a type the workspace
	// plainly serves.
	WorkspaceAccessRoleName = "system:kcp:workspace:access"
)

// ManagedByLabel marks a role this project maintains, and so a role it may
// rewrite. A role without it is somebody else's, whatever it is called.
const (
	ManagedByLabel = "cluster.x-k8s.io/managed-by"
	ManagedByValue = capiexports.WorkspaceExport
)

// ClusterAPIGroup is the API group Cluster API's own types live in, and the
// suffix every provider's group carries.
//
// This is how a bound APIExport is recognised as a Cluster API one without
// resolving it back to the export it came from - which a controller inside a
// tenant workspace cannot do, because the export lives in a workspace it
// cannot read. Every provider in the Cluster API ecosystem serves its types in
// `<contract>.cluster.x-k8s.io`: it is what the contract's own group naming
// says, and what `clusterctl` relies on to find them.
//
// The cost of the shortcut is stated rather than hidden: a provider that
// published its types in some other group would be bound, served, and left out
// of these roles. The failure is visible at the first `kubectl get` and is
// fixed by a role of the tenant's own, not by a silent widening here.
const ClusterAPIGroup = "cluster.x-k8s.io"

// WritableResource is the one Cluster API type a tenant writes, and the group
// it lives in.
//
// # Why exactly one
//
// A cluster here is a ClusterClass based cluster. The Cluster names a class and
// a shape; the infrastructure cluster, the control plane, the worker
// MachineDeployment and the templates each is stamped from are created by the
// topology controller under the *manager's* identity, never the tenant's. A
// tenant who could write them would be holding the grant the hand-built model
// needed against a system that no longer builds clusters that way. Scaling and
// version changes do not reopen it: both are fields of spec.topology, which
// write on clusters already carries.
//
// The ClusterClass is the other half, and is deliberately read-only. Writing a
// class decides what a cluster in this installation is made of, which is the
// platform's answer rather than a tenant's - and because a tenant cannot
// create one either, the only class spec.topology.classRef can name is one
// they were given.
//
// Deleting a Machine to force a replacement is deliberately absent too. It is
// a real operation and a real temptation, but it is remediation, and a Machine
// deleted underneath the topology controller is a change it did not make.
const (
	WritableGroup    = ClusterAPIGroup
	WritableResource = "clusters"
)

// Verb sets, named for what they let a subject do.
var (
	read  = []string{"get", "list", "watch"}
	write = []string{"create", "update", "patch", "delete"}
	use   = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
)

// APIGroups returns the Cluster API groups the given APIBindings serve,
// sorted and deduplicated.
//
// It reads status rather than spec, and that is the point: `status.boundResources`
// is what the workspace actually serves, so a binding that is accepted but not
// yet bound contributes nothing and the role does not promise access to a type
// that is not there yet.
func APIGroups(bindings []apisv1alpha2.APIBinding) []string {
	groups := make([]string, 0, len(bindings))
	for i := range bindings {
		for _, bound := range bindings[i].Status.BoundResources {
			if bound.Group != ClusterAPIGroup && !strings.HasSuffix(bound.Group, "."+ClusterAPIGroup) {
				continue
			}
			if !slices.Contains(groups, bound.Group) {
				groups = append(groups, bound.Group)
			}
		}
	}
	slices.Sort(groups)
	return groups
}

// Roles returns the ClusterRoles a Cluster API workspace holding these
// APIBindings should have, in a fixed order.
//
// A pure function of the bindings, so that what a workspace grants can be
// asserted without a server and so that the initializer and the maintaining
// controller cannot disagree about it.
func Roles(bindings []apisv1alpha2.APIBinding) []*rbacv1.ClusterRole {
	groups := APIGroups(bindings)

	admin := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: AdminRoleName, Labels: managedBy()},
		Rules: []rbacv1.PolicyRule{
			// Namespaces because a cluster is created in one; Secrets because
			// a cluster's admin kubeconfig is one and reading it is the point
			// of having built the cluster; ConfigMaps and Events because
			// that is where a provider says what went wrong.
			//
			// Read-only, all four. A tenant of this workspace does not write
			// the Secrets - the control plane provider does - and a tenant who
			// could would be able to forge a cluster's certificate authority.
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces", "secrets", "configmaps", "events"},
				Verbs:     read,
			},
			// Enabling a provider is creating an APIBinding, so an admin of
			// this workspace can do it without an operator's help. This is the
			// grant that makes "the tenant turns on the provider they want" a
			// true sentence rather than a description of what an administrator
			// does on their behalf.
			{
				APIGroups: []string{apisv1alpha2.SchemeGroupVersion.Group},
				Resources: []string{"apibindings"},
				Verbs:     use,
			},
			{
				APIGroups: []string{"core.kcp.io"},
				Resources: []string{"logicalclusters"},
				Verbs:     read,
			},
		},
	}

	view := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: ViewRoleName, Labels: managedBy()},
		Rules: []rbacv1.PolicyRule{
			// No Secrets. Everything else the admin role reads.
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces", "configmaps", "events"},
				Verbs:     read,
			},
			{
				APIGroups: []string{apisv1alpha2.SchemeGroupVersion.Group},
				Resources: []string{"apibindings"},
				Verbs:     read,
			},
			{
				APIGroups: []string{"core.kcp.io"},
				Resources: []string{"logicalclusters"},
				Verbs:     read,
			},
		},
	}

	// The derived rules, and the only ones that move. Prepended rather than
	// appended so that the thing a reader of `kubectl describe clusterrole`
	// came for is the first line.
	//
	// Read is a wildcard over the discovered groups, so that a provider
	// publishing a new type does not silently fall outside what an owner may
	// watch. Write is one rule naming one resource, because that is the whole
	// of what a tenant writes and it should be readable as such - see
	// WritableResource.
	//
	// Omitted entirely when nothing is bound: a rule naming no API group is
	// not a narrower grant, it is a malformed one, and an empty workspace
	// should carry a role that grants nothing rather than one that grants
	// everything.
	if len(groups) > 0 {
		admin.Rules = append([]rbacv1.PolicyRule{
			{APIGroups: groups, Resources: []string{"*"}, Verbs: read},
			{APIGroups: []string{WritableGroup}, Resources: []string{WritableResource}, Verbs: write},
		}, admin.Rules...)
		view.Rules = append([]rbacv1.PolicyRule{{
			APIGroups: groups,
			Resources: []string{"*"},
			Verbs:     read,
		}}, view.Rules...)
	}

	return []*rbacv1.ClusterRole{admin, view}
}

func managedBy() map[string]string {
	return map[string]string{ManagedByLabel: ManagedByValue}
}

// NewBinding binds one of the roles above, or kcp's workspace-access role, to
// one subject in the workspace it is created in.
func NewBinding(role, user string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role + ":" + user},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{{
			APIGroup: rbacv1.GroupName,
			Kind:     rbacv1.UserKind,
			Name:     user,
		}},
	}
}

// ProviderBinderRoleName is what lets somebody enable a provider.
//
// It is granted in the workspace the APIExports live in, not in the workspace
// the provider is being enabled in, because that is where kcp checks it:
// creating an APIBinding is authorized as the verb "bind" on the APIExport
// being bound, evaluated in the exporting workspace. Granting it in the
// tenant's own workspace has no effect at all, which is a confusing thing to
// debug the first time.
//
// This is the operator's half of "the tenant enables the provider they want".
// Which providers a tenant may turn on is a decision somebody makes for them;
// which of those they actually use is not.
const ProviderBinderRoleName = "cluster-api-provider-binder"

// NewProviderBinderRole lets the named exports be bound by whoever holds it.
//
// Named one by one rather than granted over the resource: `bind` on every
// APIExport in that workspace would let a tenant bind an export that has
// nothing to do with Cluster API, and that workspace is where every export in
// the installation lives.
func NewProviderBinderRole(exports []string) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: ProviderBinderRoleName, Labels: managedBy()},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{apisv1alpha2.SchemeGroupVersion.Group},
			Resources:     []string{"apiexports"},
			ResourceNames: exports,
			Verbs:         []string{"bind"},
		}},
	}
}

// GrantProviderBinding lets each of users enable any of exports for
// themselves, in the workspace cl is scoped to - which must be the one holding
// the exports.
func GrantProviderBinding(ctx context.Context, cl client.Client, users, exports []string) error {
	role := NewProviderBinderRole(exports)
	if _, err := applyRole(ctx, cl, role); err != nil {
		return err
	}
	for _, user := range users {
		if err := createBinding(ctx, cl, NewBinding(role.Name, user)); err != nil {
			return err
		}
	}
	return nil
}

// GrantRoles binds roles that already exist to one user in the workspace cl is
// scoped to, along with kcp's workspace-access role.
//
// It creates no role. In a Cluster API workspace the roles were written by the
// WorkspaceType's initializer before the workspace was ready and are kept
// current by the workspace manager; what is left to decide is who holds them.
//
// The access binding is separate because it answers to a different authorizer.
// kcp's workspace content authorizer decides whether a request may be in the
// workspace at all, and refuses everything before RBAC on the resource is
// reached; ordinary RBAC decides the rest. Granting only the second is the
// mistake that produces "access denied" on a type the workspace plainly
// serves.
func GrantRoles(ctx context.Context, cl client.Client, user string, roles ...string) error {
	for _, role := range append([]string{WorkspaceAccessRoleName}, roles...) {
		if err := createBinding(ctx, cl, NewBinding(role, user)); err != nil {
			return err
		}
	}
	return nil
}

func createBinding(ctx context.Context, cl client.Client, binding *rbacv1.ClusterRoleBinding) error {
	if err := cl.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", binding.Name, err)
	}
	return nil
}
