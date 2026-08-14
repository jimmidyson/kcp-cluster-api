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

// Command core-manager is the Phase 1 "walking skeleton" for kcp-cluster-api
// (see kcp/docs/conversion-plan.md and kcp/docs/adr-0001-per-workspace-manager-pool.md):
// it wires unmodified upstream Cluster API core and docker/dev-infrastructure
// reconcilers and admission/conversion webhooks onto a *single, hardcoded*
// KCP workspace, discovered and cached via
// github.com/kcp-dev/multicluster-provider + sigs.k8s.io/multicluster-runtime.
//
// This is intentionally narrow: it hardcodes one target workspace rather than
// dynamically engaging every workspace bound to the APIExport (that
// generalization is Phase 2's G2 per-workspace glue), and it wires only the
// Cluster/Machine reconcilers plus the docker/dev infrastructure provider's
// DevCluster/DevMachine reconcilers, per ADR-0001's D3 scope decision - not
// core/main.go's full reconciler set.
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
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/multicluster-provider/apiexport"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/kcp/internal/coremanager"
	infrav1beta1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	endpointSliceName  string
	workspaceCluster   string
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

	fs.StringVar(&workspaceCluster, "workspace-cluster-name", "",
		"Internal logical cluster name (not the human-readable workspace path) of the single, "+
			"hardcoded workspace this Phase 1 walking skeleton engages. See ADR-0001.")

	fs.DurationVar(&engageTimeout, "engage-timeout", 5*time.Minute,
		"How long to wait for --workspace-cluster-name to become available via the provider "+
			"(i.e. for its APIBinding to become Ready) before giving up.")

	fs.DurationVar(&engagePollInterval, "engage-poll-interval", time.Second,
		"How often to poll for --workspace-cluster-name to become available.")

	fs.IntVar(&webhookPort, "webhook-port", 9443, "Webhook server port")
	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/", "Webhook cert dir.")
	fs.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "Webhook cert name.")
	fs.StringVar(&webhookKeyName, "webhook-key-name", "tls.key", "Webhook key name.")
	fs.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
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
	if workspaceCluster == "" {
		setupLog.Error(fmt.Errorf("--workspace-cluster-name is required"), "Unable to start manager")
		os.Exit(1)
	}

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

	go func() {
		setupLog.Info("Starting manager")
		if err := mgr.Start(ctx); err != nil {
			setupLog.Error(err, "Problem running manager")
			os.Exit(1)
		}
	}()

	<-mgr.Elected()

	setupLog.Info("Waiting for the target workspace to be engaged", "clusterName", workspaceCluster)
	wsMgr, err := coremanager.WaitForManager(ctx, mgr, multicluster.ClusterName(workspaceCluster), engagePollInterval, engageTimeout)
	if err != nil {
		setupLog.Error(err, "Target workspace never became available")
		os.Exit(1)
	}

	setupLog.Info("Wiring reconcilers and webhooks onto the engaged workspace", "clusterName", workspaceCluster)
	if err := coremanager.SetupReconcilers(ctx, wsMgr); err != nil {
		setupLog.Error(err, "Unable to set up reconcilers")
		os.Exit(1)
	}
	if err := coremanager.SetupWebhooks(wsMgr); err != nil {
		setupLog.Error(err, "Unable to set up webhooks")
		os.Exit(1)
	}

	<-ctx.Done()
}
