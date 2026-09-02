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

// Package deployed_test measures the four managers as four Deployments in a
// Kubernetes cluster, rather than as four sets of controllers in one process.
//
// See `specs/20260831-210000-deployed-fleet-scale/spec.md`. What it measures
// that the in-process instruments structurally cannot: cost split per
// deployment, CPU, the multiplier from a live heap to a container limit, the
// network between components, and an OOMKill as a capacity finding.
package deployed_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaletarget"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

var (
	kubeconfig = flag.String("deployed-kubeconfig", "",
		"Kubeconfig of the cluster to run against. Empty uses the usual resolution (KUBECONFIG, then the default "+
			"path). kind is one way to produce a cluster and a real multi-node one is where the figures are worth "+
			"quoting from; this harness cannot tell them apart and must not try.")
	kubecontext = flag.String("deployed-context", "",
		"Kubeconfig context to use. Empty uses the current one — which is the wrong default for something that "+
			"creates workloads, so the task targets name it explicitly.")
	namespace = flag.String("deployed-namespace", "kcp-scale",
		"Namespace holding everything the run creates, and what tearing it down deletes.")
	kcpImage = flag.String("deployed-kcp-image", "",
		"Image for kcp. Required: a default would silently measure a version nobody chose.")
	managerImage = flag.String("deployed-manager-image", "",
		"Image prefix for the managers. Each is <prefix>/<binary>:<tag> unless -deployed-images names them "+
			"individually. Required.")
	imageTag = flag.String("deployed-image-tag", "latest",
		"Tag appended to the manager image prefix.")
	imagePullPolicy = flag.String("deployed-image-pull-policy", string(deployedscale.DefaultImagePullPolicy),
		"How the kubelet fetches the images. IfNotPresent is the default because the local path loads images "+
			"straight onto the nodes with no registry to pull from, and Kubernetes' own tag-derived default of "+
			"Always would look for one. Set Always against a real registry with a moving tag.")
	componentNames = flag.String("deployed-components", "all",
		"Comma-separated managers to deploy, or 'all'. Narrowing to one is how a deployed figure gets checked "+
			"against an in-process one for the same deployment; it also measures a weaker end state, since a "+
			"cluster is taken to readiness by all four together.")
	spreadAcrossNodes = flag.Bool("deployed-spread", false,
		"Require every component on a different node. Needs a cluster with at least that many, and makes every pod "+
			"unschedulable where there are not — which is the honest failure, rather than a co-located run "+
			"reported as a spread one.")
	clusters = flag.Int("deployed-clusters", 4,
		"How many clusters the fleet holds in total.")
	nodesPerCluster = flag.Int("deployed-nodes-per-cluster", 1,
		"How many nodes each cluster reaches, control plane included.")
	controlPlaneNodes = flag.Int("deployed-control-plane-nodes", 1,
		"How many of each cluster's nodes are control plane machines.")
	clustersPerWorkspace = flag.String("deployed-clusters-per-workspace", "",
		"How the clusters are spread over workspaces, comma separated for more than one spread. Each spread is a "+
			"sub-test: the run is torn down and stood up again for it, because a deployed spread is a whole "+
			"cluster's worth of deployments. Empty derives it from the cluster count.")
	endState = flag.String("deployed-end-state", "",
		"What a checkpoint waits for: 'engaged' (every workspace bound and holding its objects) or 'ready' "+
			"(every control plane ready and every Machine Ready). Empty picks the strongest state the deployed "+
			"set can actually reach — 'ready' needs all four providers, because a cluster is taken to readiness "+
			"by all four.")
	checkpoints = flag.String("deployed-checkpoints", "50",
		"Percentages of the workspace target to stop and sample at. The target is always the last.")
	reference = flag.String("deployed-reference", "",
		"A committed in-process sweep report to reconcile against, e.g. "+
			"specs/20260820-152056-clusterclass-based-clusters/evidence/sweep-report-core.json. Empty skips the "+
			"reconciliation, which makes the run a second instrument with nothing keeping it honest.")
	tolerance = flag.Float64("deployed-tolerance", deployedscale.DefaultTolerance,
		"How far a deployed per-workspace figure may sit from the in-process one before it is a disagreement.")
	budget         = flag.Duration("deployed-budget", 60*time.Minute, "Wall-clock budget for the run.")
	kcpMemoryLimit = flag.String("deployed-kcp-memory-limit", "",
		"Memory limit for kcp, e.g. 8Gi. Empty keeps the default. kcp is the component measured runs have "+
			"found to hit its limit first, so this is the knob that maps where that limit sits.")
	managerMemoryLimit = flag.String("deployed-manager-memory-limit", "",
		"Memory limit for each manager, e.g. 4Gi. Empty keeps the default.")
	readyTimeout = flag.Duration("deployed-ready-timeout", 10*time.Minute,
		"How long to wait for kcp and each manager to become available.")
	stepTimeout = flag.Duration("deployed-step-timeout", 20*time.Minute,
		"How long one checkpoint may take to reach its end state.")
	pollInterval  = flag.Duration("deployed-poll-interval", 5*time.Second, "How often the fleet's state is polled.")
	keepNamespace = flag.Bool("deployed-keep", false,
		"Leave the namespace behind after the run, so a failure can be inspected.")
)

// TestDeployedFleet drives a fleet against managers running as Deployments and
// reports what each of them cost.
//
// # It reports "could not run" rather than failing
//
// A cluster is a capability, not a given. With none reachable this skips with
// a message beginning "could not run", which is the only outcome Go's testing
// package offers that is distinct from a failure — and the distinction is the
// point (FR-005, and Constitution Principle IV). It is deliberately outside
// `verify` and `check` for the same reason `test:scale` is.
func TestDeployedFleet(t *testing.T) {
	plans := plansFromFlags(t)
	options, err := optionsFromFlags(t)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}

	for _, plan := range plans {
		t.Run(plan.Shape.String(), func(t *testing.T) {
			runDeployed(t, plan, options)
		})
	}
}

// runDeployed stands the fleet up, measures it, and takes it down again.
//
// One spread at a time and torn down in between, sharing one namespace. Two
// spreads deployed side by side would be two kcps and two sets of managers on
// one cluster, and the second's figures would include the first's load.
func runDeployed(t *testing.T, plan scaletarget.Plan, options deployedscale.Options) {

	cfg, err := deployedscale.ClusterConfig(*kubeconfig, *kubecontext)
	if err != nil {
		t.Skipf("could not run: %v", err)
	}
	ctx := t.Context()
	if err := deployedscale.ClusterReachable(ctx, cfg); err != nil {
		t.Skipf("could not run: %v", err)
	}

	scheme := deployedScheme(t)
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a cluster client: %v", err)
	}

	report := &deployedscale.Report{Title: fmt.Sprintf(
		"Deployed fleet: %d clusters over %d workspaces, %d nodes each",
		plan.Shape.Clusters(), plan.Shape.Workspaces, plan.Machines.PerCluster())}
	report.AddFact("spread", plan.Shape.String())
	report.AddFact("targetWorkspaces", fmt.Sprint(plan.Shape.Workspaces))
	report.AddFact("targetClusters", fmt.Sprint(plan.Shape.Clusters()))
	report.AddFact("nodesPerCluster", fmt.Sprint(plan.Machines.PerCluster()))
	report.AddFact("devClusterBackend", "inMemory")
	report.AddFact("deployment", "one Deployment per manager — the shape an installation runs")
	report.AddFact("components", strings.Join(componentNamesOf(options.Components), ", "))
	report.AddFact("antiAffinity", fmt.Sprint(*spreadAcrossNodes))
	report.AddFact("imagePullPolicy", *imagePullPolicy)
	wanted, err := deployedscale.ResolveEndState(*endState, options.Components)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}
	report.AddFact("endState", deployedscale.EndStateDescription(wanted))
	report.AddFact("cluster", cfg.Host)
	if *kubecontext != "" {
		report.AddFact("kubecontext", *kubecontext)
	}

	t.Cleanup(func() {
		t.Logf("\n%s", report.Markdown())
		dir := reportDir(t)
		name := "deployed-" + plan.Shape.String()
		if err := report.Write(dir, name); err != nil {
			t.Errorf("writing the report: %v", err)
			return
		}
		t.Logf("deployed report written to %s", filepath.Join(dir, name+".md"))
	})

	// --- Stand the run up.
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

	if !*keepNamespace {
		t.Cleanup(func() {
			// A fresh context: the test's own is cancelled by the time
			// cleanup runs, and a teardown that cannot issue its delete leaves
			// a namespace behind on every run.
			teardownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			// Waited on, not just requested: the next spread deploys into this
			// namespace, and one still terminating would be measured with it.
			if err := deployedscale.TeardownAndWait(teardownCtx, cl, options.Namespace, 8*time.Minute, *pollInterval); err != nil {
				t.Errorf("tearing down: %v", err)
			}
		})
	}

	// A clean namespace before anything is applied, not only after.
	//
	// Tearing down at the end is not enough, because a run that was interrupted
	// never got there — and what survives is not inert. The credentials are
	// minted fresh every run, so a surviving kcp pod goes on serving the previous
	// run's certificate while this one trusts a CA that never signed it:
	//
	//	tls: failed to verify certificate: x509: certificate signed by unknown
	//	authority ... "kcp-cluster-api-scale-ca"
	//
	// Other leftovers are worse for being silent. A Service cannot be changed
	// to headless in place because clusterIP is immutable, so a namespace from
	// before that fix keeps a Service that quietly breaks workspace
	// initialization. A measurement that inherits any of this is not a
	// measurement of what was built.
	if !*keepNamespace {
		if err := deployedscale.TeardownAndWait(ctx, cl, options.Namespace, 8*time.Minute, *pollInterval); err != nil {
			t.Fatalf("clearing anything left from an earlier run: %v", err)
		}
	}

	// kcp first, and the managers only once there is something for them to
	// resolve. See Options.ManagerObjects.
	if err := deployedscale.Apply(ctx, cl, infrastructure); err != nil {
		t.Fatalf("applying the manifests: %v", err)
	}

	if err := deployedscale.WaitForDeployment(ctx, cl, options.Namespace,
		deployedscale.KcpName, *readyTimeout, *pollInterval); err != nil {
		t.Fatalf("kcp did not come up: %v", err)
	}

	// Whether kcp can reach the address it advertises decides whether any
	// workspace ever initializes, and a Service that already existed cannot
	// be changed to headless — clusterIP is immutable, so a namespace left
	// over from an earlier run keeps the old one. Recording what is actually
	// in the cluster is the difference between knowing the fix was in effect
	// and assuming it.
	var kcpService corev1.Service
	if err := cl.Get(ctx, client.ObjectKey{Namespace: options.Namespace, Name: deployedscale.KcpName},
		&kcpService); err == nil {
		report.AddFact("kcpServiceClusterIP", kcpService.Spec.ClusterIP)
		if kcpService.Spec.ClusterIP != corev1.ClusterIPNone {
			t.Logf("NOTE: the kcp Service has a virtual IP (%s) rather than being headless. kcp has to reach "+
				"the address it advertises, and a pod dialling its own ClusterIP is the hairpin case. If this "+
				"namespace survived an earlier run, its Service predates the fix and cannot be changed in "+
				"place — clusterIP is immutable. Delete the namespace and rerun.", kcpService.Spec.ClusterIP)
		}
	}

	// --- Reach kcp from outside the cluster.
	kcpPods, err := deployedscale.ComponentPods(ctx, cl, options.Namespace, deployedscale.KcpName)
	if err != nil || len(kcpPods) == 0 {
		t.Fatalf("finding the kcp pod: %v", err)
	}
	forward, err := deployedscale.PortForward(ctx, cfg, options.Namespace, kcpPods[0].Name, deployedscale.KcpPort)
	if err != nil {
		t.Fatalf("forwarding a port to kcp: %v", err)
	}
	t.Cleanup(forward.Stop)
	local := forward.Local
	// Recorded because a flapping tunnel is the driver's problem and not the
	// fleet's — the managers reach kcp through the Service — but a run that
	// fought its instrument all the way through should say so on its face.
	t.Cleanup(func() {
		if n := forward.Restarts(); n > 0 {
			report.AddFact("kcpPortForwardRestarts", fmt.Sprintf("%d", n))
			t.Logf("NOTE: the port-forward to kcp was rebuilt %d time(s) during this run. That is the "+
				"driver's path only; the managers reach kcp through its Service.", n)
		}
	})

	kcpCfg := restConfigFor(creds, "https://"+local+"/clusters/"+deployedscale.RootWorkspace)
	rootClient, err := client.New(kcpCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a kcp client: %v", err)
	}

	// The shard's profiles are read as a second, privileged identity. Granting
	// them to the run's own identity was tried twice — cluster-admin in every
	// workspace, then a ClusterRole and binding for the non-resource URLs
	// inside :root — and refused identically both times. See
	// deployedscale.ProfilingGroup.
	profilingCfg := restConfigFor(creds, "https://"+local)
	profilingCfg.BearerToken = creds.ProfilingToken

	// --- Publish the exports an installation publishes, not a synthesised
	// one. These managers discover through the real APIExportEndpointSlices,
	// so the run has to create the real exports.
	providers := capiexports.All()
	discovery, err := capiexports.Publish(ctx, rootClient, providers, 2*time.Minute)
	if err != nil {
		t.Fatalf("publishing the provider exports: %v", err)
	}

	// One workspace bound before the managers are created. Publishing the
	// export is not enough: its APIExportEndpointSlice carries no endpoints
	// until something binds it, and a manager that starts before then exits
	// and crash loops. See Options.ManagerObjects.
	tenants := make([]*tenant, 0, plan.Shape.Workspaces)
	first, err := newTenant(ctx, kcpCfg, rootClient, scheme, 0, plan, providers, discovery)
	if err != nil {
		// Everything the cluster knows about why. A workspace held out of
		// Ready reports the initializer that is outstanding, and the reason
		// that initializer is stuck lives on the LogicalCluster rather than
		// on the Workspace — so reporting only the Workspace names the
		// symptom and hides the cause.
		diagnoseWorkspace(t, ctx, kcpCfg, rootClient, scheme, "scale-0000")
		logs, narrow := deployedscale.ContainerLogsMatching(ctx, cfg, cl, options.Namespace, deployedscale.KcpName,
			deployedscale.InitializationLogPatterns, deployedscale.StartupFailurePatterns, 60)
		switch {
		case logs != "" && narrow:
			t.Logf("kcp logs about initialization:\n%s", logs)
		case logs != "":
			t.Logf("kcp never mentioned initialization for this workspace, which is itself the finding: its "+
				"apibinder did not run. What it did complain about:\n%s", logs)
		default:
			t.Logf("kcp logged nothing about initialization and nothing that looks like a failure")
		}
		t.Fatalf("provisioning the first workspace: %v", err)
	}
	tenants = append(tenants, first)

	if err := deployedscale.Apply(ctx, cl, managerObjects); err != nil {
		t.Fatalf("applying the manager manifests: %v", err)
	}

	for _, c := range options.Components {
		if err := deployedscale.WaitForDeployment(ctx, cl, options.Namespace, c.Name, *readyTimeout, *pollInterval); err != nil {
			// What the container said before it died. The pod status carries a
			// reason; the logs carry the sentence that explains it, and a run
			// that reports only "CrashLoopBackOff" has made somebody go and
			// fetch them by hand.
			if logs := deployedscale.ContainerLogs(ctx, cfg, cl, options.Namespace, c.Name, 40); logs != "" {
				t.Logf("%s logs:\n%s", c.Name, logs)
			}
			t.Fatalf("%s did not come up: %v", c.Name, err)
		}
	}

	// A second forward, to the embedded etcd's own metrics. Best effort: it
	// answers which half of the shard's memory is the database and which is the
	// watch cache, and a run that cannot tell is still a run.
	var etcdLocal string
	if f, err := deployedscale.PortForward(ctx, cfg, options.Namespace, kcpPods[0].Name,
		deployedscale.KcpEtcdPort); err == nil {
		t.Cleanup(f.Stop)
		etcdLocal = f.Local
	} else {
		t.Logf("NOTE: no forward to the embedded etcd (%v), so this run cannot say how much of kcp's "+
			"memory is its backend database rather than its caches", err)
	}

	scraper, err := deployedscale.NewScraper(cfg)
	if err != nil {
		t.Fatalf("building the scraper: %v", err)
	}

	// --- A sample before any workspace exists.
	//
	// Every slope this harness reports is a difference between two large
	// numbers, and without this the smaller of them is still a fleet. kcp's
	// first sample was at 130 Machines and 1.44 GiB, which says nothing about
	// how much of that 1.44 GiB is the shard merely existing — so a per-Machine
	// figure derived from it was quietly a per-Machine figure plus a share of
	// an intercept nobody had measured.
	//
	// The in-process sweeps have taken a baseline from the beginning, labelled
	// "baseline (manager not started)". This is the deployed equivalent: the
	// managers are up and have engaged nothing.
	// What the collections this harness forces have cost the shard in CPU, so
	// that a per-Machine CPU figure drawn from these samples can be corrected.
	var forcedCollectionCPU float64
	if components, err := scraper.SampleComponents(ctx, cl, options.Namespace, options.Components); err == nil {
		settleKcpHeap(t, ctx, profilingCfg, kcpCfg, report, &forcedCollectionCPU)
		if kcpSample, err := deployedscale.KcpSample(ctx, kcpCfg, cl, options.Namespace); err == nil {
			components = append(components, kcpSample)
		}
		report.Add(deployedscale.Sample{
			Label:      "baseline (no workspaces)",
			Workspaces: 0,
			Clusters:   0,
			Nodes:      0,
			Components: components,
		})
	} else {
		t.Logf("NOTE: no baseline sample (%v), so every slope in this run rests on an unmeasured "+
			"intercept", err)
	}

	// --- Walk to the target, sampling at each checkpoint.
	kcpScrapeReported := false
	deadline := time.Now().Add(*budget)
	var reached int
	var stoppedBy string

	for _, checkpoint := range plan.Checkpoints {
		if time.Now().After(deadline) {
			stoppedBy = fmt.Sprintf("wall-clock budget %s", *budget)
			break
		}

		for len(tenants) < checkpoint {
			tn, err := newTenant(ctx, kcpCfg, rootClient, scheme, len(tenants), plan, providers, discovery)
			if err != nil {
				stoppedBy = fmt.Sprintf("provisioning workspace %d: %v", len(tenants), err)
				break
			}
			tenants = append(tenants, tn)
		}
		if stoppedBy != "" {
			break
		}

		health := func() string { return deployedscale.ServerTrouble(ctx, cl, options.Namespace) }
		if err := awaitEndState(ctx, wanted, tenants, plan,
			min(*stepTimeout, time.Until(deadline)), *pollInterval, health); err != nil {
			stoppedBy = fmt.Sprintf("waiting for %d workspaces to reach the end state: %v", checkpoint, err)
			break
		}

		components, err := scraper.SampleComponents(ctx, cl, options.Namespace, options.Components)
		if err != nil {
			stoppedBy = fmt.Sprintf("sampling at %d workspaces: %v", checkpoint, err)
			break
		}
		// The shard, alongside the managers. It is the component that runs out
		// first — OOM killed at 200 clusters of fifty nodes while the managers
		// sat at a fifth of their limits — and until now the only thing this
		// harness could say about it was that it had died.
		//
		// Not fatal if it fails. The shard's cost is the more interesting half
		// and it is still the newer half; a run that measures four managers and
		// says why it could not measure kcp is worth more than one that throws
		// the managers away too.
		settleKcpHeap(t, ctx, profilingCfg, kcpCfg, report, &forcedCollectionCPU)
		if kcpSample, err := deployedscale.KcpSample(ctx, kcpCfg, cl, options.Namespace); err == nil {
			components = append(components, kcpSample)
			// What the shard is holding, not just how big it got. "kcp needed
			// more than 4 GiB" is not actionable; "it is holding 2,500
			// Machines, 2,500 KubeadmConfigs and 2,600 Secrets" says where to
			// look. Overwritten at each checkpoint, so the fact describes the
			// largest fleet the run reached.
			if counts, err := deployedscale.ScrapeKcpStorage(ctx, kcpCfg); err == nil {
				report.AddFact("kcpStorageObjects", deployedscale.TopStorage(counts, 12))
			}
			// The discriminator. kcp runs etcd in its own process, so one limit
			// covers the watch cache and the backend database, and the two grow
			// for different reasons — the cache with what is stored, the
			// database with every write since the last compaction.
			if etcdLocal != "" {
				if etcd, err := deployedscale.ScrapeEtcd(ctx, etcdLocal); err == nil {
					report.AddFact("kcpEtcd", etcd.Describe())
					report.AddFact("kcpEtcdDBBytes", fmt.Sprint(etcd.DBTotalBytes))
				}
			}
			// A heap profile of the shard at the largest fleet this run
			// reached. Overwritten at each checkpoint, so what survives is the
			// biggest — and the biggest is the one worth opening.
			//
			// Two explanations for this memory have been offered and both were
			// wrong. A profile does not need one: it names what is holding the
			// bytes.
			captureKcpHeap(t, ctx, profilingCfg, report, checkpoint)
		} else if !kcpScrapeReported {
			kcpScrapeReported = true
			report.AddFact("kcpMetrics", "not scraped: "+err.Error())
			t.Logf("NOTE: kcp's own metrics could not be read, so this run measures the managers and not "+
				"the shard — which is the component that runs out first: %v", err)
		}
		report.Add(deployedscale.Sample{
			Label:      fmt.Sprintf("%d workspaces", checkpoint),
			Workspaces: checkpoint,
			Clusters:   checkpoint * plan.Shape.ClustersPerWorkspace,
			Nodes:      checkpoint * plan.Shape.ClustersPerWorkspace * plan.Machines.PerCluster(),
			Components: components,
		})
		reached = checkpoint
	}

	if stoppedBy == "" {
		stoppedBy = fmt.Sprintf("reached the requested target of %d workspaces", plan.Shape.Workspaces)
	}
	verdict := scaletarget.Classify(reached, plan.Shape.Workspaces, stoppedBy)
	report.AddFact("outcome", verdict.Outcome.String())
	report.AddFact("reachedWorkspaces", fmt.Sprint(verdict.Reached))
	report.AddFact("stoppedBy", verdict.StoppedBy)
	if verdict.Note != "" {
		report.AddFact("note", verdict.Note)
	}

	// --- What the deployed run is for: a figure per deployment, and the
	// multiplier from a heap to a limit.
	for _, component := range report.Components() {
		if slope, ok := report.PerWorkspace(component, deployedscale.Goroutines); ok {
			report.AddFact(component+".goroutinesPerWorkspace", fmt.Sprintf("%.1f", slope))
		}
		if slope, ok := report.PerWorkspace(component, deployedscale.Resident); ok {
			report.AddFact(component+".residentBytesPerWorkspace", fmt.Sprintf("%.0f", slope))
		}
		// Per cluster too, and it is the one to read. Twenty-five clusters
		// measured as 25x1 and as 5x5 disagree per workspace (17.0 against
		// 77.0) and agree per cluster (17.0 against 15.4), so the per-workspace
		// figure alone reads as a cost of packing that is not there. See
		// deployedscale.PerCluster.
		if slope, ok := report.PerCluster(component, deployedscale.Goroutines); ok {
			report.AddFact(component+".goroutinesPerCluster", fmt.Sprintf("%.1f", slope))
		}
		if slope, ok := report.PerCluster(component, deployedscale.Resident); ok {
			report.AddFact(component+".residentBytesPerCluster", fmt.Sprintf("%.0f", slope))
		}
		// Live heap as well as resident. Resident is what a limit is set
		// against, but it carries the collector's headroom: in the run that
		// first sampled kcp idle, the shard's resident series missed its own
		// line by 7% of its range while its heap series missed by 1.4%. The
		// heap figure is the one that reproduces, so it is recorded too.
		if slope, ok := report.PerCluster(component, deployedscale.HeapAlloc); ok {
			report.AddFact(component+".heapBytesPerCluster", fmt.Sprintf("%.0f", slope))
		}
		// And what the process cost before the run created anything, which is
		// the only measurement that separates a fixed cost from a fleet's.
		if idle, ok := report.Idle(component); ok {
			report.AddFact(component+".idleGoroutines", fmt.Sprint(idle.Process.Goroutines))
			report.AddFact(component+".idleResidentBytes", fmt.Sprint(idle.Process.ResidentBytes))
			report.AddFact(component+".idleHeapBytes", fmt.Sprint(idle.Process.HeapAllocBytes))
		}
		if last := lastSampleOf(report, component); last != nil {
			report.AddFact(component+".residentToHeapRatio", fmt.Sprintf("%.2f", last.Process.ResidentToHeapRatio()))
			report.AddFact(component+".node", last.Pod.Node)
			if last.Pod.OOMKilled {
				// The capacity finding. Never a smaller measurement.
				report.AddFact(component+".oomKilled",
					fmt.Sprintf("yes — the container exceeded its %d byte limit at %d workspaces",
						last.Pod.MemoryLimitBytes, reached))
			}
		}
	}

	if reached == 0 {
		t.Fatalf("could not run: %s (stopped by: %s)", verdict.Note, verdict.StoppedBy)
	}

	// --- The check that keeps a second instrument honest.
	if *reference != "" {
		reconcile(t, report, *reference, plan, wanted)
	} else {
		t.Log("NOTE: no in-process reference was given, so nothing checks these figures against the instrument " +
			"they are meant to agree with. Pass -deployed-reference.")
	}

	for _, disturbed := range report.Disturbed() {
		t.Errorf("%s restarted during the run (%d times, last: %s): its samples are a fresh process's and are "+
			"not comparable with the others", disturbed.Component, disturbed.Pod.RestartCount, disturbed.Pod.LastReason)
	}

	nodes, coLocated := report.Placement()
	t.Logf("deployed fleet: %s", plan.Shape.String())
	t.Logf("  outcome:     %s", verdict.Outcome)
	t.Logf("  workspaces:  %d of %d", verdict.Reached, plan.Shape.Workspaces)
	t.Logf("  nodes:       %s", strings.Join(nodes, ", "))
	if coLocated {
		t.Logf("NOTE: every component ran on one node. This is not a multi-node figure.")
	}
	if verdict.Note != "" {
		t.Logf("NOTE: %s.", verdict.Note)
	}
}

// reconcile checks the deployed figures against a committed in-process run.
func reconcile(t *testing.T, report *deployedscale.Report, path string, plan scaletarget.Plan, endState string) {
	t.Helper()

	// Relative paths are relative to the repository, not to this package. The
	// flag's own documentation gives "specs/…/evidence/sweep-report-core.json",
	// which is how a person names it and how the Taskfile passes it — and `go
	// test` runs with this directory as the working directory, so taking it
	// literally opened nothing and reported the reference as missing on a run
	// where it was committed all along.
	if !filepath.IsAbs(path) {
		if root, err := findRepoRoot(); err == nil {
			path = filepath.Join(root, path)
		}
	}

	ref, err := deployedscale.LoadSweepReference(path)
	if err != nil {
		t.Errorf("the in-process reference could not be read, so nothing checks this run: %v", err)
		return
	}

	deployed, ok := report.PerWorkspace(ref.DeploymentName, deployedscale.Goroutines)
	if !ok {
		// Not a failure: a run this small cannot produce a slope to check. A
		// per-workspace figure needs at least three distinct workspace counts
		// (see deployedscale.PerWorkspace), and a smoke-sized run — one
		// workspace, or the 1 and 2 that CLUSTERS=2 checkpoints to — has fewer.
		// Failing here would make every small run unpassable and would say
		// nothing about the fleet.
		report.AddFact("reconciliation", "not attempted: too few distinct workspace counts to fit a "+
			"per-workspace slope. Run more workspaces to check this instrument against the in-process one.")
		t.Logf("NOTE: no per-workspace goroutine figure for %s, so nothing checks this run against the "+
			"in-process instrument. That needs at least three distinct workspace counts; this run had too "+
			"few. It is a smaller claim, not a wrong one.", ref.DeploymentName)
		return
	}

	rec := deployedscale.Reconcile("goroutinesPerWorkspace", ref.DeploymentName, path,
		deployed, ref.GoroutinesPerWorkspace, *tolerance)

	// The comparison is only like-for-like at engagement.
	//
	// The in-process sweeps stop there: every workspace bound and holding its
	// objects. A run of all four providers goes on to take every cluster to
	// Ready, and a ready cluster costs the core manager a live ClusterCache —
	// a connection, informers and their goroutines — per workload cluster,
	// which the reference never paid for. deployedscale.EndStateEngaged says
	// as much, and this check used to ignore it and compare anyway.
	//
	// It disagreed by 8.5x, reproducibly: the same 17.0 goroutines per
	// workspace from independent runs at 2/4/8 and 3/5/10 workspaces against
	// the reference's 2.0. That is a sound measurement of work the reference
	// does not do, and reporting it as two instruments disagreeing was wrong.
	if endState != deployedscale.EndStateEngaged {
		rec = deployedscale.Incomparable(rec, fmt.Sprintf(
			"This run measured %q while the in-process reference measured %q, so the ratio above is not a "+
				"disagreement between instruments. A ready cluster costs the core manager a live ClusterCache "+
				"per workload cluster that the reference never opened. To check the two against each other, "+
				"run one deployment (COMPONENTS=core-manager), which measures engagement as the reference does.",
			deployedscale.EndStateDescription(endState),
			deployedscale.EndStateDescription(deployedscale.EndStateEngaged)))
		report.Reconciliations = append(report.Reconciliations, rec)
		t.Logf("NOTE: %s deployed against %s in process (%.2fx). %s",
			formatFloat(rec.Deployed), formatFloat(rec.InProcess), rec.Ratio, rec.Why)
		return
	}

	report.Reconciliations = append(report.Reconciliations, rec)

	if !rec.WithinTolerance {
		t.Errorf("the deployed and in-process instruments disagree about %s: %.1f goroutines per workspace "+
			"deployed against %.1f in process (%.2fx, tolerance %.0f%%). The same program doing the same work "+
			"should agree, so this is a finding about one of the two instruments rather than a figure about the "+
			"fleet at %s.",
			ref.DeploymentName, rec.Deployed, rec.InProcess, rec.Ratio, rec.Tolerance*100, plan.Shape)
	}
}

func lastSampleOf(report *deployedscale.Report, component string) *deployedscale.ComponentSample {
	for i := len(report.Samples) - 1; i >= 0; i-- {
		if c, ok := report.Samples[i].Component(component); ok {
			return &c
		}
	}
	return nil
}

// diagnoseWorkspace prints what kcp knows about a workspace that will not
// become ready.
//
// # Why the LogicalCluster and not just the Workspace
//
// A Workspace held out of Ready says which initializer is outstanding and
// nothing more: "Initializers still exist: [system:apibindings]". The reason
// that initializer cannot finish — the APIBindingsInitialized condition, and
// the server error inside its message — is written on the LogicalCluster
// inside the workspace, which is a different object in a different logical
// cluster. Reading only the Workspace therefore reports the symptom every
// time and the cause never, which is what made this failure take several
// rounds to place.
//
// Best effort throughout: this runs when something has already gone wrong,
// and a diagnostic that fails should not replace the error being diagnosed.
func diagnoseWorkspace(t *testing.T, ctx context.Context, base *rest.Config, rootClient client.Client,
	scheme *k8sruntime.Scheme, name string,
) {
	t.Helper()

	var ws tenancyv1alpha1.Workspace
	if err := rootClient.Get(ctx, client.ObjectKey{Name: name}, &ws); err != nil {
		t.Logf("diagnose: reading workspace %s: %v", name, err)
		return
	}
	t.Logf("diagnose: workspace %s phase=%s cluster=%s", name, ws.Status.Phase, ws.Spec.Cluster)
	for _, c := range ws.Status.Conditions {
		t.Logf("diagnose:   workspace condition %s=%s %s: %s", c.Type, c.Status, c.Reason, c.Message)
	}

	if ws.Spec.Cluster == "" {
		t.Logf("diagnose: the workspace has no logical cluster yet, so there is no LogicalCluster to read")
		return
	}

	// Read over raw REST rather than through a typed client. A typed client
	// builds a RESTMapper by discovery, and a diagnostic that can fail on its
	// own machinery is one that leaves a reader with nothing at exactly the
	// moment they need something — which is what happened here twice.
	//
	// The AbsPath below also means this addresses the workspace itself rather
	// than relying on the host, so it reports on a workspace no typed client
	// can be built for.
	inside := rest.CopyConfig(base)
	inside.Host = deployedscale.ServerURL(base.Host)
	inside.GroupVersion = &corev1alpha1.SchemeGroupVersion
	inside.APIPath = "/apis"
	inside.NegotiatedSerializer = serializer.NewCodecFactory(scheme).WithoutConversion()

	restClient, err := rest.RESTClientFor(inside)
	if err != nil {
		t.Logf("diagnose: building a REST client for %s: %v", ws.Spec.Cluster, err)
		return
	}
	raw, err := restClient.Get().
		AbsPath("/clusters/"+ws.Spec.Cluster, "/apis", corev1alpha1.SchemeGroupVersion.Group,
			corev1alpha1.SchemeGroupVersion.Version, "logicalclusters", "cluster").
		DoRaw(ctx)
	if err != nil {
		t.Logf("diagnose: reading the LogicalCluster inside %s: %v", ws.Spec.Cluster, err)
		return
	}

	var logical corev1alpha1.LogicalCluster
	if err := json.Unmarshal(raw, &logical); err != nil {
		t.Logf("diagnose: decoding the LogicalCluster inside %s: %v", ws.Spec.Cluster, err)
		return
	}
	t.Logf("diagnose: LogicalCluster phase=%s initializers=%v", logical.Status.Phase, logical.Status.Initializers)
	for _, c := range logical.Status.Conditions {
		t.Logf("diagnose:   logicalcluster condition %s=%s %s: %s", c.Type, c.Status, c.Reason, c.Message)
	}
}

// tenant is one workspace and the clusters in it.
type tenant struct {
	name     string
	client   client.Client
	clusters []string
}

func newTenant(ctx context.Context, base *rest.Config, rootClient client.Client, scheme *k8sruntime.Scheme,
	index int, plan scaletarget.Plan, providers []capiexports.Provider, discovery capiexports.Discovery,
) (*tenant, error) {
	name := fmt.Sprintf("scale-%04d", index)
	logical, err := kcpfixtures.EnsureWorkspace(ctx, rootClient, name, 2*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("creating workspace %s: %w", name, err)
	}

	cl, err := client.New(deployedscale.WorkspaceConfig(base, logical), client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("client for workspace %s: %w", name, err)
	}

	tn := &tenant{name: logical, client: cl}
	for n := range plan.Shape.ClustersPerWorkspace {
		// Unique across the run: the in-memory backend keys its
		// workload-cluster listeners by namespace and name in a process-global
		// map, and every workspace here uses one namespace.
		tn.clusters = append(tn.clusters, fmt.Sprintf("t%04d-c%03d", index, n))
	}

	// Every export, with each provider's own computed claims — not the
	// single-export fixture's pair of core-type claims. A provider reads and
	// writes across exports (core's Clusters, the bootstrap provider's
	// Secrets), and a binding that granted only what a one-export fixture
	// needs would leave those reads refused: the manager would engage the
	// workspace and then reconcile nothing, which is a run that measures an
	// idle fleet and calls it an active one.
	for _, provider := range providers {
		if err := kcpfixtures.BindExport(ctx, cl, kcpfixtures.BindExportOptions{
			BindingName:      provider.Export,
			ExportPath:       deployedscale.RootWorkspace,
			ExportName:       provider.Export,
			PermissionClaims: provider.Claims(discovery.Identities(), discovery),
			ReadyTimeout:     2 * time.Minute,
		}); err != nil {
			return nil, fmt.Errorf("binding %s in %s: %w", provider.Export, name, err)
		}
	}

	for _, obj := range demo.Blueprint(demo.BackendInMemory) {
		if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating %T %s in %s: %w", obj, obj.GetName(), name, err)
		}
	}
	for _, cluster := range tn.clusters {
		obj := demo.NewCluster(cluster, plan.Machines.ControlPlane, plan.Machines.Workers, demo.DefaultKubernetesVersion)
		if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating Cluster %s in %s: %w", cluster, name, err)
		}
	}
	return tn, nil
}

// awaitEndState waits for every workspace to reach the state this run can
// reach.
func awaitEndState(ctx context.Context, state string, tenants []*tenant, plan scaletarget.Plan,
	timeout, poll time.Duration, health func() string,
) error {
	if state == deployedscale.EndStateEngaged {
		return awaitEngaged(ctx, tenants, plan, timeout, poll, health)
	}
	return awaitReady(ctx, tenants, plan, timeout, poll, health)
}

// serverDown turns a health report into the error a wait should stop on.
//
// Checked only when workspaces are short, and then before the deadline: a run
// where nothing is advancing has two possible causes and they want opposite
// responses. A slow fleet is worth waiting out. A dead server is worth twenty
// minutes of nobody's time.
func serverDown(health func() string) error {
	if health == nil {
		return nil
	}
	if reason := health(); reason != "" {
		return fmt.Errorf("kcp stopped serving, so no workspace could advance and this run measured "+
			"nothing about the fleet: %s", reason)
	}
	return nil
}

// awaitEngaged waits for every workspace to hold the objects it was given.
//
// Reading them back through each workspace's own client is not a formality: it
// is what shows the binding took and the objects are being served, which is
// the whole of what a single deployment can be measured against.
func awaitEngaged(ctx context.Context, tenants []*tenant, plan scaletarget.Plan,
	timeout, poll time.Duration, health func() string,
) error {
	if timeout <= 0 {
		return fmt.Errorf("no time left in the budget")
	}
	deadline := time.Now().Add(timeout)
	var last string

	for {
		short := 0
		for _, tn := range tenants {
			var clusters clusterv1.ClusterList
			if err := tn.client.List(ctx, &clusters, client.InNamespace(demo.Namespace)); err != nil {
				last = fmt.Sprintf("listing clusters in %s: %v", tn.name, err)
				short++
				continue
			}
			if len(clusters.Items) < plan.Shape.ClustersPerWorkspace {
				short++
				last = fmt.Sprintf("%s: %d of %d clusters", tn.name, len(clusters.Items), plan.Shape.ClustersPerWorkspace)
			}
		}
		if short == 0 {
			return nil
		}
		if err := serverDown(health); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %d of %d workspaces short (%s)", timeout, short, len(tenants), last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// awaitReady waits for every cluster to reach an initialized, Ready state.
func awaitReady(ctx context.Context, tenants []*tenant, plan scaletarget.Plan,
	timeout, poll time.Duration, health func() string,
) error {
	if timeout <= 0 {
		return fmt.Errorf("no time left in the budget")
	}
	want := plan.Shape.ClustersPerWorkspace * plan.Machines.PerCluster()
	deadline := time.Now().Add(timeout)
	var last string

	for {
		short := 0
		for _, tn := range tenants {
			var controlPlanes controlplanev1.KubeadmControlPlaneList
			if err := tn.client.List(ctx, &controlPlanes, client.InNamespace(demo.Namespace)); err != nil {
				last = fmt.Sprintf("listing control planes in %s: %v", tn.name, err)
				short++
				continue
			}
			var machines clusterv1.MachineList
			if err := tn.client.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
				last = fmt.Sprintf("listing machines in %s: %v", tn.name, err)
				short++
				continue
			}

			ready := 0
			for i := range machines.Items {
				if demo.SummariseMachine("", "", &machines.Items[i], nil).Ready {
					ready++
				}
			}
			done := len(controlPlanes.Items) >= plan.Shape.ClustersPerWorkspace && ready >= want
			for i := range controlPlanes.Items {
				if !demo.SummariseControlPlane("", "", &controlPlanes.Items[i]).Ready {
					done = false
				}
			}
			if !done {
				short++
				last = fmt.Sprintf("%s: %d of %d machines Ready", tn.name, ready, want)
			}
		}

		if short == 0 {
			return nil
		}
		if err := serverDown(health); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %d of %d workspaces short (%s)", timeout, short, len(tenants), last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

func componentNamesOf(components []deployedscale.Component) []string {
	out := make([]string, 0, len(components))
	for _, c := range components {
		out = append(out, c.Name)
	}
	return out
}

func plansFromFlags(t *testing.T) []scaletarget.Plan {
	t.Helper()
	percents, err := scaletarget.ParsePercents(*checkpoints)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}

	// Empty is not an error: the fleet derives its own spreads.
	var spreads []int
	if strings.TrimSpace(*clustersPerWorkspace) != "" {
		spreads, err = scaletarget.ParseCounts(*clustersPerWorkspace)
		if err != nil {
			t.Fatalf("could not run: clusters per workspace: %v", err)
		}
	}

	plans, err := scaletarget.Fleet{
		Clusters:             *clusters,
		NodesPerCluster:      *nodesPerCluster,
		ControlPlaneNodes:    *controlPlaneNodes,
		ClustersPerWorkspace: spreads,
	}.Plans(percents)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}
	return plans
}

func optionsFromFlags(t *testing.T) (deployedscale.Options, error) {
	t.Helper()

	if *kcpImage == "" || *managerImage == "" {
		t.Fatalf("could not run: -deployed-kcp-image and -deployed-manager-image are required; a default would " +
			"silently measure a build nobody chose")
	}

	names := strings.Split(*componentNames, ",")
	if strings.TrimSpace(*componentNames) == "all" {
		names = nil
		for _, c := range deployedscale.Components() {
			names = append(names, c.Name)
		}
	}
	components, err := deployedscale.ComponentsNamed(names...)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}

	images := map[string]string{}
	for _, c := range components {
		images[c.Name] = fmt.Sprintf("%s/%s:%s", strings.TrimSuffix(*managerImage, "/"), c.Name, *imageTag)
	}

	opts := deployedscale.Options{
		Namespace:         *namespace,
		KcpImage:          *kcpImage,
		Images:            images,
		Components:        components,
		SpreadAcrossNodes: *spreadAcrossNodes,
		ImagePullPolicy:   corev1.PullPolicy(*imagePullPolicy),
	}
	// A memory limit is the knob that turns "it was OOM killed" into a
	// capacity curve. Without it the limit is a constant and the only finding
	// available is the one fleet size that happens to exceed it.
	if *kcpMemoryLimit != "" {
		q, err := resource.ParseQuantity(*kcpMemoryLimit)
		if err != nil {
			return deployedscale.Options{}, fmt.Errorf("parsing -deployed-kcp-memory-limit %q: %w", *kcpMemoryLimit, err)
		}
		opts.KcpResources = deployedscale.DefaultKcpResources()
		opts.KcpResources.Limits[corev1.ResourceMemory] = q
	}
	if *managerMemoryLimit != "" {
		q, err := resource.ParseQuantity(*managerMemoryLimit)
		if err != nil {
			return deployedscale.Options{}, fmt.Errorf("parsing -deployed-manager-memory-limit %q: %w",
				*managerMemoryLimit, err)
		}
		opts.ManagerResources = deployedscale.DefaultManagerResources()
		opts.ManagerResources.Limits[corev1.ResourceMemory] = q
	}
	return opts, nil
}

// restConfigFor addresses kcp with the credentials this run minted: the token
// kcp's own token file carries, and trust of the CA that signed its serving
// certificate.
func restConfigFor(creds *deployedscale.Credentials, server string) *rest.Config {
	return &rest.Config{
		Host:            server,
		BearerToken:     creds.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: creds.CACertPEM},
	}
}

func deployedScheme(t *testing.T) *k8sruntime.Scheme {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	for _, add := range []func(*k8sruntime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
		apisv1alpha2.AddToScheme,
		tenancyv1alpha1.AddToScheme,
		corev1alpha1.AddToScheme,
		clusterv1.AddToScheme,
		bootstrapv1.AddToScheme,
		controlplanev1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building the scheme: %v", err)
		}
	}
	return scheme
}

func reportDir(t *testing.T) string {
	t.Helper()
	if dir, ok := os.LookupEnv("SCALE_REPORT_DIR"); ok && dir != "" {
		return dir
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "bin"))
	if err != nil {
		t.Logf("resolving the report directory: %v", err)
		return "bin"
	}
	return dir
}

// findRepoRoot walks up from the working directory to the module root, so a
// path written relative to the repository resolves from a test's own directory.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not find the repository root: no go.mod in any parent")
		}
		dir = parent
	}
}

// formatFloat renders a per-workspace figure the way the report does, so a log
// line and the report do not disagree about the same number.
func formatFloat(v float64) string {
	return fmt.Sprintf("%.1f goroutines per workspace", v)
}

// settleKcpHeap asks the shard to collect before it is sampled, so that the
// live heap in the sample means the same thing at every checkpoint and in every
// run, and reports what that cost.
//
// Three runs at one, five and ten nodes per cluster each fitted their own heap
// samples cleanly and then disagreed with each other: the five-node run priced
// a cluster at 35.3 MB and the ten-node run, with twice the Machines, at 13.6.
// What differed was where the collector happened to be — 73% of heapSys live at
// one sample against 52% at the other — and no amount of care about the fits
// could recover from it. Retaken this way, that five-node figure is 15.5 MB.
// See deployedscale.CollectGarbage.
//
// # Why the cost is reported rather than absorbed
//
// A forced collection is CPU the shard would not otherwise have spent, and the
// run charges it to whatever checkpoint it happens at. The five-node run's CPU
// per cluster went from 20.2 to 22.2 seconds across the change, which is the
// same order as the per-Machine figure being drawn from it. So the seconds are
// measured — a scrape either side of the collection — and recorded, rather than
// left to inflate a cost per Machine that a reader has no way to correct.
//
// Best effort, and said out loud either way: a run whose heap figures are
// post-collection and a run whose are not should not be compared, so the report
// carries which it is.
func settleKcpHeap(t *testing.T, ctx context.Context, profiling, shard *rest.Config,
	report *deployedscale.Report, forced *float64,
) {
	t.Helper()
	before, beforeErr := deployedscale.ScrapeKcp(ctx, shard)
	if err := deployedscale.CollectGarbage(ctx, profiling); err != nil {
		if _, already := report.Facts["kcpHeapSample"]; !already {
			report.AddFact("kcpHeapSample", "as scraped, with no collection forced first: "+err.Error()+
				" — heap figures include whatever had not been collected and are not comparable with "+
				"other runs")
			t.Logf("NOTE: the shard would not collect on request (%v), so this run's heap figures "+
				"carry the collector's timing", err)
		}
		return
	}
	report.AddFact("kcpHeapSample", "after a forced collection (/debug/pprof/heap?gc=1), so live heap "+
		"is the retained set — and the shard's resident figure in the same sample is taken immediately "+
		"after that collection")
	if beforeErr != nil {
		return
	}
	if after, err := deployedscale.ScrapeKcp(ctx, shard); err == nil && after.CPUSeconds > before.CPUSeconds {
		*forced += after.CPUSeconds - before.CPUSeconds
		report.AddFact("kcpForcedCollectionCPUSeconds", fmt.Sprintf("%.1f", *forced))
	}
}

// captureKcpHeap writes the shard's heap profile beside the report and
// summarises it, best effort throughout.
//
// Nothing here is allowed to fail a run. A profile is a diagnostic for a
// question this measurement raised rather than part of the measurement, and a
// fleet that was successfully measured must not be thrown away because pprof
// was unavailable.
func captureKcpHeap(t *testing.T, ctx context.Context, kcpCfg *rest.Config,
	report *deployedscale.Report, checkpoint int,
) {
	t.Helper()

	raw, err := deployedscale.FetchProfile(ctx, kcpCfg, "heap")
	if err != nil {
		report.AddFact("kcpHeapProfile", "not captured: "+err.Error())
		return
	}
	dir := reportDir(t)
	path := filepath.Join(dir, fmt.Sprintf("kcp-heap-%s.pb.gz", report.Facts["spread"]))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		report.AddFact("kcpHeapProfile", "not written: "+err.Error())
		return
	}
	report.AddFact("kcpHeapProfile", fmt.Sprintf("%s (at %d workspaces) — open with `go tool pprof %s`",
		path, checkpoint, path))

	if top, err := deployedscale.TopAllocations(ctx, path, 15); err == nil {
		t.Logf("kcp heap at %d workspaces, by retained bytes:\n%s", checkpoint, top)
		report.AddFact("kcpHeapTop", firstLines(top, 18))
	}
}

// firstLines keeps a fact readable: a pprof top table is wide and a report is
// a table, so the fact carries the head of it and the file carries the rest.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " / ")
}
