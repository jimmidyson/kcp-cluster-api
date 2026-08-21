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
	"slices"
	"testing"

	"k8s.io/utils/ptr"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

// Maintain is the whole of the claim-propagation half of ADR-0001's decision 3
// and this project writes no code for it. InitializeOnly - which is what kcp
// defaults to - would freeze each workspace's accepted claims at the moment it
// was created, so a provider onboarded later would be invisible to core's
// controllers in every workspace that already existed.
func TestWorkspaceTypeMaintainsItsBindings(t *testing.T) {
	wt := NewWorkspaceType("root", DefaultExports())

	if wt.Spec.DefaultAPIBindingLifecycle == nil {
		t.Fatal("the WorkspaceType sets no defaultAPIBindingLifecycle, so kcp defaults it to InitializeOnly")
	}
	if got := *wt.Spec.DefaultAPIBindingLifecycle; got != tenancyv1alpha1.APIBindingLifecycleModeMaintain {
		t.Errorf("defaultAPIBindingLifecycle = %q, want %q", got, tenancyv1alpha1.APIBindingLifecycleModeMaintain)
	}
	if !ptr.Equal(wt.Spec.DefaultAPIBindingLifecycle, ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain)) {
		t.Error("defaultAPIBindingLifecycle is not Maintain")
	}
}

// The onboarding export has to be bound before core's. kcp labels an object
// with the claims its workspace had accepted when the object was written, and
// it is the onboarding binding that accepts the claim on APIBindings; bind it
// second and the core binding written moments earlier is invisible to the
// controller that has to see it.
func TestWorkspaceTypeBindsTheOnboardingExportFirst(t *testing.T) {
	wt := NewWorkspaceType("root", DefaultExports())

	if len(wt.Spec.DefaultAPIBindings) < 2 {
		t.Fatalf("the WorkspaceType binds %d exports, want the onboarding export and core", len(wt.Spec.DefaultAPIBindings))
	}
	if got := wt.Spec.DefaultAPIBindings[0].Export; got != capiexports.WorkspaceExport {
		t.Errorf("the first default binding is %s, want %s", got, capiexports.WorkspaceExport)
	}
	if got := wt.Spec.DefaultAPIBindings[1].Export; got != capiexports.CoreExport {
		t.Errorf("the second default binding is %s, want %s", got, capiexports.CoreExport)
	}
	for _, ref := range wt.Spec.DefaultAPIBindings {
		if ref.Path != "root" {
			t.Errorf("the default binding to %s names path %q, want the workspace holding the exports", ref.Export, ref.Path)
		}
	}
}

// Which infrastructure provider a workspace uses is the tenant's decision. A
// WorkspaceType that bound one would be making it for them, and would defeat
// the thing the tenant-side APIBinding demonstrates.
func TestWorkspaceTypeBindsNoProvider(t *testing.T) {
	bound := DefaultExports()
	for _, p := range capiexports.All() {
		if p.Export == capiexports.CoreExport {
			continue
		}
		if slices.Contains(bound, p.Export) {
			t.Errorf("the WorkspaceType binds %s; enabling a provider is the tenant's step", p.Export)
		}
	}
}

// A workspace of this type is held out of Ready until its roles exist, which
// is the only reason to use an initializer rather than to write the roles once
// the workspace is up.
func TestWorkspaceTypeHoldsTheWorkspaceUntilItsRolesExist(t *testing.T) {
	wt := NewWorkspaceType("root", DefaultExports())

	if !wt.Spec.Initializer {
		t.Error("the WorkspaceType declares no initializer, so a workspace becomes Ready before it grants anybody anything")
	}
}

// Left unset, kcp's initializing virtual workspace impersonates the workspace
// owner - cluster-admin - for every workspace in the installation. Writing two
// ClusterRoles does not need that.
func TestInitializerPermissionsAreNarrowerThanClusterAdmin(t *testing.T) {
	wt := NewWorkspaceType("root", DefaultExports())

	if len(wt.Spec.InitializerPermissions) == 0 {
		t.Fatal("the WorkspaceType grants its initializer no explicit permissions, so kcp falls back to cluster-admin")
	}
	for _, rule := range wt.Spec.InitializerPermissions {
		if slices.Contains(rule.APIGroups, "*") || slices.Contains(rule.Resources, "*") || slices.Contains(rule.Verbs, "*") {
			t.Errorf("the initializer is granted a wildcard rule: %v", rule)
		}
		if slices.Contains(rule.Resources, "secrets") {
			t.Errorf("the initializer is granted access to secrets: %v", rule)
		}
	}

	// It must be able to finish. A workspace whose initializer cannot remove
	// itself from status.initializers never becomes Ready, which is a hang
	// rather than an error.
	var canFinish bool
	for _, rule := range wt.Spec.InitializerPermissions {
		if !slices.Contains(rule.APIGroups, "core.kcp.io") || !slices.Contains(rule.Resources, "logicalclusters/status") {
			continue
		}
		canFinish = slices.Contains(rule.Verbs, "patch") || slices.Contains(rule.Verbs, "update")
	}
	if !canFinish {
		t.Error("the initializer cannot patch logicalclusters/status, so a workspace of this type never becomes Ready")
	}
}

// Extending universal keeps a Cluster API workspace an ordinary workspace: it
// can hold children and live where a universal workspace can.
func TestWorkspaceTypeExtendsUniversal(t *testing.T) {
	wt := NewWorkspaceType("root", DefaultExports())

	if !slices.Contains(wt.Spec.Extend.With, UniversalType) {
		t.Errorf("the WorkspaceType extends %v, want it to extend %v", wt.Spec.Extend.With, UniversalType)
	}
}
