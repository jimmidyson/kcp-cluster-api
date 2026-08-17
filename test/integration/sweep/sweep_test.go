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

// Package sweep_test measures what one core-manager process costs as the
// number of workspaces it actively reconciles grows, against a real kcp
// server.
//
// docs/conversion-plan.md's "Scalability" section is the reason this exists.
// It makes three claims about the design this project is built on — watches
// and startup LISTs are O(types) rather than O(types × workspaces), no cache
// or transport is duplicated per workspace, and per-workspace controller
// overhead is "cheap relative to a duplicated cache, but not free" — and
// Constitution Principle V requires a claim about a dependency's behaviour to
// be verified rather than assumed. The first two were argued from
// multicluster-provider's source; the third was left as a shrug. None of them
// had a number.
//
// The workspaces here are active, not merely bound: each one holds objects
// that a real controller reconciles through the workspace's own manager. A
// sweep over idle workspaces would measure the cheapest possible case and
// prove nothing about the one that matters.
package sweep_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	exportName  = "cluster-api-sweep"
	bindingName = "cluster-api-sweep"

	// defaultWorkspaces is how far the sweep goes when nothing says otherwise.
	// Four points are enough to tell a flat curve from a rising one, and keep
	// this inside the time an ordinary pull request run can afford. A
	// quantifying run sets SWEEP_WORKSPACES higher; see docs/site's
	// workspace resource usage page.
	defaultWorkspaces = 4

	// defaultObjects is how many Cluster objects each workspace holds. The
	// point is that reconcilers run and workqueues have something in them, not
	// to measure throughput.
	defaultObjects = 5

	// watchedTypes is how many types each workspace's controllers watch. One,
	// here: the sweep wires a single Cluster controller. Retained-goroutine
	// arithmetic is per watched type, so this is a factor rather than a
	// comment.
	watchedTypes = 1

	// retainedGoroutinesPerWatchedType is what a departing workspace does not
	// give back, per type it watched, measured rather than budgeted. See the
	// assertion that uses it for why it is not zero.
	retainedGoroutinesPerWatchedType = 2

	pollInterval = 250 * time.Millisecond
	pollTimeout  = 120 * time.Second

	// settleQuiet is how long the goroutine count must hold still before a
	// sample is taken. Engaging a workspace keeps starting things well after
	// the wiring reports it done — informers sync, a workqueue drains, HTTP/2
	// streams open — so a sample taken the instant Engage returns attributes
	// the tail of one workspace's cost to the next one.
	settleQuiet   = 2 * time.Second
	settleTimeout = 60 * time.Second
)

// coreCRDs is the smallest set that makes a workspace active: one type to
// publish, bind, create objects of, and reconcile.
var coreCRDs = []string{"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml"}

// keepOneVersion trims the published CRD to v1beta2 alone: a multi-version CRD
// needs a conversion webhook before kcp accepts it as an APIResourceSchema,
// and this sweep deliberately serves no webhooks — they are single-workspace
// by construction (FR-008) and so have nothing to say about a curve.
func keepOneVersion(crd *apiextensionsv1.CustomResourceDefinition) {
	if crd.Spec.Group != clusterv1.GroupVersion.Group {
		return
	}
	kept := crd.Spec.Versions[:0]
	for _, v := range crd.Spec.Versions {
		if v.Name == clusterv1.GroupVersion.Version {
			kept = append(kept, v)
		}
	}
	crd.Spec.Versions = kept
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return value
}

// reconcileLog records which objects each workspace's controller has
// reconciled. It is how the sweep knows a workspace is active rather than
// merely bound: until a workspace's own controller has reconciled the objects
// written into that workspace, the sample would be of a process that has not
// yet started doing the work being measured.
type reconcileLog struct {
	mu   sync.Mutex
	seen map[multicluster.ClusterName]map[string]struct{}
}

func newReconcileLog() *reconcileLog {
	return &reconcileLog{seen: map[multicluster.ClusterName]map[string]struct{}{}}
}

func (l *reconcileLog) record(workspace multicluster.ClusterName, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[workspace] == nil {
		l.seen[workspace] = map[string]struct{}{}
	}
	l.seen[workspace][name] = struct{}{}
}

func (l *reconcileLog) count(workspace multicluster.ClusterName) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen[workspace])
}

func eventually(t *testing.T, describe string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", pollTimeout, describe)
		}
		time.Sleep(pollInterval)
	}
}

// sample records one measurement, and — when asked — a goroutine profile
// alongside it.
//
// The profile is opt-in because it is a diagnostic, not a measurement: what
// the report says is how much the process costs, and what a profile says is
// where. Set SWEEP_GOROUTINE_PROFILE to a directory to get one per sample,
// which is how a per-workspace figure that moves gets attributed to the code
// that moved it.
func sample(t *testing.T, report *sweep.Report, counter *sweep.Counter, phase sweep.Phase, label string, workspaces int) sweep.Sample {
	t.Helper()

	s := sweep.Take(phase, label, workspaces, counter)
	report.Add(s)

	dir, ok := os.LookupEnv("SWEEP_GOROUTINE_PROFILE")
	if !ok || dir == "" {
		return s
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the profile directory %s: %v", dir, err)
	}
	name := filepath.Join(dir, fmt.Sprintf("goroutines-%02d-%s.txt", len(report.Samples), strings.ReplaceAll(label, " ", "-")))
	f, err := os.Create(name) //nolint:gosec // the path is developer-supplied, not user input.
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	defer f.Close() //nolint:errcheck // a profile that failed to flush is a diagnostic lost, not a test result changed.
	// Granularity 1 groups goroutines by stack with counts, which is what
	// makes two profiles subtractable by eye.
	if err := pprof.Lookup("goroutine").WriteTo(f, 1); err != nil {
		t.Fatalf("writing the goroutine profile: %v", err)
	}
	return s
}

// settle waits for the process to stop moving before a sample is taken.
func settle(t *testing.T, what string) {
	t.Helper()
	if !sweep.Settle(settleQuiet, settleTimeout) {
		t.Fatalf("the goroutine count never settled %s: a sample taken now would measure work in flight rather than what the process costs", what)
	}
}

// tenant is one workspace in the sweep: its logical cluster name, and a
// client that talks to it directly rather than through the manager.
type tenant struct {
	name         multicluster.ClusterName
	directClient client.Client
}

// TestActiveWorkspaceSweep is the measurement. It engages workspaces one at a
// time, makes each one active, samples the process after every step, then
// unbinds them one at a time and samples again.
//
// What it asserts is only what the design claims. The rest — heap, goroutines
// per workspace, discovery traffic — is reported rather than bounded, because
// no requirement states a budget for them and inventing one here would make
// this test fail for a reason nobody had agreed on. The numbers are the
// deliverable; see bin/sweep-report.md after a run.
func TestActiveWorkspaceSweep(t *testing.T) {
	ctrl.SetLogger(testr.NewWithOptions(t, testr.Options{LogTimestamp: false}))
	ctx := t.Context()

	workspaceCount := envInt(t, "SWEEP_WORKSPACES", defaultWorkspaces)
	objectCount := envInt(t, "SWEEP_OBJECTS", defaultObjects)

	report := &sweep.Report{Title: "Active workspace sweep"}
	report.AddFact("workspaces", fmt.Sprint(workspaceCount))
	report.AddFact("objectsPerWorkspace", fmt.Sprint(objectCount))
	report.AddFact("reconciledTypes", "cluster.x-k8s.io/clusters")
	report.AddFact("watchedTypesPerWorkspace", fmt.Sprint(watchedTypes))
	report.AddFact("goMaxProcs", fmt.Sprint(runtime.GOMAXPROCS(0)))
	report.AddFact("goVersion", runtime.Version())
	t.Cleanup(func() {
		t.Logf("\n%s", report.Markdown())
		dir := reportDir(t)
		if err := report.Write(dir, "sweep-report"); err != nil {
			t.Errorf("writing the sweep report: %v", err)
			return
		}
		t.Logf("sweep report written to %s", filepath.Join(dir, "sweep-report.md"))
	})

	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreCRDs...)
	must(t, err)

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))

	// Fixture traffic is deliberately not counted. The counter goes on the
	// config the provider and the manager are built from, and nowhere else, so
	// that what it reports is what serving workspaces costs rather than what
	// this test's own setup costs.
	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	must(t, err)

	must(t, kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   exportName,
		SchemaPrefix: "v1",
		CRDPaths:     crdPaths,
		CRDTransform: keepOneVersion,
	}))

	// --- The workspaces. All are created and bound up front, because creating
	// a workspace is kcp's cost rather than the manager's, and paying it in
	// the middle of the sweep would show up as the manager's.
	//
	// Binding is what makes a workspace engage, so that is done one at a time
	// below.
	tenants := make([]*tenant, 0, workspaceCount)
	for range workspaceCount {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		cfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
		directClient, err := client.New(cfg, client.Options{Scheme: scheme})
		must(t, err)
		tenants = append(tenants, &tenant{
			name:         multicluster.ClusterName(ws.Spec.Cluster),
			directClient: directClient,
		})
	}

	// --- One manager, one provider, instrumented.
	counter := sweep.NewCounter()
	countedCfg := counter.WrapConfig(rootCfg)

	provider, err := apiexport.New(countedCfg, exportName, apiexport.Options{Scheme: scheme})
	must(t, err)

	mgr, err := mcmanager.New(countedCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	must(t, err)

	reconciled := newReconcileLog()
	setup := func(_ context.Context, workspace multicluster.ClusterName, wsMgr manager.Manager) error {
		// A real controller, wired the way a provider binary wires one: its
		// watch, its workqueue, its rate limiter and its goroutines are what
		// the per-workspace cost in the report is made of.
		//
		// The name is per workspace because controller-runtime records
		// controller names in a process-global set that is never emptied, and
		// SkipNameValidation is set because that set is never emptied on
		// disengagement either — a re-engaged workspace would otherwise fail.
		return builder.ControllerManagedBy(wsMgr).
			For(&clusterv1.Cluster{}).
			Named(fmt.Sprintf("cluster-%s", workspace)).
			WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
			Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				reconciled.record(workspace, req.Name)
				return reconcile.Result{}, nil
			}))
	}

	wiring, err := providerwiring.AddToManager(mgr, setup, providerwiring.Options{
		Log: ctrl.Log.WithName("providerwiring"),
	})
	must(t, err)

	// The baseline is taken before the manager starts: the first workspace has
	// to be bound before kcp populates the APIExportEndpointSlice the provider
	// needs (ADR-0001, "APIExportEndpointSlice activation is lazy"), so a
	// running manager serving no workspaces is not a state this sweep can
	// stand in. What the baseline is for is the size of the fixed cost, not
	// the slope, and for that it is the honest starting point.
	settle(t, "before the manager started")
	sample(t, report, counter, sweep.PhaseBaseline, "baseline (manager not started)", 0)

	mgrCtx, stopManager := context.WithCancel(ctx)
	t.Cleanup(stopManager)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()

	// --- Up: bind, engage, activate, sample.
	for i, tn := range tenants {
		count := i + 1

		must(t, kcpfixtures.BindExport(ctx, tn.directClient, kcpfixtures.BindExportOptions{
			BindingName: bindingName,
			ExportPath:  "root",
			ExportName:  exportName,
		}))
		if i == 0 {
			must(t, kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 60*time.Second))
		}

		eventually(t, fmt.Sprintf("workspace %s to be engaged", tn.name), func() bool {
			return len(wiring.Engaged()) == count
		})
		settle(t, fmt.Sprintf("with %d workspaces engaged and idle", count))
		sample(t, report, counter, sweep.PhaseEngaged, fmt.Sprintf("%d bound, idle", count), count)

		// Now make it active. The objects are written by a client of this
		// test's own, not by the manager's, so that what the manager does with
		// them is all that the counter sees.
		for n := range objectCount {
			err := tn.directClient.Create(ctx, &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sweep-%02d", n), Namespace: "default"},
				// Every field of ClusterSpec is optional; paused makes the
				// object valid without asking anything of a provider that is
				// not wired here.
				Spec: clusterv1.ClusterSpec{Paused: ptr.To(true)},
			})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatalf("creating Cluster %d in workspace %s: %v", n, tn.name, err)
			}
		}

		eventually(t, fmt.Sprintf("workspace %s to reconcile its %d Clusters", tn.name, objectCount), func() bool {
			return reconciled.count(tn.name) == objectCount
		})
		settle(t, fmt.Sprintf("with %d workspaces active", count))
		sample(t, report, counter, sweep.PhaseActive, fmt.Sprintf("%d active", count), count)
	}

	// --- What the design claims, asserted.
	active := sweep.InPhase(report.Samples, sweep.PhaseActive)
	peak := active[len(active)-1]

	watchesPerWorkspace := sweep.PerWorkspace(active, func(s sweep.Sample) float64 {
		return float64(s.WatchStreams)
	})
	report.AddFact("watchStreamsPerWorkspace", fmt.Sprintf("%.2f", watchesPerWorkspace))
	report.AddFact("goroutinesPerWorkspace", fmt.Sprintf("%.1f",
		sweep.PerWorkspace(active, sweep.Goroutines)))
	report.AddFact("heapBytesPerWorkspace", fmt.Sprintf("%.0f",
		sweep.PerWorkspace(active, sweep.Heap)))
	report.AddFact("discoveryRequestsPerWorkspace", fmt.Sprintf("%.1f",
		sweep.PerWorkspace(active, func(s sweep.Sample) float64 { return float64(s.Discovery) })))

	// The conversion plan's central claim: watches are O(types), not
	// O(types × workspaces). A slope of zero is what that means; anything
	// approaching one watch per workspace is the multiplication onto a shared
	// shard the wildcard cache was adopted to avoid.
	if watchesPerWorkspace >= 0.5 {
		t.Errorf("each additional workspace opens %.2f more watch streams (%d streams at %d workspaces): "+
			"docs/conversion-plan.md's Scalability section claims watches are O(types), not O(types × workspaces)",
			watchesPerWorkspace, peak.WatchStreams, peak.Workspaces)
	}

	// The same claim from the other side, and the reason it is worth asserting
	// twice: a flat slope would also be produced by a process that opened one
	// watch per tenant up front. What must not exist is a watch addressed to a
	// tenant's own logical cluster, because every tenant read is meant to come
	// from the one wildcard cache. Watches scoped to the workspace that owns
	// the APIExport are a different thing — there is one of those regardless of
	// how many tenants bind — so the assertion names the tenants rather than
	// rejecting workspace-scoped requests wholesale.
	tenantNames := make([]string, 0, len(tenants))
	for _, tn := range tenants {
		tenantNames = append(tenantNames, string(tn.name))
	}
	if perTenant := peak.Counts.Streams(sweep.And(sweep.IsWatch, sweep.InClusters(tenantNames...))); perTenant > 0 {
		t.Errorf("%d watch streams are addressed to a tenant's own logical cluster rather than to /clusters/*: "+
			"every tenant read is meant to come from the one wildcard cache", perTenant)
	}

	// --- Down: unbind one at a time. User story 2 says a workspace that
	// unbinds "stops costing anything"; this is the measurement of that.
	for i, tn := range tenants {
		remaining := workspaceCount - i - 1

		must(t, tn.directClient.Delete(ctx, &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: bindingName},
		}))
		eventually(t, fmt.Sprintf("workspace %s to disengage", tn.name), func() bool {
			return len(wiring.Engaged()) == remaining
		})
		settle(t, fmt.Sprintf("with %d workspaces left", remaining))
		sample(t, report, counter, sweep.PhaseDisengaged, fmt.Sprintf("%d left", remaining), remaining)
	}

	drained := report.Samples[len(report.Samples)-1]
	first := active[0]
	report.AddFact("goroutinesReclaimedPerWorkspace", fmt.Sprintf("%.1f",
		-sweep.PerWorkspace(sweep.InPhase(report.Samples, sweep.PhaseDisengaged), sweep.Goroutines)))

	// What a departure does not give back. Comparing a teardown sample with
	// the sample taken at the same workspace count on the way up is the only
	// way to see it: both describe a process serving k workspaces, and any
	// difference is what the workspaces that left are still costing.
	if retained, departed, ok := retainedPerDeparture(report.Samples, workspaceCount); ok {
		perDeparture := float64(retained) / float64(departed)
		report.AddFact("goroutinesRetainedPerDepartedWorkspace", fmt.Sprintf("%.1f", perDeparture))

		// Measured, not desired. A departing workspace leaves two goroutines
		// behind per type its controllers watched: controller-runtime's Kind
		// source adds an event handler to the informer it watches through
		// (pkg/internal/source/kind.go) and has no path that ever removes it,
		// because in an ordinary deployment the informer dies with the
		// controller. Here it does not — the informer belongs to the wildcard
		// cache shared by every workspace — so the handler, and the
		// processorListener run/pop pair kcp's informers start for it, outlive
		// the workspace. They are released when the wildcard cache itself
		// stops, which is why the last departure gives back far more than the
		// others.
		//
		// The assertion is that this does not get worse. The target is zero;
		// see the workspace resource usage design page for what reaching it
		// would take.
		if budget := float64(retainedGoroutinesPerWatchedType * watchedTypes); perDeparture > budget {
			t.Errorf("each departed workspace left %.1f goroutines behind, more than the %.1f already known to be retained "+
				"(%d watched type(s) × %d): something has started keeping more per workspace than the shared informer's event handler",
				perDeparture, budget, watchedTypes, retainedGoroutinesPerWatchedType)
		}
	}

	// The inventory behind the headline: every stream the process held at its
	// widest, with tenant names made readable so two runs can be compared.
	report.Streams = sweep.Inventory(peak.Counts, sweep.IsWatch, tenantRenamer(tenants))

	// Serving no workspaces must not cost more than serving one did. This is
	// the leak assertion, and it is stated as a comparison rather than as an
	// absolute budget because the fixed cost of a running manager is not the
	// subject: what must not survive a workspace's departure is that
	// workspace's share.
	if drained.Goroutines > first.Goroutines {
		t.Errorf("after all %d workspaces unbound the process holds %d goroutines, more than the %d it held while serving one: "+
			"%.1f goroutines per departed workspace were not reclaimed",
			workspaceCount, drained.Goroutines, first.Goroutines,
			float64(drained.Goroutines-first.Goroutines)/float64(workspaceCount))
	}

	// A sweep that measured nothing must not pass quietly.
	if got := len(active); got != workspaceCount {
		t.Fatalf("the sweep took %d active samples for %d workspaces", got, workspaceCount)
	}
	if peak.WatchStreams == 0 || math.IsNaN(watchesPerWorkspace) {
		t.Fatalf("no watch traffic was observed (%+v): the counter is not on the config the manager uses", peak.Traffic)
	}
}

// retainedPerDeparture compares the teardown with the way up, at the same
// workspace count, and reports what the departed workspaces are still
// costing. It uses the point where the most workspaces have left while at
// least one remains, because the last departure also shuts the shared
// wildcard cache down — kcp empties the APIExportEndpointSlice when the last
// APIBinding goes — and that is a fixed cost coming off, not a workspace's.
func retainedPerDeparture(samples []sweep.Sample, workspaceCount int) (retained, departed int, ok bool) {
	var up, down *sweep.Sample
	for i := range samples {
		s := &samples[i]
		if s.Workspaces != 1 {
			continue
		}
		switch s.Phase {
		case sweep.PhaseActive:
			up = s
		case sweep.PhaseDisengaged:
			down = s
		}
	}
	if up == nil || down == nil || workspaceCount < 2 {
		return 0, 0, false
	}
	return down.Goroutines - up.Goroutines, workspaceCount - 1, true
}

// tenantRenamer maps kcp's generated logical cluster names onto stable ones,
// so that the stream inventory in two reports can be compared line by line.
func tenantRenamer(tenants []*tenant) func(string) string {
	names := make(map[string]string, len(tenants))
	for i, tn := range tenants {
		names[string(tn.name)] = fmt.Sprintf("tenant-%d", i+1)
	}
	return func(cluster string) string {
		if name, ok := names[cluster]; ok {
			return name
		}
		return cluster
	}
}

// reportDir is where the sweep writes its report: bin/, alongside the
// verification result, because that is where this repository already puts
// machine-readable output meant to outlive a run.
//
// A test's working directory is its own source directory, so the repository
// root is three levels up from test/integration/sweep. SWEEP_REPORT_DIR
// overrides it for a run whose report is being collected somewhere else.
func reportDir(t *testing.T) string {
	t.Helper()
	if dir, ok := os.LookupEnv("SWEEP_REPORT_DIR"); ok && dir != "" {
		return dir
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "bin"))
	if err != nil {
		// Not fatal: a report in the working directory is worth more than no
		// report at all.
		t.Logf("resolving the report directory: %v", err)
		return "bin"
	}
	return dir
}
