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

// Command kubeadm-control-plane-manager runs the unmodified upstream kubeadm
// control plane provider against every KCP workspace bound to its APIExport -
// the conversion plan's P2.
//
// It is the provider that turns bootstrap data into a cluster: it creates the
// Machines a control plane is made of, the KubeadmConfigs that bootstrap them,
// and the certificates they share, then connects to the resulting API server
// and etcd members to report on them.
//
// That makes its claims the widest of any provider's - Machines and
// KubeadmConfigs from two other exports, for writing, plus Secrets and
// ConfigMaps - which internal/capiexports declares and maintains. Running this
// against exports that do not carry those claims produces a control plane that
// never scales up, with the refusal visible only in this process's logs.
//
// Webhooks are not served here; see cmd/core-manager for the one-workspace
// constraint they are still under.
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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	endpointSliceName           string
	healthAddr                  string
	etcdDialTimeout             time.Duration
	etcdCallTimeout             time.Duration
	remoteConditionsGracePeriod time.Duration
	maxConcurrentReconciles     int

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
	_ = controlplanev1.AddToScheme(scheme)
}

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&endpointSliceName, "endpoint-slice-name", capiexports.ControlPlaneExport,
		"Name of the APIExportEndpointSlice (in the workspace targeted by --kubeconfig/in-cluster config) "+
			"whose virtual workspace URLs are used to discover and cache bound workspaces.")
	fs.StringVar(&healthAddr, "health-addr", ":9440", "The address the health endpoint binds to.")
	fs.DurationVar(&etcdDialTimeout, "etcd-dial-timeout", controlplanemanager.DefaultEtcdDialTimeout,
		"Duration that the etcd client waits at most to establish a connection with a workload cluster's etcd.")
	fs.DurationVar(&etcdCallTimeout, "etcd-call-timeout", controlplanemanager.DefaultEtcdCallTimeout,
		"Duration that a single etcd call may take before it is cancelled.")
	fs.DurationVar(&remoteConditionsGracePeriod, "remote-conditions-grace-period", controlplanemanager.DefaultRemoteConditionsGracePeriod,
		"How long to wait before reporting a control plane this process cannot reach as unhealthy. Must be at least 2m, "+
			"so that the ClusterCache drops the connection first; the reconciler refuses to start otherwise.")
	fs.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", coremanager.DefaultFleetMaxConcurrentReconciles,
		"Worker goroutines for the KubeadmControlPlane controller. One pool for the process, shared by every "+
			"workspace it serves.")

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
	})
	if err != nil {
		setupLog.Error(err, "Unable to set up multicluster manager")
		os.Exit(1)
	}

	setupLog.Info("Wiring the fleet-wide control plane reconcilers")
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
	if err := controlplanemanager.SetupFleetControllers(ctx, mgr, fleet, controlplanemanager.Options{
		EtcdDialTimeout:             etcdDialTimeout,
		EtcdCallTimeout:             etcdCallTimeout,
		RemoteConditionsGracePeriod: remoteConditionsGracePeriod,
	}); err != nil {
		setupLog.Error(err, "Unable to wire the fleet-wide control plane reconcilers")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}
