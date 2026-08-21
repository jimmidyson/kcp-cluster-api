/*
Copyright 2026 The kcp-cluster-api Authors.

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

// Command nutanix-infrastructure-manager runs the Nutanix infrastructure
// provider's reconcilers across every workspace bound to its APIExport.
//
// It is the dev infrastructure manager's shape with a different provider
// wired into it, and it lives in its own module rather than in cmd/ because
// of what that provider drags in: the Nutanix SDK is ten modules and pulls a
// further twelve of the AWS SDK behind it, none of which belongs in the graph
// of a repository whose other managers never touch Nutanix. See ADR-0004.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"

	capxv1 "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
	capxcontrollers "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/controllers"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
)

var (
	scheme     = runtime.NewScheme()
	setupLog   = ctrl.Log.WithName("setup")
	logOptions = logs.NewOptions()

	endpointSliceName       string
	healthAddr              string
	maxConcurrentReconciles int
)

func init() {
	utilRuntimeMust(clientgoscheme.AddToScheme(scheme))
	utilRuntimeMust(clusterv1.AddToScheme(scheme))
	utilRuntimeMust(capxv1.AddToScheme(scheme))
}

func utilRuntimeMust(err error) {
	if err != nil {
		panic(err)
	}
}

func initFlags(fs *pflag.FlagSet) {
	logsv1.AddFlags(logOptions, fs)
	fs.StringVar(&endpointSliceName, "endpoint-slice-name", capiexports.NutanixInfraExport,
		"Name of the APIExportEndpointSlice this manager serves.")
	fs.StringVar(&healthAddr, "health-probe-bind-address", ":9440",
		"Address the health endpoint binds to.")
	fs.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", coremanager.DefaultFleetMaxConcurrentReconciles,
		"Concurrent reconciles per fleet-wide controller.")

	coremanager.MustSetFeatureGateDefaults()
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

	fleet, err := coremanager.NewFleet(ctx, mgr, wildcardRegistry, coremanager.SetupOptions{
		FleetMaxConcurrentReconciles: maxConcurrentReconciles,
		ShardConfig:                  cfg,
	})
	if err != nil {
		setupLog.Error(err, "Unable to build the fleet")
		os.Exit(1)
	}

	setupLog.Info("Wiring the fleet-wide Nutanix infrastructure reconcilers")
	if err := setupControllers(ctx, mgr, fleet); err != nil {
		setupLog.Error(err, "Unable to wire the fleet-wide Nutanix reconcilers")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

// setupControllers wires the two Nutanix reconcilers onto the fleet.
//
// Every client the reconcilers are given resolves the workspace from the
// context of the call, and all three matter for a different reason:
//
//   - Client is what the reconcile path reads and writes through.
//   - APIReader is what metro placement enumerates sibling NutanixMachines
//     with, so a local reader would compute one tenant's placement from
//     another tenant's machines.
//   - CredentialReader is what Prism Central credentials are read through, so
//     a local reader would provision one tenant's cluster with another
//     tenant's credentials.
//
// The reconcilers refuse to be set up fleet-wide without the last two rather
// than falling back, because each fallback is wrong in a way that succeeds.
func setupControllers(ctx context.Context, mgr mcmanager.Manager, fleet *coremanager.Fleet) error {
	clusterReconciler, err := capxcontrollers.NewNutanixClusterReconciler(
		fleet.Client, nil, nil, mgr.GetLocalManager().GetScheme(),
	)
	if err != nil {
		return fmt.Errorf("building the NutanixCluster reconciler: %w", err)
	}
	clusterReconciler.CredentialReader = fleet.APIReader
	if err := clusterReconciler.SetupWithMulticlusterManager(ctx, mgr, fleet.Options, fleet.BuilderOptions()...); err != nil {
		return fmt.Errorf("wiring the fleet-wide NutanixCluster controller: %w", err)
	}

	machineReconciler, err := capxcontrollers.NewNutanixMachineReconciler(
		fleet.Client, nil, nil, mgr.GetLocalManager().GetScheme(),
	)
	if err != nil {
		return fmt.Errorf("building the NutanixMachine reconciler: %w", err)
	}
	machineReconciler.APIReader = fleet.APIReader
	machineReconciler.CredentialReader = fleet.APIReader
	if err := machineReconciler.SetupWithMulticlusterManager(ctx, mgr, fleet.Options, fleet.BuilderOptions()...); err != nil {
		return fmt.Errorf("wiring the fleet-wide NutanixMachine controller: %w", err)
	}

	return nil
}
