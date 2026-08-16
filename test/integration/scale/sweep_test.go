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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness/capiservice"
	"github.com/jimmidyson/kcp-cluster-api/internal/workspacetelemetry"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

var (
	sweepProfile = flag.String("profile", "idle-heavy",
		"Which load profile to sweep: idle-heavy or active-heavy. Capacity is stated per profile, so this is part of the answer.")
	sweepPoints = flag.String("points", "1,2,4,8",
		"Comma-separated workspace counts, geometrically spaced. The departure point is derived from these, so the set is recorded with the result.")
	sweepTolerance = flag.Float64("tolerance", scaleharness.DefaultTolerance,
		"Fractional excess over the linear projection that counts as a departure.")
	engageTimeout = flag.Duration("engage-timeout", 5*time.Minute,
		"How long to wait for every provisioned workspace to be engaged before a point is abandoned.")
)

// TestSweep measures what a workspace costs the process that reconciles it.
//
// The manager is the point. Provisioning workspaces without engaging them
// would measure the test's own clients rather than the wiring, and the wiring
// is the whole subject: listeners registered per workspace, workers started
// eagerly per controller per workspace, a REST mapper and client per workspace.
// So a real multicluster manager runs here, engaging every workspace the sweep
// provisions, and the heap sampled is a heap that contains it.
//
// What it deliberately does not wire is the full Cluster API reconciler set.
// That needs the docker/dev infrastructure backend and therefore a container
// runtime, and the property being measured — the per-workspace cost of
// registering watches and starting workers — is the same whether the reconciler
// behind them is upstream's or a stub. Keeping it container-free means this
// runs anywhere kcp does, which is the same reasoning the wiring test records.
// The consequence is stated rather than hidden: these figures bound the wiring,
// not a production reconciler set.
func TestSweep(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	profile, err := profileByName(*sweepProfile)
	if err != nil {
		t.Fatalf("%v", err)
	}
	points, err := parsePoints(*sweepPoints)
	if err != nil {
		t.Fatalf("%v", err)
	}

	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreCRDs...)
	if err != nil {
		t.Fatalf("resolving CRD manifests: %v", err)
	}

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
		clusterv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("root client: %v", err)
	}

	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   exportName,
		SchemaPrefix: "v1",
		CRDPaths:     crdPaths,
		CRDTransform: keepOneVersion,
	}); err != nil {
		t.Fatalf("publishing APIExport: %v", err)
	}

	fleet := &kcpFleet{
		t:       t,
		ctx:     ctx,
		server:  server,
		baseCfg: baseCfg,
		scheme:  scheme,
	}

	// One workspace up front: the endpoint slice has no virtual workspace URLs
	// until something has bound, and the provider needs those to start.
	if _, err := fleet.Provision(ctx, 1); err != nil {
		t.Fatalf("seeding the first workspace: %v", err)
	}
	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 60*time.Second); err != nil {
		t.Fatalf("waiting for endpoint slice: %v", err)
	}

	telemetry := workspacetelemetry.New(workspacetelemetry.Options{})
	wiring := startManager(t, ctx, rootCfg, scheme, telemetry)

	// Engagement is asynchronous, so a point must not be measured until every
	// workspace it provisioned is actually wired. Measuring early would report
	// the cost of a fleet the process has not finished taking on.
	fleet.awaitEngagement = func(want int) error {
		deadline := time.Now().Add(*engageTimeout)
		for {
			if got := len(wiring.Engaged()); got >= want {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("only %d of %d workspaces engaged within %s", len(wiring.Engaged()), want, *engageTimeout)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	run, err := scaleharness.Sweep(ctx, scaleharness.SweepOptions{
		Service:    capiservice.Service{Prefix: "sweep"},
		Profile:    profile,
		Workspaces: points,
		Provision:  fleet.Provision,
		Mode:       scaleharness.ModeSynthetic,
		Departure:  scaleharness.DepartureOptions{Tolerance: *sweepTolerance},
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("sweep produced an unquotable run: %v", err)
	}

	t.Logf("%s", run.Summary())
	t.Logf("  %-12s %-14s %-12s %-14s %s", "workspaces", "heap", "goroutines", "load", "events")
	for _, m := range run.Measurements {
		t.Logf("  %-12d %-14s %-12d %-14s %d",
			m.Workspaces, humanBytes(m.HeapBytes), m.Goroutines, m.LoadDuration.Round(time.Millisecond), m.Events)
	}

	perWorkspace(t, run)

	snap := telemetry.Snapshot()
	t.Logf("  telemetry: engaged=%d failures=%d labelled-series=%d",
		snap.EngagedWorkspaces, snap.EngagementFailures, len(snap.LabelledWorkspaces()))

	// The outcome is the finding, not a verdict on the code. A sweep that
	// measured linear cost has established that capacity is above the swept
	// range; one too short to establish anything must say so rather than be
	// read as headroom.
	switch {
	case run.Departure.CouldNotRun:
		t.Logf("COULD NOT RUN: %s", run.Departure.Reason)
	case run.Departure.Found:
		t.Logf("DEPARTURE at %d workspaces — capacity should be set below this with headroom", run.Departure.Workspaces)
	default:
		t.Logf("NO DEPARTURE POINT within the swept range: %s", run.Departure.Reason)
		t.Logf("  Any capacity quoted above %d workspaces is an extrapolation and must be labelled one.",
			points[len(points)-1])
	}
}

// perWorkspace reports the marginal cost of a workspace between the smallest
// and largest points, which is the coefficient a sizing table is built from.
func perWorkspace(t *testing.T, run scaleharness.SweepRun) {
	t.Helper()
	if len(run.Measurements) < 2 {
		return
	}
	first, last := run.Measurements[0], run.Measurements[len(run.Measurements)-1]
	dw := last.Workspaces - first.Workspaces
	if dw <= 0 {
		return
	}
	heapPer := (float64(last.HeapBytes) - float64(first.HeapBytes)) / float64(dw)
	goroutinesPer := float64(last.Goroutines-first.Goroutines) / float64(dw)
	t.Logf("  marginal per workspace between %d and %d: heap %s, goroutines %.1f",
		first.Workspaces, last.Workspaces, humanBytes(uint64(max(heapPer, 0))), goroutinesPer)
}

// kcpFleet provisions workspaces against a real kcp, accumulating rather than
// rebuilding.
//
// Accumulating is both cheaper and more faithful: a shard grows, and the
// question is what a process holding N workspaces costs, not what it costs to
// have created N of them from nothing.
type kcpFleet struct {
	t       *testing.T
	ctx     context.Context
	server  kcptestingserver.RunningServer
	baseCfg *rest.Config
	scheme  *runtime.Scheme

	clients         []client.Client
	awaitEngagement func(want int) error
}

func (f *kcpFleet) Provision(ctx context.Context, workspaces int) ([]client.Client, error) {
	for len(f.clients) < workspaces {
		wsPath, _ := kcptesting.NewWorkspaceFixture(f.t, f.server, logicalcluster.NewPath("root"))
		cfg := kcpclient.SetCluster(rest.CopyConfig(f.baseCfg), wsPath)
		c, err := client.New(cfg, client.Options{Scheme: f.scheme})
		if err != nil {
			return nil, fmt.Errorf("client for workspace %d: %w", len(f.clients), err)
		}
		if err := kcpfixtures.BindExport(ctx, c, kcpfixtures.BindExportOptions{
			BindingName:  bindingName,
			ExportPath:   "root",
			ExportName:   exportName,
			ReadyTimeout: 90 * time.Second,
		}); err != nil {
			return nil, fmt.Errorf("binding workspace %d: %w", len(f.clients), err)
		}
		f.clients = append(f.clients, c)
	}

	if f.awaitEngagement != nil {
		if err := f.awaitEngagement(workspaces); err != nil {
			return nil, err
		}
	}
	return f.clients[:workspaces], nil
}

// startManager runs a real multicluster manager with per-workspace wiring, so
// the heap the sweep samples contains the machinery under measurement.
func startManager(t *testing.T, ctx context.Context, rootCfg *rest.Config, scheme *runtime.Scheme, telemetry *workspacetelemetry.Recorder) *providerwiring.Wiring {
	t.Helper()

	provider, err := apiexport.New(rootCfg, exportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	mgr, err := mcmanager.New(rootCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	wiring, err := providerwiring.AddToManager(mgr, setupWatches, providerwiring.Options{
		Log:       ctrl.Log.WithName("providerwiring"),
		Telemetry: telemetry,
	})
	if err != nil {
		t.Fatalf("wiring: %v", err)
	}

	mgrCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()
	return wiring
}

// setupWatches registers one controller per workspace watching the same type
// the core reconcilers watch.
//
// This is the registration whose cost the feature is about: a listener on the
// shared informer, a workqueue, and MaxConcurrentReconciles workers started
// eagerly. The reconciler behind it does nothing, because what it does is not
// what is being measured.
func setupWatches(ctx context.Context, _ multicluster.ClusterName, mgr manager.Manager) error {
	c, err := controller.New("scale-probe", mgr, controller.Options{
		Reconciler:              reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) { return reconcile.Result{}, nil }),
		MaxConcurrentReconciles: 2,
		SkipNameValidation:      ptr.To(true),
	})
	if err != nil {
		return fmt.Errorf("creating probe controller: %w", err)
	}
	return c.Watch(source.Kind(mgr.GetCache(), &clusterv1.Cluster{},
		&handler.TypedEnqueueRequestForObject[*clusterv1.Cluster]{}))
}

func profileByName(name string) (scaleharness.Profile, error) {
	switch name {
	case "idle-heavy":
		return scaleharness.IdleHeavy(), nil
	case "active-heavy":
		return scaleharness.ActiveHeavy(), nil
	default:
		return scaleharness.Profile{}, fmt.Errorf("unknown profile %q: capacity is stated per profile, so this must name one", name)
	}
}

func parsePoints(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("parsing sweep points %q: %w", s, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("sweep point %d is not a workspace count", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}
