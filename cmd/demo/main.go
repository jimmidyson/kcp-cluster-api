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

// Command demo provisions Cluster API clusters across several kcp workspaces
// from one manager, and reports what each one is doing.
//
// It is the answer to "show me": until this existed, the wiring this
// repository builds was only observable by reading an integration test. With
// no arguments it starts its own single-shard kcp server, publishes the
// Cluster API types, creates two workspaces bound to them, runs the same
// fleet-wide controllers cmd/core-manager runs, and provisions a cluster in
// each.
//
// See docs/site/content/en/docs/user/demo.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

type options struct {
	kubeconfig      string
	kubeconfigCtx   string
	kcpDirectory    string
	kcpArgs         []string
	workspaces      int
	clusters        int
	machines        int
	backend         demo.Backend
	parent          string
	workspacePrefix string
	timeout         time.Duration
	pollInterval    time.Duration
	runManager      bool
	wait            bool
	log             logr.Logger
}

func main() {
	var (
		// Named --kcp-kubeconfig because controller-runtime's config package
		// registers --kubeconfig on flag.CommandLine from an init function,
		// and redefining a flag panics at startup. A --kubeconfig passed
		// anyway is picked up below rather than silently ignored.
		kubeconfig      = flag.String("kcp-kubeconfig", "", "Kubeconfig of an existing kcp server. Empty starts one, with its state under --kcp-directory. --kubeconfig is accepted too.")
		kubeconfigCtx   = flag.String("kcp-kubeconfig-context", demo.BaseContext, "Kubeconfig context to use. It must be cluster-unaware: the demo scopes it to each workspace itself.")
		kcpDirectory    = flag.String("kcp-directory", ".demo/kcp", "Where a demo-started kcp server keeps its state and its log.")
		kcpArgs         = flag.String("kcp-args", "", "Extra space-separated flags for a demo-started kcp server, e.g. \"--v=5\".")
		workspaces      = flag.Int("workspaces", demo.DefaultWorkspaces, "How many workspaces to create, bind and provision a cluster in.")
		clusters        = flag.Int("clusters", demo.DefaultClusters, "How many clusters per workspace. They are named identically in every workspace, on purpose.")
		machines        = flag.Int("control-plane-machines", 0, "Control plane replicas per cluster. Asking for any creates a KubeadmControlPlane and wires the kubeadm bootstrap and control plane providers, which create the Machines themselves.")
		backend         = flag.String("backend", string(demo.BackendInMemory), "DevCluster backend: inmemory (needs no container runtime) or docker (real containers, pulls kindest images).")
		parent          = flag.String("parent", demo.DefaultParent, "Workspace the APIExport is published in and the demo workspaces are created under.")
		workspacePrefix = flag.String("workspace-prefix", demo.DefaultWorkspacePrefix, "Prefix for the created workspace names.")
		timeout         = flag.Duration("timeout", demo.DefaultTimeout, "How long to wait for every cluster to be provisioned.")
		pollInterval    = flag.Duration("poll-interval", demo.DefaultPollInterval, "How often to refresh the status table.")
		noManager       = flag.Bool("no-manager", false, "Do not run the manager in this process: create the workspaces and objects only, against a core-manager started separately.")
		wait            = flag.Bool("wait", false, "Stay running after every cluster is provisioned, so the server and the manager are still there to look at. Ctrl-C stops it.")
	)
	flag.Parse()

	log := funcr.New(func(prefix, args string) {
		if prefix != "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", prefix, args)
			return
		}
		fmt.Fprintln(os.Stderr, args)
	}, funcr.Options{})
	ctrl.SetLogger(log)

	err := run(ctrl.SetupSignalHandler(), options{
		kubeconfig:      firstSet(*kubeconfig, lookupString("kubeconfig")),
		kubeconfigCtx:   *kubeconfigCtx,
		kcpDirectory:    *kcpDirectory,
		kcpArgs:         strings.Fields(*kcpArgs),
		workspaces:      *workspaces,
		clusters:        *clusters,
		machines:        *machines,
		backend:         demo.Backend(*backend),
		parent:          *parent,
		workspacePrefix: *workspacePrefix,
		timeout:         *timeout,
		pollInterval:    *pollInterval,
		runManager:      !*noManager,
		wait:            *wait,
		log:             log,
	})
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		// Ctrl-C. The demo did what it was asked to; stopping it is not a
		// failure to report as one.
	default:
		fmt.Fprintf(os.Stderr, "\ndemo failed: %v\n", err)
		os.Exit(1)
	}
}

// lookupString reads a flag somebody else registered, or "" if there is none.
func lookupString(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func run(ctx context.Context, opts options) error {
	baseConfig, kubeconfigPath, stop, err := connect(ctx, opts)
	if err != nil {
		return err
	}
	// Deferred, not left to the context: a demo that returns early - a failed
	// run, a bad flag - would otherwise leave a kcp server holding its ports
	// and its data directory, and the next run would fail for a reason that
	// has nothing to do with it.
	defer stop()

	result, runErr := demo.Run(ctx, demo.Options{
		BaseConfig:           baseConfig,
		Parent:               opts.parent,
		WorkspacePrefix:      opts.workspacePrefix,
		Workspaces:           opts.workspaces,
		ClustersPerWorkspace: opts.clusters,
		ControlPlaneMachines: opts.machines,
		Backend:              opts.backend,
		RunManager:           opts.runManager,
		Timeout:              opts.timeout,
		PollInterval:         opts.pollInterval,
		Out:                  os.Stdout,
		Log:                  opts.log,
	})

	fmt.Println()
	if err := demo.RenderTable(os.Stdout, result.Statuses); err != nil {
		return err
	}
	if len(result.ControlPlanes) > 0 {
		fmt.Println()
		if err := demo.RenderControlPlaneTable(os.Stdout, result.ControlPlanes); err != nil {
			return err
		}
	}
	if len(result.Machines) > 0 {
		fmt.Println()
		if err := demo.RenderMachineTable(os.Stdout, result.Machines); err != nil {
			return err
		}
	}
	fmt.Println()

	if runErr != nil {
		return runErr
	}

	printNextSteps(result, baseConfig, kubeconfigPath)

	if opts.wait {
		fmt.Println("Running until interrupted. Ctrl-C stops the manager and, if the demo started it, the kcp server.")
		<-ctx.Done()
	}
	return nil
}

// connect returns the shard config the demo runs against, starting a kcp
// server first when no kubeconfig was given, along with the function that
// stops it. Stopping somebody else's server is not this command's business,
// so that function does nothing when a kubeconfig was given.
func connect(ctx context.Context, opts options) (cfg *rest.Config, kubeconfigPath string, stop func(), err error) {
	if opts.kubeconfig != "" {
		cfg, err := demo.ConfigFromKubeconfig(opts.kubeconfig, opts.kubeconfigCtx)
		if err != nil {
			return nil, "", func() {}, err
		}
		return cfg, opts.kubeconfig, func() {}, nil
	}

	server, err := demo.StartKcp(ctx, opts.kcpDirectory, 0, opts.log, opts.kcpArgs...)
	if err != nil {
		return nil, "", func() {}, err
	}
	return server.BaseConfig, server.KubeconfigPath, server.Stop, nil
}

func printNextSteps(result demo.Result, baseConfig *rest.Config, kubeconfigPath string) {
	fmt.Println("One shard, one manager per provider, every workspace above served by all of them.")
	fmt.Println()
	fmt.Println("Look around, one workspace at a time:")
	host := strings.TrimSuffix(baseConfig.Host, "/")
	for _, ws := range result.Workspaces {
		fmt.Printf("  kubectl --kubeconfig %s --context %s --server %s/clusters/%s get clusters,devclusters -A\n",
			kubeconfigPath, demo.BaseContext, host, ws.Path)
	}
}
