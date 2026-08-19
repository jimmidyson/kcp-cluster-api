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
// until every workspace has a provisioned cluster.
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
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"

	"github.com/jimmidyson/kcp-cluster-api/internal/bootstrapmanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
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
	DefaultExportName      = "cluster-api"
	DefaultWorkspacePrefix = "capi-demo"
	DefaultWorkspaces      = 2
	DefaultClusters        = 1
	DefaultTimeout         = 5 * time.Minute
	DefaultPollInterval    = 2 * time.Second
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

// PermissionClaims are what the export claims beyond the types it publishes.
//
// Secrets because the bootstrap provider is made of them - the data secret it
// produces and the cluster certificates it generates - and ConfigMaps because
// the control plane init lock is one. A claim is only a declaration; each
// workspace's APIBinding accepts it, which is what BindExport does here.
//
// They are claimed whether or not a run wires the bootstrap provider: a demo
// that changed the export's shape depending on its flags would make two runs
// against one server disagree about what the export is.
var PermissionClaims = []apisv1alpha1.PermissionClaim{
	{GroupResource: apisv1alpha1.GroupResource{Resource: "secrets"}, All: true},
	{GroupResource: apisv1alpha1.GroupResource{Resource: "configmaps"}, All: true},
}

// Options configures a demo run.
type Options struct {
	// BaseConfig addresses the kcp shard, cluster-unaware: the demo scopes it
	// to each workspace it touches. Required.
	BaseConfig *rest.Config

	// Parent is the workspace the APIExport is published in and the demo
	// workspaces are created under. Empty means DefaultParent.
	Parent string

	// ExportName names the APIExport and its APIExportEndpointSlice. Empty
	// means DefaultExportName.
	ExportName string

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
	// gets. Zero means none, and none is the default: a machine needs the
	// bootstrap provider, which is wired only when some are asked for.
	//
	// Each one is a Machine, a KubeadmConfig and a DevMachine. They are
	// standalone control plane machines - the Cluster has no controlPlaneRef -
	// because the control plane provider is the conversion plan's P2 and is
	// not wired.
	ControlPlaneMachines int

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

	// Timeout bounds waiting for every cluster to be provisioned. Zero means
	// DefaultTimeout.
	Timeout time.Duration

	// PollInterval is how often the status table is refreshed. Zero means
	// DefaultPollInterval.
	PollInterval time.Duration

	// Out receives the status tables. Nil discards them.
	Out io.Writer

	// Log receives progress. The zero value discards it.
	Log logr.Logger
}

func (o *Options) applyDefaults() {
	if o.Parent == "" {
		o.Parent = DefaultParent
	}
	if o.ExportName == "" {
		o.ExportName = DefaultExportName
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

	// Machines is empty unless the run asked for control plane machines.
	Machines []MachineStatus

	// Manager is the running multi-cluster manager, or nil when the run was
	// told not to start one. It is exposed so a test can ask the fleet's own
	// clients what they can see and do, which is a different question from
	// what kcp serves: the two differ, and where they differ is where a
	// provider stops working.
	Manager mcmanager.Manager
}

// Provisioned reports whether every cluster reached provisioned, and every
// machine the run asked for has its bootstrap data.
func (r Result) Provisioned() bool {
	return AllProvisioned(r.Statuses) && AllBootstrapped(r.Machines)
}

func fixtureScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
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
// waits for all of them to be provisioned - printing the status table as it
// goes.
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

	// 1. Publish the Cluster API types out of the parent workspace.
	crdPaths, err := manifestPaths()
	if err != nil {
		return Result{}, err
	}
	log.Info("Publishing the APIExport", "workspace", opts.Parent, "export", opts.ExportName, "types", len(crdPaths))
	if err := kcpfixtures.PublishAPIExport(ctx, parentClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:       opts.ExportName,
		SchemaPrefix:     "v1",
		CRDPaths:         crdPaths,
		PermissionClaims: PermissionClaims,
		// No webhook server, so no conversion strategy, so one version per
		// type. See the package comment.
		CRDTransform: kcpfixtures.KeepStorageVersion,
	}); err != nil {
		return Result{}, fmt.Errorf("publishing the APIExport: %w", err)
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

		if err := kcpfixtures.BindExport(ctx, wsClient, kcpfixtures.BindExportOptions{
			BindingName:      opts.ExportName,
			ExportPath:       opts.Parent,
			ExportName:       opts.ExportName,
			PermissionClaims: PermissionClaims,
			ReadyTimeout:     time.Minute,
		}); err != nil {
			return Result{}, fmt.Errorf("binding the APIExport into %s: %w", wsPath, err)
		}

		log.Info("Workspace bound", "workspace", wsPath.String(), "logicalCluster", clusterName)
		workspaces = append(workspaces, Workspace{
			Path:           wsPath.String(),
			LogicalCluster: clusterName,
			Client:         wsClient,
		})
	}

	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, parentClient, opts.ExportName, time.Minute); err != nil {
		return Result{}, fmt.Errorf("waiting for the APIExportEndpointSlice to get an endpoint: %w", err)
	}

	// 3. The manager: the same wiring cmd/core-manager runs, serving every
	// workspace bound to the export from one set of controllers.
	var manager mcmanager.Manager
	if opts.RunManager {
		mgr, err := startManager(ctx, opts, parentCfg, parentClient, scheme, log)
		if err != nil {
			return Result{}, err
		}
		manager = mgr
		for _, ws := range workspaces {
			if _, err := coremanager.WaitForManager(ctx, mgr,
				multicluster.ClusterName(ws.LogicalCluster), time.Second, 2*time.Minute); err != nil {
				return Result{}, fmt.Errorf("workspace %s was never engaged: %w", ws.Path, err)
			}
			log.Info("Workspace engaged by the manager", "workspace", ws.Path)
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
			if err := create(ctx, ws.Client, NewCluster(name, opts.Backend)); err != nil {
				return Result{}, fmt.Errorf("creating Cluster %s in %s: %w", name, ws.Path, err)
			}

			for m := range opts.ControlPlaneMachines {
				machine := MachineName(name, m)
				// Infrastructure and bootstrap configuration before the
				// Machine, for the same reason the DevCluster comes before the
				// Cluster: the Machine reconciler resolves both references and
				// takes ownership of what it finds, and that ownership is what
				// starts the other two controllers working.
				if err := create(ctx, ws.Client, NewDevMachine(name, machine, opts.Backend)); err != nil {
					return Result{}, fmt.Errorf("creating DevMachine %s in %s: %w", machine, ws.Path, err)
				}
				if err := create(ctx, ws.Client, NewKubeadmConfig(name, machine)); err != nil {
					return Result{}, fmt.Errorf("creating KubeadmConfig %s in %s: %w", machine, ws.Path, err)
				}
				if err := create(ctx, ws.Client, NewControlPlaneMachine(name, machine, opts.KubernetesVersion)); err != nil {
					return Result{}, fmt.Errorf("creating Machine %s in %s: %w", machine, ws.Path, err)
				}
			}
		}
		log.Info("Clusters created", "workspace", ws.Path,
			"clusters", opts.ClustersPerWorkspace, "controlPlaneMachines", opts.ClustersPerWorkspace*opts.ControlPlaneMachines)
	}

	// 5. Watch them come up.
	result, err := waitForProvisioned(ctx, opts, workspaces)
	result.Manager = manager
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

// startManager wires and starts the fleet-wide controllers, exactly as
// cmd/core-manager does. Anything this did differently would make the demo a
// demonstration of something nobody deploys.
func startManager(
	ctx context.Context,
	opts Options,
	parentCfg *rest.Config,
	parentClient client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
) (mcmanager.Manager, error) {
	// MachinePool is on by default upstream and watched as an event source by
	// the core reconcilers; it is outside ADR-0001's D3 publishing scope, so
	// leaving it on stalls their cache sync against a workspace that cannot
	// serve it.
	if err := feature.MutableGates.Set("MachinePool=false"); err != nil {
		return nil, fmt.Errorf("disabling the MachinePool feature gate: %w", err)
	}
	coremanager.SetupProcessGlobals()

	registry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(parentCfg, opts.ExportName, scheme, registry)
	if err != nil {
		return nil, fmt.Errorf("constructing the kcp APIExport provider: %w", err)
	}

	// The manager is addressed at the export's virtual workspace, not at the
	// workspace holding the export: its RESTMapper has to describe the API
	// surface the engaged workspaces share, which the exporting workspace
	// does not bind.
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, parentClient, opts.ExportName, opts.BaseConfig, time.Minute)
	if err != nil {
		return nil, fmt.Errorf("resolving the APIExport's virtual workspace: %w", err)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("setting up the multicluster manager: %w", err)
	}

	// Ports of its own, rather than upstream's fixed ones. A demo is something
	// somebody runs next to whatever else they are running - another demo, an
	// integration test, a manager they left going - and the failure when those
	// collide arrives as "address already in use" from a component the reader
	// has no reason to have heard of.
	debugPort, minPort, maxPort, err := devInfrastructurePorts()
	if err != nil {
		return nil, err
	}
	dev, err := coremanager.NewDevInfrastructure(ctx,
		inmemoryserver.CustomPorts{MinPort: minPort, MaxPort: maxPort, DebugPort: debugPort})
	if err != nil {
		return nil, fmt.Errorf("setting up the dev infrastructure provider backend: %w", err)
	}

	// Before Start, and that ordering is load-bearing: multicluster-runtime
	// hands each engagement to the components registered at that moment and
	// never replays earlier ones.
	//
	// The fleet is built once and handed to each provider. Two providers each
	// building their own would each build a ClusterCache, and the second is
	// rejected rather than duplicated - see coremanager.Fleet.
	fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
		// The shard, not the manager's config: the ClusterCache reads
		// kubeconfig Secrets, which live in the workspaces themselves.
		ShardConfig: opts.BaseConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("building the fleet: %w", err)
	}
	if err := coremanager.SetupCoreControllers(ctx, mgr, fleet, dev); err != nil {
		return nil, fmt.Errorf("wiring the fleet-wide reconcilers: %w", err)
	}
	if opts.ControlPlaneMachines > 0 {
		if err := bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{}); err != nil {
			return nil, fmt.Errorf("wiring the fleet-wide bootstrap reconcilers: %w", err)
		}
	}

	go func() {
		if err := mgr.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error(err, "Manager exited")
		}
	}()

	return mgr, nil
}

// waitForProvisioned polls every workspace directly - not through the
// manager's caches - and renders the table until everything is provisioned or
// the timeout expires.
func waitForProvisioned(ctx context.Context, opts Options, workspaces []Workspace) (Result, error) {
	deadline := time.Now().Add(opts.Timeout)
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		statuses, err := Snapshot(ctx, workspaces, opts.ClustersPerWorkspace)
		if err != nil {
			return Result{Workspaces: workspaces}, err
		}
		machines, err := SnapshotMachines(ctx, workspaces, opts.ClustersPerWorkspace, opts.ControlPlaneMachines)
		if err != nil {
			return Result{Workspaces: workspaces, Statuses: statuses}, err
		}
		result := Result{Workspaces: workspaces, Statuses: statuses, Machines: machines}

		if err := RenderTable(opts.Out, statuses); err != nil {
			return result, err
		}
		if err := RenderMachineTable(opts.Out, machines); err != nil {
			return result, err
		}

		if result.Provisioned() {
			return result, nil
		}
		if time.Now().After(deadline) {
			return result, fmt.Errorf("timed out after %s with %d of %d clusters provisioned and %d of %d machines bootstrapped",
				opts.Timeout, provisionedCount(statuses), len(statuses), bootstrappedCount(machines), len(machines))
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

// SnapshotMachines reads every workspace's control plane machines, and the
// KubeadmConfig each one's bootstrap data comes from.
func SnapshotMachines(ctx context.Context, workspaces []Workspace, clustersPerWorkspace, machinesPerCluster int) ([]MachineStatus, error) {
	if machinesPerCluster == 0 {
		return nil, nil
	}

	statuses := make([]MachineStatus, 0, len(workspaces)*clustersPerWorkspace*machinesPerCluster)
	for _, ws := range workspaces {
		for n := range clustersPerWorkspace {
			for m := range machinesPerCluster {
				name := MachineName(ClusterName(n), m)
				key := client.ObjectKey{Namespace: Namespace, Name: name}

				machine := &clusterv1.Machine{}
				if err := ws.Client.Get(ctx, key, machine); err != nil {
					if apierrors.IsNotFound(err) {
						statuses = append(statuses, MachineStatus{
							Workspace: ws.Path, LogicalCluster: ws.LogicalCluster,
							Machine: name, Detail: "not created yet",
						})
						continue
					}
					return nil, fmt.Errorf("reading Machine %s in %s: %w", name, ws.Path, err)
				}

				config := &bootstrapv1.KubeadmConfig{}
				if err := ws.Client.Get(ctx, key, config); err != nil {
					if !apierrors.IsNotFound(err) {
						return nil, fmt.Errorf("reading KubeadmConfig %s in %s: %w", name, ws.Path, err)
					}
					config = nil
				}

				statuses = append(statuses, SummariseMachine(ws.Path, ws.LogicalCluster, machine, config))
			}
		}
	}
	return statuses, nil
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
