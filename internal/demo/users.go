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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metav1validation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// DefaultUsers are the tenants a demo run creates when nothing says
// otherwise. Two, for the reason there are two workspaces: one tenant
// demonstrates nothing about tenancy.
var DefaultUsers = []string{"alice", "bob"}

// Role names. One role per level, because the two levels grant genuinely
// different things: a home is where a tenant sees their workspaces, and a
// workspace is where they see their cluster.
const (
	HomeRoleName      = "demo-home-owner"
	WorkspaceRoleName = "demo-workspace-owner"

	// WorkspaceAccessRoleName is kcp's own ClusterRole for the right to be in
	// a workspace at all. It is bound rather than reproduced: kcp bootstraps
	// it into the local admin logical cluster and merges that cluster's roles
	// when it resolves the check, so a binding in any workspace resolves it.
	//
	// The check it satisfies is kcp's rather than Kubernetes': before RBAC on
	// the resource is consulted, the workspace content authorizer asks for the
	// verb "access" on the non-resource URL "/". Writing that rule out here
	// would work and would be a copy of kcp's policy, drifting silently the
	// day kcp changes it. Binding the role by name is what kcp's own e2e
	// framework does (AdmitWorkspaceAccess) and what its root workspace does
	// for system:authenticated.
	WorkspaceAccessRoleName = "system:kcp:workspace:access"
)

// The two owners in the access table that are not users.
const (
	// OwnerEveryone is the parent workspace - `root` by default. kcp binds
	// tenancy reads there to system:authenticated, so every authenticated user
	// can list its direct children whether or not they can enter any of them.
	//
	// Reported rather than asserted: what a shard's parent workspace lets an
	// authenticated user list is that shard's policy, not this demo's, and a
	// deployment that has narrowed it is not a demo that has failed.
	OwnerEveryone = "everyone"

	// OwnerNobody is the org workspace holding every user's home. Nothing
	// grants any tenant anything in it, which is what stops a tenant
	// discovering that the other tenants exist.
	OwnerNobody = "nobody"
)

// User is one tenant of a demo run: a name kcp authorizes requests as, and the
// workspace that holds everything they own.
//
// There is no credential here. The demo authenticates to kcp as the admin and
// asks the server to evaluate each request as the user - see ConfigForUser -
// so what a user "has" is entirely what the RBAC in each workspace grants
// them, which is the thing being demonstrated.
type User struct {
	Name string

	// Home is the workspace path holding this user's workspaces. Only this
	// user can read it.
	Home string
}

// WorkspacePlan is one workspace a run will create: where it goes, and who
// owns it.
//
// A run with no users puts every workspace directly under the parent and
// leaves Owner empty, which is what the sweeps and the scale harness drive.
// A run with users gives each of them a home workspace under an org workspace
// and puts their workspaces inside it, which is what `task demo` does.
type WorkspacePlan struct {
	// Name is the workspace's own name, not its path.
	Name string

	// Parent is the path of the workspace this one is created under.
	Parent string

	// Path is the full path, which is what everything else addresses it by.
	Path string

	// Owner is the user this workspace belongs to, or empty when the run has
	// no users.
	Owner string
}

// OrgPath is the workspace holding every user's home: created by the demo,
// owned by nobody, and readable by no tenant.
//
// It exists because kcp's root workspace grants every authenticated user
// tenancy reads by default, so homes placed directly under root would be
// listable by everyone and the isolation this demonstrates would be a
// property of the demo's own RBAC rather than of the tree.
func OrgPath(parent, prefix string) string {
	return logicalcluster.NewPath(parent).Join(prefix).String()
}

// HomePath is one user's home workspace.
func HomePath(parent, prefix, user string) string {
	return logicalcluster.NewPath(OrgPath(parent, prefix)).Join(user).String()
}

// PlanWorkspaces spreads workspaces over users, round-robin, and says where
// each one goes.
//
// The nth workspace of every user carries the same name, for the reason the
// Cluster in every workspace does: a leak between two `capi-demo-1`s cannot
// hide behind names that happen not to collide. Round-robin rather than
// contiguous blocks so that an uneven count leaves a user short rather than
// leaving the last one with nothing.
func PlanWorkspaces(parent, prefix string, users []string, workspaces int) []WorkspacePlan {
	plans := make([]WorkspacePlan, 0, workspaces)
	for i := range workspaces {
		if len(users) == 0 {
			name := fmt.Sprintf("%s-%d", prefix, i+1)
			plans = append(plans, WorkspacePlan{
				Name:   name,
				Parent: parent,
				Path:   logicalcluster.NewPath(parent).Join(name).String(),
			})
			continue
		}

		owner := users[i%len(users)]
		name := fmt.Sprintf("%s-%d", prefix, i/len(users)+1)
		home := HomePath(parent, prefix, owner)
		plans = append(plans, WorkspacePlan{
			Name:   name,
			Parent: home,
			Path:   logicalcluster.NewPath(home).Join(name).String(),
			Owner:  owner,
		})
	}
	return plans
}

// validateUsers rejects a user list that cannot produce the tree
// PlanWorkspaces describes.
//
// Each user's home workspace is named after them, so a name kcp cannot name a
// workspace has to fail here rather than as an admission error halfway through
// a run, and two users sharing a name would share a home - which is the one
// thing this demo exists to show does not happen.
func validateUsers(users []string) error {
	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if user == "" {
			return fmt.Errorf("empty user name in %v", users)
		}
		if errs := metav1validation.IsDNS1123Label(user); len(errs) > 0 {
			return fmt.Errorf("user %q cannot name a workspace: %s", user, strings.Join(errs, "; "))
		}
		if user == OwnerEveryone || user == OwnerNobody {
			return fmt.Errorf("user %q is how the access table spells an owner that is not a user", user)
		}
		if _, dup := seen[user]; dup {
			return fmt.Errorf("user %q appears twice: two tenants would share one home workspace", user)
		}
		seen[user] = struct{}{}
	}
	return nil
}

// ConfigForUser returns a copy of base that addresses one workspace and is
// authorized as one user.
//
// The demo holds admin credentials and impersonates: kcp runs its whole
// authorization stack against the impersonated user, so a request made through
// this config is allowed or refused by exactly the RBAC that user has in that
// workspace. That is what makes the denials the demo reports real ones rather
// than a simulation, and it is why the printed kubectl commands say `--as`.
func ConfigForUser(base *rest.Config, user, path string) *rest.Config {
	cfg := kcpclient.SetCluster(rest.CopyConfig(base), logicalcluster.NewPath(path))
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user}
	return cfg
}

// NewHomeRole is what a user may do in their own home workspace: read the
// workspaces it holds.
//
// Being in the workspace at all is the separate grant WorkspaceAccessRoleName
// carries, and GrantOwner makes both. Without it every request into the
// workspace is refused with "access denied" whatever this role says, which is
// a confusing failure the first time you meet it.
//
// Read-only, deliberately. Creating workspaces is the demo's job; a role that
// could do it would claim more than anything here demonstrates.
func NewHomeRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: HomeRoleName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{tenancyv1alpha1.SchemeGroupVersion.Group},
				Resources: []string{"workspaces"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"core.kcp.io"},
				Resources: []string{"logicalclusters"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

// NewWorkspaceRole is what a user may do in each of their own workspaces: read
// every Cluster API type bound there, and write exactly one of them, the
// Cluster. Being in the workspace is WorkspaceAccessRoleName's grant, as in
// the home.
//
// # One writable type, because a demo cluster is a ClusterClass based cluster
//
// The Cluster names a class and a shape. The DevCluster, the
// KubeadmControlPlane, the worker MachineDeployment and the templates each is
// stamped from are created by the topology controller under the manager's
// identity, never the tenant's, so a tenant who could write them would be
// holding the grant the hand-built model needed against a demo that no longer
// builds clusters that way. Scaling and version changes do not reopen it:
// both are fields of spec.topology, which write on clusters already carries.
//
// The blueprint is the other half. The ClusterClass and the five templates it
// refers to are seeded into the workspace by whoever runs the demo, the way a
// platform operator seeds a tenant's - and are read-only once there, because
// writing a class is deciding what a cluster in this installation is made of
// rather than asking for one, and that answer is not a tenant's to give.
//
// Deleting a Machine to force a replacement is deliberately absent. It is a
// real operation and a real temptation, but it is remediation, which is the
// platform's job here; a tenant changes their cluster through spec.topology,
// and a Machine deleted underneath the topology controller is a change it did
// not make.
//
// # Why the shape of the rules
//
// The groups are named rather than granted as "*", for the reason the
// permission claims are narrowed by verb: this is the place where what a
// tenant may do is written down, so it should say it. Within them read is a
// wildcard, so that an export publishing a new type does not silently fall
// outside what an owner may watch, and write is one rule naming one resource,
// because that is the whole claim and it should be readable as such.
//
// Secrets are readable because a cluster's admin kubeconfig is one, and are
// not writable because a tenant never writes one - the control plane provider
// does.
func NewWorkspaceRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: WorkspaceRoleName},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{
					"cluster.x-k8s.io",
					"bootstrap.cluster.x-k8s.io",
					"controlplane.cluster.x-k8s.io",
					"infrastructure.cluster.x-k8s.io",
				},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"cluster.x-k8s.io"},
				Resources: []string{"clusters"},
				Verbs:     []string{"create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces", "secrets", "configmaps", "events"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"core.kcp.io"},
				Resources: []string{"logicalclusters"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

// NewOwnerBinding binds one of the roles above to one user in the workspace it
// is created in.
func NewOwnerBinding(role, user string) *rbacv1.ClusterRoleBinding {
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

// GrantOwner gives one user their two grants in the workspace cl is scoped to:
// the right to be in it, and whatever role says they may do there. Idempotent,
// like everything else the demo creates.
//
// Two bindings rather than one because they answer to different authorizers.
// kcp's workspace content authorizer decides the first and refuses everything
// before RBAC on the resource is reached; ordinary RBAC decides the second.
// Granting only the second is the mistake that produces "access denied" on a
// type the workspace plainly serves.
func GrantOwner(ctx context.Context, cl client.Client, role *rbacv1.ClusterRole, user string) error {
	access := NewOwnerBinding(WorkspaceAccessRoleName, user)
	if err := create(ctx, cl, access); err != nil {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", access.Name, err)
	}
	if err := create(ctx, cl, role); err != nil {
		return fmt.Errorf("creating ClusterRole %s: %w", role.Name, err)
	}
	binding := NewOwnerBinding(role.Name, user)
	if err := create(ctx, cl, binding); err != nil {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", binding.Name, err)
	}
	return nil
}

// AccessCheck is one user's attempt to list one resource in one workspace, and
// what kcp said about it.
type AccessCheck struct {
	// User is who the request was authorized as.
	User string

	// Workspace is the path it was made against.
	Workspace string

	// Owner is who that workspace belongs to: a user's name, OwnerNobody for
	// the org workspace, or OwnerEveryone for the parent workspace kcp opens
	// to every authenticated user.
	Owner string

	// Resource is what was listed: "workspaces" in a home, "clusters" in a
	// workspace that holds one.
	Resource string

	// Allowed is whether kcp served the list.
	Allowed bool

	// Detail is what came back: how much was listed, or why it was refused.
	Detail string
}

// Expected is what should have happened: a user reads their own workspaces and
// nobody else's, nobody reads the org workspace that holds every home, and
// everybody reads the parent workspace kcp opens to authenticated users.
func (c AccessCheck) Expected() bool {
	if c.Owner == OwnerEveryone {
		return true
	}
	return c.Owner != "" && c.Owner != OwnerNobody && c.Owner == c.User
}

// Reported says this check is here to say what a tenant can see rather than to
// assert it. Only the parent workspace is: its policy is the shard's, and a
// shard that has narrowed it has broken nothing this demo claims.
func (c AccessCheck) Reported() bool {
	return c.Owner == OwnerEveryone
}

// AsIntended reports whether this check came out the way it was meant to. A
// check that did not is either a leak - one tenant reading another's - or a
// tenant locked out of their own.
func (c AccessCheck) AsIntended() bool {
	return c.Reported() || c.Allowed == c.Expected()
}

// Isolated reports whether every check came out as intended.
//
// An empty set is not isolated. A run that checked nothing proved nothing, and
// reporting success for it would be the one answer a tenancy assertion must
// not give.
func Isolated(checks []AccessCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, c := range checks {
		if !c.AsIntended() {
			return false
		}
	}
	return true
}

// RenderAccessTable writes the checks as an aligned table.
func RenderAccessTable(w io.Writer, checks []AccessCheck) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "USER\tWORKSPACE\tOWNER\tRESOURCE\tALLOWED\tDETAIL"); err != nil {
		return err
	}
	for _, c := range checks {
		detail := c.Detail
		if !c.AsIntended() {
			// Loud, because the table's whole shape is "ALLOWED is yes exactly
			// where OWNER is you, or everyone", and a reader scanning it for
			// that pattern is the person who most needs to be told when it
			// does not hold.
			detail = "LEAK: " + detail
			if c.Expected() {
				detail = "LOCKED OUT: " + c.Detail
			}
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.User, c.Workspace, c.Owner, c.Resource, yesNo(c.Allowed), detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// CheckAccess asks, for every pair of users, what each can read of the other's
// workspaces - and what either can read of the two workspaces above them.
//
// Three probes per pair, which is the smallest set that distinguishes the two
// ways isolation can be wrong. A user must be able to list their own
// workspaces and their own Clusters, and must be refused both of another's; a
// run that only checked the refusals would pass with an RBAC bug that locked
// everybody out of everything.
//
// Two more per user say where the tenant boundary starts, which is the
// question a reader asks next: what can they see from the top? A `Workspace`
// list is not recursive and is not filtered by what the caller can enter - it
// returns the workspaces stored in the one workspace addressed, all of them or
// none - so "the workspaces I have access to" is not a question kcp answers.
// What a tenant has instead is the path to their own home. The parent and the
// org rows are those two facts: the parent workspace lists its direct children
// to any authenticated user, and the org workspace lists nothing to anybody.
//
// The lists go out as raw requests rather than through a typed client on
// purpose. A client discovers the API surface before it lists anything, and in
// a workspace the user cannot enter that discovery fails first - producing a
// RESTMapper error about a missing kind, rather than the 403 that is the
// answer to the question being asked.
func CheckAccess(ctx context.Context, base *rest.Config, parent, org string, users []User, workspaces []Workspace) ([]AccessCheck, error) {
	// One workspace per owner is enough: the question is whether a tenant
	// boundary holds, and asking it of every workspace asks it again rather
	// than asking more.
	firstOwned := map[string]Workspace{}
	for _, ws := range workspaces {
		if ws.Owner == "" {
			continue
		}
		if _, ok := firstOwned[ws.Owner]; !ok {
			firstOwned[ws.Owner] = ws
		}
	}

	var checks []AccessCheck
	for _, user := range users {
		// The two workspaces above the tenants, top down. The parent says what
		// a tenant can see from the top of the tree; the org says that it
		// stops there. Together they are the reason a tenant cannot enumerate
		// the other tenants - and the reason the org workspace exists at all.
		check, err := listAs(ctx, base, user.Name, parent, OwnerEveryone, workspaceResource)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)

		check, err = listAs(ctx, base, user.Name, org, OwnerNobody, workspaceResource)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)

		for _, owner := range users {
			check, err := listAs(ctx, base, user.Name, owner.Home, owner.Name, workspaceResource)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)

			ws, ok := firstOwned[owner.Name]
			if !ok {
				// A run with more users than workspaces leaves somebody
				// without one. Their home is still checked above.
				continue
			}
			check, err = listAs(ctx, base, user.Name, ws.Path, owner.Name, clusterResource)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
		}
	}
	return checks, nil
}

// resource is one thing CheckAccess lists, in the form a raw request needs.
type resource struct {
	name     string
	apiPath  string
	group    string
	version  string
	singular string
}

var (
	workspaceResource = resource{
		name:     "workspaces",
		apiPath:  "/apis",
		group:    tenancyv1alpha1.SchemeGroupVersion.Group,
		version:  tenancyv1alpha1.SchemeGroupVersion.Version,
		singular: "workspace",
	}
	clusterResource = resource{
		name:     "clusters",
		apiPath:  "/apis",
		group:    clusterv1.GroupVersion.Group,
		version:  clusterv1.GroupVersion.Version,
		singular: "cluster",
	}
)

// listAs makes one list request as one user against one workspace and reports
// what happened.
//
// A refusal is a result, not an error: it is the answer the demo is asking
// for. Anything else - a server that is not there, a type that is not served -
// is returned as an error, because reporting it as a denial would turn a
// broken run into a passing tenancy assertion.
func listAs(ctx context.Context, base *rest.Config, user, path, owner string, res resource) (AccessCheck, error) {
	check := AccessCheck{User: user, Workspace: path, Owner: owner, Resource: res.name}

	cfg := ConfigForUser(base, user, path)
	cfg.APIPath = res.apiPath
	cfg.GroupVersion = &schema.GroupVersion{Group: res.group, Version: res.version}
	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	cl, err := rest.RESTClientFor(cfg)
	if err != nil {
		return check, fmt.Errorf("building a client for %s as %s: %w", path, user, err)
	}

	raw, err := cl.Get().Resource(res.name).Do(ctx).Raw()
	switch {
	case err == nil:
	case apierrors.IsForbidden(err):
		check.Detail = "forbidden"
		return check, nil
	case apierrors.IsUnauthorized(err):
		check.Detail = "unauthorized"
		return check, nil
	default:
		return check, fmt.Errorf("listing %s in %s as %s: %w", res.name, path, user, err)
	}

	list := &unstructured.UnstructuredList{}
	if err := json.Unmarshal(raw, list); err != nil {
		return check, fmt.Errorf("decoding the %s list from %s: %w", res.name, path, err)
	}

	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].GetName())
	}
	slices.Sort(names)

	check.Allowed = true
	check.Detail = fmt.Sprintf("%d %s", len(names), plural(res.singular, len(names)))
	if len(names) > 0 {
		check.Detail += ": " + strings.Join(names, ", ")
	}
	return check, nil
}

func plural(singular string, n int) string {
	if n == 1 {
		return singular
	}
	return singular + "s"
}
