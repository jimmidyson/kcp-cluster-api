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

// Command core-manager runs the unmodified upstream Cluster API core
// reconcilers against every KCP workspace bound to the core APIExport,
// discovered and cached via
// github.com/kcp-dev/multicluster-provider + sigs.k8s.io/multicluster-runtime.
// See docs/conversion-plan.md and docs/adr-0001-per-workspace-manager-pool.md.
//
// Workspaces are not named in configuration: each is set up as it binds and
// torn down as it unbinds, by internal/providerwiring. Two things remain
// narrower than upstream's own core/main.go, both deliberately:
//
//   - Only the Cluster and Machine reconcilers are wired, rather than the full
//     core reconciler set. The other providers are deployments of their own,
//     each consuming its own APIExport - see internal/capiexports and
//     cmd/kubeadm-bootstrap-manager, cmd/dev-infrastructure-manager.
//   - Webhooks are served for at most one workspace, named by
//     --webhook-workspace-cluster-name, because routing an admission request
//     to its own workspace is the conversion plan's G4 and is unbuilt. Left
//     unset, no webhooks are served.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/workspacetelemetry"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	infrav1beta1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	endpointSliceName  string
	startupTimeout     time.Duration
	webhookWorkspace   string
	engageTimeout      time.Duration
	engagePollInterval time.Duration
	webhookPort        int
	webhookCertDir     string
	webhookCertName    string
	webhookKeyName     string
	healthAddr         string
	metricsAddr        string

	maxConcurrentReconciles int

	logOptions = logs.NewOptions()
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = apisv1alpha1.AddToScheme(scheme)
	_ = apisv1alpha2.AddToScheme(scheme)

	_ = clusterv1beta1.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)

	_ = infrav1beta1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)
}

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&endpointSliceName, "endpoint-slice-name", capiexports.CoreExport,
		"Name of the APIExportEndpointSlice (in the workspace targeted by --kubeconfig/in-cluster config) "+
			"whose virtual workspace URLs are used to discover and cache bound workspaces.")

	fs.DurationVar(&startupTimeout, "startup-timeout", time.Minute,
		"How long to wait for --endpoint-slice-name to have a virtual workspace endpoint before giving "+
			"up. kcp gives an APIExport an endpoint only once a workspace has bound it, so a manager "+
			"started before the first tenant - which is what a Deployment applied alongside everything "+
			"else is - waits here. Raise it for an installation whose workspaces arrive later than its "+
			"controllers; a process run by hand against a shard that is already set up wants the default.")

	fs.StringVar(&webhookWorkspace, "webhook-workspace-cluster-name", "",
		"Internal logical cluster name (not the human-readable workspace path) of the one workspace "+
			"whose admission and conversion webhooks this process serves. Serving more than one "+
			"requires resolving each admission request to its workspace, which is not built yet, so "+
			"this is one workspace or none. Leave unset to serve no webhooks. Reconciliation is "+
			"unaffected either way: every bound workspace is reconciled regardless.")

	fs.DurationVar(&engageTimeout, "engage-timeout", 5*time.Minute,
		"How long to wait for --webhook-workspace-cluster-name to become available via the provider "+
			"(i.e. for its APIBinding to become Ready) before giving up. Ignored when it is unset.")

	fs.DurationVar(&engagePollInterval, "engage-poll-interval", time.Second,
		"How often to poll for --webhook-workspace-cluster-name to become available.")

	fs.IntVar(&webhookPort, "webhook-port", 9443, "Webhook server port")
	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/", "Webhook cert dir.")
	fs.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "Webhook cert name.")
	fs.StringVar(&webhookKeyName, "webhook-key-name", "tls.key", "Webhook key name.")
	fs.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")

	fs.StringVar(&metricsAddr, "metrics-addr", ":8080",
		"The address the metrics endpoint binds to. \"0\" disables it. It is a flag because a "+
			"deployment scrapes it and a machine running several of these managers at once cannot give "+
			"them all the same port - controller-runtime's default is one every manager would take.")

	fs.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", coremanager.DefaultMaxConcurrentReconciles,
		"Worker goroutines per controller, per workspace. This is paid once for every engaged workspace, "+
			"not once for the process, and controller-runtime starts the workers eagerly — so the total is "+
			"this value times the number of controllers times the number of workspaces, whether or not those "+
			"workspaces hold any objects. Upstream's single-tenant default of 10 is deliberately not used here. "+
			"Raise it for a small fleet with busy workspaces; leave it alone for a large one.")

	// cluster.Reconciler and machine.Reconciler unconditionally watch every
	// core type gated by a feature flag they support (e.g. MachinePool,
	// enabled by default upstream) as an event source that can trigger a
	// reconcile - not just the types this walking skeleton's SetupReconcilers
	// actually reconciles. Any such type has to be bound in the workspace's
	// APIExport too, or that watch's cache sync stalls the whole controller
	// (see test/integration/dockerbackend's crdPaths comment). Exposing
	// these flags lets an operator disable a gate instead of also having to
	// publish and bind that type's CRD.
	// This project's defaults, set before the flag is defined so that
	// --feature-gates overrides them rather than the other way round. See
	// coremanager.SetFeatureGateDefaults for what differs from upstream.
	coremanager.MustSetFeatureGateDefaults()
	feature.MutableGates.AddFlag(fs)
}

func main() {
	initFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	if err := pflag.CommandLine.Set("v", "2"); err != nil {
		fmt.Printf("Failed to set default log level: %v\n", err)
		os.Exit(1)
	}
	pflag.Parse()

	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		fmt.Printf("Unable to start manager: %v\n", err)
		os.Exit(1)
	}
	ctrl.SetLogger(klog.Background())

	if endpointSliceName == "" {
		setupLog.Error(fmt.Errorf("--endpoint-slice-name is required"), "Unable to start manager")
		os.Exit(1)
	}

	coremanager.SetupProcessGlobals()

	ctx := ctrl.SetupSignalHandler()

	cfg := ctrl.GetConfigOrDie()

	// The registry is what joins the fleet-wide controllers' watches to the
	// caches the provider builds for each shard. Both sides are wired below and
	// neither exists when the other is created, which is the whole reason it is
	// a registry rather than a value passed one way.
	wildcardRegistry := &capicontrollerutil.WildcardRegistry{}

	provider, err := providerwiring.NewAPIExportProvider(cfg, endpointSliceName, scheme, wildcardRegistry,
		// The indexes this process's controllers list through, registered on
		// each shard's cache before its watches are. See
		// coremanager.FleetCacheIndexes.
		providerwiring.WithCacheIndexes(ctx, coremanager.FleetCacheIndexes()...))
	if err != nil {
		setupLog.Error(err, "Unable to construct kcp APIExport cluster provider")
		os.Exit(1)
	}

	// The local manager is addressed at the APIExport's virtual workspace, not
	// at the shard this process was configured with. Its RESTMapper answers
	// every question a fleet-wide controller asks that has no cluster to
	// resolve from, and setup asks several before any workspace has engaged —
	// so it has to describe the API surface the engaged clusters share, which
	// the exporting workspace does not. See providerwiring.VirtualWorkspaceConfig.
	shardClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Unable to build a client for the shard")
		os.Exit(1)
	}
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, shardClient, endpointSliceName, cfg, startupTimeout)
	if err != nil {
		setupLog.Error(err, "Unable to resolve the APIExport's virtual workspace")
		os.Exit(1)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthAddr,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:     webhookPort,
			CertDir:  webhookCertDir,
			CertName: webhookCertName,
			KeyName:  webhookKeyName,
		}),
	})
	if err != nil {
		setupLog.Error(err, "Unable to set up multicluster manager")
		os.Exit(1)
	}

	// Without these the health endpoint serves nothing. controller-runtime
	// creates its handler when the first check is registered and routes
	// nothing when there is none, so --health-addr accepts connections and
	// answers 404 - which a kubelet reads as a container that failed to
	// start, and a person reads as a manager that is broken.
	//
	// They say the process is up and its manager was constructed, and no more
	// than that: a fleet-wide controller with no workspaces engaged is
	// correct, so readiness cannot wait for one.
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the readiness check")
		os.Exit(1)
	}

	// Wired before Start, and that ordering is load-bearing: the controllers
	// register their watches with the multi-cluster manager, and
	// multicluster-runtime hands each engagement to the components registered
	// at that moment and never replays earlier ones. Wiring after the manager
	// is running misses every workspace that engaged in the meantime - without
	// an error, and without a log line.
	//
	// One set of controllers for the process. Each resolves the workspace from
	// the context of the reconcile it is running, so there is no per-workspace
	// setup left to run and nothing to re-run as workspaces come and go.
	setupLog.Info("Wiring fleet-wide reconcilers")
	fleet, err := coremanager.NewFleet(ctx, mgr, wildcardRegistry, coremanager.SetupOptions{
		FleetMaxConcurrentReconciles: maxConcurrentReconciles,

		// The shard, deliberately, and not the manager's config: kubeconfig
		// Secrets live in the workspaces on the shard, and the virtual
		// workspace above serves only what the core export publishes and
		// claims.
		//
		// Derived rather than passed straight through, because --kubeconfig
		// addresses the workspace the exports live in and everything reading a
		// tenant's Secrets scopes the config itself. See
		// providerwiring.ShardConfig for what passing the scoped one looks
		// like when it goes wrong.
		ShardConfig: providerwiring.ShardConfig(cfg),
	})
	if err != nil {
		setupLog.Error(err, "Unable to build the fleet")
		os.Exit(1)
	}
	if err := coremanager.SetupCoreControllers(ctx, mgr, fleet, nil); err != nil {
		setupLog.Error(err, "Unable to wire fleet-wide reconcilers")
		os.Exit(1)
	}

	// Per-workspace wiring with nothing to wire.
	//
	// There is no longer any per-workspace setup: the controllers above serve
	// every workspace. What remains worth having is the lifecycle itself -
	// which workspaces engaged, which failed, how many are live - because an
	// operator sizing a shard needs the count and a workspace that never
	// engages is otherwise invisible. So the seam stays, with an empty
	// SetupFunc, purely to drive the recorder.
	//
	// One recorder for the process. It attributes load without letting exported
	// series grow with workspace count; see internal/workspacetelemetry for why
	// that asymmetry is deliberate.
	telemetry := workspacetelemetry.New(workspacetelemetry.Options{})

	if _, err := providerwiring.AddToManager(mgr, func(_ context.Context, workspace multicluster.ClusterName, _ manager.Manager) error {
		setupLog.V(4).Info("Workspace engaged", "clusterName", workspace)
		return nil
	}, providerwiring.Options{
		Log:       ctrl.Log.WithName("providerwiring"),
		Telemetry: telemetry,
	}); err != nil {
		setupLog.Error(err, "Unable to register workspace engagement telemetry")
		os.Exit(1)
	}

	go func() {
		setupLog.Info("Starting manager")
		if err := mgr.Start(ctx); err != nil {
			setupLog.Error(err, "Problem running manager")
			os.Exit(1)
		}
	}()

	<-mgr.Elected()

	if webhookWorkspace != "" {
		setupLog.Info("Waiting for the webhook workspace to be engaged", "clusterName", webhookWorkspace)
		workspace := multicluster.ClusterName(webhookWorkspace)
		wsMgr, err := coremanager.WaitForManager(ctx, mgr, workspace, engagePollInterval, engageTimeout)
		if err != nil {
			setupLog.Error(err, "Webhook workspace never became available")
			os.Exit(1)
		}
		if err := coremanager.SetupCoreWebhooks(workspace, wsMgr); err != nil {
			setupLog.Error(err, "Unable to set up webhooks")
			os.Exit(1)
		}
		setupLog.Info("Serving webhooks for one workspace", "clusterName", webhookWorkspace)
	} else {
		setupLog.Info("Serving no webhooks: --webhook-workspace-cluster-name is unset")
	}

	<-ctx.Done()
}
