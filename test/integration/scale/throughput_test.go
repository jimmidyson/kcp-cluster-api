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
	"sync"
	"time"

	"testing"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness"
	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness/capiservice"
	"github.com/jimmidyson/kcp-cluster-api/internal/workspacetelemetry"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

var (
	throughputWorkspaces = flag.Int("throughput-workspaces", 8,
		"How many workspaces share the shard during the throughput run.")
	throughputObjects = flag.Int("throughput-objects", 20,
		"Objects per workspace, all of which are mutated at once to build a backlog. "+
			"Must exceed the worker count or the queue never becomes the constraint.")
	throughputWorkers = flag.String("throughput-workers", "1,2,4,8",
		"Comma-separated MaxConcurrentReconciles values to compare.")
	reconcileDuration = flag.Duration("reconcile-duration", 50*time.Millisecond,
		"How long the probe reconciler takes. A real Cluster API reconcile is dominated by API "+
			"calls and infrastructure waits, not CPU, so this sleeps rather than spins — which "+
			"isolates the worker count as the constraint instead of the machine's core count.")
	throughputTimeout = flag.Duration("throughput-timeout", 5*time.Minute,
		"How long one worker-count point may take before it is reported as a shortfall.")
)

// TestReconcileThroughput measures how fast one workspace's backlog drains, as
// a function of the per-controller worker count.
//
// # Why this needs its own test rather than a sweep point
//
// The sweep measures latency with one event in flight per workspace, which is
// the right shape for asking what a fleet costs — and the wrong shape for
// asking what it can retire. `MaxConcurrentReconciles` does not affect the
// latency of an event arriving into an empty queue at all. It caps the rate a
// *backlog* drains, so a backlog is what this builds.
//
// # What the numbers mean, and what they do not
//
// Workers here are **per controller per workspace**, not per process: a shard
// with 800 workspaces and 5 controllers at 2 workers has 8,000 of them. So the
// question this answers is not "can the shard do enough work" — it is "can one
// tenant's backlog drain fast enough", which is a per-workspace question with a
// statically partitioned answer.
//
// The reconciler sleeps rather than computing. That is deliberate and it is a
// limitation: it isolates queueing from CPU contention, so these figures say
// what the worker count permits, not what the machine can sustain. A real
// reconciler competing for cores would do worse.
func TestReconcileThroughput(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	workerCounts, err := parsePoints(*throughputWorkers)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if *throughputObjects <= workerCounts[len(workerCounts)-1] {
		t.Fatalf("objects per workspace (%d) must exceed the largest worker count (%d), "+
			"or the queue never becomes the constraint and every point measures the same thing",
			*throughputObjects, workerCounts[len(workerCounts)-1])
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
		apisv1alpha2.AddToScheme,
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

	svc := capiservice.Service{Prefix: "throughput"}

	// Provisioned once, outside the loop, and deliberately so.
	//
	// The first version of this test built a fresh fleet per worker count. The
	// kcp server persists across points, so workspaces accumulated: point two
	// measured sixteen workspaces, point three twenty-four, each still holding
	// the previous point's objects. Every point after the first was measuring a
	// different fleet from the one it reported.
	fleet := &kcpFleet{t: t, ctx: ctx, server: server, baseCfg: baseCfg, scheme: scheme}
	workspaces, err := fleet.Provision(ctx, *throughputWorkspaces)
	if err != nil {
		t.Fatalf("provisioning: %v", err)
	}
	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 60*time.Second); err != nil {
		t.Fatalf("waiting for endpoint slice: %v", err)
	}
	for i, ws := range workspaces {
		if err := svc.Populate(ctx, ws.Client, *throughputObjects); err != nil {
			t.Fatalf("populating workspace %d: %v", i, err)
		}
	}

	t.Logf("%-8s %-12s %-14s %-16s %s", "workers", "completions", "elapsed", "reconciles/s", "per workspace")

	var results []workerPoint
	for _, workers := range workerCounts {
		res := measureThroughput(t, ctx, throughputEnv{
			rootCfg:    rootCfg,
			scheme:     scheme,
			svc:        svc,
			workers:    workers,
			workspaces: workspaces,
		})
		results = append(results, workerPoint{Workers: workers, Result: res})

		perWorkspace := res.PerSecond / float64(*throughputWorkspaces)
		t.Logf("%-8d %-12d %-14s %-16.1f %.1f/s",
			workers, res.Completions, res.Elapsed.Round(time.Millisecond), res.PerSecond, perWorkspace)
	}

	report(t, results)
}

type workerPoint struct {
	Workers int
	Result  scaleharness.Result
}

// report states whether throughput actually scaled with the worker count.
//
// The expected shape is a straight line up to the point where something else
// binds: doubling workers should roughly double the drain rate while the queue
// is deep enough to keep them all busy. Where it stops doing so is the figure
// that matters, because it is where adding workers stops buying anything.
func report(t *testing.T, results []workerPoint) {
	t.Helper()
	if len(results) < 2 {
		return
	}
	base := results[0]
	t.Logf("scaling relative to %d worker(s):", base.Workers)
	for _, r := range results[1:] {
		workerRatio := float64(r.Workers) / float64(base.Workers)
		rateRatio := r.Result.PerSecond / base.Result.PerSecond
		efficiency := rateRatio / workerRatio
		t.Logf("  %2d workers: %.2f× the workers, %.2f× the rate (%.0f%% of linear)",
			r.Workers, workerRatio, rateRatio, efficiency*100)
	}
}

type throughputEnv struct {
	rootCfg    *rest.Config
	scheme     *runtime.Scheme
	svc        capiservice.Service
	workers    int
	workspaces []scaleharness.Workspace
}

// measureThroughput runs one worker-count point against a freshly engaged
// fleet.
//
// A fresh manager per point, rather than reconfiguring one: worker counts are
// fixed when a controller is constructed, and a manager that had already
// drained one backlog would carry warmed caches and settled queues into the
// next point, flattering whichever ran last.
func measureThroughput(t *testing.T, ctx context.Context, env throughputEnv) scaleharness.Result {
	t.Helper()

	workspaces := env.workspaces
	tp := scaleharness.NewThroughput()
	telemetry := workspacetelemetry.New(workspacetelemetry.Options{})

	// Stopped before this function returns, not deferred to the end of the
	// test. Managers left running would each keep engaging every workspace and
	// each keep counting completions into the next point's measurement — which
	// is precisely how the first version of this test produced rates above
	// linear and completion counts above its own target.
	mgrCtx, stopManager := context.WithCancel(ctx)
	defer stopManager()

	wiring := startThroughputManager(t, mgrCtx, env, telemetry, tp)

	deadline := time.Now().Add(*engageTimeout)
	for len(wiring.Engaged()) < len(workspaces) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d workspaces engaged", len(wiring.Engaged()), len(workspaces))
		}
		time.Sleep(250 * time.Millisecond)
	}

	// Let the registration replay settle before the clock starts. Those
	// reconciles are real work but nobody asked for them, and counting them
	// would credit the worker pool with a burst it did not have to schedule.
	time.Sleep(3 * time.Second)

	tp.Begin(len(workspaces) * *throughputObjects)

	// Issued concurrently across workspaces, and by patch rather than
	// get-then-update.
	//
	// The first attempt at this measurement did neither, and the load generator
	// became the constraint: 320 sequential round trips at ~6 ms each put a
	// ~3.8 s floor under every worker count, which is why 16 workers appeared
	// to buy only 1.7x over one. The thing being measured has to be faster than
	// the thing measuring it.
	issueStart := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, len(workspaces))
	for i, ws := range workspaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = touchAll(ctx, ws.Client, env.svc, *throughputObjects)
		}()
	}
	wg.Wait()
	issueDuration := time.Since(issueStart)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("building backlog in workspace %d: %v", i, err)
		}
	}

	res, err := tp.Await(ctx, *throughputTimeout)
	if err != nil {
		// A shortfall is a result: it means the workers could not drain the
		// backlog in the time allowed, which is the condition being tested for.
		t.Logf("  %d workers: %v", env.workers, err)
	}

	// Reported alongside, never subtracted. If issuing took a large share of
	// the elapsed time then the generator was competing with the workers for
	// the measurement, and the rate understates what the workers could do — a
	// reader has to be able to see that rather than take the rate at face
	// value.
	if issueDuration > res.Elapsed/2 {
		t.Logf("  %d workers: WARNING issuing the backlog took %s of %s elapsed — "+
			"the load generator, not the worker pool, is the constraint at this point",
			env.workers, issueDuration.Round(time.Millisecond), res.Elapsed.Round(time.Millisecond))
	}
	t.Logf("  %d workers: backlog issued in %s, %d completions",
		env.workers, issueDuration.Round(time.Millisecond), res.Completions)

	// Drained before the next point starts. Without this the next manager
	// engages while this one is still retiring work, and its settle window
	// absorbs reconciles this point paid for.
	stopManager()
	time.Sleep(2 * time.Second)
	return res
}

// touchAll mutates every object in a workspace, so the queue depth is the
// object count rather than one.
//
// A merge patch rather than get-then-update: a patch needs no resourceVersion,
// so it costs one round trip instead of two and cannot conflict. Halving the
// generator's cost matters because the generator competes with the workers for
// the wall-clock this measurement is made of.
func touchAll(ctx context.Context, c client.Client, svc capiservice.Service, objects int) error {
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	patch := []byte(fmt.Sprintf(`{"metadata":{"labels":{"scaleharness-touch":%q}}}`, stamp))

	for i := range objects {
		cluster := &clusterv1.Cluster{}
		cluster.Namespace = capiservice.Namespace
		cluster.Name = svc.ObjectName(i)
		if err := c.Patch(ctx, cluster, client.RawPatch(types.MergePatchType, patch)); err != nil {
			return fmt.Errorf("patching %s: %w", cluster.Name, err)
		}
	}
	return nil
}

// startThroughputManager wires one controller per workspace at the worker count
// under test, with a reconciler that takes a stated amount of time.
//
// One controller, not the wired census: the question is how a single
// controller's worker pool drains its own queue, and five controllers watching
// the same type would split the same backlog five ways and measure something
// else.
func startThroughputManager(
	t *testing.T,
	ctx context.Context,
	env throughputEnv,
	telemetry *workspacetelemetry.Recorder,
	tp *scaleharness.Throughput,
) *providerwiring.Wiring {
	t.Helper()

	provider, err := apiexport.New(env.rootCfg, exportName, apiexport.Options{Scheme: env.scheme})
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	mgr, err := mcmanager.New(env.rootCfg, provider, ctrl.Options{
		Scheme:                 env.scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	setup := func(ctx context.Context, workspace multicluster.ClusterName, wsMgr manager.Manager) error {
		c, err := controller.New("throughput-probe", wsMgr, controller.Options{
			Reconciler: reconcile.Func(func(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
				// Sleep rather than spin. A real Cluster API reconcile waits on
				// API calls and infrastructure far more than it computes, and
				// sleeping isolates the worker count from the machine's core
				// count — which is the variable under test.
				select {
				case <-time.After(*reconcileDuration):
				case <-ctx.Done():
					return reconcile.Result{}, ctx.Err()
				}
				tp.Completed()
				return reconcile.Result{}, nil
			}),
			MaxConcurrentReconciles: env.workers,
			SkipNameValidation:      ptr.To(true),
		})
		if err != nil {
			return fmt.Errorf("creating throughput controller: %w", err)
		}
		return c.Watch(source.Kind(wsMgr.GetCache(), &clusterv1.Cluster{},
			&handler.TypedEnqueueRequestForObject[*clusterv1.Cluster]{}))
	}

	wiring, err := providerwiring.AddToManager(mgr, setup, providerwiring.Options{
		Log:       ctrl.Log.WithName("providerwiring"),
		Telemetry: telemetry,
	})
	if err != nil {
		t.Fatalf("wiring: %v", err)
	}

	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()
	return wiring
}
