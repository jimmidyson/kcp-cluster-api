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

// Package onboarding_test asserts the three things a tenant is promised about
// getting a Cluster API workspace, none of which they should have to do by
// hand:
//
//  1. Creating a workspace of the Cluster API WorkspaceType is the whole of
//     onboarding: kcp binds Cluster API's core APIExport into it, and this
//     project's initializer writes the roles that say who may use it, before
//     the workspace is Ready.
//
//  2. Enabling a provider is the tenant's own step, made with the tenant's own
//     permissions - not the operator's, on their behalf.
//
//  3. Nothing has to be edited afterwards. The provider's types become
//     reachable by core's controllers, and usable by the tenant, because a
//     controller noticed rather than because somebody changed a manifest.
package onboarding_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/workspacemanager"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

const (
	providerPath = "root"
	tenant       = "alice"
	outsider     = "mallory"

	// Generous, and for a reason worth stating: onboarding waits on kcp's own
	// APIBinding initializer, which waits on permission claims being applied,
	// which races a bound-CRD materialisation this project has to poke along
	// (capiworkspaces.NudgeUnappliedClaims). Ten seconds is the usual figure;
	// this is headroom, not a prediction.
	readyTimeout = 3 * time.Minute
	pollInterval = 2 * time.Second
)

// configAs addresses one workspace and is authorized as one user.
//
// base must be privileged. kcp scopes an impersonated user to the logical
// cluster the request addresses unless the impersonator is in system:masters
// (pkg/server/filters/impersonation.go), and a scoped user is refused
// everywhere else whatever RBAC says - including the workspace holding the
// APIExports, which is where the right to enable a provider is checked. An
// under-privileged impersonator therefore makes the tenant look less able than
// the real user, and TestATenantEnablesAProviderAndTheirRoleFollows fails with
// "no permission to bind to export ..." for a reason that has nothing to do
// with the grant under test.
func configAs(base *rest.Config, user, path string) *rest.Config {
	cfg := kcpclient.SetCluster(rest.CopyConfig(base), logicalcluster.NewPath(path))
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user}
	return cfg
}

func clientAt(t *testing.T, base *rest.Config, path string, scheme *runtime.Scheme) client.Client {
	t.Helper()
	cl, err := client.New(kcpclient.SetCluster(rest.CopyConfig(base), logicalcluster.NewPath(path)), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a client for %s: %v", path, err)
	}
	return cl
}

// installation is one kcp server with Cluster API published into it and the
// workspace manager running, which is what a deployment of this project is.
type installation struct {
	base          *rest.Config
	impersonation *rest.Config
	scheme        *runtime.Scheme
	provider      client.Client
	published     []capiexports.Provider
	discovery     capiexports.Discovery
}

func install(t *testing.T, ctx context.Context) *installation {
	t.Helper()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	scheme, err := workspacemanager.Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	inst := &installation{
		base: server.BaseConfig(t),
		// The privileged credential. kcp scopes an impersonated user to the
		// logical cluster the request addresses unless the impersonator is in
		// system:masters, and a scoped tenant is refused in the workspace
		// holding the APIExports - which is where the right to enable a
		// provider is checked. See demo.ConfigForUser.
		impersonation: server.RootShardSystemMasterBaseConfig(t),
		scheme:        scheme,
	}
	inst.provider = clientAt(t, inst.base, providerPath, scheme)

	// Core and one provider. Two is the smallest set that can show a claim
	// being discovered rather than declared.
	inst.published = []capiexports.Provider{
		capiexports.Core(), capiexports.Infrastructure(), capiexports.Workspaces(),
	}
	// Retried: a client built moments after the server starts serving can hold
	// a discovery document with no apis.kcp.io in it at all.
	deadline := time.Now().Add(time.Minute)
	for {
		inst.discovery, err = capiexports.Publish(ctx, inst.provider, inst.published, time.Minute)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publishing the APIExports: %v", err)
		}
		time.Sleep(pollInterval)
	}

	workspaceType := capiworkspaces.NewWorkspaceType(providerPath, capiworkspaces.DefaultExports())
	if err := capiworkspaces.EnsureWorkspaceType(ctx, inst.provider, workspaceType, time.Minute); err != nil {
		t.Fatalf("publishing the WorkspaceType: %v", err)
	}

	runner, err := workspacemanager.New(ctx, workspacemanager.Options{
		BaseConfig:   inst.base,
		ProviderPath: providerPath,
		Providers:    inst.published,
		Timeout:      readyTimeout,
		// Several installations in one test process, and
		// controller-runtime's name registry is never emptied.
		SkipControllerNameValidation: true,
	})
	if err != nil {
		t.Fatalf("setting up the workspace manager: %v", err)
	}
	go func() {
		if err := runner.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("the workspace manager exited: %v", err)
		}
	}()

	return inst
}

// onboard creates one workspace of the Cluster API type and waits for it.
func (i *installation) onboard(t *testing.T, ctx context.Context, name string) client.Client {
	t.Helper()

	if _, err := kcpfixtures.EnsureWorkspaceOfType(ctx, i.provider, name,
		capiworkspaces.TypeReference(providerPath), readyTimeout); err != nil {
		t.Fatalf("onboarding workspace %s: %v", name, err)
	}
	return clientAt(t, i.base, providerPath+":"+name, i.scheme)
}

// adminRoleGroups is the Cluster API groups the workspace's admin role covers,
// read back from the role.
func adminRoleGroups(t *testing.T, ctx context.Context, cl client.Client) []string {
	t.Helper()

	role := &rbacv1.ClusterRole{}
	if err := cl.Get(ctx, client.ObjectKey{Name: capiworkspaces.AdminRoleName}, role); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatalf("reading %s: %v", capiworkspaces.AdminRoleName, err)
	}

	var groups []string
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			if group == capiworkspaces.ClusterAPIGroup || strings.HasSuffix(group, "."+capiworkspaces.ClusterAPIGroup) {
				groups = append(groups, group)
			}
		}
	}
	return groups
}

func waitFor(t *testing.T, ctx context.Context, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCreatingAWorkspaceIsTheWholeOfOnboarding is the promise the WorkspaceType
// makes. Nothing in this test creates an APIBinding or writes a role.
func TestCreatingAWorkspaceIsTheWholeOfOnboarding(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	inst := install(t, ctx)
	ws := inst.onboard(t, ctx, "onboarded")

	// Ready is the assertion, not a precondition: kcp holds a workspace of
	// this type out of Ready until the initializer has finished, so a
	// workspace that is Ready is one whose roles were written before anybody
	// could look at it.
	bindings := &apisv1alpha2.APIBindingList{}
	if err := ws.List(ctx, bindings); err != nil {
		t.Fatalf("listing APIBindings: %v", err)
	}
	var boundToCore bool
	for i := range bindings.Items {
		if ref := bindings.Items[i].Spec.Reference.Export; ref != nil && ref.Name == capiexports.CoreExport {
			boundToCore = true
		}
	}
	if !boundToCore {
		t.Error("the workspace has no APIBinding to Cluster API's core APIExport")
	}

	for _, name := range []string{capiworkspaces.AdminRoleName, capiworkspaces.ViewRoleName} {
		role := &rbacv1.ClusterRole{}
		if err := ws.Get(ctx, client.ObjectKey{Name: name}, role); err != nil {
			t.Errorf("the workspace has no %s role: %v", name, err)
		}
	}

	waitFor(t, ctx, "the admin role to cover Cluster API's own group", func() bool {
		return len(adminRoleGroups(t, ctx, ws)) > 0
	})
	if groups := adminRoleGroups(t, ctx, ws); !containsGroup(groups, capiworkspaces.ClusterAPIGroup) {
		t.Errorf("the admin role covers %v, want Cluster API's own group among them", groups)
	}
}

// TestATenantEnablesAProviderAndTheirRoleFollows is the second and third
// promises together, because the second is only interesting if the third
// holds: a tenant who has to ask an operator to widen their role afterwards
// has not enabled anything themselves.
func TestATenantEnablesAProviderAndTheirRoleFollows(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	inst := install(t, ctx)
	ws := inst.onboard(t, ctx, "tenant-enables")
	path := providerPath + ":tenant-enables"

	// The operator's half: who may enable which provider, granted where kcp
	// checks it - the workspace holding the export - and the tenant's use of
	// their own workspace, granted where the objects are.
	if err := capiworkspaces.GrantProviderBinding(ctx, inst.provider, []string{tenant}, []string{capiexports.InfraExport}); err != nil {
		t.Fatalf("granting %s the right to enable providers: %v", tenant, err)
	}
	if err := capiworkspaces.GrantRoles(ctx, ws, tenant, capiworkspaces.AdminRoleName); err != nil {
		t.Fatalf("granting %s their workspace: %v", tenant, err)
	}

	waitFor(t, ctx, "the admin role to be written", func() bool {
		return len(adminRoleGroups(t, ctx, ws)) > 0
	})
	before := adminRoleGroups(t, ctx, ws)
	if containsGroup(before, "infrastructure.cluster.x-k8s.io") {
		t.Fatalf("the admin role already covers the infrastructure group before anything enabled it: %v", before)
	}

	// The tenant's half, made as the tenant.
	alice, err := client.New(configAs(inst.impersonation, tenant, path), client.Options{Scheme: inst.scheme})
	if err != nil {
		t.Fatalf("building a client as %s: %v", tenant, err)
	}
	infra := capiexports.Infrastructure()
	if err := kcpfixtures.BindExport(ctx, alice, kcpfixtures.BindExportOptions{
		BindingName:      capiexports.InfraExport,
		ExportPath:       providerPath,
		ExportName:       capiexports.InfraExport,
		PermissionClaims: infra.Claims(inst.discovery.Identities(), inst.discovery),
		ReadyTimeout:     readyTimeout,
	}); err != nil {
		t.Fatalf("%s could not enable the infrastructure provider: %v", tenant, err)
	}

	// And the role follows, with nothing editing it.
	waitFor(t, ctx, "the admin role to cover the provider the tenant enabled", func() bool {
		return containsGroup(adminRoleGroups(t, ctx, ws), "infrastructure.cluster.x-k8s.io")
	})
}

// TestEnablingAProviderIsAPermissionSomebodyGrants is the other half of the
// second promise. A tenant who could bind any export in the installation would
// not be demonstrating a permission, only the absence of one.
func TestEnablingAProviderIsAPermissionSomebodyGrants(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	inst := install(t, ctx)
	ws := inst.onboard(t, ctx, "no-grant")
	path := providerPath + ":no-grant"

	// Everything except the right to bind: full use of their own workspace,
	// including creating APIBindings in it.
	if err := capiworkspaces.GrantRoles(ctx, ws, outsider, capiworkspaces.AdminRoleName); err != nil {
		t.Fatalf("granting %s their workspace: %v", outsider, err)
	}

	cl, err := client.New(configAs(inst.impersonation, outsider, path), client.Options{Scheme: inst.scheme})
	if err != nil {
		t.Fatalf("building a client as %s: %v", outsider, err)
	}
	infra := capiexports.Infrastructure()
	err = kcpfixtures.BindExport(ctx, cl, kcpfixtures.BindExportOptions{
		BindingName:      capiexports.InfraExport,
		ExportPath:       providerPath,
		ExportName:       capiexports.InfraExport,
		PermissionClaims: infra.Claims(inst.discovery.Identities(), inst.discovery),
		ReadyTimeout:     10 * time.Second,
	})
	if err == nil {
		t.Fatalf("%s enabled a provider nobody granted them", outsider)
	}
	if !apierrors.IsForbidden(err) && !strings.Contains(err.Error(), "no permission to bind") {
		t.Errorf("%s was refused with %v, want a forbidden on the bind", outsider, err)
	}
}

// TestCoreClaimsAProviderNobodyNamed is the claim half of the third promise:
// core reaches an infrastructure provider's types because that provider
// published a labelled APIExport, not because this repository listed them.
func TestCoreClaimsAProviderNobodyNamed(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	inst := install(t, ctx)

	core := &apisv1alpha2.APIExport{}
	if err := inst.provider.Get(ctx, client.ObjectKey{Name: capiexports.CoreExport}, core); err != nil {
		t.Fatalf("reading core's APIExport: %v", err)
	}

	infra := &apisv1alpha2.APIExport{}
	if err := inst.provider.Get(ctx, client.ObjectKey{Name: capiexports.InfraExport}, infra); err != nil {
		t.Fatalf("reading the infrastructure APIExport: %v", err)
	}
	if got := infra.Labels[capiexports.ContractLabel]; got != string(capiexports.ContractInfrastructure) {
		t.Fatalf("the infrastructure export is labelled %s=%q, which is how it is discovered", capiexports.ContractLabel, got)
	}

	// Every resource it publishes, under its identity. Core's own definition
	// names none of them.
	for _, resource := range infra.Spec.Resources {
		var claimed bool
		for _, claim := range core.Spec.PermissionClaims {
			if claim.Group == resource.Group && claim.Resource == resource.Name && claim.IdentityHash == infra.Status.IdentityHash {
				claimed = true
				break
			}
		}
		if !claimed {
			t.Errorf("core does not claim %s.%s, which the infrastructure provider publishes", resource.Name, resource.Group)
		}
	}
	if len(capiexports.Core().ProviderClaims) != 0 {
		t.Errorf("core names %v in ProviderClaims; every provider claim should be discovered",
			capiexports.Core().ProviderClaims)
	}
}

func containsGroup(groups []string, want string) bool {
	for _, g := range groups {
		if g == want {
			return true
		}
	}
	return false
}
