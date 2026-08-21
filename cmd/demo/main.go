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
	users           []string
	clusters        int
	machines        int
	workers         int
	nutanixExport   bool
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
		users           = flag.String("users", strings.Join(demo.DefaultUsers, ","), "Comma-separated tenants to share the workspaces out between, one home workspace each. Each is granted their own workspaces and nothing else, and the run reports what each can and cannot read of the others. Empty means no users: every workspace sits under --parent and only admin credentials touch it.")
		clusters        = flag.Int("clusters", demo.DefaultClusters, "How many clusters per workspace. They are named identically in every workspace, on purpose.")
		workers         = flag.Int("worker-machines", demo.DefaultWorkerMachines, "Worker machines per cluster, as a MachineDeployment. Needs --control-plane-machines: a worker has no control plane to join otherwise.")
		machines        = flag.Int("control-plane-machines", demo.DefaultControlPlaneMachines, "Control plane replicas per cluster, at least one. The ClusterClass every demo cluster is built from always names a control plane, so a run asking for none asks for a blueprint it cannot satisfy.")
		nutanixExport   = flag.Bool("nutanix-export", false, "Also publish the Nutanix infrastructure provider's APIExport, so its types can be bound in each workspace. Nothing here reconciles them - this makes the export visible, not the provider live.")
		backend         = flag.String("backend", string(demo.BackendInMemory), "DevCluster backend: inmemory (needs no container runtime) or docker (real containers, pulls kindest images).")
		parent          = flag.String("parent", demo.DefaultParent, "Workspace the APIExport is published in and the demo workspaces are created under.")
		workspacePrefix = flag.String("workspace-prefix", demo.DefaultWorkspacePrefix, "Prefix for the created workspace names.")
		timeout         = flag.Duration("timeout", demo.DefaultTimeout, "How long to wait for every cluster to be ready.")
		pollInterval    = flag.Duration("poll-interval", demo.DefaultPollInterval, "How often to refresh the status table.")
		noManager       = flag.Bool("no-manager", false, "Do not run the manager in this process: create the workspaces and objects only, against a core-manager started separately.")
		wait            = flag.Bool("wait", false, "Stay running after every cluster is ready, so the server and the manager are still there to look at. Ctrl-C stops it.")
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
		users:           splitUsers(*users),
		clusters:        *clusters,
		machines:        *machines,
		workers:         *workers,
		nutanixExport:   *nutanixExport,
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

// splitUsers turns the --users flag into the list demo.Options takes. An
// empty or whitespace-only value means no users rather than one user with no
// name, which is what strings.Split would produce.
func splitUsers(value string) []string {
	var users []string
	for _, user := range strings.Split(value, ",") {
		if user = strings.TrimSpace(user); user != "" {
			users = append(users, user)
		}
	}
	return users
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
	baseConfig, impersonationConfig, kubeconfigPath, stop, err := connect(ctx, opts)
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
		ImpersonationConfig:  impersonationConfig,
		Parent:               opts.parent,
		WorkspacePrefix:      opts.workspacePrefix,
		Workspaces:           opts.workspaces,
		Users:                opts.users,
		ClustersPerWorkspace: opts.clusters,
		ControlPlaneMachines: opts.machines,
		WorkerMachines:       opts.workers,
		Backend:              opts.backend,
		NutanixExport:        opts.nutanixExport,
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
	if len(result.Onboarding) > 0 {
		fmt.Println()
		fmt.Println("How each workspace came to serve Cluster API. Nothing in the last three")
		fmt.Println("columns was written out by hand:")
		if err := demo.RenderOnboardingTable(os.Stdout, result.Onboarding); err != nil {
			return err
		}
	}
	if len(result.Claims) > 0 {
		fmt.Println()
		fmt.Println("What each provider's controllers may reach. A \"discovered\" claim exists")
		fmt.Println("because a provider published a labelled APIExport, not because anybody")
		fmt.Println("named it - and every workspace accepted it without being asked:")
		if err := demo.RenderClaimsTable(os.Stdout, result.Claims); err != nil {
			return err
		}
	}
	if len(result.Access) > 0 {
		fmt.Println()
		if err := demo.RenderAccessTable(os.Stdout, result.Access); err != nil {
			return err
		}
	}
	fmt.Println()

	if runErr != nil {
		return runErr
	}
	// A run with users that reached ready but leaked between them has not
	// done what it was asked. Reporting it as a success would make the
	// tenancy table decoration rather than a result.
	if len(result.Users) > 0 && !result.Isolated() {
		return errors.New("the clusters are ready but the workspaces are not isolated: see the access table above")
	}

	printNextSteps(result, baseConfig, kubeconfigPath)

	if opts.wait {
		fmt.Println("Running until interrupted. Ctrl-C stops the manager and, if the demo started it, the kcp server.")
		<-ctx.Done()
	}
	return nil
}

// connect returns the demo's own credential, the privileged one it
// impersonates tenants from, where the kubeconfig is, and how to stop a server
// this function started.
//
// The two configs are different users of the same kubeconfig. See
// demo.Options.ImpersonationConfig for why impersonating from an ordinary
// admin is not enough. Stopping somebody else's server is not this command's
// business, so the returned stop function does nothing when a kubeconfig was
// given.
func connect(ctx context.Context, opts options) (cfg, impersonation *rest.Config, kubeconfigPath string, stop func(), err error) {
	noop := func() {}
	if opts.kubeconfig != "" {
		cfg, err := demo.ConfigFromKubeconfig(opts.kubeconfig, opts.kubeconfigCtx)
		if err != nil {
			return nil, nil, "", noop, err
		}
		// Best effort: a kubeconfig somebody supplied need not carry the
		// shard admin, and a run with no tenants never impersonates anybody.
		// A run that does will fail where the permission is missing, which
		// says more than refusing to start would.
		impersonation, err := demo.ConfigFromKubeconfig(opts.kubeconfig, demo.ShardBaseContext)
		if err != nil {
			impersonation = cfg
		}
		return cfg, impersonation, opts.kubeconfig, noop, nil
	}

	server, err := demo.StartKcp(ctx, opts.kcpDirectory, 0, opts.log, opts.kcpArgs...)
	if err != nil {
		return nil, nil, "", noop, err
	}
	return server.BaseConfig, server.ImpersonationConfig, server.KubeconfigPath, server.Stop, nil
}

func printNextSteps(result demo.Result, baseConfig *rest.Config, kubeconfigPath string) {
	fmt.Println("One shard, one manager per provider, every workspace above served by all of them.")
	fmt.Println()
	fmt.Println("Look around, one workspace at a time:")
	host := strings.TrimSuffix(baseConfig.Host, "/")
	for _, ws := range result.Workspaces {
		fmt.Printf("  kubectl --kubeconfig %s --context %s --server %s/clusters/%s get clusters,machines -A\n",
			kubeconfigPath, demo.BaseContext, host, ws.Path)
	}

	printUserSteps(result, host, kubeconfigPath)

	if len(result.ControlPlanes) == 0 {
		return
	}

	// The workload clusters themselves. Their API servers are the dev
	// provider's in-memory ones, served by this process - so they answer only
	// while the demo is running, which is what --wait is for.
	fmt.Println()
	fmt.Println("Talk to a workload cluster (while this is running):")
	for _, ws := range result.Workspaces {
		cluster := demo.ClusterName(0)
		// The file is named after the workspace, not the cluster. Every
		// workspace's cluster has the same name, so a file named after the
		// cluster would have each of these commands overwrite the last one's
		// kubeconfig and every `get nodes` talk to the same workload cluster.
		file := "/tmp/" + strings.ReplaceAll(ws.Path, ":", "_") + ".kubeconfig"
		fmt.Printf("  kubectl --kubeconfig %s --context %s --server %s/clusters/%s -n %s get secret %s -o jsonpath='{.data.value}' | base64 -d > %s\n",
			kubeconfigPath, demo.BaseContext, host, ws.Path, demo.Namespace, demo.KubeconfigSecretName(cluster), file)
		fmt.Printf("  kubectl --kubeconfig %s get nodes   # %s\n", file, ws.Path)
	}
}

// printUserSteps prints the commands that reproduce the access table by hand.
//
// `--as` rather than a second credential: the kubeconfig is the kcp admin's,
// and kcp evaluates the whole request as the impersonated user, so what comes
// back is that user's authorization and not a rehearsal of it. Which is also
// why the refused commands are worth pasting - the "Forbidden" is the point.
func printUserSteps(result demo.Result, host, kubeconfigPath string) {
	if len(result.Users) == 0 {
		return
	}

	kubectl := func(user, path, args string) {
		fmt.Printf("  kubectl --kubeconfig %s --context %s --as %s --server %s/clusters/%s %s\n",
			kubeconfigPath, demo.BaseContext, user, host, path, args)
	}

	fmt.Println()
	fmt.Println("Each user sees their own workspaces, and only by knowing their own home:")
	for _, user := range result.Users {
		kubectl(user.Name, user.Home, "get workspaces")
	}
	fmt.Printf("A workspace list is neither recursive nor filtered by what the caller can enter,\n")
	fmt.Printf("so listing %s shows its direct children to any authenticated user and nothing below them:\n", result.Parent)
	kubectl(result.Users[0].Name, result.Parent, "get workspaces")

	fmt.Println()
	fmt.Println("And each is refused everybody else's, and the org workspace holding them all:")
	for _, user := range result.Users {
		for _, other := range result.Users {
			if other.Name != user.Name {
				kubectl(user.Name, other.Home, "get workspaces   # Forbidden")
			}
		}
		kubectl(user.Name, result.Org, "get workspaces   # Forbidden")
	}

	fmt.Println()
	fmt.Println("Same again for what is inside them:")
	for _, user := range result.Users {
		// One of the user's own and one of somebody else's. Every pair would
		// print the same two facts once per workspace, and a ten-workspace run
		// would bury the tables above in commands that all say this.
		if own, ok := firstOwnedBy(result.Workspaces, user.Name); ok {
			kubectl(user.Name, own.Path, "get clusters -A")
		}
		if other, ok := firstNotOwnedBy(result.Workspaces, user.Name); ok {
			kubectl(user.Name, other.Path, "get clusters -A   # Forbidden")
		}
	}
}

func firstOwnedBy(workspaces []demo.Workspace, user string) (demo.Workspace, bool) {
	for _, ws := range workspaces {
		if ws.Owner == user {
			return ws, true
		}
	}
	return demo.Workspace{}, false
}

func firstNotOwnedBy(workspaces []demo.Workspace, user string) (demo.Workspace, bool) {
	for _, ws := range workspaces {
		if ws.Owner != "" && ws.Owner != user {
			return ws, true
		}
	}
	return demo.Workspace{}, false
}
