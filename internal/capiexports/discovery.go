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
	"context"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// Contract is the Cluster API provider contract an APIExport serves.
//
// Cluster API is defined by four contracts and one process per contract, and
// what a controller may do with another's objects follows from the pair of
// contracts rather than from the provider's name: core owns whatever an
// infrastructure provider publishes, whichever provider that is. Naming the
// contract on the export is therefore what makes a claim list derivable
// instead of hand-written - ADR-0001's open question 3, settled here.
type Contract string

const (
	// ContractCore is the Cluster API core provider. It is the one contract
	// nothing discovers: core's own types are named in this package because a
	// provider is written against them, and an installation has exactly one.
	ContractCore Contract = "core"

	// ContractBootstrap is a bootstrap provider - it turns a Machine into
	// bootstrap data.
	ContractBootstrap Contract = "bootstrap"

	// ContractControlPlane is a control plane provider.
	ContractControlPlane Contract = "control-plane"

	// ContractInfrastructure is an infrastructure provider.
	ContractInfrastructure Contract = "infrastructure"
)

// ContractLabel is how an APIExport says which Cluster API provider contract
// it serves, and so how this project recognises a provider it has never heard
// of.
//
// The convention is a label rather than an annotation because it is a
// selector: the claim controller lists provider exports by it. It lives in the
// `cluster.x-k8s.io` domain because it is a statement about Cluster API, not
// about kcp or about this repository - a third-party provider setting it is
// saying "I implement this contract", which is the same thing its
// `cluster.x-k8s.io/provider` label says to `clusterctl`.
const ContractLabel = "cluster.x-k8s.io/provider-contract"

// KnownContracts are the contracts a provider export may declare. A label
// value outside this set is ignored rather than guessed at: an export saying
// something this project does not understand should get no claims, not
// arbitrary ones.
var KnownContracts = []Contract{ContractCore, ContractBootstrap, ContractControlPlane, ContractInfrastructure}

// DiscoveredProvider is one provider APIExport as the claim controller found
// it: what it calls itself, what contract it serves, the identity a claim on
// its resources must carry, and the resources it publishes.
type DiscoveredProvider struct {
	// Export is the APIExport's name.
	Export string

	// Contract is what its ContractLabel said.
	Contract Contract

	// IdentityHash is the server-assigned identity every claim on this
	// export's resources carries.
	IdentityHash string

	// Resources are the group/resource pairs the export publishes, in the
	// order the export lists them.
	Resources []Resource
}

// Discover returns the provider exports among exports, sorted by name.
//
// An export with no contract label is not a provider and is skipped; so is one
// whose contract this project does not know, and so is one the server has not
// yet given an identity hash. The last is the reason this is a controller and
// not a one-shot: an export is published before it is identified, and a claim
// written with an empty identity hash does not mean "any export", it means "a
// built-in type".
func Discover(exports []apisv1alpha2.APIExport) Discovery {
	discovered := make(Discovery, 0, len(exports))
	for i := range exports {
		export := &exports[i]
		contract := Contract(export.Labels[ContractLabel])
		if !slices.Contains(KnownContracts, contract) {
			continue
		}
		if export.Status.IdentityHash == "" {
			continue
		}

		resources := make([]Resource, 0, len(export.Spec.Resources))
		for _, r := range export.Spec.Resources {
			resources = append(resources, Resource{Group: r.Group, Resource: r.Name})
		}
		discovered = append(discovered, DiscoveredProvider{
			Export:       export.Name,
			Contract:     contract,
			IdentityHash: export.Status.IdentityHash,
			Resources:    resources,
		})
	}

	slices.SortFunc(discovered, func(a, b DiscoveredProvider) int {
		return compare(a.Export, b.Export)
	})
	return discovered
}

// Identities returns the identity hash of each discovered export, in the shape
// the static claims take.
func (d Discovery) Identities() Identities {
	identities := Identities{}
	for _, p := range d {
		identities[p.Export] = p.IdentityHash
	}
	return identities
}

// Discovery is the set of provider exports an installation has, as Discover
// returned them.
type Discovery []DiscoveredProvider

// ReconcileClaims brings every export in providers to the claim list its
// contract and the installation's discovered providers imply, and reports the
// exports it rewrote.
//
// This is the whole of ADR-0001's "self-maintaining claim list": onboarding a
// provider is publishing a labelled APIExport, and nothing else. Every tenant
// workspace whose core APIBinding is managed by the Cluster API WorkspaceType
// then accepts the new claims without being asked, because kcp's own
// `Maintain` lifecycle rebuilds an accepted-claim list from the export's on
// every export update.
//
// Idempotent, and quiet when nothing changed: the claim list is compared
// against what is on the export, so a reconcile that finds no new provider
// writes nothing.
func ReconcileClaims(ctx context.Context, cl client.Client, providers []Provider) ([]string, error) {
	discovered, err := DiscoverIn(ctx, cl)
	if err != nil {
		return nil, err
	}
	identities := discovered.Identities()

	var updated []string
	for _, p := range providers {
		changed, err := setClaims(ctx, cl, p.Export, p.Claims(identities, discovered))
		if err != nil {
			return updated, err
		}
		if changed {
			updated = append(updated, p.Export)
		}
	}
	return updated, nil
}
