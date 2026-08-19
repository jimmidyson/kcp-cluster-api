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
// that real controllers reconcile through the workspace's own manager. A sweep
// over idle workspaces would measure the cheapest possible case and prove
// nothing about the one that matters.
//
// # Two shapes, one harness
//
// This file is the harness. The shapes it sweeps live beside it:
//
//   - TestActiveWorkspaceSweep, in single_type_sweep_test.go: one controller
//     watching one type. The floor — what the wiring itself costs, with as
//     little else in the measurement as possible. Cheap enough to gate every
//     pull request.
//   - TestCoreReconcilerWorkspaceSweep, in coremanager_sweep_test.go: the real
//     reconciler set cmd/core-manager wires, on the dev provider's in-memory
//     backend. What a deployment actually pays.
//
// Both go through [runSweep], so the two are comparable: same instrument, same
// settling rules, same assertions, different workload.
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
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

const (
	bindingName = "cluster-api-sweep"

	pollInterval = 250 * time.Millisecond
	pollTimeout  = 180 * time.Second

	// settleQuiet is how long the goroutine count must hold still before a
	// sample is taken. Engaging a workspace keeps starting things well after
	// the wiring reports it done — informers sync, a workqueue drains, HTTP/2
	// streams open — so a sample taken the instant Engage returns attributes
	// the tail of one workspace's cost to the next one.
	settleQuiet   = 2 * time.Second
	settleTimeout = 120 * time.Second

	// retainedGoroutinesPerEventHandler is what one event-handler registration
	// costs after the workspace that made it has gone: the processorListener
	// run/pop pair kcp's informers start per handler. Measured rather than
	// budgeted; see the assertion in runSweep for why it is not zero.
	//
	// The unit is the registration, not the type. Several controllers commonly
	// watch the same type — each registers its own handler on the one shared
	// informer — which is why a shape's retention is stated as its own
	// constant rather than derived from how many types it watches.
	retainedGoroutinesPerEventHandler = 2
)

// sweepConfig is one workload shape to measure. Everything that differs
// between the two sweeps is here; everything that must not differ — the
// instrument, the settling, the assertions — is in [runSweep].
type sweepConfig struct {
	// title and reportName name the run and the files it writes to bin/.
	title      string
	reportName string
	// exportName is the APIExport this sweep publishes and binds.
	exportName string

	// workspacesEnv and objectsEnv are the environment variables that size
	// this sweep, named per shape so that widening the cheap sweep does not
	// silently widen the expensive one.
	workspacesEnv     string
	objectsEnv        string
	defaultWorkspaces int
	defaultObjects    int

	// watchedTypes is how many published types one workspace's controllers
	// watch. Reported, so a per-workspace figure can be read against the size
	// of the workload that produced it.
	watchedTypes int
	// eventHandlers is how many event-handler registrations one workspace's
	// controllers make: the number that decides how much a departure fails to
	// give back. It is usually larger than watchedTypes, because several
	// controllers watch the same type. Both are declared rather than inferred
	// — the assertion is what catches a shape whose wiring has changed
	// underneath the number.
	eventHandlers int
	// facts describing this shape, recorded with the numbers.
	facts map[string]string

	// scheme is used for the manager, the provider and the fixture clients.
	scheme *k8sruntime.Scheme
	// crds resolves the CRD manifests to publish.
	crds func(t *testing.T) []string
	// crdTransform is applied to each CRD before it is published.
	crdTransform func(*apiextensionsv1.CustomResourceDefinition)

	// newSetup builds the per-workspace wiring under measurement. It is called
	// once, before the manager starts, so anything process-global it needs is
	// installed exactly once.
	newSetup func(t *testing.T, ctx context.Context) providerwiring.SetupFunc

	// newFleetSetup builds wiring that is installed once for every workspace
	// rather than once per workspace. Optional; nil means the sweep measures
	// per-workspace wiring alone.
	//
	// When it is set the manager's local cluster is the APIExport's virtual
	// workspace rather than the workspace holding the export, because a
	// fleet-wide controller resolves types through the local RESTMapper at
	// setup time and the exporting workspace does not bind what it exports.
	//
	// It is handed the shard config as well as the manager, because the two
	// address different API surfaces and some wiring needs the one the manager
	// is not built on: the virtual workspace serves what the APIExport serves,
	// and a kubeconfig Secret is not that.
	newFleetSetup func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry)

	// diagnose runs when a workspace never becomes active or never disengages,
	// with the fixture still up. Optional.
	diagnose func(t *testing.T, ctx context.Context, tn *tenant, objects int)

	// activate writes the objects that make one workspace active, and active
	// reports whether that workspace's controllers have finished acting on
	// them. Both use the workspace's own client rather than the manager's, so
	// that what the counter sees is the manager's traffic alone.
	activate func(t *testing.T, ctx context.Context, tn *tenant, objects int)
	active   func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool
}

// tenant is one workspace in the sweep: its logical cluster name, and a client
// that talks to it directly rather than through the manager.
type tenant struct {
	name         multicluster.ClusterName
	directClient client.Client
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

// eventually polls until the condition holds, and on timeout runs diagnose
// before failing.
//
// The diagnostic is not decoration. When a workspace stops progressing, the
// question is always which object is in which state, and the kcp fixture is
// torn down with the test — so a timeout that says only "timed out" throws away
// the one moment the answer was still reachable.
func eventually(t *testing.T, describe string, condition func() bool, diagnose ...func()) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			for _, d := range diagnose {
				d()
			}
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
	name := filepath.Join(dir, fmt.Sprintf("goroutines-%03d-%s.txt", len(report.Samples), strings.ReplaceAll(label, " ", "-")))
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

// keepStorageVersion trims a CRD to the single version kcp will store.
//
// These sweeps deliberately serve no webhooks: webhook wiring is
// single-workspace by construction (the specification's FR-008), so it has
// nothing to say about how cost scales with workspace count, and standing a
// server up would add a fixed cost to every measurement for no measured
// claim. A published type therefore carries one version - see
// kcpfixtures.KeepStorageVersion for why the two go together.
var keepStorageVersion = kcpfixtures.KeepStorageVersion

// runSweep engages workspaces one at a time, makes each one active, samples
// the process after every step, then unbinds them one at a time and samples
// again.
//
// What it asserts is only what the design claims. The rest — heap, goroutines
// per workspace, step times — is reported rather than bounded, because no
// requirement states a budget for them and inventing one here would make a
// test fail for a reason nobody had agreed on. The numbers are the
// deliverable; see bin/<reportName>.md after a run.
func runSweep(t *testing.T, cfg sweepConfig) {
	ctrl.SetLogger(testr.NewWithOptions(t, testr.Options{LogTimestamp: false, Verbosity: envInt(t, "SWEEP_LOG_VERBOSITY", 0)}))
	ctx := t.Context()

	workspaceCount := envInt(t, cfg.workspacesEnv, cfg.defaultWorkspaces)
	objectCount := envInt(t, cfg.objectsEnv, cfg.defaultObjects)

	report := &sweep.Report{Title: cfg.title}
	for key, value := range cfg.facts {
		report.AddFact(key, value)
	}
	report.AddFact("workspaces", fmt.Sprint(workspaceCount))
	report.AddFact("objectsPerWorkspace", fmt.Sprint(objectCount))
	report.AddFact("watchedTypesPerWorkspace", fmt.Sprint(cfg.watchedTypes))
	report.AddFact("eventHandlersPerWorkspace", fmt.Sprint(cfg.eventHandlers))
	report.AddFact("goMaxProcs", fmt.Sprint(runtime.GOMAXPROCS(0)))
	report.AddFact("goVersion", runtime.Version())
	t.Cleanup(func() {
		t.Logf("\n%s", report.Markdown())
		dir := reportDir(t)
		if err := report.Write(dir, cfg.reportName); err != nil {
			t.Errorf("writing the sweep report: %v", err)
			return
		}
		t.Logf("sweep report written to %s", filepath.Join(dir, cfg.reportName+".md"))
	})

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	// Fixture traffic is deliberately not counted. The counter goes on the
	// config the provider and the manager are built from, and nowhere else, so
	// that what it reports is what serving workspaces costs rather than what
	// this test's own setup costs.
	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: cfg.scheme})
	must(t, err)

	must(t, kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   cfg.exportName,
		SchemaPrefix: "v1",
		CRDPaths:     cfg.crds(t),
		CRDTransform: cfg.crdTransform,
	}))

	// --- The workspaces. All are created up front, because creating a
	// workspace is kcp's cost rather than the manager's, and paying it in the
	// middle of the sweep would show up as the manager's. Binding is what
	// makes a workspace engage, so that is done one at a time below.
	tenants := make([]*tenant, 0, workspaceCount)
	for range workspaceCount {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		wsCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
		directClient, err := client.New(wsCfg, client.Options{Scheme: cfg.scheme})
		must(t, err)
		tenants = append(tenants, &tenant{
			name:         multicluster.ClusterName(ws.Spec.Cluster),
			directClient: directClient,
		})
	}

	// --- One manager, one provider, instrumented.
	counter := sweep.NewCounter()
	countedCfg := counter.WrapConfig(rootCfg)

	// Built through providerwiring rather than apiexport.New so that the caches
	// it makes per shard are reachable: the fleet-wide watches have to be
	// registered on the cache their reconcilers read through, and through
	// apiexport.New that cache cannot be got at.
	wildcardRegistry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(countedCfg, cfg.exportName, cfg.scheme, wildcardRegistry)
	must(t, err)

	// The manager's local cluster. Per-workspace wiring wants the workspace
	// holding the export; fleet-wide wiring wants the virtual workspace, whose
	// discovery describes the API surface every engaged workspace shares.
	//
	// The URL is derived rather than read from the APIExportEndpointSlice,
	// which is what production does (providerwiring.VirtualWorkspaceConfig).
	// The slice is empty until a workspace has bound, and this sweep binds them
	// one at a time *after* the manager is built, deliberately — reading the
	// slice here would force a workspace to be bound before the baseline, and
	// the baseline is the one sample taken with none.
	localCfg := countedCfg
	if cfg.newFleetSetup != nil {
		vw := rest.CopyConfig(baseCfg)
		vw.Host = strings.TrimSuffix(baseCfg.Host, "/") + "/services/apiexport/root/" + cfg.exportName + "/clusters/*"
		localCfg = counter.WrapConfig(vw)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme:                 cfg.scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	must(t, err)

	wiring, err := providerwiring.AddToManager(mgr, cfg.newSetup(t, ctx), providerwiring.Options{
		Log: ctrl.Log.WithName("providerwiring"),
	})
	must(t, err)

	if cfg.newFleetSetup != nil {
		cfg.newFleetSetup(t, ctx, mgr, baseCfg, wildcardRegistry)
	}

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
			ExportName:  cfg.exportName,
		}))
		if i == 0 {
			must(t, kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, cfg.exportName, 60*time.Second))
		}

		eventually(t, fmt.Sprintf("workspace %s to be engaged", tn.name), func() bool {
			return len(wiring.Engaged()) == count
		})
		settle(t, fmt.Sprintf("with %d workspaces engaged and idle", count))
		sample(t, report, counter, sweep.PhaseEngaged, fmt.Sprintf("%d bound, idle", count), count)

		cfg.activate(t, ctx, tn, objectCount)
		eventually(t, fmt.Sprintf("workspace %s to reconcile its %d object set(s)", tn.name, objectCount), func() bool {
			return cfg.active(t, ctx, tn, objectCount)
		}, func() {
			if cfg.diagnose != nil {
				cfg.diagnose(t, ctx, tn, objectCount)
			}
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
	report.AddFact("secondsPerWorkspaceEngagement", fmt.Sprintf("%.1f",
		sweep.PerWorkspace(active, func(s sweep.Sample) float64 { return s.StepSeconds })))

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
	if perTenant := peak.Counts.DistinctStreams(sweep.And(sweep.IsWatch, sweep.InClusters(tenantNames...))); perTenant > 0 {
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
		}, func() {
			if cfg.diagnose != nil {
				cfg.diagnose(t, ctx, tn, objectCount)
			}
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
		if budget := float64(retainedGoroutinesPerEventHandler * cfg.eventHandlers); perDeparture > budget {
			t.Errorf("each departed workspace left %.1f goroutines behind, more than the %.1f already known to be retained "+
				"(%d event-handler registration(s) × %d): this shape is now keeping more per workspace than its handlers on the shared informers account for",
				perDeparture, budget, cfg.eventHandlers, retainedGoroutinesPerEventHandler)
		}
	}

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

	// The inventory behind the headline: every stream the process held at its
	// widest, with tenant names made readable so two runs can be compared.
	report.Streams = sweep.Inventory(peak.Counts, sweep.IsWatch, tenantRenamer(tenants))
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

// reportDir is where a sweep writes its report: bin/, alongside the
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
