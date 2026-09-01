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

package workspacemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpconfig"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
)

// Scheme is what every manager in this deployment reads and writes through:
// kcp's own types, and core Kubernetes for the ClusterRoles.
//
// No Cluster API types at all. This deployment reconciles none of them - it
// reconciles the permission to use them - and a scheme that carried them would
// suggest otherwise.
func Scheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apisv1alpha1.AddToScheme,
		apisv1alpha2.AddToScheme,
		corev1alpha1.AddToScheme,
		tenancyv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("building the scheme: %w", err)
		}
	}
	return scheme, nil
}

// Options configures the workspace manager.
type Options struct {
	// BaseConfig addresses the kcp shard, cluster-unaware. Required.
	BaseConfig *rest.Config

	// ProviderPath is the workspace the Cluster API APIExports and the
	// WorkspaceType live in. Required.
	ProviderPath string

	// Providers are the exports whose permission claims this deployment
	// maintains. Empty means every provider this repository ships.
	Providers []capiexports.Provider

	// Scheme is what the managers read through. Nil means Scheme().
	Scheme *runtime.Scheme

	// SkipControllerNameValidation turns off controller-runtime's
	// process-global check that no two controllers share a name. A deployment
	// runs one of these and should leave it off; a test process that stands
	// several installations up in turn has to set it, because that registry is
	// never emptied.
	SkipControllerNameValidation bool

	// Timeout bounds each wait for something kcp populates asynchronously -
	// the WorkspaceType's virtual workspace URL, the onboarding export's
	// endpoint. Zero means one minute.
	Timeout time.Duration
}

func (o *Options) applyDefaults() error {
	if o.BaseConfig == nil {
		return errors.New("BaseConfig is required")
	}
	if o.ProviderPath == "" {
		return errors.New("ProviderPath is required: the workspace the APIExports live in")
	}
	if len(o.Providers) == 0 {
		o.Providers = append(capiexports.All(), capiexports.Workspaces())
	}
	if o.Timeout == 0 {
		o.Timeout = time.Minute
	}
	if o.Scheme == nil {
		scheme, err := Scheme()
		if err != nil {
			return err
		}
		o.Scheme = scheme
	}
	return nil
}

// Runner is the deployment: three managers, started together.
type Runner struct {
	opts           Options
	providerClient client.Client

	// Claims maintains the permission claims on the APIExports. It is scoped
	// to the workspace they live in.
	Claims manager.Manager

	// Initializer serves the workspaces waiting on the Cluster API
	// WorkspaceType's initializer.
	Initializer mcmanager.Manager
}

// New builds the two managers that can exist before any workspace does.
//
// The third - the fleet-wide role maintainer - cannot: it is built on the
// onboarding APIExport's virtual workspace, and kcp gives that export an
// endpoint only once something has bound it. Start builds it when it can be
// built, which is why it is not a field here.
func New(ctx context.Context, opts Options) (*Runner, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}

	providerCfg := kcpconfig.ForCluster(opts.BaseConfig, opts.ProviderPath)
	providerClient, err := client.New(providerCfg, client.Options{Scheme: opts.Scheme})
	if err != nil {
		return nil, fmt.Errorf("building a client for %s: %w", opts.ProviderPath, err)
	}

	claims, err := ctrl.NewManager(providerCfg, ctrl.Options{
		Scheme:                 opts.Scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("setting up the permission-claim manager: %w", err)
	}
	if err := AddClaimsToManager(claims, opts.Providers, opts.SkipControllerNameValidation); err != nil {
		return nil, fmt.Errorf("wiring the permission-claim controller: %w", err)
	}

	initializer, err := newInitializerManager(ctx, opts, providerCfg, providerClient)
	if err != nil {
		return nil, err
	}

	return &Runner{opts: opts, providerClient: providerClient, Claims: claims, Initializer: initializer}, nil
}

// Start runs every manager until ctx is done, and returns the first error any
// of them fails with.
//
// The claim controller and the initializer start immediately, because nothing
// can be onboarded until they are running: a workspace of the Cluster API type
// does not become Ready until the initializer has written its roles. The role
// maintainer starts as soon as the onboarding export has an endpoint, which
// the first workspace to be created provides.
func (r *Runner) Start(ctx context.Context) error {
	errs := make(chan error, 3)
	go func() { errs <- r.Claims.Start(ctx) }()
	go func() { errs <- r.Initializer.Start(ctx) }()
	go func() { errs <- r.startMaintainer(ctx) }()

	for range 3 {
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}

func (r *Runner) startMaintainer(ctx context.Context) error {
	mgr, err := newMaintainerManager(ctx, r.opts, r.providerClient)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	return mgr.Start(ctx)
}

// newInitializerManager builds the manager that serves workspaces waiting on
// the Cluster API initializer.
func newInitializerManager(ctx context.Context, opts Options, providerCfg *rest.Config, providerClient client.Client) (mcmanager.Manager, error) {
	workspaceType := &tenancyv1alpha1.WorkspaceType{}
	key := client.ObjectKey{Name: string(capiworkspaces.WorkspaceTypeName)}
	if err := providerClient.Get(ctx, key, workspaceType); err != nil {
		return nil, fmt.Errorf("reading WorkspaceType %s in %s: %w", key.Name, opts.ProviderPath, err)
	}

	// Read off the live object rather than rebuilt from the path. kcp derives
	// the initializer's name from the type's *logical cluster* name, which
	// equals its path only in `root`; rebuilding it works in every test and
	// fails in the one deployment that publishes its exports somewhere else.
	initializer := initialization.InitializerForType(workspaceType)

	// The provider workspace's config, not the cluster-unaware base one. The
	// provider builds a cache over the WorkspaceType to read the initializing
	// virtual workspace URLs off its status, and a config addressing no
	// logical cluster cannot even do discovery: it fails with "failed to get
	// server groups: unknown" before it looks at anything.
	provider, err := capiworkspaces.NewInitializerProvider(providerCfg, opts.Scheme)
	if err != nil {
		return nil, fmt.Errorf("constructing the initializing-workspaces provider: %w", err)
	}
	mgr, err := mcmanager.New(providerCfg, provider, ctrl.Options{
		Scheme:                 opts.Scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("setting up the initializer manager: %w", err)
	}
	if err := capiworkspaces.AddInitializerToManager(mgr, initializer, opts.SkipControllerNameValidation); err != nil {
		return nil, fmt.Errorf("wiring the workspace initializer: %w", err)
	}
	return mgr, nil
}

// newMaintainerManager builds the fleet-wide manager that keeps every Cluster
// API workspace's roles current.
func newMaintainerManager(ctx context.Context, opts Options, providerClient client.Client) (mcmanager.Manager, error) {
	export := capiexports.WorkspaceExport
	registry := &capicontrollerutil.WildcardRegistry{}
	// Discovered by the workspace's LogicalCluster rather than by an
	// APIBinding, and that is not optional here. This deployment claims
	// `apibindings`, which replaces the virtual workspace's normally-filtered
	// view with every APIBinding the workspace holds - kcp's own tenancy and
	// topology bindings among them, which never go away. A provider
	// disengages only when nothing it watches remains for that workspace, so
	// discovering by APIBinding engages correctly and never disengages,
	// whatever the filter says. There is one LogicalCluster per workspace and
	// it is visible only while this export is bound, so the count is one and
	// reaches zero. See providerwiring.WithLogicalClusterDiscovery.
	provider, err := providerwiring.NewAPIExportProvider(
		kcpconfig.ForCluster(opts.BaseConfig, opts.ProviderPath),
		export, opts.Scheme, registry, providerwiring.WithLogicalClusterDiscovery())
	if err != nil {
		return nil, fmt.Errorf("constructing the kcp APIExport provider for %s: %w", export, err)
	}

	// Addressed at the export's virtual workspace rather than at the workspace
	// holding it, for the reason VirtualWorkspaceConfig gives: the local
	// manager's RESTMapper has to describe what the engaged workspaces serve,
	// and the exporting workspace does not bind what it exports.
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, providerClient, export, opts.BaseConfig, opts.Timeout)
	if err != nil {
		return nil, fmt.Errorf("resolving %s's virtual workspace: %w", export, err)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 opts.Scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("setting up the role-maintaining manager: %w", err)
	}
	if err := capiworkspaces.AddMaintainerToManager(mgr, opts.SkipControllerNameValidation); err != nil {
		return nil, fmt.Errorf("wiring the role maintainer: %w", err)
	}
	return mgr, nil
}
