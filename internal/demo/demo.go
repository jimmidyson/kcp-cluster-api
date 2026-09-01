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

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpconfig"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

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
	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/workspacemanager"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
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

// operatorEnabled is what OnboardingStatus.EnabledBy says when a run has no
// tenants and the demo's own credentials made the bindings.
const operatorEnabled = "the operator"

// Onboarding is how a run's workspaces come to serve Cluster API. The two
// modes are the two kcp offers, and ADR-0001 names both: a managed
// WorkspaceType, and doing it by hand.
type Onboarding string

const (
	// OnboardingWorkspaceType creates each workspace with the Cluster API
	// WorkspaceType. kcp binds Cluster API's core APIExport into it and keeps
	// the accepted claims current, and this project's initializer writes the
	// workspace's roles before kcp lets it become Ready. The tenant enables
	// whichever providers they want afterwards, themselves.
	//
	// This is the default, and what a deployment should do.
	OnboardingWorkspaceType Onboarding = "workspace-type"

	// OnboardingManual creates universal workspaces and writes every
	// APIBinding and every role from here, with the demo's own credentials.
	//
	// It is not a legacy path. It is the opt-out ADR-0001 documents: a tenant
	// who hand-creates their APIBinding to core is not on the managed
	// lifecycle, so nothing rewrites their accepted claims and nothing
	// recreates a binding they delete. A run that wants to take a workspace
	// apart needs exactly that - which is why test/integration/teardown uses
	// it, and why this mode is worth keeping working rather than deleting.
	OnboardingManual Onboarding = "manual"
)

// Validate rejects a mode this package does not implement.
func (o Onboarding) Validate() error {
	switch o {
	case OnboardingWorkspaceType, OnboardingManual:
		return nil
	default:
		return fmt.Errorf("unknown onboarding mode %q, want %q or %q", o, OnboardingWorkspaceType, OnboardingManual)
	}
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

	// Users are the tenants the workspaces are shared out between, one home
	// workspace each under an org workspace the demo owns. Asking for any
	// turns the run into a multi-tenant one: every workspace gets an owner,
	// each owner is granted only their own, and the run reports what each of
	// them can and cannot read of the others (Result.Access).
	//
	// Empty means no users at all: every workspace sits directly under Parent
	// and only the demo's own credentials ever touch it. That is what the
	// sweeps and the scale harness drive, because what they measure is the
	// controllers rather than the tenancy. cmd/demo asks for DefaultUsers,
	// because a demo of a multi-tenant system with one tenant is not one.
	//
	// There must be at least as many workspaces as users; a tenant with no
	// workspace is a row of nothing in every table.
	Users []string

	// ClustersPerWorkspace is how many Cluster/DevCluster pairs each
	// workspace gets. Zero means DefaultClusters.
	ClustersPerWorkspace int

	// ControlPlaneMachines is how many control plane machines each cluster
	// gets, as a KubeadmControlPlane the Cluster points at. The kubeadm
	// bootstrap and control plane providers create each machine's Machine,
	// KubeadmConfig and DevMachine themselves.
	//
	// At least one. The ClusterClass every demo cluster is built from always
	// names a control plane, so a run asking for none asks for a blueprint it
	// cannot satisfy.
	ControlPlaneMachines int

	// WorkerMachines is how many worker machines each cluster gets, as a
	// MachineDeployment. They always have a control plane to join, because
	// ControlPlaneMachines is at least one.
	WorkerMachines int

	// KubernetesVersion is what those machines ask for. Empty means
	// DefaultKubernetesVersion.
	KubernetesVersion string

	// NutanixExport publishes the Nutanix infrastructure provider's APIExport
	// alongside the others, so a workspace can bind its types.
	//
	// Off by default, for the reason the bootstrap and control plane exports
	// are conditional: nothing in this repository reconciles the Nutanix
	// types, so publishing them by default would bind types nothing uses into
	// every workspace and move the per-workspace cost this project measures.
	// It is a flag rather than an always-on because the export is real and
	// worth being able to see, not because anything here can act on it.
	NutanixExport bool

	// Backend selects the DevCluster backend. Empty means BackendInMemory,
	// the only one that needs neither a container runtime nor image pulls.
	Backend Backend

	// ImpersonationConfig is the credential the demo impersonates tenants
	// from. Empty means BaseConfig, which is right only when BaseConfig is
	// itself privileged.
	//
	// It is separate from BaseConfig because kcp scopes an impersonated user
	// to the logical cluster the request addresses unless the impersonator is
	// in system:masters - so a tenant impersonated from an ordinary admin is a
	// strictly weaker stand-in than the real user, and is refused outright in
	// any other workspace. Enabling a provider is authorized in the workspace
	// holding the APIExport, so an under-privileged impersonator turns "the
	// tenant enabled it" into "no permission to bind to export ...". See
	// ConfigForUser.
	//
	// For a demo-started kcp this is the shard-base context of its
	// kubeconfig; for the test fixture it is
	// RootShardSystemMasterBaseConfig.
	ImpersonationConfig *rest.Config

	// Onboarding is how a run's workspaces come to serve Cluster API. Empty
	// means OnboardingWorkspaceType.
	Onboarding Onboarding

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
	if o.Onboarding == "" {
		o.Onboarding = OnboardingWorkspaceType
	}
	if o.ImpersonationConfig == nil {
		o.ImpersonationConfig = o.BaseConfig
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
// All four, always. The bootstrap and control plane exports used to be
// conditional on the run asking for machines, from when a demo cluster was a
// Cluster with an infrastructureRef and nothing else — with no machines their
// types really were unused. A cluster is a ClusterClass based cluster now, the
// class always names a KubeadmControlPlaneTemplate, and Blueprint creates one
// unconditionally, so the types are always used and the condition had become a
// way to publish a blueprint whose kinds nobody had bound.
//
// The Nutanix export is the exception, and is the case that condition was
// wrongly generalising from: nothing here reconciles its types, so publishing
// it by default would bind schemas into every workspace that no controller
// acts on. It is asked for rather than assumed.
func (o Options) providers() []capiexports.Provider {
	providers := []capiexports.Provider{
		capiexports.Core(),
		capiexports.Infrastructure(),
		capiexports.Bootstrap(),
		capiexports.ControlPlane(),
	}
	if o.NutanixExport {
		providers = append(providers, capiexports.NutanixInfrastructure())
	}
	return providers
}

// reconciled is the subset of providers this process runs controllers for.
//
// Every export the demo publishes has a manager except the Nutanix one, which
// is published so its types can be bound and has nothing here to reconcile it.
// Giving it a manager anyway would engage every workspace to watch nothing,
// which is both a cost and a thing that reads as a wiring bug later.
func reconciled(providers []capiexports.Provider) []capiexports.Provider {
	out := make([]capiexports.Provider, 0, len(providers))
	for _, p := range providers {
		if p.Export == capiexports.NutanixInfraExport {
			continue
		}
		out = append(out, p)
	}
	return out
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
	if o.ControlPlaneMachines < 1 {
		return fmt.Errorf("ControlPlaneMachines = %d, want at least 1: the ClusterClass every demo cluster is built from always names a control plane, so a run with none asks for a blueprint it cannot satisfy",
			o.ControlPlaneMachines)
	}
	if err := validateUsers(o.Users); err != nil {
		return err
	}
	if len(o.Users) > o.Workspaces {
		return fmt.Errorf("%d users and %d workspaces: a user with no workspace owns nothing to be isolated from",
			len(o.Users), o.Workspaces)
	}
	if err := o.Onboarding.Validate(); err != nil {
		return err
	}
	return o.Backend.Validate()
}

// Workspace is one workspace the demo created, in both the names it has: the
// path a person uses and the logical cluster name the manager engages by.
type Workspace struct {
	Path           string
	LogicalCluster string

	// Owner is the user this workspace belongs to, or empty when the run has
	// no users. It is the demo's own record of who was granted what, and it is
	// what the access checks compare kcp's answers against.
	Owner string

	// ProvidersEnabled are the provider APIExports bound in this workspace by
	// somebody choosing to enable them. Core is not among them under the
	// WorkspaceType: nobody chose it, the type binds it.
	ProvidersEnabled []string

	// EnabledBy is who made those bindings - the owner's name in a run with
	// tenants, or "the operator" in one without. It is reported because the
	// difference is the point: a provider a tenant enabled is proof of a
	// permission, and one the demo's admin credentials enabled is not.
	EnabledBy string

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

	// Users is every tenant the run created, or empty when it had none.
	Users []User

	// Org is the workspace holding every user's home. Empty when the run had
	// no users.
	Org string

	// Parent is the workspace the exports were published in and the org
	// workspace was created under. It is reported because it is the top of the
	// tree a tenant can see anything of at all - see AccessCheck.
	Parent string

	// Access is what each user could and could not read of every other user's
	// workspaces, as kcp answered it. Empty when the run had no users, and
	// when the run did not get far enough to ask.
	Access []AccessCheck

	// Onboarding is how each workspace came to serve Cluster API: what it was
	// bound to without asking, what its tenant enabled, and what its roles
	// ended up covering.
	Onboarding []OnboardingStatus

	// Claims is every permission claim on the exports this run published,
	// with the export each one lands on resolved from its identity hash - and
	// which of them nobody wrote down.
	Claims []ClaimStatus

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
// There is no fallback for a run with no control planes. Every run has one
// now, so an empty ControlPlanes means the snapshot failed rather than that
// none was asked for, and reporting that as ready-by-provisioned would call a
// broken run finished.
func (r Result) Ready() bool {
	if len(r.ControlPlanes) == 0 {
		return false
	}
	return AllClustersReady(r.Statuses) &&
		AllControlPlanesReady(r.ControlPlanes) &&
		len(r.Machines) >= r.ExpectedMachines &&
		AllMachinesReady(r.Machines)
}

// Isolated reports whether every access check came out as intended: each user
// read their own workspaces, and none of them read anybody else's or the org
// workspace holding every home. The parent workspace is reported rather than
// asserted and does not count either way - see AccessCheck.Reported.
//
// A run with no users is not isolated, for the reason an empty check set is
// not: it demonstrated nothing about tenancy, which is a different answer from
// demonstrating that tenancy holds.
func (r Result) Isolated() bool {
	return Isolated(r.Access)
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
	parentCfg := kcpconfig.ForCluster(opts.BaseConfig, parentPath.String())
	parentClient, err := client.New(parentCfg, client.Options{Scheme: scheme})
	if err != nil {
		return Result{}, fmt.Errorf("building a client for %s: %w", opts.Parent, err)
	}

	// 1. Publish one APIExport per provider, and resolve the claims that let
	// each provider's controllers reach the types another one publishes.
	providers := opts.providers()
	published := providers
	if opts.Onboarding == OnboardingWorkspaceType {
		// The onboarding export as well. It publishes no Cluster API type; it
		// is the identity the controllers that write a workspace's roles act
		// under. See capiexports.Workspaces.
		published = append(slices.Clone(providers), capiexports.Workspaces())
	}
	log.Info("Publishing the APIExports", "workspace", opts.Parent, "exports", exportNames(published))
	discovery, err := capiexports.Publish(ctx, parentClient, published, 2*time.Minute)
	if err != nil {
		return Result{}, err
	}

	// 2. The WorkspaceType a tenant onboards with, and the deployment behind
	// it. Both before any workspace exists: a workspace of this type is held
	// out of Ready until the initializer has written its roles, so creating
	// one with nothing running would simply time out.
	if opts.Onboarding == OnboardingWorkspaceType {
		workspaceType := capiworkspaces.NewWorkspaceType(opts.Parent, capiworkspaces.DefaultExports())
		if err := capiworkspaces.EnsureWorkspaceType(ctx, parentClient, workspaceType, time.Minute); err != nil {
			return Result{}, err
		}
		log.Info("Published the Cluster API WorkspaceType",
			"workspace", opts.Parent, "type", capiworkspaces.WorkspaceTypeName,
			"binds", exportRefNames(workspaceType.Spec.DefaultAPIBindings))

		if opts.RunManager {
			runner, err := workspacemanager.New(ctx, workspacemanager.Options{
				BaseConfig:   opts.BaseConfig,
				ProviderPath: opts.Parent,
				Providers:    published,
				Timeout:      2 * time.Minute,
				// This process runs every manager, which a deployment does
				// not, and a test binary runs several demos in turn.
				SkipControllerNameValidation: true,
			})
			if err != nil {
				return Result{}, fmt.Errorf("setting up the workspace manager: %w", err)
			}
			go func() {
				if err := runner.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Error(err, "Workspace manager exited")
				}
			}()
		}
	}

	// 3. The tenants, when the run has any: an org workspace holding one home
	// workspace per user, each readable only by its own user. Everything a
	// user owns is created inside their home, so the isolation the run reports
	// is a property of where the workspaces are and who was granted what -
	// not of anything the demo does at read time.
	users, err := ensureUsers(ctx, opts, parentClient, scheme, log)
	if err != nil {
		return Result{}, err
	}

	// Tenants may enable a provider for themselves, which is a grant made
	// here rather than in their own workspace: kcp authorizes creating an
	// APIBinding as the verb "bind" on the APIExport, in the workspace the
	// export lives in.
	bindable := tenantProviders(providers, opts.Onboarding)
	if len(opts.Users) > 0 && len(bindable) > 0 {
		if err := capiworkspaces.GrantProviderBinding(ctx, parentClient, opts.Users, bindable); err != nil {
			return Result{}, fmt.Errorf("letting the tenants enable providers: %w", err)
		}
		log.Info("Tenants may enable these providers themselves", "users", opts.Users, "exports", bindable)
	}

	// 4. Create the workspaces and enable the providers in each. The endpoint
	// slice stays empty until at least one binding consumes the export, so
	// this has to happen before anything waits on it.
	plans := PlanWorkspaces(opts.Parent, opts.WorkspacePrefix, opts.Users, opts.Workspaces)
	workspaces := make([]Workspace, 0, len(plans))
	for _, plan := range plans {
		planParent := parentClient
		if plan.Parent != opts.Parent {
			planParent, err = clientFor(opts.BaseConfig, plan.Parent, scheme)
			if err != nil {
				return Result{}, err
			}
		}

		workspaceType := kcpfixtures.DefaultWorkspaceType
		if opts.Onboarding == OnboardingWorkspaceType {
			workspaceType = capiworkspaces.TypeReference(opts.Parent)
		}
		// Longer than a universal workspace needs, because this one waits on
		// more: kcp's own initializers, then the default APIBindings, then
		// this project's initializer writing the roles.
		clusterName, err := kcpfixtures.EnsureWorkspaceOfType(ctx, planParent, plan.Name, workspaceType, 2*time.Minute)
		if err != nil {
			return Result{}, err
		}

		wsPath := logicalcluster.NewPath(plan.Path)
		wsClient, err := clientFor(opts.BaseConfig, plan.Path, scheme)
		if err != nil {
			return Result{}, err
		}

		// The owner's grants first, and that ordering is the change the
		// WorkspaceType made: a tenant who is about to enable a provider for
		// themselves has to be able to enter the workspace and write an
		// APIBinding in it before they can. The roles they are being given
		// already exist - the initializer wrote them - so this only says who
		// holds them.
		//
		// Hand-onboarded, they do not exist yet and are written below. A
		// ClusterRoleBinding naming a role that is not there is not an error
		// and starts granting the moment the role appears, so the order is
		// still this one.
		if plan.Owner != "" {
			if err := capiworkspaces.GrantRoles(ctx, wsClient, plan.Owner, capiworkspaces.AdminRoleName); err != nil {
				return Result{}, fmt.Errorf("granting %s their workspace %s: %w", plan.Owner, plan.Path, err)
			}
		}

		// One binding per provider, made by whoever the run says makes it. In
		// a multi-tenant run that is the tenant, impersonated - so a binding
		// that appears is proof the tenant was allowed to make it, not proof
		// that the demo's admin credentials were.
		//
		// Core is not among them under the WorkspaceType: kcp bound it when
		// the workspace was created, under a name it generated, and binding it
		// again would bind the same export twice.
		enabler, enabledBy := wsClient, operatorEnabled
		if plan.Owner != "" && opts.Onboarding == OnboardingWorkspaceType {
			// ConfigForUser has already scoped the config to the workspace,
			// so this builds a client from it rather than going through
			// clientFor, which would scope it a second time and address
			// /clusters/<path>/clusters/<path>.
			enabler, err = client.New(ConfigForUser(opts.ImpersonationConfig, plan.Owner, plan.Path), client.Options{Scheme: scheme})
			if err != nil {
				return Result{}, fmt.Errorf("building a client for %s as %s: %w", plan.Path, plan.Owner, err)
			}
			enabledBy = plan.Owner
		}
		enabled := make([]string, 0, len(providers))
		for _, provider := range providers {
			if opts.Onboarding == OnboardingWorkspaceType && provider.Export == capiexports.CoreExport {
				continue
			}
			if err := kcpfixtures.BindExport(ctx, enabler, kcpfixtures.BindExportOptions{
				BindingName:      provider.Export,
				ExportPath:       opts.Parent,
				ExportName:       provider.Export,
				PermissionClaims: provider.Claims(discovery.Identities(), discovery),
				ReadyTimeout:     time.Minute,
			}); err != nil {
				if apierrors.IsForbidden(err) && enabledBy != operatorEnabled {
					// The likeliest cause by a distance, and it is not
					// something more RBAC will fix: an impersonated tenant is
					// scoped to their own workspace unless the impersonator is
					// privileged, and the right to enable a provider is
					// checked in the workspace holding the export. See
					// Options.ImpersonationConfig.
					return Result{}, fmt.Errorf("enabling %s in %s as %s: %w; "+
						"if this is a refusal to bind, the credential tenants are impersonated from is not privileged "+
						"enough - see demo.Options.ImpersonationConfig", provider.Export, wsPath, enabledBy, err)
				}
				return Result{}, fmt.Errorf("enabling %s in %s as %s: %w", provider.Export, wsPath, enabledBy, err)
			}
			enabled = append(enabled, provider.Export)
		}

		// Hand-onboarded workspaces get their roles from here, because nothing
		// else is going to write them. Exactly the roles the WorkspaceType's
		// initializer would have written - the same function - so the two
		// modes differ in who acts, not in what a tenant ends up with.
		if opts.Onboarding == OnboardingManual {
			if _, err := capiworkspaces.ReconcileRoles(ctx, wsClient); err != nil {
				return Result{}, fmt.Errorf("writing the Cluster API roles in %s: %w", wsPath, err)
			}
		}

		log.Info("Workspace ready to hold clusters", "workspace", wsPath.String(),
			"logicalCluster", clusterName, "owner", plan.Owner,
			"providersEnabled", enabled, "enabledBy", enabledBy)
		workspaces = append(workspaces, Workspace{
			Path:             wsPath.String(),
			LogicalCluster:   clusterName,
			Owner:            plan.Owner,
			ProvidersEnabled: enabled,
			EnabledBy:        enabledBy,
			Client:           wsClient,
		})
	}

	for _, provider := range providers {
		if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, parentClient, provider.Export, time.Minute); err != nil {
			return Result{}, fmt.Errorf("waiting for %s's APIExportEndpointSlice to get an endpoint: %w", provider.Export, err)
		}
	}

	// 5. The provider managers: the same wiring cmd/core-manager and its
	// siblings run, serving every workspace bound to their export from one set
	// of controllers each.
	var manager mcmanager.Manager
	var byExport map[string]mcmanager.Manager
	if opts.RunManager {
		// One subset, derived once. Published and reconciled differ — an
		// export can be published with nothing here to reconcile it — and
		// deriving that twice is how the two come apart: startManagers built
		// its map from the reconciled set while the wait loop below iterated
		// the published one, so the export with no manager was waited on and
		// WaitForManager was handed a nil.
		running := reconciled(providers)

		managers, err := startManagers(ctx, opts, running, parentCfg, parentClient, scheme, log)
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
			for _, provider := range running {
				if _, err := coremanager.WaitForManager(ctx, managers[provider.Export],
					multicluster.ClusterName(ws.LogicalCluster), time.Second, 2*time.Minute); err != nil {
					return Result{}, fmt.Errorf("workspace %s was never engaged by %s: %w", ws.Path, provider.Export, err)
				}
			}
			log.Info("Workspace engaged by every provider", "workspace", ws.Path)
		}
	}

	// 5. The blueprint, then a cluster in every workspace, all with the same
	// names.
	for _, ws := range workspaces {
		// The ClusterClass and the five templates it refers to, once per
		// workspace. Templates before the class that names them: the
		// ClusterClass reconciler resolves every reference and reports the
		// class not ready until they all resolve, and a class created first
		// simply waits - but a demo that watched it wait would be showing the
		// wait rather than the cluster.
		for _, obj := range Blueprint(opts.Backend) {
			if err := create(ctx, ws.Client, obj); err != nil {
				return Result{}, fmt.Errorf("creating %T %s in %s: %w", obj, obj.GetName(), ws.Path, err)
			}
		}

		for n := range opts.ClustersPerWorkspace {
			name := ClusterName(n)
			// One object per cluster, and it names a class rather than the
			// objects under it. The topology controller creates the DevCluster,
			// the KubeadmControlPlane and the worker MachineDeployment from the
			// class, and the reconcilers that were already wired take those on
			// exactly as they did when the demo wrote them out by hand.
			if err := create(ctx, ws.Client, NewCluster(name, opts.ControlPlaneMachines, opts.WorkerMachines, opts.KubernetesVersion)); err != nil {
				return Result{}, fmt.Errorf("creating Cluster %s in %s: %w", name, ws.Path, err)
			}
		}
		log.Info("Clusters created", "workspace", ws.Path,
			"class", ClassName,
			"clusters", opts.ClustersPerWorkspace,
			"controlPlaneMachines", opts.ClustersPerWorkspace*opts.ControlPlaneMachines,
			"workerMachines", opts.ClustersPerWorkspace*opts.WorkerMachines)
	}

	// 6. Anything the caller has to do while they come up, in parallel with
	// watching. See Options.WhileProvisioning.
	if opts.WhileProvisioning != nil {
		go opts.WhileProvisioning(ctx, workspaces)
	}

	// 7. Watch them come up.
	result, waitErr := waitForReady(ctx, opts, workspaces)
	result.Manager = manager
	result.Managers = byExport
	result.Users = users
	result.Org = OrgPath(opts.Parent, opts.WorkspacePrefix)
	result.Parent = opts.Parent
	if waitErr != nil {
		return result, waitErr
	}

	// 8. Ask kcp, as each user in turn, what it will let them read of the
	// others. Last, because a check that ran before the clusters existed
	// would report an empty list where it means to report a leak.
	//
	// Returned rather than printed: unlike the status tables there is nothing
	// to watch here, so it belongs with the run's final report rather than in
	// the poll loop. cmd/demo renders it there.
	if len(users) > 0 {
		access, err := CheckAccess(ctx, opts.ImpersonationConfig, opts.Parent, result.Org, users, workspaces)
		if err != nil {
			return result, fmt.Errorf("checking what each user can read: %w", err)
		}
		result.Access = access
	}

	// 9. What each workspace was onboarded with, and what the claim list ended
	// up saying. Both read back from the server rather than reported from what
	// the run did: a table built from the demo's own intentions would look the
	// same whether or not any of it worked.
	onboarding, err := SnapshotOnboarding(ctx, workspaces)
	if err != nil {
		return result, err
	}
	result.Onboarding = onboarding

	claims, err := SnapshotClaims(ctx, parentClient, exportNames(published))
	if err != nil {
		return result, err
	}
	result.Claims = claims

	return result, nil
}

// clientFor builds a client scoped to one workspace path.
func clientFor(base *rest.Config, path string, scheme *runtime.Scheme) (client.Client, error) {
	cfg := kcpconfig.ForCluster(base, path)
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("building a client for workspace %s: %w", path, err)
	}
	return cl, nil
}

// ensureUsers creates the org workspace and one home workspace per user, and
// grants each user their own home and nothing else.
//
// The org workspace is what makes the demonstration mean anything. kcp's root
// workspace grants every authenticated user tenancy reads by default, so homes
// created directly under root would be listable by everybody; under an org
// workspace nothing grants, a user can read their own home and is refused
// every other, including the org itself. A run with no users creates none of
// this.
func ensureUsers(
	ctx context.Context,
	opts Options,
	parentClient client.Client,
	scheme *runtime.Scheme,
	log logr.Logger,
) ([]User, error) {
	if len(opts.Users) == 0 {
		return nil, nil
	}

	orgPath := OrgPath(opts.Parent, opts.WorkspacePrefix)
	if _, err := kcpfixtures.EnsureWorkspace(ctx, parentClient, opts.WorkspacePrefix, time.Minute); err != nil {
		return nil, fmt.Errorf("creating the org workspace %s: %w", orgPath, err)
	}
	orgClient, err := clientFor(opts.BaseConfig, orgPath, scheme)
	if err != nil {
		return nil, err
	}

	users := make([]User, 0, len(opts.Users))
	for _, name := range opts.Users {
		if _, err := kcpfixtures.EnsureWorkspace(ctx, orgClient, name, time.Minute); err != nil {
			return nil, fmt.Errorf("creating %s's home workspace: %w", name, err)
		}
		homePath := HomePath(opts.Parent, opts.WorkspacePrefix, name)
		homeClient, err := clientFor(opts.BaseConfig, homePath, scheme)
		if err != nil {
			return nil, err
		}
		if err := GrantHomeOwner(ctx, homeClient, name); err != nil {
			return nil, fmt.Errorf("granting %s their home workspace: %w", name, err)
		}

		log.Info("User home workspace ready", "user", name, "workspace", homePath)
		users = append(users, User{Name: name, Home: homePath})
	}
	return users, nil
}

// Blueprint is every object a workspace needs before a Cluster can name a
// class: the templates, and the ClusterClass that refers to them.
//
// One set per workspace rather than one per cluster, and in this order - the
// class last, so that by the time it exists everything it refers to does.
//
// Exported because the integration tests build clusters the same way the demo
// does, and a second copy of a class is a second thing to keep in step with the
// exports that publish its types.
func Blueprint(backend Backend) []client.Object {
	return []client.Object{
		NewDevClusterTemplate(backend),
		NewDevMachineTemplate(ControlPlaneMachineTemplateName, backend),
		NewDevMachineTemplate(WorkerMachineTemplateName, backend),
		NewKubeadmControlPlaneTemplate(),
		NewKubeadmConfigTemplate(WorkerBootstrapTemplateName),
		NewClusterClass(backend),
	}
}

func create(ctx context.Context, cl client.Client, obj client.Object) error {
	if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
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
	// The same gates a deployment runs with, set the same way: ClusterTopology
	// on, because a demo cluster is a ClusterClass based cluster, and
	// MachinePool off, because these exports publish no MachinePool CRD and a
	// watch on a type the server does not serve stalls the cache sync.
	if err := coremanager.SetFeatureGateDefaults(); err != nil {
		return nil, err
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
	provider, err := providerwiring.NewAPIExportProvider(parentCfg, export, scheme, registry,
		providerwiring.WithCacheIndexes(ctx, coremanager.FleetCacheIndexes()...))
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

// tenantProviders are the exports a tenant is expected to enable for
// themselves: every provider the run wires, minus the one their WorkspaceType
// already bound for them.
func tenantProviders(providers []capiexports.Provider, onboarding Onboarding) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		if onboarding == OnboardingWorkspaceType && p.Export == capiexports.CoreExport {
			continue
		}
		names = append(names, p.Export)
	}
	return names
}

func exportRefNames(refs []tenancyv1alpha1.APIExportReference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Export)
	}
	return names
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
		controlPlanes, err := SnapshotControlPlanes(ctx, workspaces, opts.ClustersPerWorkspace)
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
func SnapshotControlPlanes(ctx context.Context, workspaces []Workspace, clustersPerWorkspace int) ([]ControlPlaneStatus, error) {
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
