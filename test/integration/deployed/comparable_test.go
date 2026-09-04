//go:build integration

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

package deployed_test

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

// The comparable run's own flags. The images, the namespace and the cluster
// come from the deployed run's, because they are the same things.
var (
	// Off unless asked for. This test shares a package with the deployed
	// harness, which a cluster run drives with its own flags — and without this
	// guard `task test:scale:cluster` would start a 24-hour climb nobody asked
	// for, on the cluster it was pointed at, because the package's tests all
	// run together.
	comparableRun = flag.Bool("comparable-run", false,
		"Run the comparable kcp climb. Off by default: it is hours of wall clock against whatever cluster "+
			"the deployed flags name, and it shares a package with the deployed harness.")

	comparableStartClusters = flag.Int("comparable-start-clusters", 25, "The first rung.")
	comparableMaxClusters   = flag.Int("comparable-max-clusters", 400, "The last rung the ladder will offer.")
	comparableNodesPer      = flag.Int("comparable-nodes-per-cluster", 10,
		"Nodes per cluster, control plane included.")
	comparableControlPlanes = flag.Int("comparable-control-plane-nodes", 3,
		"Of those, how many are control plane.")
	comparablePerWorkspace = flag.Int("comparable-clusters-per-workspace", 10,
		"How many clusters share a workspace. The stock side puts the same number in a namespace.")

	comparableShardReplicas = flag.Int("comparable-shard-replicas", 3,
		"How many processes serve the shard. Three, because the stock side is three kube-apiservers behind a "+
			"VIP and each holds its own watch cache.")
	comparableEtcdMembers = flag.Int("comparable-etcd-members", 3,
		"How many etcd members kcp is given. Three, as kubeadm gives the stock side.")
	comparableEtcdStorageClass = flag.String("comparable-etcd-storage-class", "",
		"StorageClass for each member's volume. Empty takes the cluster's default, which on a cluster with "+
			"none leaves the members Pending.")
	comparableEtcdStorageSize = flag.String("comparable-etcd-storage-size", "100Gi",
		"Size of each member's volume.")
	comparableEtcdQuotaBytes = flag.Int64("comparable-etcd-quota-bytes", 8*1024*1024*1024,
		"--quota-backend-bytes. Match the stock side's, or a run that reaches it reports a different ceiling.")
	comparableControlPlaneNodes = flag.String("comparable-control-plane-node-selector", "",
		"key=value pinning the shard and its store to the nodes the comparison gives the control plane under "+
			"test. Empty leaves them to the scheduler, which is not the same budget the stock side gets.")
	comparableManagerNodes = flag.String("comparable-manager-node-selector", "",
		"key=value keeping the four managers off those nodes. The other half of giving a control plane its "+
			"own nodes: a manager sharing a node with the shard it is driving makes the shard's figures a "+
			"measurement of both.")
	comparableEtcdCPU    = flag.String("comparable-etcd-cpu", "2", "CPU request for each etcd member.")
	comparableEtcdMemory = flag.String("comparable-etcd-memory", "8Gi",
		"Memory request and limit for each etcd member.")

	comparableStepTimeout = flag.Duration("comparable-step-timeout", 45*time.Minute,
		"How long one rung may take to converge.")
	comparablePollInterval = flag.Duration("comparable-poll-interval", 15*time.Second,
		"How often to check a rung's progress.")
	comparableSoak = flag.Duration("comparable-soak", 30*time.Minute,
		"How long to hold the last rung that converged.")
	comparableSoakInterval = flag.Duration("comparable-soak-interval", 5*time.Minute,
		"How often to sample during the soak.")
	comparableTeardownTimeout = flag.Duration("comparable-teardown-timeout", 30*time.Minute,
		"How long teardown may wait for the fleet's workspaces to go.")
	comparableSettleTimeout = flag.Duration("comparable-settle-timeout", 5*time.Minute,
		"How long to wait for the managers to stop moving before taking the baseline.")
	comparableSettleTolerance = flag.Float64("comparable-settle-tolerance", 0.02,
		"How much a manager's goroutine count may move between samples and still count as settled.")
	comparableHeapSamples = flag.Int("comparable-shard-heap-samples", 5,
		"How many times to read each shard replica's heap, taking the lowest as the floor.")
	comparableHeapGap = flag.Duration("comparable-shard-heap-gap", 2*time.Second,
		"How long between those reads.")
	comparableCreateConcurrency = flag.Int("comparable-create-concurrency", 16,
		"How many workspaces to create at once.")
	comparableClientQPS = flag.Float64("comparable-client-qps", 200,
		"The driver's client-side rate limit. client-go defaults to 5, which times the driver rather than "+
			"the system under test.")
	comparableClientBurst = flag.Int("comparable-client-burst", 400, "Its burst.")

	comparableOut     = flag.String("comparable-out", "bin", "Where the report is written.")
	comparableOutName = flag.String("comparable-out-name", "kcp-scale", "Its name.")
	comparableKeep    = flag.Bool("comparable-keep", false,
		"Leave the run behind for inspection. What a run leaves behind is what the next one measures.")
)

// TestKcpClusterApiClimbsUntilSomethingGives is the kcp half of the comparison,
// driven by the same Runner as the stock half.
//
// See specs/20260904-090000-comparable-kcp-stock-scale/spec.md. Everything the
// two runs do differently is in the two Targets: this one's tenant is a
// Workspace, its fleet lives in the shard, its managers are four Deployments in
// one namespace and its store is the etcd this run deploys. The ladder, the
// settle, the sampling, the defragmentation, the soak, the drift check and the
// report are the same code, which is what makes the two reports subtractable.
//
// It skips rather than fails when there is no cluster, like every other scale
// measurement here.
func TestKcpClusterApiClimbsUntilSomethingGives(t *testing.T) {
	if !*comparableRun {
		t.Skip("could not run: not asked for. This climb takes hours against whatever cluster the " +
			"deployed flags name, so it runs only with -comparable-run; `task test:kcp:scale` passes it.")
	}

	cfg, err := deployedscale.ClusterConfig(*kubeconfig, *kubecontext)
	if err != nil {
		t.Skipf("could not run: %v", err)
	}
	// client-go applies 5 QPS and a burst of 10 when they are left at zero, so
	// a driver that never sets them times itself rather than the system under
	// test. The stock side sets the same pair, from the same reasoning.
	cfg.QPS, cfg.Burst = float32(*comparableClientQPS), *comparableClientBurst

	ctx := t.Context()
	if err := deployedscale.ClusterReachable(ctx, cfg); err != nil {
		t.Skipf("could not run: %v", err)
	}

	scheme := deployedScheme(t)
	host, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a cluster client: %v", err)
	}

	options, err := comparableOptions(t)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}

	// Before anything is applied: this cluster is generated for the stock side,
	// where nothing asks for a volume, and the store asks for one per member.
	// See deployedscale.StorageAvailable for what it looks like without this.
	if err := deployedscale.StorageAvailable(ctx, host, options.Etcd.StorageClass); err != nil {
		t.Fatalf("could not run: %v", err)
	}

	creds, err := deployedscale.NewCredentials(
		deployedscale.ServiceNames(deployedscale.KcpName, options.Namespace),
		deployedscale.LoopbackIPs(), 24*time.Hour)
	if err != nil {
		t.Fatalf("minting credentials: %v", err)
	}
	infrastructure, err := options.InfrastructureObjects(creds)
	if err != nil {
		t.Fatalf("building the manifests: %v", err)
	}
	managerObjects, err := options.ManagerObjects()
	if err != nil {
		t.Fatalf("building the manifests: %v", err)
	}

	// A clean namespace before anything is applied, not only after: the
	// credentials are minted fresh every run, so a surviving kcp pod goes on
	// serving the previous run's certificate while this one trusts a CA that
	// never signed it. See runDeployed, where the same reasoning is written out
	// in full.
	if !*comparableKeep {
		if err := deployedscale.TeardownAndWait(ctx, host, options.Namespace,
			8*time.Minute, *comparablePollInterval); err != nil {
			t.Fatalf("clearing anything left from an earlier run: %v", err)
		}
		t.Cleanup(func() {
			teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
			defer cancel()
			if err := deployedscale.TeardownAndWait(teardownCtx, host, options.Namespace,
				10*time.Minute, *comparablePollInterval); err != nil {
				t.Errorf("tearing down: %v", err)
			}
		})
	}

	// The store and the shard first, the managers once there is something for
	// them to resolve.
	if err := deployedscale.Apply(ctx, host, infrastructure); err != nil {
		t.Fatalf("applying the manifests: %v", err)
	}
	if err := deployedscale.WaitForDeployment(ctx, host, options.Namespace,
		deployedscale.KcpName, *readyTimeout, *comparablePollInterval); err != nil {
		t.Fatalf("the shard did not come up: %v", err)
	}

	// The driver's own path to the shard. The managers reach it through its
	// Service; this is one forwarded port to one replica, which is enough to
	// create workspaces through — they are the same server.
	shardPods, err := deployedscale.ComponentPods(ctx, host, options.Namespace, deployedscale.KcpName)
	if err != nil || len(shardPods) == 0 {
		t.Fatalf("finding the shard's pods: %v", err)
	}
	forward, err := deployedscale.PortForward(ctx, cfg, options.Namespace, shardPods[0].Name, deployedscale.KcpPort)
	if err != nil {
		t.Fatalf("forwarding a port to the shard: %v", err)
	}
	t.Cleanup(forward.Stop)

	shardCfg := restConfigFor(creds, "https://"+forward.Local)
	rootClient, err := client.New(
		restConfigFor(creds, "https://"+forward.Local+"/clusters/"+deployedscale.RootWorkspace),
		client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a client for :root: %v", err)
	}

	// The exports an installation publishes, not a synthesised one: these
	// managers discover through the real APIExportEndpointSlices.
	providers := capiexports.All()
	discovery, err := capiexports.Publish(ctx, rootClient, providers, 2*time.Minute)
	if err != nil {
		t.Fatalf("publishing the provider exports: %v", err)
	}

	if err := deployedscale.Apply(ctx, host, managerObjects); err != nil {
		t.Fatalf("applying the manager manifests: %v", err)
	}
	for _, c := range options.Components {
		if err := deployedscale.WaitForDeployment(ctx, host, options.Namespace, c.Name,
			*readyTimeout, *comparablePollInterval); err != nil {
			if logs := deployedscale.ContainerLogs(ctx, cfg, host, options.Namespace, c.Name, 40); logs != "" {
				t.Logf("%s logs:\n%s", c.Name, logs)
			}
			t.Fatalf("%s did not come up: %v", c.Name, err)
		}
	}

	sampler, err := upstreamscale.NewSampler(cfg)
	if err != nil {
		t.Fatalf("building a sampler: %v", err)
	}

	// Every replica read as itself, with the credentials the pod proxy would
	// have stripped. See upstreamscale.ForwardedShard.
	profiling := restConfigFor(creds, "https://"+forward.Local)
	profiling.BearerToken = creds.ProfilingToken
	shard := &upstreamscale.ForwardedShard{
		Host:      cfg,
		Shard:     profiling,
		Namespace: options.Namespace,
		Port:      deployedscale.KcpPort,
	}
	t.Cleanup(shard.Close)

	target := &upstreamscale.KcpTarget{
		Tenancy: &upstreamscale.WorkspaceTenancy{
			Root:      rootClient,
			Base:      shardCfg,
			Scheme:    scheme,
			Providers: providers,
			Discovery: discovery,
			Timeout:   2 * time.Minute,
		},
		Shard:     shard,
		Sampler:   sampler,
		Namespace: options.Namespace,
		Shape: upstreamscale.FleetShape{
			ClustersPerNamespace: *comparablePerWorkspace,
			ControlPlaneMachines: *comparableControlPlanes,
			WorkerMachines:       *comparableNodesPer - *comparableControlPlanes,
		},
		NodesPerCluster: *comparableNodesPer,
	}

	runner := &upstreamscale.Runner{
		Target:       target,
		Host:         host,
		Sampler:      sampler,
		Defragmenter: upstreamscale.NewDefragmenter(cfg, mustCoreRESTClient(t, cfg)),
		Logf:         t.Logf,
		Options: upstreamscale.RunOptions{
			StartClusters:     *comparableStartClusters,
			MaxClusters:       *comparableMaxClusters,
			NodesPerCluster:   *comparableNodesPer,
			CreateConcurrency: *comparableCreateConcurrency,
			SettleTolerance:   *comparableSettleTolerance,
			SettleTimeout:     *comparableSettleTimeout,
			StepTimeout:       *comparableStepTimeout,
			PollInterval:      *comparablePollInterval,
			Soak:              *comparableSoak,
			SoakInterval:      *comparableSoakInterval,
			TeardownTimeout:   *comparableTeardownTimeout,
			APIHeapSamples:    *comparableHeapSamples,
			APIHeapGap:        *comparableHeapGap,
			DriverFact: fmt.Sprintf("creates %d workspaces at once, at %g QPS (burst %d): client-go's "+
				"default is 5 QPS, which times the driver rather than the system under test",
				*comparableCreateConcurrency, *comparableClientQPS, *comparableClientBurst),
		},
	}

	// Registered before the run, so that a half-built rung is removed too.
	t.Cleanup(func() {
		if *comparableKeep {
			t.Logf("NOTE: leaving %d workspaces behind (--comparable-keep)", len(runner.Created))
			return
		}
		teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), *comparableTeardownTimeout)
		defer cancel()
		if err := runner.Teardown(teardownCtx); err != nil {
			t.Errorf("teardown did not finish: %v", err)
		}
	})

	report, _, runErr := runner.Run(ctx)
	if report != nil {
		report.AddFact("shardReplicas", fmt.Sprint(*comparableShardReplicas))
		report.AddFact("etcdMembers", fmt.Sprint(*comparableEtcdMembers))
		if n := shard.Restarts(); n > 0 {
			// The driver's own path, not the fleet's: the managers reach the
			// shard through its Service. A run that fought its instrument
			// should say so on its face rather than have it read as the
			// fleet's doing.
			report.AddFact("shardForwardRestarts", fmt.Sprint(n))
		}
		if err := report.Write(*comparableOut, *comparableOutName); err != nil {
			t.Errorf("writing the report: %v", err)
		}
		t.Logf("report written to %s", filepath.Join(*comparableOut, *comparableOutName+".md"))
		t.Logf("\n%s", report.Markdown())
	}
	if runErr != nil {
		t.Fatalf("%v", runErr)
	}
}

// comparableOptions is the deployed run's options with the comparison's own
// shape on top: an external store, several shard replicas, both pinned to the
// nodes the control plane under test is given, and pprof open so that the
// managers are read with the instrument the stock side's are.
func comparableOptions(t *testing.T) (deployedscale.Options, error) {
	t.Helper()

	options, err := optionsFromFlags(t)
	if err != nil {
		return deployedscale.Options{}, err
	}

	selector, err := parseSelector(*comparableControlPlaneNodes)
	if err != nil {
		return deployedscale.Options{}, err
	}
	managers, err := parseSelector(*comparableManagerNodes)
	if err != nil {
		return deployedscale.Options{}, err
	}

	cpu, err := resource.ParseQuantity(*comparableEtcdCPU)
	if err != nil {
		return deployedscale.Options{}, fmt.Errorf("parsing -comparable-etcd-cpu: %w", err)
	}
	memory, err := resource.ParseQuantity(*comparableEtcdMemory)
	if err != nil {
		return deployedscale.Options{}, fmt.Errorf("parsing -comparable-etcd-memory: %w", err)
	}

	options.ShardReplicas = int32(*comparableShardReplicas) //nolint:gosec // a replica count from a flag.
	options.KcpNodeSelector = selector
	options.ManagerNodeSelector = managers
	options.ProfilerPort = deployedscale.ProfilerPort
	options.Etcd = deployedscale.EtcdOptions{
		Members:      int32(*comparableEtcdMembers), //nolint:gosec // a member count from a flag.
		StorageClass: *comparableEtcdStorageClass,
		StorageSize:  *comparableEtcdStorageSize,
		QuotaBytes:   *comparableEtcdQuotaBytes,
		NodeSelector: selector,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory},
			Limits:   corev1.ResourceList{corev1.ResourceMemory: memory},
		},
	}
	return options, nil
}

// parseSelector reads a key=value node selector, and refuses anything else
// rather than silently pinning nothing.
func parseSelector(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	key, value, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("node selector %q is not key=value", s)
	}
	return map[string]string{strings.TrimSpace(key): strings.TrimSpace(value)}, nil
}

func mustCoreRESTClient(t *testing.T, cfg *rest.Config) rest.Interface {
	t.Helper()
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &corev1.SchemeGroupVersion
	c.APIPath = "/api"
	c.NegotiatedSerializer = clientgoscheme.Codecs.WithoutConversion()
	rc, err := rest.RESTClientFor(c)
	if err != nil {
		t.Fatalf("building a REST client: %v", err)
	}
	return rc
}
