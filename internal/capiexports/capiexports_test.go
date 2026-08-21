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

package capiexports

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// installed is the four in-repo exports as kcp reports them once published:
// labelled with their contract, identified, and listing what they publish.
//
// It is what the claim controller sees, so a claim list computed from it is
// the claim list a real installation of this repository gets.
func installed(t *testing.T) Discovery {
	t.Helper()

	exports := make([]apisv1alpha2.APIExport, 0, len(All()))
	for _, p := range All() {
		exports = append(exports, apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: p.Export, Labels: p.labels()},
			Spec:       apisv1alpha2.APIExportSpec{Resources: publishedResources(t, p)},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: identityOf(p.Export)},
		})
	}
	return Discover(exports)
}

// identityOf is a stand-in for the hash the server assigns. Derived from the
// name so a failure names the export rather than a hash nobody can place.
func identityOf(export string) string { return export + "-identity" }

// publishedResources is what an export's CRD manifests publish, read off the
// manifest names: controller-gen writes one file per CRD as
// `<group>_<plural>.yaml`, which is the group and resource a claim needs.
//
// In the test rather than in the package because production code should not
// learn a filename convention it can read out of the manifest itself - and
// because the point of this fixture is to derive the resource list
// *independently* of the claim list it is used to check.
func publishedResources(t *testing.T, p Provider) []apisv1alpha2.ResourceSchema {
	t.Helper()

	resources := make([]apisv1alpha2.ResourceSchema, 0, len(p.CRDs))
	for _, path := range p.CRDs {
		base := strings.TrimSuffix(filepath.Base(path), ".yaml")
		group, plural, ok := strings.Cut(base, "_")
		if !ok {
			t.Fatalf("CRD manifest %q is not named <group>_<plural>.yaml, so this fixture cannot read it", path)
		}
		resources = append(resources, apisv1alpha2.ResourceSchema{
			Name:   plural,
			Group:  group,
			Schema: fmt.Sprintf("v1.%s.%s", plural, group),
		})
	}
	return resources
}

func claimKeys(claims []apisv1alpha2.PermissionClaim) map[string][]string {
	got := map[string][]string{}
	for _, c := range claims {
		key := c.Resource
		if c.Group != "" {
			key = c.Resource + "." + c.Group
		}
		got[key] = c.Verbs
	}
	return got
}

// An identity hash is not decoration: an empty one means "a core type", so a
// provider claim written with an empty identity claims something other than
// what was asked for. Until the identity is known, the claim is left out.
func TestClaimsOmitProviderClaimsWithoutIdentities(t *testing.T) {
	claims := Core().Claims(nil, nil)

	for _, c := range claims {
		if c.Group != "" {
			t.Errorf("claim %s.%s was emitted with no identity for its export", c.Resource, c.Group)
		}
	}
	if len(claims) != len(Core().CoreClaims) {
		t.Errorf("Claims(nil, nil) returned %d claims, want the %d core-type ones", len(claims), len(Core().CoreClaims))
	}
}

// The claim list core needs is not written down anywhere: it is derived from
// the providers an installation turns out to have. This is that derivation,
// against the four this repository ships.
func TestCoreClaimsEveryProviderResourceItDiscovers(t *testing.T) {
	discovery := installed(t)
	claims := Core().Claims(discovery.Identities(), discovery)

	want := map[string]string{
		"kubeadmconfigs.bootstrap.cluster.x-k8s.io":                  identityOf(BootstrapExport),
		"kubeadmconfigtemplates.bootstrap.cluster.x-k8s.io":          identityOf(BootstrapExport),
		"kubeadmcontrolplanes.controlplane.cluster.x-k8s.io":         identityOf(ControlPlaneExport),
		"kubeadmcontrolplanetemplates.controlplane.cluster.x-k8s.io": identityOf(ControlPlaneExport),
		"devclusters.infrastructure.cluster.x-k8s.io":                identityOf(InfraExport),
		"devclustertemplates.infrastructure.cluster.x-k8s.io":        identityOf(InfraExport),
		"devmachines.infrastructure.cluster.x-k8s.io":                identityOf(InfraExport),
		"devmachinetemplates.infrastructure.cluster.x-k8s.io":        identityOf(InfraExport),
		"secrets":    "",
		"configmaps": "",
	}

	got := map[string]string{}
	for _, c := range claims {
		key := c.Resource
		if c.Group != "" {
			key = c.Resource + "." + c.Group
		}
		got[key] = c.IdentityHash
		if len(c.Verbs) == 0 {
			t.Errorf("claim for %s carries no verbs: v1alpha2 requires at least one, and a claim granting nothing grants nothing", key)
		}
	}

	for resource, identity := range want {
		if got[resource] != identity {
			t.Errorf("claim for %s carries identity %q, want %q", resource, got[resource], identity)
		}
	}
	if len(got) != len(want) {
		t.Errorf("Claims() returned %v, want exactly %v", got, want)
	}
}

// Core publishes no ClusterClass claim on itself and no provider claim on a
// provider that is not there. A run that publishes only core and the
// infrastructure provider - which the demo does, when it is asked for no
// machines - must not claim the bootstrap provider's types.
func TestClaimsOnlyCoverDiscoveredProviders(t *testing.T) {
	discovery := installed(t)
	onlyInfra := Discovery{}
	for _, p := range discovery {
		if p.Contract == ContractInfrastructure {
			onlyInfra = append(onlyInfra, p)
		}
	}

	for key := range claimKeys(Core().Claims(nil, onlyInfra)) {
		if strings.Contains(key, "bootstrap.cluster.x-k8s.io") || strings.Contains(key, "controlplane.cluster.x-k8s.io") {
			t.Errorf("core claims %s with no such provider installed", key)
		}
	}
}

// An export publishes its own types and kcp serves them to it without a claim.
// Claiming them would name an identity hash of its own, which kcp rejects.
func TestNoProviderClaimsItsOwnResources(t *testing.T) {
	discovery := installed(t)
	for _, p := range All() {
		for _, c := range p.Claims(discovery.Identities(), discovery) {
			if c.IdentityHash == identityOf(p.Export) {
				t.Errorf("%s claims %s.%s under its own identity", p.Export, c.Resource, c.Group)
			}
		}
	}
}

// The claim list is compared against what is already on the export to decide
// whether to write, so an unstable order would rewrite the export forever.
func TestClaimsAreDeterministic(t *testing.T) {
	discovery := installed(t)
	identities := discovery.Identities()

	for _, p := range All() {
		first := p.Claims(identities, discovery)
		for range 5 {
			next := p.Claims(identities, discovery)
			if !slices.EqualFunc(first, next, sameClaim) {
				t.Fatalf("%s's Claims() is not deterministic:\n%v\n%v", p.Export, first, next)
			}
		}
	}
}

func TestMissingIdentities(t *testing.T) {
	all := Identities{CoreExport: "c"}

	got := Bootstrap().MissingIdentities(Identities{})
	if len(got) != 1 || got[0] != CoreExport {
		t.Errorf("MissingIdentities() = %v, want [%s]", got, CoreExport)
	}

	if got := Bootstrap().MissingIdentities(all); len(got) != 0 {
		t.Errorf("MissingIdentities() with every identity known = %v, want none", got)
	}

	// An identity that is present but empty is missing, not known. kcp assigns
	// the hash asynchronously, so the field exists before it means anything.
	empty := Identities{CoreExport: ""}
	if got := Bootstrap().MissingIdentities(empty); len(got) != 1 {
		t.Errorf("MissingIdentities() with an empty identity = %v, want [%s]", got, CoreExport)
	}
}

// The topology has to be closed: every export a provider claims from by name
// must be one this repository publishes, or the claim can never resolve and
// the deployment waits forever on an identity nobody will assign.
//
// Only core is claimed by name. Everything else is claimed by contract, and a
// contract naming nothing installed simply produces no claims.
func TestEveryClaimedExportIsPublished(t *testing.T) {
	published := map[string]bool{}
	for _, p := range All() {
		published[p.Export] = true
	}

	for _, p := range All() {
		for export := range p.ProviderClaims {
			if export != CoreExport {
				t.Errorf("%s names %s in ProviderClaims; only core's own types are claimed by name", p.Export, export)
			}
			if !published[export] {
				t.Errorf("%s claims from %s, which no provider publishes", p.Export, export)
			}
			if export == p.Export {
				t.Errorf("%s claims from itself; it publishes those types", p.Export)
			}
		}
	}
}

// What each provider claims should be what its controllers actually touch.
// This is the cheapest guard against the list drifting from the wiring: the
// core reconcilers resolve references into every other provider, and every one
// of those watches core's types.
//
// Asserted against the resulting claims rather than against the declarations,
// because half of them are now derived and a test that read the declarations
// would stop covering the derived half.
func TestClaimGraphMatchesTheWiring(t *testing.T) {
	discovery := installed(t)
	identities := discovery.Identities()

	for _, tc := range []struct {
		provider Provider
		resource string
		verbs    []string
	}{
		{Core(), "kubeadmconfigs.bootstrap.cluster.x-k8s.io", own},
		{Core(), "devclusters.infrastructure.cluster.x-k8s.io", own},
		{Core(), "devmachines.infrastructure.cluster.x-k8s.io", own},
		{Core(), "kubeadmcontrolplanes.controlplane.cluster.x-k8s.io", own},
		{Bootstrap(), "clusters.cluster.x-k8s.io", read},
		{Bootstrap(), "machines.cluster.x-k8s.io", read},
		// The bootstrap provider reads the control plane a Machine belongs to,
		// to resolve its version - and writes none of core's types.
		{Bootstrap(), "kubeadmcontrolplanes.controlplane.cluster.x-k8s.io", read},
		// Reconciling a worker's config walks the owner chain up from its
		// Machine, so the links in it have to be readable.
		{Bootstrap(), "machinesets.cluster.x-k8s.io", read},
		{Bootstrap(), "machinedeployments.cluster.x-k8s.io", read},
		// The control plane provider authors what it claims from these two:
		// it creates the Machines its control plane is made of and the
		// KubeadmConfigs that bootstrap them.
		{ControlPlane(), "machines.cluster.x-k8s.io", own},
		{ControlPlane(), "kubeadmconfigs.bootstrap.cluster.x-k8s.io", own},
		{ControlPlane(), "devmachines.infrastructure.cluster.x-k8s.io", own},
		{Infrastructure(), "clusters.cluster.x-k8s.io", read},
		{Infrastructure(), "machines.cluster.x-k8s.io", adoptDelete},
	} {
		got := claimKeys(tc.provider.Claims(identities, discovery))
		verbs, ok := got[tc.resource]
		if !ok {
			t.Errorf("%s does not claim %s", tc.provider.Export, tc.resource)
			continue
		}
		if !slices.Equal(verbs, tc.verbs) {
			t.Errorf("%s claims %s for %v, want %v", tc.provider.Export, tc.resource, verbs, tc.verbs)
		}
	}
}

// Every provider writes Secrets or reads them: bootstrap data, cluster
// certificates, workload-cluster kubeconfigs.
func TestEveryProviderClaimsSecrets(t *testing.T) {
	for _, p := range All() {
		if !slices.ContainsFunc(p.CoreClaims, func(r Resource) bool { return r.Resource == secrets.Resource }) {
			t.Errorf("%s does not claim secrets", p.Export)
		}
	}
}

// TestEveryProvidersManifestsResolve is the check that catches a dependency
// bump moving the CRDs out from under an export.
//
// ManifestPath deliberately does not search or fall back, because the layout is
// not stable across releases — Cluster API's CRD bases moved between minor
// versions once already. Without this test the failure surfaces at run time in
// whatever was publishing the export, which is a long way from the go.mod line
// that caused it.
//
// It covers the Nutanix provider as well as the wired ones. That export is
// published from a module this repository imports no code from, so nothing else
// would notice if `go mod tidy` dropped it: the manifest-dependency anchor in
// internal/kcpfixtures is what keeps it in the build list, and this is what
// proves the anchor is still doing its job.
func TestEveryProvidersManifestsResolve(t *testing.T) {
	providers := append(All(), NutanixInfrastructure())

	for _, p := range providers {
		t.Run(p.Export, func(t *testing.T) {
			paths, err := p.manifestPaths()
			if err != nil {
				t.Fatalf("resolving manifests for %s: %v", p.Export, err)
			}
			if len(paths) != len(p.CRDs) {
				t.Errorf("resolved %d manifests for %s, want %d", len(paths), p.Export, len(p.CRDs))
			}
		})
	}
}

// The workspace onboarding export exists for two claims. Losing either turns
// its controller into one that reports success and reconciles nothing: without
// the APIBinding claim it sees only its own binding, and without the
// ClusterRole claim it can read what a workspace has enabled and do nothing
// about it.
func TestWorkspacesExportClaimsWhatItsControllerNeeds(t *testing.T) {
	claims := claimKeys(Workspaces().Claims(nil, installed(t)))

	if _, ok := claims["apibindings.apis.kcp.io"]; !ok {
		t.Error("the workspace export does not claim apibindings")
	}
	verbs, ok := claims["clusterroles.rbac.authorization.k8s.io"]
	if !ok {
		t.Fatal("the workspace export does not claim clusterroles")
	}
	if slices.Contains(verbs, "delete") || slices.Contains(verbs, "*") {
		t.Errorf("the workspace export claims clusterroles for %v: a role it did not create is not its to remove", verbs)
	}

	// It serves no contract, so it claims no provider's types however many are
	// installed - and nothing discovers it either.
	for key := range claims {
		if strings.HasSuffix(key, ".cluster.x-k8s.io") {
			t.Errorf("the workspace export claims %s; it reconciles no Cluster API type", key)
		}
	}
	if Workspaces().Contract != "" {
		t.Errorf("the workspace export declares contract %q; it serves none", Workspaces().Contract)
	}
}

func TestDiscoverSkipsWhatItCannotClaim(t *testing.T) {
	exports := []apisv1alpha2.APIExport{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unlabelled"},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: "h"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown-contract", Labels: map[string]string{ContractLabel: "storage"}},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: "h"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "not-yet-identified", Labels: map[string]string{ContractLabel: string(ContractInfrastructure)}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "usable", Labels: map[string]string{ContractLabel: string(ContractInfrastructure)}},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: "h"},
		},
	}

	got := Discover(exports)
	if len(got) != 1 || got[0].Export != "usable" {
		t.Fatalf("Discover() = %v, want only the labelled, identified export", got)
	}
}

// The claim list is written to the server when it differs from what is there,
// so discovery has to order its output rather than return it in list order.
func TestDiscoverIsSortedByExportName(t *testing.T) {
	exports := []apisv1alpha2.APIExport{}
	for _, name := range []string{"zulu", "alpha", "mike"} {
		exports = append(exports, apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{ContractLabel: string(ContractInfrastructure)}},
			Status:     apisv1alpha2.APIExportStatus{IdentityHash: name},
		})
	}

	got := Discover(exports)
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Export)
	}
	if !slices.IsSorted(names) {
		t.Errorf("Discover() returned %v, want them sorted", names)
	}
}

// Two claims on one resource are one permission in kcp, so the wider verb set
// has to win - and the result has to be ordered, or an export is rewritten
// every reconcile.
func TestUnionVerbs(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []string
		want []string
	}{
		{"orders by lifecycle, not alphabetically", []string{"delete", "get"}, nil, []string{"get", "delete"}},
		{"merges without duplicating", read, []string{"get", "patch"}, []string{"get", "list", "watch", "patch"}},
		{"a wildcard absorbs the rest", read, []string{"*"}, []string{"*"}},
		{"keeps a verb it has never heard of", []string{"get"}, []string{"bind"}, []string{"get", "bind"}},
		{"nothing is nothing", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unionVerbs(tc.a, tc.b); !slices.Equal(got, tc.want) {
				t.Errorf("unionVerbs(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			if got := unionVerbs(tc.b, tc.a); !slices.Equal(got, tc.want) {
				t.Errorf("unionVerbs(%v, %v) = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// A provider is recognised by its label and nothing else, so every export this
// repository publishes as a provider has to carry one - and the onboarding
// export, which is not a provider, must not.
func TestEveryProviderExportIsLabelledWithItsContract(t *testing.T) {
	for _, p := range All() {
		if p.Contract == "" {
			t.Errorf("%s declares no contract, so no claim controller will discover it", p.Export)
			continue
		}
		if !slices.Contains(KnownContracts, p.Contract) {
			t.Errorf("%s declares contract %q, which Discover ignores", p.Export, p.Contract)
		}
		if got := p.labels()[ContractLabel]; got != string(p.Contract) {
			t.Errorf("%s is published with %s=%q, want %q", p.Export, ContractLabel, got, p.Contract)
		}
	}
	if Workspaces().labels() != nil {
		t.Errorf("the workspace export is published with labels %v, want none", Workspaces().labels())
	}
}
