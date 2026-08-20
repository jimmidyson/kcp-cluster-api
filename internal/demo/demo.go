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

// Package demo stands the whole system up against one kcp shard and drives it
// until every workspace has a ready cluster.
//
// It exists because the project's own end-to-end behaviour was only reachable
// by reading a test: the wiring worked, and the only way to watch it work was
// `go test`. This runs the same wiring cmd/core-manager runs, against as many
// workspaces as asked for, and reports what each one's cluster is doing.
//
// What it deliberately does not do:
//
//   - Serve webhooks. They are single-workspace by construction until the
//     conversion plan's G4 lands, so a multi-workspace demo cannot use them.
//     Every object it creates is therefore fully specified, since nothing
//     defaults it, and every published type is trimmed to one version, since
//     nothing converts it.
//   - Provision Machines. A Machine reaching Ready needs a bootstrap provider
//     and a control-plane provider (the conversion plan's P1 and P2), neither
//     of which is wired yet. The demo provisions cluster infrastructure -
//     which is what DevCluster is - and says so rather than creating Machines
//     that would sit unprovisioned.
package demo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"

	"github.com/jimmidyson/kcp-cluster-api/internal/bootstrapmanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	infrav1beta1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// Defaults for Options. Two workspaces because one proves nothing about
// multi-tenancy and the demo's whole subject is that the manager serves the
// fleet.
const (
	DefaultParent          = "root"
	DefaultWorkspacePrefix = "capi-demo"
	DefaultWorkspaces      = 2
	DefaultClusters        = 1

	// What cmd/demo asks for when nothing says otherwise. One of each is the
	// smallest cluster that can reach ready: a control plane to be available
	// and connected to, and a worker to show that a machine nobody named came
	// up too.
	//
	// Constants rather than flag literals so the demo and the tests that drive
	// internal/demo agree on what "the default demo" is.
	DefaultControlPlaneMachines = 1
	DefaultWorkerMachines       = 1

	DefaultTimeout      = 5 * time.Minute
	DefaultPollInterval = 2 * time.Second
)

// coreCRDs and devCRDs are the types the demo publishes, per ADR-0001's D3
// scope. They are resolved from the pinned Cluster API modules at run time
// rather than copied here, so they cannot disagree with the version this is
// built against.
//
// MachineSet, MachineDeployment and MachineHealthCheck are published without
// being reconciled: the Cluster and Machine reconcilers register watches on
// them, and controller-runtime blocks a controller's startup on every
// registered source's cache sync - including for kinds the server does not
// serve. Leaving them out does not skip them, it hangs.
var (
	coreCRDs = []string{
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
	}
	bootstrapCRDs = []string{
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigs.yaml",
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigtemplates.yaml",
	}
	devCRDs = []string{
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
	}
)

// PermissionClaims are the core-type claims a single-export fixture needs:
// Secrets, because the bootstrap and control plane providers are made of them
// — the data secret, the cluster certificates, the admin kubeconfig — and
// ConfigMaps, because the control plane init lock is one.
//
// A demo run does not use this: it publishes one export per provider and each
// carries the claims that provider's own RBAC markers justify
// (capiexports.Provider.Claims). What uses it is the sweeps, which publish
// everything through one export because the shape under measurement is the
// controllers rather than the export topology.
//
// Deliberately not narrowed by verb. A sweep that failed because its fixture
// claimed too little would fail for a reason that has nothing to do with what
// it measures, and the per-provider claims are where least privilege is
// expressed and tested.
var PermissionClaims = []apisv1alpha2.PermissionClaim{
	{GroupResource: apisv1alpha2.GroupResource{Resource: "secrets"}, Verbs: []string{"*"}},
	{GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"}, Verbs: []string{"*"}},
}

// Options configures a demo run.
type Options struct {
	// BaseConfig addresses the kcp shard, cluster-unaware: the demo scopes it
	// to each workspace it touches. Required.
	BaseConfig *rest.Config

	// Parent is the workspace the APIExport is published in and the demo
	// workspaces are created under. Empty means DefaultParent.
	Parent string

	// WorkspacePrefix prefixes each created workspace's name. Empty means
	// DefaultWorkspacePrefix.
	WorkspacePrefix string

	// Workspaces is how many workspaces to create and bind. Zero means
	// DefaultWorkspaces.
	Workspaces int

	// ClustersPerWorkspace is how many Cluster/DevCluster pairs each
	// workspace gets. Zero means DefaultClusters.
	ClustersPerWorkspace int

	// ControlPlaneMachines is how many control plane machines each cluster
	// gets, as a KubeadmControlPlane the Cluster points at. Asking for any
	// wires the kubeadm bootstrap and control plane providers, which create
	// each machine's Machine, KubeadmConfig and DevMachine themselves.
	//
	// Zero means none, and a run with none cannot reach ready - there is no
	// control plane for the Cluster's Available condition to summarise - so it
	// waits for provisioned instead. See Result.Ready. cmd/demo asks for one
	// by default, because a demo that stops short of a cluster is not showing
	// the thing it exists to show.
	ControlPlaneMachines int

	// WorkerMachines is how many worker machines each cluster gets, as a
	// MachineDeployment. Workers need a control plane to join, so asking for
	// them without ControlPlaneMachines is rejected.
	WorkerMachines int

	// KubernetesVersion is what those machines ask for. Empty means
	// DefaultKubernetesVersion.
	KubernetesVersion string

	// Backend selects the DevCluster backend. Empty means BackendInMemory,
	// the only one that needs neither a container runtime nor image pulls.
	Backend Backend

	// RunManager runs the manager in this process. Set it false to drive
	// workspaces and objects against a core-manager started separately -
	// which is the same wiring, in the shape a deployment actually has.
	//
	// A manager started here runs until ctx is done, not until Run returns:
	// the clusters it provisioned are meant to still be there afterwards, for
	// the caller to look at or assert on.
	RunManager bool

	// Timeout bounds waiting for every cluster to be ready. Zero means
	// DefaultTimeout.
	Timeout time.Duration

	// PollInterval is how often the status table is refreshed. Zero means
	// DefaultPollInterval.
	PollInterval time.Duration

	// WhileProvisioning, if set, runs in its own goroutine once every
	// workspace holds its objects, concurrently with the wait for readiness.
	//
	// It exists for work that has to happen *during* provisioning rather than
	// before or after it, of which there is one case: a real workload cluster's
	// Nodes stay NotReady until a CNI is applied, and the CNI can only be
	// applied through a kubeconfig that does not exist until the control plane
	// is up. Waiting for ready first would deadlock, and doing it before the
	// objects exist would have nothing to talk to - so a caller that needs it
	// polls here while Run watches.
	//
	// Run does not wait for it and ignores what it does; its effect, if it has
	// one, shows up as the clusters reaching ready. A caller that needs to know
	// whether it succeeded should report that itself.
	WhileProvisioning func(ctx context.Context, workspaces []Workspace)

	// Out receives the status tables. Nil discards them.
	Out io.Writer

	// Log receives progress. The zero value discards it.
	Log logr.Logger
}

func (o *Options) applyDefaults() {
	if o.Parent == "" {
		o.Parent = DefaultParent
	}
	if o.WorkspacePrefix == "" {
		o.WorkspacePrefix = DefaultWorkspacePrefix
	}
	if o.Workspaces == 0 {
		o.Workspaces = DefaultWorkspaces
	}
	if o.ClustersPerWorkspace == 0 {
		o.ClustersPerWorkspace = DefaultClusters
	}
	if o.Backend == "" {
		o.Backend = BackendInMemory
	}
	if o.KubernetesVersion == "" {
		o.KubernetesVersion = DefaultKubernetesVersion
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.PollInterval == 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
}

// providers is the set of provider exports this run publishes and wires.
//
// The bootstrap and control plane providers are included only when the run asks
// for machines: their exports would publish types nothing uses, and their
// managers would engage every workspace to reconcile nothing. Core and the
// infrastructure provider are always there, because a cluster is made of both.
func (o Options) providers() []capiexports.Provider {
	providers := []capiexports.Provider{capiexports.Core(), capiexports.Infrastructure()}
	if o.ControlPlaneMachines > 0 {
		providers = append(providers, capiexports.Bootstrap(), capiexports.ControlPlane())
	}
	return providers
}

func (o Options) validate() error {
	if o.BaseConfig == nil {
		return errors.New("BaseConfig is required")
	}
	if o.Workspaces < 1 {
		return fmt.Errorf("Workspaces = %d, want at least 1", o.Workspaces)
	}
	if o.ClustersPerWorkspace < 1 {
		return fmt.Errorf("ClustersPerWorkspace = %d, want at least 1", o.ClustersPerWorkspace)
	}
	if o.WorkerMachines > 0 && o.ControlPlaneMachines == 0 {
		return errors.New("WorkerMachines without ControlPlaneMachines: a worker has no control plane to join")
	}
	return o.Backend.Validate()
}

// Workspace is one workspace the demo created, in both the names it has: the
// path a person uses and the logical cluster name the manager engages by.
type Workspace struct {
	Path           string
	LogicalCluster string

	// Client is scoped to this workspace and bypasses the manager's caches
	// entirely, so what it reads is what the shard holds for this workspace
	// and nothing else.
	Client client.Client
}

// Result is what a run produced, for a caller that wants to assert on it
// rather than read the table.
type Result struct {
	Workspaces []Workspace
	Statuses   []ClusterStatus

	// Machines is every Machine the workspaces hold - created by the control
	// plane provider, not by the demo.
	Machines []MachineStatus

	// ControlPlanes is empty unless the run asked for control plane machines.
	ControlPlanes []ControlPlaneStatus

	// ExpectedMachines is how many Machines the run asked for across every
	// workspace, control plane and worker together.
	ExpectedMachines int

	// Managers is every provider's running manager, keyed by its APIExport
	// name, or nil when the run was told not to start one.
	//
	// Keyed, because since the export split the answer to "can a fleet client
	// write this?" depends on which provider is asking: each claims only what
	// its own controllers do, so the ConfigMap the bootstrap provider takes as
	// an init lock is not writable through core's client and is not meant to
	// be.
	Managers map[string]mcmanager.Manager

	// Manager is the core provider's manager - the one a caller that does not
	// care which provider it is asking should use. Nil when the run was
	// told not to start one. It is exposed so a test can ask the fleet's own
	// clients what they can see and do, which is a different question from
	// what kcp serves: the two differ, and where they differ is where a
	// provider stops working.
	Manager mcmanager.Manager
}

// Provisioned reports whether every cluster reached provisioned, every control
// plane the run asked for can accept requests, and every machine it asked for
// exists with its bootstrap data.
//
// The control plane leads because it is what a person waits for: a cluster
// they can talk to. The machine count is checked as well as their state, since
// a worker pool that created no Machines at all would otherwise satisfy
// "every machine is bootstrapped" vacuously.
//
// It is the milestone on the way to Ready rather than the demo's
// done-condition: reported in the tables, not waited on. See Ready.
func (r Result) Provisioned() bool {
	return AllProvisioned(r.Statuses) &&
		AllInitialized(r.ControlPlanes) &&
		len(r.Machines) >= r.ExpectedMachines &&
		AllBootstrapped(r.Machines)
}

// Ready reports whether every cluster the run asked for is one somebody could
// use: the Cluster is Available, its control plane has every replica it was
// asked for, and every Machine is Ready.
//
// This is what the demo waits for. Provisioned is not enough and the
// difference is not cosmetic - a control plane that is initialized but whose
// machines never go Ready is exactly the shape of the bugs this wiring has
// had, and a demo that stopped at provisioned would have reported all of them
// as a success.
//
// The machine count is checked here too, for the reason Provisioned checks it:
// without it a run that created no Machines at all would satisfy "every
// machine is ready" vacuously.
//
// A run that asked for no control plane falls back to Provisioned, because
// there is nothing for readiness to mean: the Cluster's Available condition
// summarises a remote connection probe and a control plane availability that a
// cluster with no control plane never gets, so waiting for it would be waiting
// for something that cannot happen. ControlPlanes is empty only in that case -
// a run that asked for one gets a row saying "not created yet" until it
// appears.
func (r Result) Ready() bool {
	if len(r.ControlPlanes) == 0 {
		return r.Provisioned()
	}
	return AllClustersReady(r.Statuses) &&
		AllControlPlanesReady(r.ControlPlanes) &&
		len(r.Machines) >= r.ExpectedMachines &&
		AllMachinesReady(r.Machines)
}

func fixtureScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
		apisv1alpha2.AddToScheme,
		tenancyv1alpha1.AddToScheme,
		corev1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

// ManagerScheme is what the manager and the demo's own workspace clients are
// built with: kcp's API types plus the Cluster API types this project wires.
func ManagerScheme() (*runtime.Scheme, error) {
	scheme, err := fixtureScheme()
	if err != nil {
		return nil, err
	}
	for _, add := range []func(*runtime.Scheme) error{
		clusterv1beta1.AddToScheme,
		clusterv1.AddToScheme,
		bootstrapv1.AddToScheme,
		controlplanev1.AddToScheme,
		infrav1beta1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	return scheme, nil
}

// Run publishes the APIExport, creates and binds the workspaces, starts the
// manager (unless told not to), creates a cluster in every workspace, and
// waits for all of them to be ready - printing the status table as it goes.
//
// It returns the last snapshot whether or not every cluster made it, so a
// caller can report what did happen rather than only that something did not.
func Run(ctx context.Context, opts Options) (Result, error) {
	opts.applyDefaults()
	if err := opts.validate(); err != nil {
		return Result{}, err
	}
	log := opts.Log

	scheme, err := ManagerScheme()
	if err != nil {
		return Result{}, fmt.Errorf("building scheme: %w", err)
	}

	parentPath := logicalcluster.NewPath(opts.Parent)
	parentCfg := kcpclient.SetCluster(rest.CopyConfig(opts.BaseConfig), parentPath)
	parentClient, err := client.New(parentCfg, client.Options{Scheme: scheme})
	if err != nil {
		return Result{}, fmt.Errorf("building a client for %s: %w", opts.Parent, err)
	}

	// 1. Publish one APIExport per provider, and resolve the claims that let
	// each provider's controllers reach the types another one publishes.
	providers := opts.providers()
	log.Info("Publishing the APIExports", "workspace", opts.Parent, "exports", exportNames(providers))
	identities, err := capiexports.Publish(ctx, parentClient, providers, 2*time.Minute)
	if err != nil {
		return Result{}, err
	}

	// 2. Create the workspaces and bind each to the export. The endpoint
	// slice stays empty until at least one binding consumes the export, so
	// this has to happen before anything waits on it.
	workspaces := make([]Workspace, 0, opts.Workspaces)
	for i := 1; i <= opts.Workspaces; i++ {
		name := fmt.Sprintf("%s-%d", opts.WorkspacePrefix, i)
		clusterName, err := kcpfixtures.EnsureWorkspace(ctx, parentClient, name, time.Minute)
		if err != nil {
			return Result{}, err
		}

		wsPath := parentPath.Join(name)
		wsCfg := kcpclient.SetCluster(rest.CopyConfig(opts.BaseConfig), wsPath)
		wsClient, err := client.New(wsCfg, client.Options{Scheme: scheme})
		if err != nil {
			return Result{}, fmt.Errorf("building a client for workspace %s: %w", wsPath, err)
		}

		// One binding per export, each accepting that export's claims. A
		// deployment automates this through a WorkspaceType's
		// defaultAPIBindings; a tenant is not meant to hand-accept a claim per
		// provider.
		for _, provider := range providers {
			if err := kcpfixtures.BindExport(ctx, wsClient, kcpfixtures.BindExportOptions{
				BindingName:      provider.Export,
				ExportPath:       opts.Parent,
				ExportName:       provider.Export,
				PermissionClaims: provider.Claims(identities),
				ReadyTimeout:     time.Minute,
			}); err != nil {
				return Result{}, fmt.Errorf("binding %s into %s: %w", provider.Export, wsPath, err)
			}
		}

		log.Info("Workspace bound", "workspace", wsPath.String(), "logicalCluster", clusterName)
		workspaces = append(workspaces, Workspace{
			Path:           wsPath.String(),
			LogicalCluster: clusterName,
			Client:         wsClient,
		})
	}

	for _, provider := range providers {
		if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, parentClient, provider.Export, time.Minute); err != nil {
			return Result{}, fmt.Errorf("waiting for %s's APIExportEndpointSlice to get an endpoint: %w", provider.Export, err)
		}
	}

	// 3. The manager: the same wiring cmd/core-manager runs, serving every
	// workspace bound to the export from one set of controllers.
	var manager mcmanager.Manager
	var byExport map[string]mcmanager.Manager
	if opts.RunManager {
		managers, err := startManagers(ctx, opts, providers, parentCfg, parentClient, scheme, log)
		if err != nil {
			return Result{}, err
		}
		manager = managers[capiexports.CoreExport]
		byExport = managers

		// Every provider's manager has to have engaged the workspace before
		// its objects are created, for the reason the wiring contract gives:
		// an engagement is handed to the components registered at that moment
		// and never replayed. Waiting on one of them would leave the others
		// racing the first object.
		for _, ws := range workspaces {
			for _, provider := range providers {
				if _, err := coremanager.WaitForManager(ctx, managers[provider.Export],
					multicluster.ClusterName(ws.LogicalCluster), time.Second, 2*time.Minute); err != nil {
					return Result{}, fmt.Errorf("workspace %s was never engaged by %s: %w", ws.Path, provider.Export, err)
				}
			}
			log.Info("Workspace engaged by every provider", "workspace", ws.Path)
		}
	}

	// 4. A cluster in every workspace, all with the same names.
	for _, ws := range workspaces {
		for n := range opts.ClustersPerWorkspace {
			name := ClusterName(n)
			// The infrastructure object first: the Cluster reconciler
			// resolves spec.infrastructureRef and takes ownership of what it
			// finds, and that ownership is what starts the DevCluster
			// reconciler working.
			if err := create(ctx, ws.Client, NewDevCluster(name, opts.Backend)); err != nil {
				return Result{}, fmt.Errorf("creating DevCluster %s in %s: %w", name, ws.Path, err)
			}
			// The control plane and the template it stamps Machines from,
			// before the Cluster that refers to them: the Cluster reconciler
			// resolves spec.controlPlaneRef and takes ownership of what it
			// finds, and that ownership is what starts the control plane
			// provider working.
			if opts.ControlPlaneMachines > 0 {
				if err := create(ctx, ws.Client, NewDevMachineTemplate(name, opts.Backend)); err != nil {
					return Result{}, fmt.Errorf("creating DevMachineTemplate for %s in %s: %w", name, ws.Path, err)
				}
				if err := create(ctx, ws.Client, NewKubeadmControlPlane(name, opts.ControlPlaneMachines, opts.KubernetesVersion)); err != nil {
					return Result{}, fmt.Errorf("creating KubeadmControlPlane for %s in %s: %w", name, ws.Path, err)
				}
			}

			if err := create(ctx, ws.Client, NewCluster(name, opts.Backend, opts.ControlPlaneMachines > 0)); err != nil {
				return Result{}, fmt.Errorf("creating Cluster %s in %s: %w", name, ws.Path, err)
			}

			// The worker pool last: its Machines cannot join a control plane
			// that does not exist yet, and the MachineDeployment is what
			// creates them.
			if opts.WorkerMachines > 0 {
				if err := create(ctx, ws.Client, NewKubeadmConfigTemplate(name)); err != nil {
					return Result{}, fmt.Errorf("creating KubeadmConfigTemplate for %s in %s: %w", name, ws.Path, err)
				}
				if err := create(ctx, ws.Client, NewMachineDeployment(name, opts.WorkerMachines, opts.KubernetesVersion)); err != nil {
					return Result{}, fmt.Errorf("creating MachineDeployment for %s in %s: %w", name, ws.Path, err)
				}
			}
		}
		log.Info("Clusters created", "workspace", ws.Path,
			"clusters", opts.ClustersPerWorkspace,
			"controlPlaneMachines", opts.ClustersPerWorkspace*opts.ControlPlaneMachines,
			"workerMachines", opts.ClustersPerWorkspace*opts.WorkerMachines)
	}

	// 5. Anything the caller has to do while they come up, in parallel with
	// watching. See Options.WhileProvisioning.
	if opts.WhileProvisioning != nil {
		go opts.WhileProvisioning(ctx, workspaces)
	}

	// 6. Watch them come up.
	result, err := waitForReady(ctx, opts, workspaces)
	result.Manager = manager
	result.Managers = byExport
	return result, err
}

func create(ctx context.Context, cl client.Client, obj client.Object) error {
	if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func manifestPaths() ([]string, error) {
	core, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, append(slices.Clone(coreCRDs), bootstrapCRDs...)...)
	if err != nil {
		return nil, fmt.Errorf("resolving core CRD manifests: %w", err)
	}
	dev, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest, devCRDs...)
	if err != nil {
		return nil, fmt.Errorf("resolving dev provider CRD manifests: %w", err)
	}
	return append(core, dev...), nil
}

// startManagers starts one manager per provider, each addressed at its own
// APIExport's virtual workspace - which is what a deployment does, one process
// each. The demo runs them together so that one command shows the whole
// system; nothing else about them differs.
//
// Each manager gets its own provider, its own wildcard cache and its own
// fleet. They cannot share: a fleet is built against one export's virtual
// workspace, and an export serves what it publishes and what it claims.
func startManagers(
	ctx context.Context,
	opts Options,
	providers []capiexports.Provider,
	parentCfg *rest.Config,
	parentClient client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
) (map[string]mcmanager.Manager, error) {
	// MachinePool is on by default upstream and watched as an event source by
	// the core reconcilers; it is outside what these exports publish, so
	// leaving it on stalls their cache sync.
	if err := feature.MutableGates.Set("MachinePool=false"); err != nil {
		return nil, fmt.Errorf("disabling the MachinePool feature gate: %w", err)
	}
	coremanager.SetupProcessGlobals()

	managers := make(map[string]mcmanager.Manager, len(providers))
	for _, provider := range providers {
		mgr, fleet, err := newFleetFor(ctx, opts, provider.Export, parentCfg, parentClient, scheme)
		if err != nil {
			return nil, err
		}

		switch provider.Export {
		case capiexports.CoreExport:
			if err := coremanager.SetupCoreControllers(ctx, mgr, fleet, nil); err != nil {
				return nil, fmt.Errorf("wiring the core reconcilers: %w", err)
			}
		case capiexports.BootstrapExport:
			if err := bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{}); err != nil {
				return nil, fmt.Errorf("wiring the bootstrap reconcilers: %w", err)
			}
		case capiexports.ControlPlaneExport:
			if err := controlplanemanager.SetupFleetControllers(ctx, mgr, fleet, controlplanemanager.Options{}); err != nil {
				return nil, fmt.Errorf("wiring the control plane reconcilers: %w", err)
			}
		case capiexports.InfraExport:
			// Ports of its own, rather than upstream's fixed ones. A demo is
			// something somebody runs next to whatever else they are running -
			// another demo, an integration test, a manager they left going -
			// and the failure when those collide arrives as "address already
			// in use" from a component the reader has no reason to have heard
			// of.
			debugPort, minPort, maxPort, err := devInfrastructurePorts()
			if err != nil {
				return nil, err
			}
			// Loopback, because the workload clusters this stands up are
			// served by this process and reached from it. An empty host is
			// what upstream's POD_IP gives outside a pod, and it produces
			// endpoints like ":20000" that no client can connect to.
			dev, err := coremanager.NewDevInfrastructure(ctx, "127.0.0.1",
				inmemoryserver.CustomPorts{MinPort: minPort, MaxPort: maxPort, DebugPort: debugPort})
			if err != nil {
				return nil, fmt.Errorf("setting up the dev infrastructure provider backend: %w", err)
			}
			if err := coremanager.SetupDevInfrastructureControllers(ctx, mgr, fleet, dev); err != nil {
				return nil, fmt.Errorf("wiring the dev infrastructure reconcilers: %w", err)
			}
		default:
			return nil, fmt.Errorf("no wiring for APIExport %s", provider.Export)
		}

		go func(export string, mgr mcmanager.Manager) {
			if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error(err, "Manager exited", "export", export)
			}
		}(provider.Export, mgr)

		managers[provider.Export] = mgr
	}

	return managers, nil
}

// newFleetFor builds the manager and fleet for one provider, against that
// provider's own export.
func newFleetFor(
	ctx context.Context,
	opts Options,
	export string,
	parentCfg *rest.Config,
	parentClient client.Client,
	scheme *runtime.Scheme,
) (mcmanager.Manager, *coremanager.Fleet, error) {
	registry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(parentCfg, export, scheme, registry)
	if err != nil {
		return nil, nil, fmt.Errorf("constructing the kcp APIExport provider for %s: %w", export, err)
	}

	// The manager is addressed at the export's virtual workspace, not at the
	// workspace holding the export: its RESTMapper has to describe the API
	// surface the engaged workspaces share, which the exporting workspace does
	// not bind.
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, parentClient, export, opts.BaseConfig, time.Minute)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving %s's virtual workspace: %w", export, err)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("setting up the multicluster manager for %s: %w", export, err)
	}

	fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
		// The shard, not the manager's config: the ClusterCache reads
		// kubeconfig Secrets, which live in the workspaces themselves.
		ShardConfig: opts.BaseConfig,
		// This process runs every provider, which a deployment does not. See
		// the field's comment for what it costs.
		SkipControllerNameValidation: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building the fleet for %s: %w", export, err)
	}
	return mgr, fleet, nil
}

func exportNames(providers []capiexports.Provider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Export)
	}
	return names
}

// waitForReady polls every workspace directly - not through the manager's
// caches - and renders the table until every cluster is ready or the timeout
// expires.
func waitForReady(ctx context.Context, opts Options, workspaces []Workspace) (Result, error) {
	deadline := time.Now().Add(opts.Timeout)
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		statuses, err := Snapshot(ctx, workspaces, opts.ClustersPerWorkspace)
		if err != nil {
			return Result{Workspaces: workspaces}, err
		}
		machines, err := SnapshotMachines(ctx, workspaces)
		if err != nil {
			return Result{Workspaces: workspaces, Statuses: statuses}, err
		}
		controlPlanes, err := SnapshotControlPlanes(ctx, workspaces, opts.ClustersPerWorkspace, opts.ControlPlaneMachines)
		if err != nil {
			return Result{Workspaces: workspaces, Statuses: statuses, Machines: machines}, err
		}
		result := Result{
			Workspaces:       workspaces,
			Statuses:         statuses,
			Machines:         machines,
			ControlPlanes:    controlPlanes,
			ExpectedMachines: expectedMachines(opts),
		}

		if err := RenderTable(opts.Out, statuses); err != nil {
			return result, err
		}
		if err := RenderControlPlaneTable(opts.Out, controlPlanes); err != nil {
			return result, err
		}
		if err := RenderMachineTable(opts.Out, machines); err != nil {
			return result, err
		}

		if result.Ready() {
			return result, nil
		}
		if time.Now().After(deadline) {
			return result, fmt.Errorf("timed out after %s with %d of %d clusters ready, %d of %d control planes ready and %d of %d machines ready",
				opts.Timeout, readyCount(statuses), len(statuses),
				controlPlanesReadyCount(controlPlanes), len(controlPlanes),
				machinesReadyCount(machines), result.ExpectedMachines)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-ticker.C:
		}
	}
}

func provisionedCount(statuses []ClusterStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Provisioned {
			n++
		}
	}
	return n
}

func readyCount(statuses []ClusterStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Ready {
			n++
		}
	}
	return n
}

// SnapshotMachines lists the Machines each workspace holds, and the
// KubeadmConfig each one's bootstrap data comes from.
//
// They are listed rather than looked up by name: the control plane provider
// names the Machines it creates, so the demo does not know their names - which
// is the point of having a control plane provider.
func SnapshotMachines(ctx context.Context, workspaces []Workspace) ([]MachineStatus, error) {
	var statuses []MachineStatus
	for _, ws := range workspaces {
		machines := &clusterv1.MachineList{}
		if err := ws.Client.List(ctx, machines, client.InNamespace(Namespace)); err != nil {
			return nil, fmt.Errorf("listing Machines in %s: %w", ws.Path, err)
		}

		for i := range machines.Items {
			machine := &machines.Items[i]

			config := &bootstrapv1.KubeadmConfig{}
			key := client.ObjectKey{Namespace: machine.Namespace, Name: machine.Spec.Bootstrap.ConfigRef.Name}
			if key.Name == "" {
				config = nil
			} else if err := ws.Client.Get(ctx, key, config); err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, fmt.Errorf("reading KubeadmConfig %s in %s: %w", key.Name, ws.Path, err)
				}
				config = nil
			}

			statuses = append(statuses, SummariseMachine(ws.Path, ws.LogicalCluster, machine, config))
		}
	}
	return statuses, nil
}

// SnapshotControlPlanes reads every cluster's control plane.
func SnapshotControlPlanes(ctx context.Context, workspaces []Workspace, clustersPerWorkspace, machinesPerCluster int) ([]ControlPlaneStatus, error) {
	if machinesPerCluster == 0 {
		return nil, nil
	}

	statuses := make([]ControlPlaneStatus, 0, len(workspaces)*clustersPerWorkspace)
	for _, ws := range workspaces {
		for n := range clustersPerWorkspace {
			cluster := ClusterName(n)
			key := client.ObjectKey{Namespace: Namespace, Name: ControlPlaneName(cluster)}

			kcp := &controlplanev1.KubeadmControlPlane{}
			if err := ws.Client.Get(ctx, key, kcp); err != nil {
				if apierrors.IsNotFound(err) {
					statuses = append(statuses, ControlPlaneStatus{
						Workspace: ws.Path, LogicalCluster: ws.LogicalCluster,
						ControlPlane: key.Name, Detail: "not created yet",
					})
					continue
				}
				return nil, fmt.Errorf("reading KubeadmControlPlane %s in %s: %w", key.Name, ws.Path, err)
			}

			statuses = append(statuses, SummariseControlPlane(ws.Path, ws.LogicalCluster, kcp))
		}
	}
	return statuses, nil
}

// expectedMachines is how many Machines the run asked for, which is not how
// many it created: the control plane and the MachineDeployment create them.
func expectedMachines(opts Options) int {
	perCluster := opts.ControlPlaneMachines + opts.WorkerMachines
	return opts.Workspaces * opts.ClustersPerWorkspace * perCluster
}

func initializedCount(statuses []ControlPlaneStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Initialized {
			n++
		}
	}
	return n
}

func bootstrappedCount(statuses []MachineStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Bootstrapped {
			n++
		}
	}
	return n
}

func controlPlanesReadyCount(statuses []ControlPlaneStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Ready {
			n++
		}
	}
	return n
}

func machinesReadyCount(statuses []MachineStatus) int {
	n := 0
	for _, s := range statuses {
		if s.Ready {
			n++
		}
	}
	return n
}

// Snapshot reads every workspace's clusters through that workspace's own
// client.
func Snapshot(ctx context.Context, workspaces []Workspace, clustersPerWorkspace int) ([]ClusterStatus, error) {
	statuses := make([]ClusterStatus, 0, len(workspaces)*clustersPerWorkspace)
	for _, ws := range workspaces {
		for n := range clustersPerWorkspace {
			name := ClusterName(n)
			key := client.ObjectKey{Namespace: Namespace, Name: name}

			cluster := &clusterv1.Cluster{}
			if err := ws.Client.Get(ctx, key, cluster); err != nil {
				if apierrors.IsNotFound(err) {
					statuses = append(statuses, ClusterStatus{
						Workspace:      ws.Path,
						LogicalCluster: ws.LogicalCluster,
						Cluster:        name,
						Detail:         "not created yet",
					})
					continue
				}
				return nil, fmt.Errorf("reading Cluster %s in %s: %w", name, ws.Path, err)
			}

			devCluster := &infrav1.DevCluster{}
			if err := ws.Client.Get(ctx, key, devCluster); err != nil {
				if !apierrors.IsNotFound(err) {
					return nil, fmt.Errorf("reading DevCluster %s in %s: %w", name, ws.Path, err)
				}
				devCluster = nil
			}

			statuses = append(statuses, Summarise(ws.Path, ws.LogicalCluster, cluster, devCluster))
		}
	}
	return statuses, nil
}
