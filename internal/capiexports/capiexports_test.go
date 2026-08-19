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
	"slices"
	"testing"
)

// An identity hash is not decoration: an empty one means "a core type", so a
// provider claim written with an empty identity claims something other than
// what was asked for. Until the identity is known, the claim is left out.
func TestClaimsOmitProviderClaimsWithoutIdentities(t *testing.T) {
	claims := Core().Claims(nil)

	for _, c := range claims {
		if c.Group != "" {
			t.Errorf("claim %s.%s was emitted with no identity for its export", c.Resource, c.Group)
		}
	}
	if len(claims) != len(Core().CoreClaims) {
		t.Errorf("Claims(nil) returned %d claims, want the %d core-type ones", len(claims), len(Core().CoreClaims))
	}
}

func TestClaimsCarryTheOtherExportsIdentity(t *testing.T) {
	identities := Identities{
		BootstrapExport:    "bootstrap-hash",
		ControlPlaneExport: "controlplane-hash",
		InfraExport:        "infra-hash",
	}
	claims := Core().Claims(identities)

	want := map[string]string{
		"kubeadmconfigs":               "bootstrap-hash",
		"kubeadmconfigtemplates":       "bootstrap-hash",
		"kubeadmcontrolplanes":         "controlplane-hash",
		"kubeadmcontrolplanetemplates": "controlplane-hash",
		"devclusters":                  "infra-hash",
		"devmachines":                  "infra-hash",
		"devmachinetemplates":          "infra-hash",
		"secrets":                      "",
		"configmaps":                   "",
	}
	got := map[string]string{}
	for _, c := range claims {
		got[c.Resource] = c.IdentityHash
		if len(c.Verbs) == 0 {
			t.Errorf("claim for %s carries no verbs: v1alpha2 requires at least one, and a claim granting nothing grants nothing", c.Resource)
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

// The claim list is compared against what is already on the export to decide
// whether to write, so an unstable order would rewrite the export forever.
func TestClaimsAreDeterministic(t *testing.T) {
	identities := Identities{BootstrapExport: "b", ControlPlaneExport: "c", InfraExport: "i"}

	first := Core().Claims(identities)
	for range 5 {
		next := Core().Claims(identities)
		if !slices.EqualFunc(first, next, sameClaim) {
			t.Fatalf("Claims() is not deterministic:\n%v\n%v", first, next)
		}
	}
}

func TestMissingIdentities(t *testing.T) {
	all := Identities{BootstrapExport: "b", ControlPlaneExport: "c", InfraExport: "i"}

	got := Core().MissingIdentities(Identities{ControlPlaneExport: "c", InfraExport: "i"})
	if len(got) != 1 || got[0] != BootstrapExport {
		t.Errorf("MissingIdentities() = %v, want [%s]", got, BootstrapExport)
	}

	if got := Core().MissingIdentities(all); len(got) != 0 {
		t.Errorf("MissingIdentities() with every identity known = %v, want none", got)
	}

	// An identity that is present but empty is missing, not known. kcp assigns
	// the hash asynchronously, so the field exists before it means anything.
	empty := Identities{BootstrapExport: "", ControlPlaneExport: "c", InfraExport: "i"}
	if got := Core().MissingIdentities(empty); len(got) != 1 {
		t.Errorf("MissingIdentities() with an empty identity = %v, want [%s]", got, BootstrapExport)
	}
}

// The topology has to be closed: every export a provider claims from must be
// one this repository publishes, or the claim can never resolve and the
// deployment waits forever on an identity nobody will assign.
func TestEveryClaimedExportIsPublished(t *testing.T) {
	published := map[string]bool{}
	for _, p := range All() {
		published[p.Export] = true
	}

	for _, p := range All() {
		for export := range p.ProviderClaims {
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
// core reconcilers resolve references into both other providers, and both of
// those watch core's types.
func TestClaimGraphMatchesTheWiring(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		export   string
		resource string
	}{
		{Core(), BootstrapExport, "kubeadmconfigs"},
		{Core(), InfraExport, "devclusters"},
		{Core(), InfraExport, "devmachines"},
		{Bootstrap(), CoreExport, "clusters"},
		{Bootstrap(), CoreExport, "machines"},
		// The bootstrap provider reads the control plane a Machine belongs to,
		// to resolve its version.
		{Bootstrap(), ControlPlaneExport, "kubeadmcontrolplanes"},
		// Reconciling a worker's config walks the owner chain up from its
		// Machine, so the links in it have to be readable.
		{Bootstrap(), CoreExport, "machinesets"},
		{Bootstrap(), CoreExport, "machinedeployments"},
		// The control plane provider authors what it claims from these two:
		// it creates the Machines its control plane is made of and the
		// KubeadmConfigs that bootstrap them.
		{ControlPlane(), CoreExport, "machines"},
		{ControlPlane(), BootstrapExport, "kubeadmconfigs"},
		{ControlPlane(), InfraExport, "devmachines"},
		{Core(), ControlPlaneExport, "kubeadmcontrolplanes"},
		{Infrastructure(), CoreExport, "clusters"},
		{Infrastructure(), CoreExport, "machines"},
	} {
		resources, ok := tc.provider.ProviderClaims[tc.export]
		if !ok {
			t.Errorf("%s claims nothing from %s", tc.provider.Export, tc.export)
			continue
		}
		if !slices.ContainsFunc(resources, func(r Resource) bool { return r.Resource == tc.resource }) {
			t.Errorf("%s does not claim %s from %s", tc.provider.Export, tc.resource, tc.export)
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
