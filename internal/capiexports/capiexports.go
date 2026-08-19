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

// Package capiexports describes Cluster API as kcp sees it: one APIExport per
// provider, each publishing its own API group, each claiming what its
// controllers need from the others.
//
// # Why one export per provider
//
// Cluster API deploys one process per provider, and an export is the unit a
// deployment consumes: its endpoint slice is what a manager watches, and its
// claims are what that manager may reach. One export for everything makes
// every provider's process able to see every other provider's objects, which
// is neither how Cluster API is deployed nor something a tenant should have to
// accept wholesale. It also publishes the *test* infrastructure provider's
// types into installations that should never see them.
//
// # The claims are the interesting part
//
// Splitting the types does not decouple the controllers, because Cluster API's
// controllers genuinely reference each other: the Cluster reconciler resolves
// spec.infrastructureRef, takes ownership of what it finds and reads its
// status; the bootstrap and infrastructure providers watch Clusters and
// Machines. So each export claims what its own controllers touch, and the
// claim graph runs both ways.
//
// A claim on another export's resource carries that export's **identity hash**,
// which the server assigns and which differs per kcp instance. It cannot be
// written into a manifest ahead of time, which is why this package resolves
// them at run time and keeps the claim lists current - the self-maintaining
// permission-claim list ADR-0001 anticipated.
package capiexports

import (
	"context"
	"fmt"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
)

// Export names. They are the APIExport, the APIExportEndpointSlice and the
// APIBinding each workspace creates, so they are what appears in a deployment's
// --endpoint-slice-name.
const (
	CoreExport         = "cluster-api-core"
	BootstrapExport    = "cluster-api-bootstrap-kubeadm"
	ControlPlaneExport = "cluster-api-controlplane-kubeadm"
	InfraExport        = "cluster-api-dev-infrastructure"
)

// Resource is a group and resource, with no version: an APIExport publishes
// schemas and a claim names resources, and neither is versioned.
type Resource struct {
	Group    string
	Resource string

	// Verbs is what the claiming provider may do with it. Empty means every
	// verb, which is what a v1alpha1 claim granted and what this project asked
	// for before v1alpha2 gave it a way to say otherwise.
	//
	// Narrowing these is worth doing and is not free to get wrong: a verb a
	// controller needs and does not have shows up as a write refused deep in a
	// reconcile, not as a validation error at bind time. Anything narrowed
	// here has to be exercised end to end - the demo brings a cluster up and
	// test/integration/teardown takes one down again, which between them cover
	// the create and delete paths.
	Verbs []string
}

// Provider is one provider's export: what it publishes, and what it needs from
// elsewhere.
type Provider struct {
	// Export is the APIExport's name.
	Export string

	// Module and CRDs locate the CRD manifests this export publishes,
	// resolved from the pinned Cluster API modules at run time so they cannot
	// disagree with the code they are published for.
	Module string
	CRDs   []string

	// CoreClaims are claims on Kubernetes' own types - Secrets, ConfigMaps.
	// They carry no identity hash: kcp serves them in every workspace and
	// there is no export to attribute them to.
	CoreClaims []Resource

	// ProviderClaims are claims on resources another export publishes, keyed
	// by that export's name. Each becomes a claim carrying that export's
	// identity hash, resolved when the export exists.
	ProviderClaims map[string][]Resource
}

// Verb sets, named for what they let a controller do. Every claim carries
// one, because v1alpha2 requires at least one verb and because "whatever it
// turns out to need" is how a claim ends up granting delete on a type its
// holder only ever reads.
//
// # Where these come from
//
// Not from reading reconcilers, which is how this nearly went wrong: the
// obvious guess is that a provider only reads another's *templates*, and it is
// wrong — Cluster API patches owner references onto templates so they are
// garbage-collected with the cluster. The source is upstream's own
// `+kubebuilder:rbac` markers, which are the authoritative statement of what
// each controller does with each type, and which the fork carries unchanged.
// Each claim below cites the marker it came from.
var (
	// read is what watching and dereferencing needs.
	read = []string{"get", "list", "watch"}
	// own is read plus the whole write path: create, take ownership of,
	// update the status of, and delete.
	own = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	// core's Secret marker, which grants everything except delete: it writes
	// kubeconfigs and reads them back, and the objects it writes are deleted
	// with their owners rather than by it.
	writeNoDelete = []string{"get", "list", "watch", "create", "update", "patch"}
	// the dev provider's Secret marker: it reads kubeconfigs and patches them,
	// and creates none.
	readPatch = []string{"get", "list", "watch", "patch"}
	// the dev provider's Machine markers, unioned: it deletes a Machine whose
	// backend has gone and patches the ones it provisions, but creates none.
	adoptDelete = []string{"get", "list", "watch", "update", "patch", "delete"}
)

// Core types every provider needs. Secrets because bootstrap data, cluster
// certificates and workload-cluster kubeconfigs are all Secrets; ConfigMaps
// because the control plane init lock is one.
var (
	secrets    = Resource{Resource: "secrets"}
	configMaps = Resource{Resource: "configmaps"}

	clusters           = Resource{Group: "cluster.x-k8s.io", Resource: "clusters"}
	machines           = Resource{Group: "cluster.x-k8s.io", Resource: "machines"}
	machineSets        = Resource{Group: "cluster.x-k8s.io", Resource: "machinesets"}
	machineDeployments = Resource{Group: "cluster.x-k8s.io", Resource: "machinedeployments"}

	kubeadmConfigs         = Resource{Group: "bootstrap.cluster.x-k8s.io", Resource: "kubeadmconfigs"}
	kubeadmConfigTemplates = Resource{Group: "bootstrap.cluster.x-k8s.io", Resource: "kubeadmconfigtemplates"}

	kubeadmControlPlanes         = Resource{Group: "controlplane.cluster.x-k8s.io", Resource: "kubeadmcontrolplanes"}
	kubeadmControlPlaneTemplates = Resource{Group: "controlplane.cluster.x-k8s.io", Resource: "kubeadmcontrolplanetemplates"}

	devClusters         = Resource{Group: "infrastructure.cluster.x-k8s.io", Resource: "devclusters"}
	devMachines         = Resource{Group: "infrastructure.cluster.x-k8s.io", Resource: "devmachines"}
	devMachineTemplates = Resource{Group: "infrastructure.cluster.x-k8s.io", Resource: "devmachinetemplates"}
)

// to returns r claimed for the given verbs. It reads at the call site as
// "devclusters, to own" — which is the sentence a claim is.
func to(r Resource, verbs []string) Resource {
	r.Verbs = verbs
	return r
}

// Core is the core provider's export.
//
// Its claims are the references its reconcilers resolve. The Cluster
// reconciler dereferences spec.infrastructureRef and spec.controlPlaneRef and
// the Machine reconciler spec.bootstrap.configRef and spec.infrastructureRef —
// and all of them write to what they find, because taking ownership of an
// external object is how the owning provider learns to act on it.
func Core() Provider {
	return Provider{
		Export: CoreExport,
		Module: kcpfixtures.ModuleClusterAPI,
		CRDs: []string{
			"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
		},
		// Secrets without delete, and ConfigMaps read-only: core's markers
		// grant `get;list;watch;create;patch;update` on Secrets and say
		// nothing at all about ConfigMaps.
		CoreClaims: []Resource{to(secrets, writeNoDelete), to(configMaps, read)},
		ProviderClaims: map[string][]Resource{
			// Everything, on all three groups. Core's marker is
			// `resources=*, verbs=get;list;watch;create;update;patch;delete`
			// across infrastructure, bootstrap and controlplane, and it earns
			// it: the Cluster reconciler deletes the control plane and the
			// infrastructure cluster, and the Machine reconciler deletes the
			// bootstrap config and the infrastructure machine.
			BootstrapExport:    {to(kubeadmConfigs, own), to(kubeadmConfigTemplates, own)},
			ControlPlaneExport: {to(kubeadmControlPlanes, own), to(kubeadmControlPlaneTemplates, own)},
			InfraExport:        {to(devClusters, own), to(devMachines, own), to(devMachineTemplates, own)},
		},
	}
}

// ControlPlane is the kubeadm control plane provider's export.
//
// Its claims are the widest of any provider's, and for a reason worth stating
// rather than absorbing: it does not only watch core's types, it authors them.
// A KubeadmControlPlane creates the Machines its control plane is made of and
// the KubeadmConfigs that bootstrap them, so it claims those from two other
// exports for writing. It is the case that most repays scoping claims by verb
// once this project expresses them in kcp's v1alpha2 shape.
func ControlPlane() Provider {
	return Provider{
		Export: ControlPlaneExport,
		Module: kcpfixtures.ModuleClusterAPI,
		CRDs: []string{
			"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanes.yaml",
			"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanetemplates.yaml",
		},
		// Secrets in full - it writes the cluster's certificate authorities
		// and the admin kubeconfig - and ConfigMaps read-only, which is more
		// than its markers ask for: they name no ConfigMaps at all.
		CoreClaims: []Resource{to(secrets, own), to(configMaps, read)},
		ProviderClaims: map[string][]Resource{
			// Clusters read-only (`clusters;clusters/status, get;list;watch`),
			// Machines in full: it creates and deletes the Machines its
			// control plane is made of.
			CoreExport: {to(clusters, read), to(machines, own)},
			// It authors the KubeadmConfig for each Machine it creates, and
			// its marker grants the bootstrap group in full.
			BootstrapExport: {to(kubeadmConfigs, own), to(kubeadmConfigTemplates, own)},
			// It reads the infrastructure template its machineTemplate names
			// and *creates* a DevMachine per control plane Machine from it, so
			// this claim is a write as much as a read.
			InfraExport: {to(devMachines, own), to(devMachineTemplates, own)},
		},
	}
}

// Bootstrap is the kubeadm bootstrap provider's export.
//
// It watches Machines and Clusters, and it writes Secrets: the bootstrap data
// it produces, and the cluster certificates it generates for the first control
// plane machine of a cluster with no control plane provider.
func Bootstrap() Provider {
	return Provider{
		Export: BootstrapExport,
		Module: kcpfixtures.ModuleClusterAPI,
		CRDs: []string{
			"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigs.yaml",
			"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigtemplates.yaml",
		},
		// Both in full: its marker is
		// `secrets;configmaps, get;list;watch;create;update;patch;delete`.
		// The data secret is its output and the init lock is a ConfigMap it
		// takes and releases.
		CoreClaims: []Resource{to(secrets, own), to(configMaps, own)},
		ProviderClaims: map[string][]Resource{
			// All read-only, which is its marker exactly:
			// `clusters;clusters/status;machinesets;machines;machines/status,
			// get;list;watch`. This provider writes nothing of core's.
			//
			// MachineSets and MachineDeployments as well as the types it
			// watches: reconciling a worker's config walks the owner chain
			// from its Machine up, and a link it cannot read fails the
			// reconcile rather than shortening the log line.
			CoreExport: {to(clusters, read), to(machines, read), to(machineSets, read), to(machineDeployments, read)},
			// It reads the KubeadmControlPlane a Machine belongs to, to
			// resolve the control plane's version when preparing a config.
			// Read-only, and its marker says so.
			ControlPlaneExport: {to(kubeadmControlPlanes, read)},
		},
	}
}

// Infrastructure is the docker/dev infrastructure provider's export.
//
// Its own reconcilers watch Clusters and Machines, and its ClusterCache reads
// workload-cluster kubeconfig Secrets.
func Infrastructure() Provider {
	return Provider{
		Export: InfraExport,
		Module: kcpfixtures.ModuleClusterAPITest,
		CRDs: []string{
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
			// The template is published because the control plane provider's
			// machineTemplate refers to one; nothing reconciles it.
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachinetemplates.yaml",
		},
		// Read and patch, no create: its markers are
		// `secrets, get;list;watch` and `secrets, get;list;watch;patch`. It
		// reads a workload cluster's kubeconfig; it does not write one.
		CoreClaims: []Resource{to(secrets, readPatch)},
		ProviderClaims: map[string][]Resource{
			// Clusters read-only; Machines read, patch and delete, which is
			// the union of its two Machine markers - it deletes a Machine
			// whose backend has gone and patches the ones it provisions,
			// and creates none.
			CoreExport: {to(clusters, read), to(machines, adoptDelete)},
		},
	}
}

// All is every provider this repository wires, in publication order.
func All() []Provider {
	return []Provider{Core(), Bootstrap(), ControlPlane(), Infrastructure()}
}

// Identities maps an export's name to the identity hash the server assigned it.
type Identities map[string]string

// Claims returns the permission claims an export should declare, given the
// identities of the exports it claims from.
//
// It is a pure function of the topology so that what a deployment asks for can
// be asserted without a server, and it is deterministic - sorted by group and
// resource - so that a claim list can be compared with what is on the export
// and left alone when it already matches.
//
// An export whose identity is not yet known contributes no claims rather than a
// claim with an empty identity. An empty identity hash does not mean "any
// export"; it means "a core type", so writing one for a provider resource
// would silently claim something else.
func (p Provider) Claims(identities Identities) []apisv1alpha2.PermissionClaim {
	claims := make([]apisv1alpha2.PermissionClaim, 0, len(p.CoreClaims))
	for _, r := range p.CoreClaims {
		claims = append(claims, claim(r, ""))
	}

	exports := make([]string, 0, len(p.ProviderClaims))
	for export := range p.ProviderClaims {
		exports = append(exports, export)
	}
	slices.Sort(exports)

	for _, export := range exports {
		identity, ok := identities[export]
		if !ok || identity == "" {
			continue
		}
		for _, r := range p.ProviderClaims[export] {
			claims = append(claims, claim(r, identity))
		}
	}

	slices.SortFunc(claims, func(a, b apisv1alpha2.PermissionClaim) int {
		if a.Group != b.Group {
			return compare(a.Group, b.Group)
		}
		return compare(a.Resource, b.Resource)
	})
	return claims
}

// MissingIdentities names the exports p claims from whose identity is not yet
// known, so a caller can say what it is waiting for rather than reporting a
// short claim list as success.
func (p Provider) MissingIdentities(identities Identities) []string {
	missing := make([]string, 0, len(p.ProviderClaims))
	for export := range p.ProviderClaims {
		if identity, ok := identities[export]; !ok || identity == "" {
			missing = append(missing, export)
		}
	}
	slices.Sort(missing)
	return missing
}

func claim(r Resource, identity string) apisv1alpha2.PermissionClaim {
	verbs := r.Verbs
	if len(verbs) == 0 {
		// v1alpha2 requires at least one verb, so "unset" has to become
		// something. Every verb is what a v1alpha1 claim granted, which keeps
		// an unnarrowed resource behaving as it did.
		verbs = []string{"*"}
	}
	return apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: r.Group, Resource: r.Resource},
		IdentityHash:  identity,
		Verbs:         verbs,
	}
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Publish creates every export, then fills in the claims that need another
// export's identity.
//
// Two passes, and the order is forced rather than chosen: the claims are
// mutually referential - core claims the providers' types and they claim
// core's - so no single ordering of one-pass creates can resolve them. The
// first pass publishes schemas and the claims that need no identity; the
// second resolves identities and updates each export's claim list.
//
// Idempotent, so it doubles as the reconcile a long-lived deployment would run:
// an export that already exists is brought to the requested shape, and one
// whose claims already match is left alone.
func Publish(ctx context.Context, cl client.Client, providers []Provider, timeout time.Duration) (Identities, error) {
	if timeout == 0 {
		timeout = time.Minute
	}

	for _, p := range providers {
		paths, err := p.manifestPaths()
		if err != nil {
			return nil, err
		}
		if err := kcpfixtures.PublishAPIExport(ctx, cl, kcpfixtures.PublishAPIExportOptions{
			ExportName:   p.Export,
			SchemaPrefix: "v1",
			CRDPaths:     paths,
			// Only the identity-free claims in the first pass. The rest need
			// identities that do not exist until every export does.
			PermissionClaims: p.Claims(nil),
			CRDTransform:     kcpfixtures.KeepStorageVersion,
		}); err != nil {
			return nil, fmt.Errorf("publishing APIExport %s: %w", p.Export, err)
		}
	}

	identities, err := WaitForIdentities(ctx, cl, providers, timeout)
	if err != nil {
		return nil, err
	}

	// A claim on an export nobody published cannot resolve, so what is checked
	// is the exports in *this* set. A topology that leaves a provider out - the
	// demo does, when it is asked for no machines - simply does not claim from
	// it, and a reference to that provider's types then fails visibly at the
	// reference rather than silently at the claim.
	published := make(map[string]bool, len(providers))
	for _, p := range providers {
		published[p.Export] = true
	}

	for _, p := range providers {
		for _, export := range p.MissingIdentities(identities) {
			if published[export] {
				return nil, fmt.Errorf("APIExport %s claims from %s, whose identity is unknown", p.Export, export)
			}
		}
		if err := setClaims(ctx, cl, p.Export, p.Claims(identities)); err != nil {
			return nil, err
		}
	}

	return identities, nil
}

// WaitForIdentities reads each export's server-assigned identity hash.
func WaitForIdentities(ctx context.Context, cl client.Client, providers []Provider, timeout time.Duration) (Identities, error) {
	identities := Identities{}
	for _, p := range providers {
		var identity string
		err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
			export := &apisv1alpha2.APIExport{}
			if err := cl.Get(ctx, client.ObjectKey{Name: p.Export}, export); err != nil {
				return false, nil //nolint:nilerr // transient; keep polling until timeout.
			}
			identity = export.Status.IdentityHash
			return identity != "", nil
		})
		if err != nil {
			return nil, fmt.Errorf("APIExport %s never got an identity hash: %w", p.Export, err)
		}
		identities[p.Export] = identity
	}
	return identities, nil
}

func setClaims(ctx context.Context, cl client.Client, name string, claims []apisv1alpha2.PermissionClaim) error {
	export := &apisv1alpha2.APIExport{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, export); err != nil {
		return fmt.Errorf("reading APIExport %s: %w", name, err)
	}
	if slices.EqualFunc(export.Spec.PermissionClaims, claims, sameClaim) {
		return nil
	}
	export.Spec.PermissionClaims = claims
	if err := cl.Update(ctx, export); err != nil {
		return fmt.Errorf("updating the claims on APIExport %s: %w", name, err)
	}
	return nil
}

func sameClaim(a, b apisv1alpha2.PermissionClaim) bool {
	return a.Group == b.Group && a.Resource == b.Resource &&
		a.IdentityHash == b.IdentityHash && slices.Equal(a.Verbs, b.Verbs)
}

func (p Provider) manifestPaths() ([]string, error) {
	paths, err := kcpfixtures.MustManifestPaths(p.Module, p.CRDs...)
	if err != nil {
		return nil, fmt.Errorf("resolving CRD manifests for %s: %w", p.Export, err)
	}
	return paths, nil
}
