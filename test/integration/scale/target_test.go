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

package scale_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcptesting "github.com/kcp-dev/sdk/testing"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/fleetfixture"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaletarget"
	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"

	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

var (
	targetClusters = flag.Int("clusters", 2,
		"How many clusters the fleet holds in total.")
	targetNodesPerCluster = flag.Int("nodes-per-cluster", 1,
		"How many nodes each cluster reaches, control plane included. Fifty nodes means fifty machines, not "+
			"fifty on top of a control plane.")
	targetControlPlaneNodes = flag.Int("control-plane-nodes", 1,
		"How many of each cluster's nodes are control plane machines. Part of the target rather than a detail "+
			"of it: on the in-memory backend a control plane machine costs a fake etcd member and API server "+
			"pod as well as a Node, so two runs at one node count and a different split are not one measurement.")
	targetClustersPerWorkspace = flag.String("clusters-per-workspace", "",
		"How the clusters are spread over workspaces, comma separated for more than one spread. Each spread is "+
			"a sub-test with its own kcp server and its own report. One fleet at two spreads is the comparison "+
			"that separates what a workspace costs from what a cluster costs. Empty derives it from the cluster "+
			"count, so tuning the fleet does not break a knob nobody touched.")
	targetCheckpoints = flag.String("target-checkpoints", "25,50",
		"Percentages of the workspace target at which to stop and take a sample. The target itself is always the "+
			"last one. Samples on the way up are what turn a run into a curve rather than one number.")
	targetConcurrency = flag.Int("target-concurrency", 8,
		"How many workspaces are bound and populated at once. Serial provisioning is the sweep's shape and would "+
			"put a fleet-sized run into the hours; this is the knob that decides how long the run takes rather "+
			"than what it measures.")
	targetBudget = flag.Duration("target-budget", 60*time.Minute,
		"Wall-clock budget per shape. Reaching it stops the run and reports the workspace count achieved, which "+
			"is a result rather than a failure.")
	targetCheckpointTimeout = flag.Duration("target-checkpoint-timeout", 20*time.Minute,
		"How long one checkpoint may take to reach its end state before the run stops and reports what it got to.")
	targetPollInterval = flag.Duration("target-poll-interval", 5*time.Second,
		"How often the fleet's end state is polled. A fleet-sized run lists every workspace on every poll, so "+
			"this is slower than the sweep's quarter-second on purpose.")
)

const (
	targetExportName  = "cluster-api-scale-target"
	targetBindingName = "cluster-api-scale-target"

	// How long the goroutine count must hold still before a sample is taken,
	// and how long to wait for that. Both are the sweep's, for the reason the
	// sweep gives: engaging a workspace keeps starting things well after the
	// wiring reports it done, and a sample taken the instant a checkpoint
	// completes attributes the tail of one checkpoint's cost to the next.
	//
	// The timeout is longer than the sweep's because a checkpoint here engages
	// many workspaces rather than one, so there is proportionally more in
	// flight to quieten down.
	targetSettleQuiet   = 2 * time.Second
	targetSettleTimeout = 10 * time.Minute
)

// TestFleetTarget drives every provider's controllers at a fleet of a stated
// size and reports what it cost to host.
//
// # Why this is not a sweep
//
// test/integration/sweep asks what one more workspace costs, and answers it by
// walking a small fleet up one workspace at a time. That is the right shape for
// a coefficient and the wrong shape for a target: at three workspaces it says
// nothing about whether two hundred can be hosted at all, and walking to two
// hundred one settled sample at a time would spend the whole run settling.
//
// This asks the other question — can this environment host the fleet somebody
// has in mind, and what did it cost — by provisioning concurrently and sampling
// at checkpoints. The two instruments overlap deliberately at their ends: the
// per-workspace slope this reports across its checkpoints should agree with the
// sweep's, and a disagreement is a finding about one of them.
//
// # Why it is worth running
//
// `specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md` quotes
// candidate capacities up to 2,020 workspaces from a linear model fitted to
// runs of 8 to 64, and names the gap itself: "64 workspaces measured; up to
// 2,020 quoted… A confirming run at 256 would cut the largest factor by four."
// This is the instrument that takes that run.
//
// # It measures, it does not gate
//
// Like the ceiling measurement beside it, this fails only when it cannot
// measure. A fleet this environment cannot host is reported as the count it
// did reach, with a note that anything above it is an extrapolation; reaching
// nothing at all is "could not run", which per AGENTS.md is never a pass. That
// is not a weakened assertion — there is no assertion here to weaken, because
// no requirement states a budget for what a fleet of a given size may cost, and
// inventing one in a test would make it fail for a reason nobody agreed on.
func TestFleetTarget(t *testing.T) {
	percents, err := scaletarget.ParsePercents(*targetCheckpoints)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}
	// Empty is not an error: the fleet derives its own spreads. See
	// scaletarget.DefaultSpreads.
	var spreads []int
	if strings.TrimSpace(*targetClustersPerWorkspace) != "" {
		spreads, err = scaletarget.ParseCounts(*targetClustersPerWorkspace)
		if err != nil {
			t.Fatalf("could not run: clusters per workspace: %v", err)
		}
	}

	plans, err := scaletarget.Fleet{
		Clusters:             *targetClusters,
		NodesPerCluster:      *targetNodesPerCluster,
		ControlPlaneNodes:    *targetControlPlaneNodes,
		ClustersPerWorkspace: spreads,
	}.Plans(percents)
	if err != nil {
		t.Fatalf("could not run: %v", err)
	}

	for i, plan := range plans {
		t.Run(plan.Shape.String(), func(t *testing.T) {
			runTarget(t, plan, i)
		})
	}
}

// targetTenant is one workspace of a target run, and what was created in it.
type targetTenant struct {
	name         multicluster.ClusterName
	directClient client.Client
	clusters     []string
}

func runTarget(t *testing.T, plan scaletarget.Plan, shapeIndex int) {
	ctrl.SetLogger(testr.NewWithOptions(t, testr.Options{LogTimestamp: false, Verbosity: 0}))
	ctx := t.Context()

	report := &sweep.Report{Title: fmt.Sprintf("Fleet target: %d clusters over %d workspaces, %d nodes each",
		plan.Shape.Clusters(), plan.Shape.Workspaces, plan.Machines.PerCluster())}
	reportName := "scale-target-" + plan.Shape.String()

	report.AddFact("shape", "every provider's controllers on one fleet: core, bootstrap, control plane, dev infrastructure")
	report.AddFact("deployment", "none — four deployments co-located, so one engagement per workspace rather than four")
	report.AddFact("devClusterBackend", "inMemory")
	report.AddFact("clusterShape", "ClusterClass based: one class per workspace, each Cluster naming it")
	report.AddFact("endState", "every control plane ready and every Machine Ready")
	report.AddFact("spread", plan.Shape.String())
	report.AddFact("targetWorkspaces", fmt.Sprint(plan.Shape.Workspaces))
	report.AddFact("targetClusters", fmt.Sprint(plan.Shape.Clusters()))
	report.AddFact("clustersPerWorkspace", fmt.Sprint(plan.Shape.ClustersPerWorkspace))
	report.AddFact("controlPlaneMachinesPerCluster", fmt.Sprint(plan.Machines.ControlPlane))
	report.AddFact("workerMachinesPerCluster", fmt.Sprint(plan.Machines.Workers))
	report.AddFact("nodesPerCluster", fmt.Sprint(plan.Machines.PerCluster()))
	report.AddFact("targetNodes", fmt.Sprint(plan.Nodes()))
	report.AddFact("provisioningConcurrency", fmt.Sprint(*targetConcurrency))
	report.AddFact("goMaxProcs", fmt.Sprint(runtime.GOMAXPROCS(0)))
	report.AddFact("goVersion", runtime.Version())
	if shapeIndex > 0 {
		// Shapes run as sub-tests of one binary, so this one's baseline is
		// taken in a process that has already served an earlier shape: its
		// manager is stopped but what it left behind is not zero. The slopes
		// are unaffected — they are fitted across this shape's own active
		// samples — but the baseline row is not comparable with the first
		// shape's, and saying so here is cheaper than a reader working out why
		// two runs of "the same" fleet start from different numbers.
		report.AddFact("baselineIsShared",
			fmt.Sprintf("yes — shape %d in this binary, so the baseline carries what earlier shapes left behind", shapeIndex+1))
	}

	t.Cleanup(func() {
		t.Logf("\n%s", report.Markdown())
		dir := targetReportDir(t)
		if err := report.Write(dir, reportName); err != nil {
			t.Errorf("writing the target report: %v", err)
			return
		}
		t.Logf("target report written to %s", filepath.Join(dir, reportName+".md"))
	})

	scheme := targetScheme(t)

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("could not run: root client: %v", err)
	}

	crdPaths, err := fleetfixture.CRDPaths()
	if err != nil {
		t.Fatalf("could not run: resolving CRD manifests: %v", err)
	}
	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:       targetExportName,
		SchemaPrefix:     "v1",
		CRDPaths:         crdPaths,
		CRDTransform:     kcpfixtures.KeepStorageVersion,
		PermissionClaims: demo.PermissionClaims,
	}); err != nil {
		t.Fatalf("could not run: publishing the APIExport: %v", err)
	}

	// Every workspace up front, and serially. Creating one is kcp's cost
	// rather than the manager's, so paying it during a checkpoint would show
	// up as the manager's — and the kcp fixture registers a cleanup per
	// workspace, which is not a thing to drive from several goroutines.
	tenants := make([]*targetTenant, 0, plan.Shape.Workspaces)
	for i := range plan.Shape.Workspaces {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		wsCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
		directClient, err := client.New(wsCfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatalf("could not run: client for workspace %d: %v", i, err)
		}
		clusters := make([]string, 0, plan.Shape.ClustersPerWorkspace)
		for n := range plan.Shape.ClustersPerWorkspace {
			// Unique across the whole run, not just within the workspace. The
			// dev provider's in-memory backend keys its workload-cluster
			// listeners by namespace and name in a process-global map, and
			// every workspace here uses the same namespace — so two clusters
			// sharing a name in two workspaces would collide in it. See
			// coremanager.DevInfrastructure.
			clusters = append(clusters, fmt.Sprintf("t%04d-c%03d", i, n))
		}
		tenants = append(tenants, &targetTenant{name: multicluster.ClusterName(ws.Spec.Cluster), directClient: directClient, clusters: clusters})
	}

	// --- One manager, all four providers, instrumented.
	counter := sweep.NewCounter()
	countedCfg := counter.WrapConfig(rootCfg)

	if err := coremanager.SetFeatureGateDefaults(); err != nil {
		t.Fatalf("could not run: setting feature gate defaults: %v", err)
	}

	wildcardRegistry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(countedCfg, targetExportName, scheme, wildcardRegistry,
		providerwiring.WithCacheIndexes(ctx, coremanager.FleetCacheIndexes()...))
	if err != nil {
		t.Fatalf("could not run: building the provider: %v", err)
	}

	// The manager's local cluster is the APIExport's virtual workspace, whose
	// discovery describes the API surface every engaged workspace shares: a
	// fleet-wide controller resolves types through the local RESTMapper at
	// setup time, and the exporting workspace does not bind what it exports.
	vw := rest.CopyConfig(baseCfg)
	vw.Host = strings.TrimSuffix(baseCfg.Host, "/") + "/services/apiexport/root/" + targetExportName + "/clusters/*"

	mgr, err := mcmanager.New(counter.WrapConfig(vw), provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("could not run: building the manager: %v", err)
	}

	wiring, err := providerwiring.AddToManager(mgr,
		func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil },
		providerwiring.Options{Log: ctrl.Log.WithName("providerwiring")})
	if err != nil {
		t.Fatalf("could not run: adding the wiring: %v", err)
	}

	ports, err := fleetfixture.MuxPorts(plan.Shape.Clusters())
	if err != nil {
		t.Fatalf("could not run: reserving in-memory listener ports: %v", err)
	}
	if err := fleetfixture.SetupFleet(ctx, mgr, wildcardRegistry, fleetfixture.FleetOptions{
		ShardConfig: baseCfg,
		Ports:       ports,
		// Two shapes in one test binary build two ClusterCaches, and
		// controller-runtime's controller-name registry is process-global.
		SkipControllerNameValidation: true,
	}); err != nil {
		t.Fatalf("could not run: wiring the fleet: %v", err)
	}

	// The baseline is taken before the manager starts, for the reason the
	// sweep gives: the first workspace has to bind before kcp populates the
	// APIExportEndpointSlice the provider needs, so a running manager serving
	// no workspaces is not a state this run can stand in.
	targetSettle(t, "before the manager started")
	report.Add(sweep.Take(sweep.PhaseBaseline, "baseline (manager not started)", 0, counter))

	mgrCtx, stopManager := context.WithCancel(ctx)
	t.Cleanup(stopManager)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()

	// --- Up to each checkpoint in turn.
	deadline := time.Now().Add(*targetBudget)
	var (
		reached   int
		stoppedBy string
		readyAt   []time.Duration
	)

	for _, checkpoint := range plan.Checkpoints {
		if time.Now().After(deadline) {
			stoppedBy = fmt.Sprintf("wall-clock budget %s", *targetBudget)
			break
		}

		wave := tenants[reached:checkpoint]
		waveStart := time.Now()

		if err := provisionWave(ctx, t, wave, plan, reached == 0, rootClient); err != nil {
			stoppedBy = fmt.Sprintf("provisioning workspaces %d-%d: %v", reached+1, checkpoint, err)
			break
		}

		if err := awaitEngaged(ctx, wiring, checkpoint, timeLeft(deadline, *targetCheckpointTimeout), *targetPollInterval); err != nil {
			stoppedBy = fmt.Sprintf("waiting for %d workspaces to engage: %v", checkpoint, err)
			break
		}

		if err := awaitReady(ctx, tenants[:checkpoint], plan, timeLeft(deadline, *targetCheckpointTimeout), *targetPollInterval); err != nil {
			diagnoseTarget(t, ctx, tenants[:checkpoint], plan)
			stoppedBy = fmt.Sprintf("waiting for %d workspaces to reach the end state: %v", checkpoint, err)
			break
		}

		readyAt = append(readyAt, time.Since(waveStart))
		reached = checkpoint

		targetSettle(t, fmt.Sprintf("with %d workspaces at the end state", reached))
		report.Add(sweep.Take(sweep.PhaseActive, fmt.Sprintf("%d workspaces, %d clusters, %d nodes",
			reached, reached*plan.Shape.ClustersPerWorkspace, reached*plan.Shape.ClustersPerWorkspace*plan.Machines.PerCluster()),
			reached, counter))
	}

	if stoppedBy == "" {
		stoppedBy = fmt.Sprintf("reached the requested target of %d workspaces", plan.Shape.Workspaces)
	}

	// What is reported as reached is the last *settled checkpoint*, not the
	// most workspaces that were ever up: a figure is only a measurement at a
	// point where the process was sampled. A checkpoint that timed out having
	// got most of the way there is not lost — pollUntil carries its last
	// reading into stoppedBy, so the report says how far the failing
	// checkpoint got as well as where the last good sample was.
	verdict := scaletarget.Classify(reached, plan.Shape.Workspaces, stoppedBy)
	report.AddFact("outcome", verdict.Outcome.String())
	report.AddFact("reachedWorkspaces", fmt.Sprint(verdict.Reached))
	report.AddFact("reachedClusters", fmt.Sprint(verdict.Reached*plan.Shape.ClustersPerWorkspace))
	report.AddFact("reachedNodes", fmt.Sprint(verdict.Reached*plan.Shape.ClustersPerWorkspace*plan.Machines.PerCluster()))
	report.AddFact("stoppedBy", verdict.StoppedBy)
	if verdict.Note != "" {
		report.AddFact("note", verdict.Note)
	}

	// A run that hosted nothing measured nothing. It is the one outcome here
	// that is not a result, and it must never be read as a fleet of zero cost.
	if verdict.Outcome == verify.OutcomeCouldNotRun {
		t.Fatalf("could not run: %s (stopped by: %s)", verdict.Note, verdict.StoppedBy)
	}

	active := sweep.InPhase(report.Samples, sweep.PhaseActive)
	addSlope(report, "goroutines", active, sweep.Goroutines, plan.Shape.ClustersPerWorkspace, "%.1f", "%.2f")
	addSlope(report, "heapBytes", active, sweep.Heap, plan.Shape.ClustersPerWorkspace, "%.0f", "%.0f")

	if len(readyAt) > 0 {
		report.AddFact("checkpointWallClock", strings.Join(durations(readyAt), ", "))
	}

	t.Logf("fleet target: %s", plan.Shape.String())
	t.Logf("  outcome:          %s", verdict.Outcome)
	t.Logf("  workspaces:       %d of %d", verdict.Reached, plan.Shape.Workspaces)
	t.Logf("  clusters:         %d of %d", verdict.Reached*plan.Shape.ClustersPerWorkspace, plan.Shape.Clusters())
	t.Logf("  nodes:            %d of %d", verdict.Reached*plan.Shape.ClustersPerWorkspace*plan.Machines.PerCluster(), plan.Nodes())
	t.Logf("  stopped by:       %s", verdict.StoppedBy)
	if verdict.Note != "" {
		t.Logf("NOTE: %s.", verdict.Note)
	}
}

// provisionWave binds and populates a batch of workspaces at once.
//
// Concurrent because serial provisioning is what makes a fleet-sized run take
// hours: a workspace's bind and its cluster's convergence are almost entirely
// waiting, and waiting for them one at a time measures the harness rather than
// the manager. Nothing here touches *testing.T from a goroutine — errors come
// back through the channel and are reported by the caller.
func provisionWave(ctx context.Context, t *testing.T, wave []*targetTenant, plan scaletarget.Plan, first bool, rootClient client.Client) error {
	t.Helper()

	// The endpoint slice is populated only once something has bound, so the
	// first binding is made alone and waited on: the provider cannot discover
	// a virtual workspace URL that does not exist yet, and a wave that bound
	// two hundred workspaces into that gap would race the provider's discovery
	// rather than measure it.
	if first {
		if err := bindAndPopulate(ctx, wave[0], plan); err != nil {
			return fmt.Errorf("the first workspace: %w", err)
		}
		if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, targetExportName, 60*time.Second); err != nil {
			return fmt.Errorf("waiting for the APIExportEndpointSlice: %w", err)
		}
		wave = wave[1:]
	}

	concurrency := max(*targetConcurrency, 1)
	sem := make(chan struct{}, concurrency)
	errs := make(chan error, len(wave))
	var wg sync.WaitGroup

	for _, tn := range wave {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := bindAndPopulate(ctx, tn, plan); err != nil {
				errs <- fmt.Errorf("workspace %s: %w", tn.name, err)
			}
		}()
	}
	wg.Wait()
	close(errs)

	// The first failure is enough: the rest of a wave that failed to provision
	// tends to fail the same way, and a run reports what stopped it rather than
	// every consequence of it.
	if err, ok := <-errs; ok {
		return err
	}
	return nil
}

// bindAndPopulate binds one workspace to the export and writes the blueprint
// and clusters it holds.
func bindAndPopulate(ctx context.Context, tn *targetTenant, plan scaletarget.Plan) error {
	if err := kcpfixtures.BindExport(ctx, tn.directClient, kcpfixtures.BindExportOptions{
		BindingName:      targetBindingName,
		ExportPath:       "root",
		ExportName:       targetExportName,
		PermissionClaims: demo.PermissionClaims,
	}); err != nil {
		return fmt.Errorf("binding: %w", err)
	}

	// The demo's own class and the demo's own Cluster, so what this measures
	// is what an installation pays for a ClusterClass based cluster rather
	// than for a shape nobody deploys.
	for _, obj := range demo.Blueprint(demo.BackendInMemory) {
		if err := createIgnoringExisting(ctx, tn.directClient, obj); err != nil {
			return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
		}
	}
	for _, name := range tn.clusters {
		cluster := demo.NewCluster(name, plan.Machines.ControlPlane, plan.Machines.Workers, demo.DefaultKubernetesVersion)
		if err := createIgnoringExisting(ctx, tn.directClient, cluster); err != nil {
			return fmt.Errorf("creating Cluster %s: %w", name, err)
		}
	}
	return nil
}

func createIgnoringExisting(ctx context.Context, cl client.Client, obj client.Object) error {
	if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// awaitEngaged waits for the manager to have engaged the expected count.
func awaitEngaged(ctx context.Context, wiring *providerwiring.Wiring, want int, timeout, poll time.Duration) error {
	return pollUntil(ctx, timeout, poll, func() (bool, string) {
		engaged := len(wiring.Engaged())
		return engaged >= want, fmt.Sprintf("%d of %d workspaces engaged", engaged, want)
	})
}

// awaitReady waits for every cluster in every given workspace to reach the end
// state: a control plane with all its replicas ready, and every Machine Ready.
//
// The end state is the demo's own done-condition rather than a weaker one.
// Stopping at "control plane initialized", as the fleet sweep does, would be a
// run whose worker machines were still being created when it was measured —
// which for a node count is measuring the wrong moment.
func awaitReady(ctx context.Context, tenants []*targetTenant, plan scaletarget.Plan, timeout, poll time.Duration) error {
	wantMachines := plan.Shape.ClustersPerWorkspace * plan.Machines.PerCluster()

	return pollUntil(ctx, timeout, poll, func() (bool, string) {
		var notReady, machines int
		for _, tn := range tenants {
			ready, found, err := tenantReady(ctx, tn, plan, wantMachines)
			if err != nil {
				return false, fmt.Sprintf("reading workspace %s: %v", tn.name, err)
			}
			machines += found
			if !ready {
				notReady++
			}
		}
		return notReady == 0, fmt.Sprintf("%d of %d workspaces short of the end state, %d of %d machines Ready",
			notReady, len(tenants), machines, len(tenants)*wantMachines)
	})
}

// tenantReady reports whether one workspace has reached the end state, and how
// many of its machines are Ready.
func tenantReady(ctx context.Context, tn *targetTenant, plan scaletarget.Plan, wantMachines int) (bool, int, error) {
	var controlPlanes controlplanev1.KubeadmControlPlaneList
	if err := tn.directClient.List(ctx, &controlPlanes, client.InNamespace(demo.Namespace)); err != nil {
		return false, 0, fmt.Errorf("listing control planes: %w", err)
	}
	var machines clusterv1.MachineList
	if err := tn.directClient.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
		return false, 0, fmt.Errorf("listing machines: %w", err)
	}

	readyMachines := 0
	for i := range machines.Items {
		if demo.SummariseMachine("", "", &machines.Items[i], nil).Ready {
			readyMachines++
		}
	}

	if len(controlPlanes.Items) < plan.Shape.ClustersPerWorkspace {
		return false, readyMachines, nil
	}
	for i := range controlPlanes.Items {
		if !demo.SummariseControlPlane("", "", &controlPlanes.Items[i]).Ready {
			return false, readyMachines, nil
		}
	}
	// The count is checked as well as the readiness, for the reason the demo's
	// own Result.Ready checks it: without it a workspace whose Machines had
	// not been created yet would satisfy "every machine is Ready" vacuously.
	return len(machines.Items) >= wantMachines && readyMachines >= wantMachines, readyMachines, nil
}

// diagnoseTarget logs what the fleet was doing when it stopped progressing.
//
// The kcp fixture is torn down with the test, so a timeout that says only
// "timed out" throws away the one moment the answer was still reachable. Only
// the workspaces that are short are logged, and only a few of them: a fleet of
// two hundred produces a log nobody reads to the end.
func diagnoseTarget(t *testing.T, ctx context.Context, tenants []*targetTenant, plan scaletarget.Plan) {
	t.Helper()

	const logged = 5
	wantMachines := plan.Shape.ClustersPerWorkspace * plan.Machines.PerCluster()

	shown := 0
	for _, tn := range tenants {
		if shown >= logged {
			t.Logf("diagnose: further workspaces not listed")
			return
		}
		ready, machines, err := tenantReady(ctx, tn, plan, wantMachines)
		if err != nil {
			t.Logf("diagnose: workspace %s: %v", tn.name, err)
			shown++
			continue
		}
		if ready {
			continue
		}
		shown++

		var controlPlanes controlplanev1.KubeadmControlPlaneList
		if err := tn.directClient.List(ctx, &controlPlanes, client.InNamespace(demo.Namespace)); err != nil {
			t.Logf("diagnose: workspace %s: %d of %d machines Ready; listing control planes: %v",
				tn.name, machines, wantMachines, err)
			continue
		}
		details := make([]string, 0, len(controlPlanes.Items))
		for i := range controlPlanes.Items {
			s := demo.SummariseControlPlane("", "", &controlPlanes.Items[i])
			details = append(details, fmt.Sprintf("%s %d/%d %s", s.ControlPlane, s.ReadyReplicas, s.DesiredReplicas, s.Detail))
		}
		t.Logf("diagnose: workspace %s: %d of %d machines Ready; control planes: %s",
			tn.name, machines, wantMachines, strings.Join(details, "; "))
	}
}

// pollUntil polls until the condition holds or the timeout expires, reporting
// the last thing the condition said about why it did not.
func pollUntil(ctx context.Context, timeout, poll time.Duration, condition func() (bool, string)) error {
	if timeout <= 0 {
		return fmt.Errorf("no time left in the budget")
	}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		ok, detail := condition()
		if ok {
			return nil
		}
		last = detail
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s: %s", timeout, last)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s", ctx.Err(), last)
		case <-time.After(poll):
		}
	}
}

// timeLeft is the smaller of a step's own timeout and what the run's budget
// has left, so a step cannot overrun a budget the run has already spent.
func timeLeft(deadline time.Time, step time.Duration) time.Duration {
	return min(step, time.Until(deadline))
}

func targetSettle(t *testing.T, what string) {
	t.Helper()
	if !sweep.Settle(targetSettleQuiet, targetSettleTimeout) {
		t.Logf("NOTE: the goroutine count never settled %s after %s; the sample that follows includes work in flight",
			what, targetSettleTimeout)
	}
}

// addSlope records a per-workspace cost and the per-cluster figure derived
// from it, or says why neither is a measurement.
//
// The derived figure is a division rather than a fit, and is labelled as such.
// The checkpoints vary the workspace count, not the clusters inside a
// workspace, so what this reports is what one workspace's clusters cost
// between them — not a per-cluster slope measured in its own right. Comparing
// the two spreads of one fleet is what separates the per-workspace term from
// the per-cluster one.
//
// A negative slope is refused rather than printed. Least squares over a
// handful of checkpoints will return one whenever the noise in a quantity
// exceeds its signal across the swept range, which is a live outcome for heap:
// the runtime returns memory to the OS lazily, so two settled samples can
// genuinely fall. A negative cost per workspace is not a cheaper fleet and
// must not be quotable as one, so it is reported as what it is — a fit that
// did not resolve — for the same reason AGENTS.md refuses to round an absent
// measurement to the nearest available number.
func addSlope(report *sweep.Report, name string, active []sweep.Sample, measure func(sweep.Sample) float64,
	clustersPerWorkspace int, perWorkspaceFormat, perClusterFormat string,
) {
	perWorkspaceKey := name + "PerWorkspace"
	perClusterKey := name + "PerClusterDerived"

	if len(active) < 2 {
		reason := fmt.Sprintf("not measured: %d checkpoint(s) reached, and one point is not a slope", len(active))
		report.AddFact(perWorkspaceKey, reason)
		report.AddFact(perClusterKey, reason)
		return
	}

	slope := sweep.PerWorkspace(active, measure)
	if slope < 0 {
		reason := fmt.Sprintf("not measured: the fit over %d checkpoints is negative (%.0f), "+
			"which means the noise exceeds the signal at this size rather than that a workspace is free",
			len(active), slope)
		report.AddFact(perWorkspaceKey, reason)
		report.AddFact(perClusterKey, reason)
		return
	}

	report.AddFact(perWorkspaceKey, fmt.Sprintf(perWorkspaceFormat, slope))
	report.AddFact(perClusterKey, fmt.Sprintf(perClusterFormat, slope/float64(clustersPerWorkspace)))
}

func targetScheme(t *testing.T) *k8sruntime.Scheme {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	for _, add := range []func(*k8sruntime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
		apisv1alpha2.AddToScheme,
		clusterv1.AddToScheme,
		bootstrapv1.AddToScheme,
		controlplanev1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("could not run: building the scheme: %v", err)
		}
	}
	return scheme
}

func targetReportDir(t *testing.T) string {
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

func durations(ds []time.Duration) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Round(time.Second).String())
	}
	return out
}
