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

// Command dev-infrastructure-manager runs the unmodified upstream docker/dev
// infrastructure provider against every KCP workspace bound to its APIExport -
// the conversion plan's P3.
//
// It is a deployment of its own, as Cluster API deploys every provider, and it
// consumes its own export: the dev provider's types are published by
// cluster-api-dev-infrastructure rather than by core's export, so an
// installation that should never see them simply does not publish it.
//
// What it needs from other exports is declared as permission claims on its own
// (internal/capiexports): Clusters and Machines, which its reconcilers watch,
// and Secrets, which its ClusterCache reads workload-cluster kubeconfigs from.
//
// This provider is upstream's *test* infrastructure provider. It exists for
// development and e2e, and one of its components is process-global in a way
// that matters here: the in-memory workload-cluster backend keys listeners by
// namespace and name, so two workspaces holding identically named Clusters
// collide in it. See coremanager.DevInfrastructure.
package main

import (
	"flag"
	"fmt"
	"os"

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
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/managermetrics"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
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

	endpointSliceName       string
	webhookWorkspace        string
	webhookPort             int
	webhookCertDir          string
	healthAddr              string
	metricsAddr             string
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

	fs.StringVar(&endpointSliceName, "endpoint-slice-name", capiexports.InfraExport,
		"Name of the APIExportEndpointSlice (in the workspace targeted by --kubeconfig/in-cluster config) "+
			"whose virtual workspace URLs are used to discover and cache bound workspaces.")
	fs.StringVar(&webhookWorkspace, "webhook-workspace-cluster-name", "",
		"Internal logical cluster name of the one workspace whose DevCluster and DevMachine admission "+
			"webhooks this process serves. Serving more than one requires resolving each admission request "+
			"to its workspace, which is not built yet. Leave unset to serve no webhooks; reconciliation is "+
			"unaffected either way.")
	fs.IntVar(&webhookPort, "webhook-port", 9443, "Webhook server port.")
	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/", "Webhook cert dir.")
	fs.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
	fs.StringVar(&metricsAddr, "metrics-bind-address", managermetrics.DefaultBindAddress,
		"The address the metrics endpoint binds to. \"0\" disables it. The endpoint serves the Go runtime "+
			"and process collectors as well as controller-runtime's own, which is what makes a deployed "+
			"measurement reconcilable with an in-process one.")
	fs.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", coremanager.DefaultFleetMaxConcurrentReconciles,
		"Worker goroutines per controller. One pool for the process, shared by every workspace it serves.")

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

	wildcardRegistry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(cfg, endpointSliceName, scheme, wildcardRegistry)
	if err != nil {
		setupLog.Error(err, "Unable to construct kcp APIExport cluster provider")
		os.Exit(1)
	}

	shardClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Unable to build a client for the shard")
		os.Exit(1)
	}
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, shardClient, endpointSliceName, cfg, 0)
	if err != nil {
		setupLog.Error(err, "Unable to resolve the APIExport's virtual workspace")
		os.Exit(1)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthAddr,
		Metrics:                managermetrics.Options(metricsAddr),
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: webhookCertDir,
		}),
	})
	if err != nil {
		setupLog.Error(err, "Unable to set up multicluster manager")
		os.Exit(1)
	}

	// Register the probes the Deployment's readiness and liveness probes ask
	// for. Without this, /readyz and /healthz do not exist.
	//
	// controller-runtime registers those routes only when a check has been
	// added — addHealthProbeServer does nothing `if cm.readyzHandler == nil`,
	// and that handler is created by AddReadyzCheck and nowhere else. A manager
	// with a health port and no checks therefore serves the port and answers
	// 404 on it, which a kubelet reads as a failed probe: the pod runs, does
	// its work, logs nothing wrong, and never goes Ready. A Deployment of it
	// stays 0/1 available for ever.
	//
	// Ping is what it says: the process is up and the manager is serving. The
	// manager has already resolved its virtual workspace and built its caches
	// by the time this serves, so there is nothing further this could usefully
	// gate on that being up does not already imply.
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the readiness check")
		os.Exit(1)
	}

	// The backend is created once per process: NewWorkloadClustersMux binds a
	// port at construction, so a second one fails.
	//
	// POD_IP is upstream's own convention for this, and it has to resolve to
	// something a client can reach: it becomes each workload cluster's
	// advertised endpoint, and an empty one leaves every Cluster without a
	// control plane endpoint for the control plane provider to wait on.
	dev, err := coremanager.NewDevInfrastructure(ctx, os.Getenv("POD_IP"))
	if err != nil {
		setupLog.Error(err, "Unable to set up the dev infrastructure provider backend")
		os.Exit(1)
	}

	setupLog.Info("Wiring the fleet-wide dev infrastructure reconcilers")
	fleet, err := coremanager.NewFleet(ctx, mgr, wildcardRegistry, coremanager.SetupOptions{
		FleetMaxConcurrentReconciles: maxConcurrentReconciles,
		ShardConfig:                  cfg,
	})
	if err != nil {
		setupLog.Error(err, "Unable to build the fleet")
		os.Exit(1)
	}
	if err := coremanager.SetupDevInfrastructureControllers(ctx, mgr, fleet, dev); err != nil {
		setupLog.Error(err, "Unable to wire the fleet-wide dev infrastructure reconcilers")
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
		workspace := multicluster.ClusterName(webhookWorkspace)
		wsMgr, err := coremanager.WaitForManager(ctx, mgr, workspace, coremanager.DefaultEngagePollInterval, coremanager.DefaultEngageTimeout)
		if err != nil {
			setupLog.Error(err, "Webhook workspace never became available")
			os.Exit(1)
		}
		if err := coremanager.SetupDevInfrastructureWebhooks(workspace, wsMgr); err != nil {
			setupLog.Error(err, "Unable to set up webhooks")
			os.Exit(1)
		}
		setupLog.Info("Serving webhooks for one workspace", "clusterName", webhookWorkspace)
	} else {
		setupLog.Info("Serving no webhooks: --webhook-workspace-cluster-name is unset")
	}

	<-ctx.Done()
}
