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

// Command kubeadm-bootstrap-manager runs the unmodified upstream kubeadm
// bootstrap provider against every KCP workspace bound to this project's
// APIExport - the conversion plan's P1.
//
// It is the same shape as core-manager, and deliberately a separate binary:
// Cluster API deploys one process per provider, and a deployment that wanted
// them together would run one of them with the other's controllers wired onto
// the same fleet (see internal/coremanager.Fleet), not a third binary.
//
// Two prerequisites beyond core-manager's, both properties of the APIExport
// rather than of this process:
//
//   - The export must publish the bootstrap types (KubeadmConfig,
//     KubeadmConfigTemplate).
//   - It must claim `secrets` and `configmaps`, and each workspace's APIBinding
//     must accept those claims. This provider's output is Secrets and its
//     control plane init lock is a ConfigMap, and without the claims every one
//     of those writes is refused - see internal/bootstrapmanager.
//
// Webhooks are not served at all here. Serving them for one workspace is what
// core-manager does, and doing the same in a second process would mean two
// webhook servers for one fleet; the conversion plan's G4 is what makes them
// multi-workspace.
package main

import (
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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/bootstrapmanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/managermetrics"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	endpointSliceName       string
	healthAddr              string
	metricsAddr             string
	tokenTTL                time.Duration
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
	_ = bootstrapv1.AddToScheme(scheme)
}

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&endpointSliceName, "endpoint-slice-name", "",
		"Name of the APIExportEndpointSlice (in the workspace targeted by --kubeconfig/in-cluster config) "+
			"whose virtual workspace URLs are used to discover and cache bound workspaces.")
	fs.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
	fs.StringVar(&metricsAddr, "metrics-bind-address", managermetrics.DefaultBindAddress,
		"The address the metrics endpoint binds to. \"0\" disables it. The endpoint serves the Go runtime "+
			"and process collectors as well as controller-runtime's own, which is what makes a deployed "+
			"measurement reconcilable with an in-process one.")
	fs.DurationVar(&tokenTTL, "bootstrap-token-ttl", bootstrapmanager.DefaultTokenTTL,
		"The amount of time a bootstrap token, and so a KubeadmConfig, stays valid.")
	fs.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", coremanager.DefaultFleetMaxConcurrentReconciles,
		"Worker goroutines for the KubeadmConfig controller. One pool for the process, shared by every "+
			"workspace it serves, rather than one per workspace.")

	// The core reconcilers this provider shares watches with are gated the
	// same way core-manager's are; see its equivalent comment.
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

	// Wired before Start: multicluster-runtime hands each engagement to the
	// components registered at that moment and never replays earlier ones.
	setupLog.Info("Wiring the fleet-wide bootstrap reconcilers")
	fleet, err := coremanager.NewFleet(ctx, mgr, wildcardRegistry, coremanager.SetupOptions{
		FleetMaxConcurrentReconciles: maxConcurrentReconciles,
		// The shard, not the manager's config: the ClusterCache reads
		// kubeconfig Secrets from the workspaces themselves.
		ShardConfig: cfg,
	})
	if err != nil {
		setupLog.Error(err, "Unable to build the fleet")
		os.Exit(1)
	}
	if err := bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{
		TokenTTL: tokenTTL,
	}); err != nil {
		setupLog.Error(err, "Unable to wire the fleet-wide bootstrap reconcilers")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
