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

// Command capiscale-prepare makes a clusterctl-installed Cluster API ready to
// be measured: Guaranteed resources and a memory ceiling on every controller, a
// pprof endpoint on every controller, and — for the DevCluster provider alone —
// the Docker socket taken away.
//
// # Why a command rather than a few lines of kubectl patch
//
// Three of the four changes apply to every controller and one applies to one of
// them, and each has a reason that is easy to get subtly wrong: GOMEMLIMIT has
// to sit below the limit rather than at it, the profiler has to bind an address
// the pod proxy can reach rather than localhost, and the socket has to go
// without taking the webhook certificate with it. The logic is in
// internal/upstreamscale where it is unit tested; this is the thing that runs
// it, so what runs and what is tested are the same code.
//
// Idempotent: a second run against a prepared cluster reports no change and
// restarts nothing, which matters because restarting a controller resets the
// process metrics the whole measurement is made of.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
	"time"
)

func main() {
	// This command's own flag set, not flag.CommandLine.
	//
	// Some of what this imports registers flags in an init function —
	// controller-runtime's config package registers --kubeconfig — and a
	// process that shares the global set with its dependencies panics at
	// startup the day one of them claims a name it wanted. Which is what
	// happened here, on a flag this command had been parsing perfectly well
	// until an import two commits later reached it.
	fs := flag.NewFlagSet("capiscale-prepare", flag.ExitOnError)
	var (
		kubeconfig = fs.String("kubeconfig", "", "Path to the kubeconfig of the cluster to prepare. "+
			"Defaults to the usual rules (KUBECONFIG, then ~/.kube/config).")
		kubecontext = fs.String("context", "", "Context to use. Named rather than taken from whatever "+
			"is current, because this patches deployments and the current context may be somewhere else.")
		// The manager's own client limits, and the window it has to keep its
		// lease. At 1,000 clusters the core manager queued its own requests
		// behind its own limiter for over a second each until a five-second
		// lease renewal could not get through, and then exited because it had
		// stopped leading. Nothing had run out; a flag had been reached. See
		// upstreamscale.ClientLimits.
		clientQPS = fs.Float64("kube-api-qps", 100,
			"Client-side request rate for each manager. Cluster API's own default, deliberately: a "+
				"ceiling found at a client rate limit is a fact about the flag rather than about the "+
				"machine, but 500 here put five times the write rate onto a store that could not "+
				"absorb it — etcd timed out lease renewals, managers exited, and no rung finished. "+
				"Raise it once a run reaches a ceiling with the managers visibly throttling and "+
				"nothing else giving way, and record that the run was taken with it raised.")
		clientBurst   = fs.Int("kube-api-burst", 200, "Client-side burst for each manager.")
		leaseDuration = fs.Duration("leader-elect-lease-duration", time.Minute,
			"How long a manager's leadership lease is held.")
		renewDeadline = fs.Duration("leader-elect-renew-deadline", 40*time.Second,
			"How long a manager has to renew that lease before it stops leading and exits. The default "+
				"10s is shorter than the pauses a loaded API server takes, and a lost lease is an "+
				"orderly exit that says nothing about running out of anything.")
		retryPeriod = fs.Duration("leader-elect-retry-period", 5*time.Second,
			"How often it retries the renewal.")

		probeTimeout = fs.Int("probe-timeout-seconds", 5,
			"How long a manager's health checks may take to answer. The stock 1s is not a ping: "+
				"Cluster API's healthz opens a TLS connection to the manager's own webhook server, "+
				"so a manager is killed when its own webhooks are busy.")
		probeFailures = fs.Int("probe-failure-threshold", 5,
			"How many health checks in a row may fail before the kubelet kills the manager.")

		profilerAddr = fs.String("profiler-address", ":6060",
			"Address each controller serves pprof on. Bind all interfaces, not localhost: the samples are "+
				"read through the API server's pod proxy, which reaches the pod IP.")
		dryRun = fs.Bool("dry-run", false, "Report what would change and change nothing.")
		only   = fs.String("only", "", "Run one step and stop: \"preflight\" checks that the cluster "+
			"serves what a run will create, and touches nothing.")
	)
	// The one list, shared with the sampler that measures what this sizes.
	// Two lists would drift, and the failure would be a run that carefully
	// sized a controller it then did not sample.
	all := upstreamscale.Controllers()
	controllers := make([]*upstreamscale.Controller, 0, len(all))
	for i := range all {
		controllers = append(controllers, &all[i])
	}
	for _, c := range controllers {
		fs.StringVar(&c.CPU, c.Name+"-cpu", c.CPU, "CPU request and limit for the "+c.Name+" controller.")
		fs.StringVar(&c.Memory, c.Name+"-memory", c.Memory,
			"Memory request and limit for the "+c.Name+" controller. Raise it and re-run when a rung is "+
				"OOM killed; that loop is the point.")
		fs.StringVar(&c.Namespace, c.Name+"-namespace", c.Namespace, "Namespace of the "+c.Name+" controller.")
		fs.StringVar(&c.Deployment, c.Name+"-deployment", c.Deployment, "Deployment name of the "+c.Name+" controller.")
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	tuning := tuning{
		QPS: *clientQPS, Burst: *clientBurst,
		Lease: *leaseDuration, Renew: *renewDeadline, Retry: *retryPeriod,
		//nolint:gosec // Flag values, bounded by whoever runs this.
		ProbeTimeout: int32(*probeTimeout),
		//nolint:gosec // As above.
		ProbeFailures: int32(*probeFailures),
	}
	if err := run(context.Background(), *kubeconfig, *kubecontext, *profilerAddr, *dryRun, *only,
		controllers, tuning); err != nil {
		fmt.Fprintf(os.Stderr, "could not prepare the cluster: %v\n", err)
		os.Exit(1)
	}
}

// tuning is what a manager is given so that what stops a run is the machine
// rather than one of its own flags. See upstreamscale.ClientLimits.
type tuning struct {
	QPS   float64
	Burst int

	Lease, Renew, Retry time.Duration

	// ProbeTimeout and ProbeFailures decide how long a manager may take to
	// answer a health check that is not a ping — see ProbePatience.
	ProbeTimeout, ProbeFailures int32
}

func run(ctx context.Context, kubeconfig, kubecontext, profilerAddr string, dryRun bool, only string,
	controllers []*upstreamscale.Controller, tune tuning,
) error {
	cfg, err := restConfig(kubeconfig, kubecontext)
	if err != nil {
		return err
	}

	// Preflight first, and on its own if that is all that was asked for.
	//
	// It is the cheapest thing here and it answers the largest open question:
	// the objects a run creates are built against this repository's fork of
	// Cluster API, off the v1.15 line, and the CRDs come from the stock release
	// clusterctl installed. This says whether those agree before a fleet exists
	// to be confused by it.
	preflightErr := preflight(ctx, cfg)
	if preflightErr == nil {
		fmt.Println("preflight               the cluster serves every kind and version a run creates")
	} else {
		fmt.Printf("preflight               FAILED\n\n%v\n\n", preflightErr)
	}
	if only == "preflight" {
		return preflightErr
	}

	cl, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return fmt.Errorf("building a client: %w", err)
	}

	var missing, gateless []string
	for _, c := range controllers {
		cpu, memory, err := c.Quantities()
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}

		var d appsv1.Deployment
		key := client.ObjectKey{Namespace: c.Namespace, Name: c.Deployment}
		if err := cl.Get(ctx, key, &d); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, fmt.Sprintf("%s (%s/%s)", c.Name, c.Namespace, c.Deployment))
				continue
			}
			return fmt.Errorf("reading %s: %w", key, err)
		}

		var did []string
		if upstreamscale.Guarantee(&d, cpu, memory) {
			did = append(did, fmt.Sprintf("Guaranteed at %s CPU / %s memory, with GOMEMLIMIT below it", &cpu, &memory))
		}
		if upstreamscale.Profiling(&d, profilerAddr) {
			did = append(did, "pprof on "+profilerAddr)
		}
		if upstreamscale.ClientLimits(&d, tune.QPS, tune.Burst) {
			did = append(did, fmt.Sprintf("client limits raised to %g QPS / %d burst", tune.QPS, tune.Burst))
		}
		if upstreamscale.LeaderElectionDeadlines(&d, tune.Lease, tune.Renew, tune.Retry) {
			did = append(did, fmt.Sprintf("leader election given %s to renew within a %s lease",
				tune.Renew, tune.Lease))
		}
		if upstreamscale.ProbePatience(&d, tune.ProbeTimeout, tune.ProbeFailures) {
			did = append(did, fmt.Sprintf("health checks given %ds and %d failures",
				tune.ProbeTimeout, tune.ProbeFailures))
		}
		if c.DevCluster && upstreamscale.RunWithoutDocker(&d) {
			did = append(did, "Docker socket, its hostPath volume and the privilege removed")
		}
		// The gate the whole run depends on, set rather than complained about:
		// clusterctl init will not revisit a provider it has already installed,
		// so a cluster whose providers arrived without CLUSTER_TOPOLOGY would
		// otherwise need a reinstall to become measurable. Only on the two
		// controllers that read it.
		if c.TopologyGate && upstreamscale.EnableTopology(&d) {
			did = append(did, "ClusterTopology feature gate enabled")
		}
		if c.TopologyGate && !upstreamscale.TopologyEnabled(&d) {
			gateless = append(gateless, c.Name)
		}

		if len(did) == 0 {
			fmt.Printf("%-22s already prepared (QoS %s)\n", c.Name, upstreamscale.QoSClass(&d))
			continue
		}
		if dryRun {
			fmt.Printf("%-22s would change: %s\n", c.Name, strings.Join(did, "; "))
			continue
		}
		if err := cl.Update(ctx, &d); err != nil {
			return fmt.Errorf("updating %s: %w", key, err)
		}
		fmt.Printf("%-22s %s (QoS %s)\n", c.Name, strings.Join(did, "; "), upstreamscale.QoSClass(&d))
	}

	// Cluster-scoped and applied once, after the managers: a FlowSchema is not
	// a property of any one of them.
	//
	// After the ClientLimits above rather than instead of them. Raising a
	// manager's client rate lets it saturate the API server, and the schema is
	// what keeps its own heartbeat out of the queue it just filled. Setting one
	// without the other is how a manager comes to lose a leader election it
	// was never short of time to renew.
	schema := upstreamscale.LeaderElectionFlowSchema(upstreamscale.ManagerNamespaces(
		derefControllers(controllers)))
	switch {
	case dryRun:
		fmt.Printf("%-22s would route lease renewals in %s to the %q priority level\n",
			"flow control", strings.Join(upstreamscale.ManagerNamespaces(derefControllers(controllers)), ", "),
			upstreamscale.LeaderElectionLevel)
	default:
		changed, err := upstreamscale.EnsureFlowSchema(ctx, cl, schema)
		if err != nil {
			return fmt.Errorf("flow control: %w", err)
		}
		if changed {
			fmt.Printf("%-22s lease renewals routed to the %q priority level, away from the "+
				"managers' own bulk writes\n", "flow control", upstreamscale.LeaderElectionLevel)
		} else {
			fmt.Printf("%-22s already prepared\n", "flow control")
		}
	}

	if len(gateless) > 0 {
		// Should be unreachable: the loop above enables it. Kept because the
		// consequence of it being off is an admission error that names the
		// object rather than the installation, and a run should not discover
		// that one namespace in.
		return fmt.Errorf("these controllers still do not have the ClusterTopology feature gate "+
			"after being patched to: %s. Every cluster this run creates is built from a "+
			"ClusterClass, and a provider without the gate refuses them at admission",
			strings.Join(gateless, ", "))
	}
	if len(missing) > 0 {
		return fmt.Errorf("these controllers are not installed: %s. Run clusterctl init for the core, "+
			"kubeadm bootstrap, kubeadm control plane and docker providers first — and note that the "+
			"docker provider is what serves DevCluster, in-memory backend included",
			strings.Join(missing, ", "))
	}
	// Reported last as well as first: the patching is worth doing either way,
	// and a preflight failure is what stops a run rather than what stops this.
	return preflightErr
}

// preflight asks the cluster which of the group versions this run builds
// against it actually serves.
//
// Version by version rather than through ServerPreferredResources, which
// returns one version per group: a cluster serving both v1beta1 and v1beta2
// would answer with whichever it prefers, and "the preferred version does not
// have this kind" is a different statement from "this cluster cannot serve it".
func preflight(ctx context.Context, cfg *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building a discovery client: %w", err)
	}
	served := map[string][]string{}
	for _, gv := range upstreamscale.NeededGroupVersions() {
		list, err := dc.ServerResourcesForGroupVersion(gv)
		if err != nil {
			// Not served at all is the answer Preflight wants, and it says
			// which provider installs it.
			continue
		}
		served[gv] = upstreamscale.IndexResources([]*metav1.APIResourceList{list})[gv]
	}
	_ = ctx
	return upstreamscale.Preflight(served)
}

func restConfig(kubeconfig, kubecontext string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building a client config: %w", err)
	}
	return cfg, nil
}

// derefControllers is the slice ManagerNamespaces wants. The flags bind to
// pointers so that a controller's fields can be overridden individually.
func derefControllers(in []*upstreamscale.Controller) []upstreamscale.Controller {
	out := make([]upstreamscale.Controller, 0, len(in))
	for _, c := range in {
		out = append(out, *c)
	}
	return out
}
