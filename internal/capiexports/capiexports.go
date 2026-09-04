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
	NutanixInfraExport = "cluster-api-nutanix-infrastructure"

	// WorkspaceExport publishes no Cluster API types at all. It is the
	// identity the workspace onboarding controllers act under inside a tenant
	// workspace: it claims the APIBindings a workspace holds, so that they can
	// be seen, and the ClusterRoles in it, so that they can be kept in line
	// with what is bound.
	//
	// A separate export rather than more claims on core's, because the two
	// identities should not be one. Core already reaches every Secret in every
	// workspace; letting it write ClusterRoles there as well would let the
	// provider that holds every tenant's kubeconfig also grant itself anything
	// else, which is not a privilege this project should hand out to get a
	// role reconciled.
	WorkspaceExport = "cluster-api-workspace"
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

	// Contract is the Cluster API provider contract this export serves. It is
	// published as the ContractLabel, which is how the claim controller
	// recognises this export - and how it recognises one this repository has
	// never heard of.
	Contract Contract

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
	//
	// Only core's own types are claimed this way. A provider is written
	// against `Cluster` and `Machine` by name, so naming them here is stating
	// what the code does; everything a provider reaches for by contract rather
	// than by name is DiscoveredClaims below.
	ProviderClaims map[string][]Resource

	// DiscoveredClaims are claims on every resource published by every export
	// serving a given contract, whatever that export turns out to be, with the
	// verbs this provider needs on it.
	//
	// This is the half of the claim list that cannot be written down, and the
	// reason ADR-0001 asks for a controller: core dereferences
	// `spec.infrastructureRef` without knowing what an infrastructure provider
	// is, so it must claim the types of a provider that may not have existed
	// when core shipped. What is fixed is the verb set, which follows from
	// what core's own reconcilers do with whatever they find there.
	//
	// A discovered claim covers *every* resource its export publishes, because
	// which of them a Cluster will reference is not knowable from outside: an
	// infrastructure provider's cluster, machine and both templates are all
	// reachable from a ClusterClass. The verbs stay as narrow as the markers
	// justify - it is the resource list that widens, not the permission.
	DiscoveredClaims map[Contract][]string
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

	// A provider's own published types are not listed here. They were, one
	// name per type, until claims on them became DiscoveredClaims — "whatever
	// a Cluster points at", resolved from the exports installed rather than
	// enumerated in this file. See Core.

	// What the workspace onboarding export claims. All three are types kcp
	// serves in every workspace rather than types an export publishes.
	apiBindings     = Resource{Group: "apis.kcp.io", Resource: "apibindings"}
	clusterRoles    = Resource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}
	logicalClusters = Resource{Group: "core.kcp.io", Resource: "logicalclusters"}
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
		Export:   CoreExport,
		Contract: ContractCore,
		Module:   kcpfixtures.ModuleClusterAPI,
		CRDs: []string{
			"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
			// The ClusterClass a Cluster's topology names. It is the core
			// provider's own type and its own controller reconciles it, so it
			// is published here rather than claimed.
			"core/config/crd/bases/cluster.x-k8s.io_clusterclasses.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
			"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
			// Published, not reconciled, and not enabled: the MachinePool
			// feature gate stays off and nothing here acts on a MachinePool.
			// The topology reconciler reads the type on every reconcile of a
			// managed topology whatever the gate says, so a workspace that
			// cannot serve it gets "no matches for kind MachinePool" and its
			// Cluster never leaves Pending - which is a message about a feature
			// nobody asked for, on a cluster that does not use it.
			"core/config/crd/bases/cluster.x-k8s.io_machinepools.yaml",
		},
		// Secrets in full, and ConfigMaps read-only.
		//
		// The delete on Secrets is the topology reconciler's, whose marker is
		// `secrets, get;create;delete` where the rest of core's is
		// `get;list;watch;create;patch;update`. It creates a *cluster shim*
		// Secret to own the objects it stamps from a class before the Cluster
		// they belong to can own them, and deletes it once the real owner
		// exists. Without the delete every ClusterClass based cluster comes up
		// completely and then reports TopologyReconciled=False forever with
		// `secrets "<cluster>-shim" is forbidden`, which is a permission
		// failure wearing the costume of a reconcile bug.
		//
		// Core's markers say nothing at all about ConfigMaps; read is what the
		// controllers that reach for one need.
		CoreClaims: []Resource{to(secrets, own), to(configMaps, read)},
		// Everything, on every provider there is. Core's marker is
		// `resources=*, verbs=get;list;watch;create;update;patch;delete`
		// across infrastructure, bootstrap and controlplane, and it earns it:
		// the Cluster reconciler deletes the control plane and the
		// infrastructure cluster, and the Machine reconciler deletes the
		// bootstrap config and the infrastructure machine.
		//
		// The topology controller widens what "resolve" means without widening
		// the claim: for a ClusterClass based cluster it does not only
		// dereference these types, it *creates* them from the templates the
		// class names - the infrastructure cluster, the control plane, and a
		// MachineDeployment's bootstrap and infrastructure templates. Every
		// template a class can name is therefore claimed alongside the object
		// stamped from it, and the verbs are already the ones that allows.
		//
		// Discovered rather than named, and that is the point: "whatever a
		// Cluster points at" is what core's reconcilers resolve and what this
		// package cannot enumerate. The claims on the dev infrastructure
		// provider are not written here any more; they appear because that
		// provider publishes a labelled export, and a third-party provider's
		// appear the same way, on the day it is installed.
		DiscoveredClaims: map[Contract][]string{
			ContractInfrastructure: own,
			ContractBootstrap:      own,
			ContractControlPlane:   own,
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
		Export:   ControlPlaneExport,
		Contract: ContractControlPlane,
		Module:   kcpfixtures.ModuleClusterAPI,
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
		},
		// It authors the KubeadmConfig for each Machine it creates, and its
		// marker grants the bootstrap group in full. It reads the
		// infrastructure template its machineTemplate names and *creates* an
		// infrastructure machine per control plane Machine from it, so that
		// claim is a write as much as a read.
		//
		// Both discovered, for core's reason: `machineTemplate.infrastructureRef`
		// and the bootstrap config it stamps are references, and which provider
		// answers them is the tenant's choice.
		DiscoveredClaims: map[Contract][]string{
			ContractInfrastructure: own,
			ContractBootstrap:      own,
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
		Export:   BootstrapExport,
		Contract: ContractBootstrap,
		Module:   kcpfixtures.ModuleClusterAPI,
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
		},
		// It reads the control plane object a Machine belongs to, to resolve
		// the control plane's version when preparing a config. Read-only, and
		// its marker says so.
		DiscoveredClaims: map[Contract][]string{
			ContractControlPlane: read,
		},
	}
}

// Infrastructure is the docker/dev infrastructure provider's export.
//
// Its own reconcilers watch Clusters and Machines, and its ClusterCache reads
// workload-cluster kubeconfig Secrets.
func Infrastructure() Provider {
	return Provider{
		Export:   InfraExport,
		Contract: ContractInfrastructure,
		Module:   kcpfixtures.ModuleClusterAPITest,
		CRDs: []string{
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
			// The templates are published because something else refers to
			// them and nothing reconciles them: the control plane provider's
			// machineTemplate names a DevMachineTemplate, and a ClusterClass
			// names a DevClusterTemplate for the infrastructure cluster and a
			// DevMachineTemplate per machine deployment class.
			"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclustertemplates.yaml",
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

// NutanixInfrastructure publishes the Nutanix infrastructure provider's types.
//
// # Published, not yet reconciled
//
// This export makes the Nutanix types bindable in a workspace. Nothing in this
// repository reconciles them yet: there is no Nutanix manager, because running
// one needs a Prism Central that CI does not have. An export with no controller
// is a real intermediate state rather than an oversight — a workspace can bind
// the types and a cluster can name them, and what is missing is the thing that
// would act on them.
//
// The claims are the dev provider's, for the same reasons: an infrastructure
// provider reads the workload cluster's kubeconfig Secret and patches it, and
// it watches Clusters while patching and adopting the Machines it provisions.
// They are declared here rather than left empty so that the export a manager
// eventually binds to is the one it needs, rather than one that has to change
// underneath it.
func NutanixInfrastructure() Provider {
	return Provider{
		Export:   NutanixInfraExport,
		Contract: ContractInfrastructure,
		Module:   kcpfixtures.ModuleCAPX,
		CRDs: []string{
			"config/crd/bases/infrastructure.cluster.x-k8s.io_nutanixclusters.yaml",
			"config/crd/bases/infrastructure.cluster.x-k8s.io_nutanixmachines.yaml",
			// The templates are published for the same reason the dev
			// provider's are: a ClusterClass names a NutanixClusterTemplate
			// for the infrastructure cluster and a NutanixMachineTemplate per
			// machine deployment class, and nothing reconciles either.
			"config/crd/bases/infrastructure.cluster.x-k8s.io_nutanixclustertemplates.yaml",
			"config/crd/bases/infrastructure.cluster.x-k8s.io_nutanixmachinetemplates.yaml",
			// A NutanixCluster names failure domains, so the type has to be
			// bindable wherever a NutanixCluster is.
			"config/crd/bases/infrastructure.cluster.x-k8s.io_nutanixfailuredomains.yaml",
		},
		CoreClaims: []Resource{to(secrets, readPatch)},
		ProviderClaims: map[string][]Resource{
			CoreExport: {to(clusters, read), to(machines, adoptDelete)},
		},
	}
}

// Workspaces is the workspace onboarding export: the identity the controllers
// that keep a tenant workspace's ClusterRoles current act under.
//
// It publishes nothing. An export with no resources is a legal one - kcp
// requires no schemas - and it is the honest shape here: this deployment adds
// no API to a workspace, it reads what the workspace has bound and writes the
// roles that follow from it.
//
// Both claims are on types kcp serves everywhere, so neither carries an
// identity hash. `apibindings` is claimable because kcp exempts its own
// `apis.kcp.io` group from the identity requirement; without the claim an
// APIExport's virtual workspace serves only the one APIBinding that binds it,
// which is precisely the thing this controller must not be limited to.
func Workspaces() Provider {
	return Provider{
		Export: WorkspaceExport,
		CoreClaims: []Resource{
			// Read: which providers a workspace has enabled is the input.
			to(apiBindings, read),
			// Read: the workspace itself, which is what this deployment
			// discovers a workspace by. One LogicalCluster exists per
			// workspace and it is reachable through this claim only while
			// this export is bound, so it appears when a workspace onboards
			// and disappears when it unbinds - which is exactly the pair of
			// events a fleet-wide manager engages and disengages on. See
			// providerwiring.WithLogicalClusterDiscovery.
			to(logicalClusters, read),
			// Write: the roles are the output. No delete - a role this
			// controller did not create is not its to remove, and a workspace
			// leaving Cluster API keeps the roles until somebody decides
			// otherwise.
			to(clusterRoles, []string{"get", "list", "watch", "create", "update", "patch"}),
		},
	}
}

// All is every Cluster API provider this repository wires, in publication
// order. Workspaces is not among them: it serves no contract and reconciles no
// Cluster API type, so a caller that wants it asks for it.
func All() []Provider {
	return []Provider{Core(), Bootstrap(), ControlPlane(), Infrastructure()}
}

// Identities maps an export's name to the identity hash the server assigned it.
type Identities map[string]string

// Claims returns the permission claims an export should declare: what it
// needs from core, and what it needs from whichever providers the installation
// turns out to have.
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
//
// A resource reached both ways - named in ProviderClaims and published by a
// discovered provider - is claimed once, for the union of the two verb sets.
// That union is the safe direction and the only one available: kcp scopes a
// claim by resource and verb, so two claims on one resource are not two
// permissions, they are one, and the narrower of them would silently be the
// one that lost.
func (p Provider) Claims(identities Identities, discovered Discovery) []apisv1alpha2.PermissionClaim {
	verbsByClaim := map[claimKey][]string{}
	add := func(r Resource, identity string) {
		key := claimKey{group: r.Group, resource: r.Resource, identity: identity}
		verbsByClaim[key] = unionVerbs(verbsByClaim[key], r.Verbs)
	}

	for _, r := range p.CoreClaims {
		add(r, "")
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
			add(r, identity)
		}
	}

	for _, found := range discovered {
		// An export does not claim its own resources: it publishes them, and
		// kcp serves an export's own types to it without a claim. A provider
		// that claimed itself would be asking the server for permission it
		// already has, on an identity hash of its own - which kcp rejects.
		if found.Export == p.Export {
			continue
		}
		verbs, wanted := p.DiscoveredClaims[found.Contract]
		if !wanted {
			continue
		}
		for _, r := range found.Resources {
			add(to(r, verbs), found.IdentityHash)
		}
	}

	claims := make([]apisv1alpha2.PermissionClaim, 0, len(verbsByClaim))
	for key, verbs := range verbsByClaim {
		if len(verbs) == 0 {
			// v1alpha2 requires at least one verb, so "unset" has to become
			// something. Every verb is what a v1alpha1 claim granted, which
			// keeps an unnarrowed resource behaving as it did.
			verbs = []string{"*"}
		}
		claims = append(claims, apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: key.group, Resource: key.resource},
			IdentityHash:  key.identity,
			Verbs:         verbs,
		})
	}

	slices.SortFunc(claims, func(a, b apisv1alpha2.PermissionClaim) int {
		if a.Group != b.Group {
			return compare(a.Group, b.Group)
		}
		if a.Resource != b.Resource {
			return compare(a.Resource, b.Resource)
		}
		return compare(a.IdentityHash, b.IdentityHash)
	})
	return claims
}

// claimKey is what makes two claims the same claim: kcp scopes a claim by the
// resource and the identity it is served under, and the verbs are the
// permission granted on that pair.
type claimKey struct {
	group    string
	resource string
	identity string
}

// verbOrder is the order verbs are written in, so that a claim list is stable
// enough to compare against the one on the server.
//
// Lifecycle order rather than alphabetical, because that is the order the
// upstream `+kubebuilder:rbac` markers these verb sets come from are written
// in, and a claim should read like the marker it was derived from.
var verbOrder = []string{"*", "get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}

// unionVerbs merges two verb sets into verbOrder, dropping duplicates.
//
// An empty result means the caller asked for no verbs at all, which v1alpha2
// forbids; claim() turns that into "*", which is what a v1alpha1 claim granted
// and so what an unnarrowed resource behaved as.
func unionVerbs(a, b []string) []string {
	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	if seen["*"] {
		// Every verb, so the rest say nothing. Keeping them would make one
		// claim compare unequal to an identical one written the other way
		// round, and rewrite the export forever.
		return []string{"*"}
	}

	verbs := make([]string, 0, len(seen))
	for _, v := range verbOrder {
		if seen[v] {
			verbs = append(verbs, v)
			delete(seen, v)
		}
	}
	// Anything verbOrder does not know about, in a stable place rather than a
	// random one. A verb this project has never seen is still a verb.
	unknown := make([]string, 0, len(seen))
	for v := range seen {
		unknown = append(unknown, v)
	}
	slices.Sort(unknown)
	if len(verbs) == 0 && len(unknown) == 0 {
		return nil
	}
	return append(verbs, unknown...)
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
// second discovers what was published and gives every export the claim list
// that follows.
//
// The second pass is ReconcileClaims, which is also what the claim controller
// runs on every APIExport event. Publishing and maintaining are the same
// operation on a different trigger, and sharing the code is what keeps a
// deployment's claim list identical to the one this repository's own tests
// assert.
//
// Idempotent, so it doubles as the reconcile a long-lived deployment would run:
// an export that already exists is brought to the requested shape, and one
// whose claims already match is left alone.
func Publish(ctx context.Context, cl client.Client, providers []Provider, timeout time.Duration) (Discovery, error) {
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
			Labels:       p.labels(),
			// Only the identity-free claims in the first pass. The rest need
			// identities that do not exist until every export does.
			PermissionClaims: p.Claims(nil, nil),
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
	}

	if _, err := ReconcileClaims(ctx, cl, providers); err != nil {
		return nil, err
	}

	return DiscoverIn(ctx, cl)
}

// DiscoverIn reads the provider exports in the workspace cl is scoped to.
func DiscoverIn(ctx context.Context, cl client.Client) (Discovery, error) {
	exports := &apisv1alpha2.APIExportList{}
	if err := cl.List(ctx, exports); err != nil {
		return nil, fmt.Errorf("listing APIExports: %w", err)
	}
	return Discover(exports.Items), nil
}

// labels are what an export carries so that the claim controller can recognise
// it. An export serving no contract carries none.
func (p Provider) labels() map[string]string {
	if p.Contract == "" {
		return nil
	}
	return map[string]string{ContractLabel: string(p.Contract)}
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

func setClaims(ctx context.Context, cl client.Client, name string, claims []apisv1alpha2.PermissionClaim) (bool, error) {
	export := &apisv1alpha2.APIExport{}
	if err := cl.Get(ctx, client.ObjectKey{Name: name}, export); err != nil {
		return false, fmt.Errorf("reading APIExport %s: %w", name, err)
	}
	if slices.EqualFunc(export.Spec.PermissionClaims, claims, sameClaim) {
		return false, nil
	}
	export.Spec.PermissionClaims = claims
	if err := cl.Update(ctx, export); err != nil {
		return false, fmt.Errorf("updating the claims on APIExport %s: %w", name, err)
	}
	return true, nil
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
