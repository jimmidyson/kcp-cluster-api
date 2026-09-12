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

// Command workloadsim fakes the workload cluster behind any Cluster API
// infrastructure provider. Point it at a management cluster and it gives
// every Cluster a fake API server at the Cluster's declared control plane
// endpoint, and every Machine a Node carrying its providerID, so Machines
// reach Running with no real nodes. See the package documentation.
package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/workloadsim"
)

func main() {
	var (
		host                    string
		portMin, portMax        int
		debugPort               int
		generateClusterSecrets  bool
		maxConcurrentReconciles int
		healthAddr              string
		metricsAddr             string
	)
	flag.StringVar(&host, "host", "127.0.0.1",
		"Address the fake API servers are reachable at. Every Cluster's control plane endpoint must name it.")
	flag.IntVar(&portMin, "port-min", 20000, "Lowest port a fake API server may listen on.")
	flag.IntVar(&portMax, "port-max", 30000, "Highest port a fake API server may listen on.")
	flag.IntVar(&debugPort, "debug-port", 19000, "Port of the debug endpoint listing the fake clusters and their objects.")
	flag.BoolVar(&generateClusterSecrets, "generate-cluster-secrets", true,
		"Generate each Cluster's CA and kubeconfig secrets when absent, standing in for a control plane provider.")
	flag.IntVar(&maxConcurrentReconciles, "max-concurrent-reconciles", 10, "Worker goroutines per controller.")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":8081", "Address the health endpoint binds to.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Address the metrics endpoint binds to; 0 disables it.")
	// --kubeconfig comes from controller-runtime's config package, and
	// KUBECONFIG is honoured without it.
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fail(log, err, "registering the core types")
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		fail(log, err, "registering the Cluster API types")
	}

	ctx := ctrl.SetupSignalHandler()

	backend, err := workloadsim.NewBackend(ctx, host, workloadsim.Ports{
		Min:   int32(portMin),   //nolint:gosec // a port.
		Max:   int32(portMax),   //nolint:gosec // a port.
		Debug: int32(debugPort), //nolint:gosec // a port.
	})
	if err != nil {
		fail(log, err, "starting the in-memory workload cluster backend")
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: healthAddr,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
	})
	if err != nil {
		fail(log, err, "creating the manager")
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		fail(log, err, "adding the health check")
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		fail(log, err, "adding the readiness check")
	}

	if err := workloadsim.Setup(mgr, backend, workloadsim.Options{
		GenerateClusterSecrets:  generateClusterSecrets,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	}); err != nil {
		fail(log, err, "wiring the reconcilers")
	}

	log.Info("Starting", "host", host, "ports", fmt.Sprintf("%d-%d", portMin, portMax), "debugPort", debugPort,
		"generateClusterSecrets", generateClusterSecrets)
	if err := mgr.Start(ctx); err != nil {
		fail(log, err, "running the manager")
	}
	if err := backend.Shutdown(ctx); err != nil {
		log.Error(err, "Shutting down the backend")
	}
}

func fail(log interface{ Error(error, string, ...any) }, err error, msg string) {
	log.Error(err, msg)
	os.Exit(1)
}
