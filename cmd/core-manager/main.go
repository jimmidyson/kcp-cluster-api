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

// Command core-manager runs unmodified upstream Cluster API core and
// docker/dev-infrastructure reconcilers against every KCP workspace bound to
// this project's APIExport, discovered and cached via
// github.com/kcp-dev/multicluster-provider + sigs.k8s.io/multicluster-runtime.
// See docs/conversion-plan.md and docs/adr-0001-per-workspace-manager-pool.md.
//
// Workspaces are not named in configuration: each is set up as it binds and
// torn down as it unbinds, by internal/providerwiring. Two things remain
// narrower than upstream's own core/main.go, both deliberately:
//
//   - Only the Cluster/Machine reconcilers and the docker/dev infrastructure
//     provider's DevCluster/DevMachine reconcilers are wired, per ADR-0001's
//     D3 scope decision, rather than the full reconciler set.
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	infrav1beta1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	endpointSliceName  string
	webhookWorkspace   string
	engageTimeout      time.Duration
	engagePollInterval time.Duration
	webhookPort        int
	webhookCertDir     string
	webhookCertName    string
	webhookKeyName     string
	healthAddr         string
	logOptions         = logs.NewOptions()
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = apisv1alpha1.AddToScheme(scheme)

	_ = clusterv1beta1.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)

	_ = infrav1beta1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)
}

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&endpointSliceName, "endpoint-slice-name", "",
		"Name of the APIExportEndpointSlice (in the workspace targeted by --kubeconfig/in-cluster config) "+
			"whose virtual workspace URLs are used to discover and cache bound workspaces.")

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

	// cluster.Reconciler and machine.Reconciler unconditionally watch every
	// core type gated by a feature flag they support (e.g. MachinePool,
	// enabled by default upstream) as an event source that can trigger a
	// reconcile - not just the types this walking skeleton's SetupReconcilers
	// actually reconciles. Any such type has to be bound in the workspace's
	// APIExport too, or that watch's cache sync stalls the whole controller
	// (see kcp/test/integration/coremanager's crdPaths comment). Exposing
	// these flags lets an operator disable a gate instead of also having to
	// publish and bind that type's CRD.
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

	cfg := ctrl.GetConfigOrDie()

	provider, err := apiexport.New(cfg, endpointSliceName, apiexport.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "Unable to construct kcp APIExport cluster provider")
		os.Exit(1)
	}

	mgr, err := mcmanager.New(cfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthAddr,
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

	ctx := ctrl.SetupSignalHandler()

	// The docker/dev infrastructure provider's backend binds a fixed port, so
	// it is created once and shared by every workspace. See
	// coremanager.DevInfrastructure for what that sharing does and does not
	// imply.
	dev, err := coremanager.NewDevInfrastructure(ctx)
	if err != nil {
		setupLog.Error(err, "Unable to set up the dev infrastructure provider backend")
		os.Exit(1)
	}

	// Registered before Start, and that ordering is load-bearing:
	// multicluster-runtime hands each engagement to the components registered
	// at that moment and never replays earlier ones, so wiring registered
	// after the manager is running misses every workspace that engaged in the
	// meantime - without an error, and without a log line.
	if _, err := providerwiring.AddToManager(mgr, func(ctx context.Context, workspace multicluster.ClusterName, wsMgr manager.Manager) error {
		setupLog.Info("Wiring reconcilers onto a workspace", "clusterName", workspace)
		return coremanager.SetupReconcilers(ctx, wsMgr, dev)
	}, providerwiring.Options{Log: ctrl.Log.WithName("providerwiring")}); err != nil {
		setupLog.Error(err, "Unable to register per-workspace wiring")
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
		if err := coremanager.SetupWebhooks(workspace, wsMgr); err != nil {
			setupLog.Error(err, "Unable to set up webhooks")
			os.Exit(1)
		}
		setupLog.Info("Serving webhooks for one workspace", "clusterName", webhookWorkspace)
	} else {
		setupLog.Info("Serving no webhooks: --webhook-workspace-cluster-name is unset")
	}

	<-ctx.Done()
}
