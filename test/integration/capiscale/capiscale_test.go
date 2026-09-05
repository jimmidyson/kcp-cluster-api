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

// Package capiscale climbs stock Cluster API on an ordinary Kubernetes cluster
// until something gives, then holds the largest fleet that converged.
//
// See specs/20260903-140000-upstream-capi-scale/spec.md. The cluster this runs
// against is built by hack/upstream-capi-scale.
package capiscale

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

var (
	kubeconfig  = flag.String("capi-kubeconfig", "", "Kubeconfig of the cluster under test.")
	kubecontext = flag.String("capi-context", "", "Context within it.")

	startClusters = flag.Int("capi-start-clusters", 25, "The first rung.")
	maxClusters   = flag.Int("capi-max-clusters", 400, "The last rung the ladder will offer.")
	rungStep      = flag.Int("capi-rung-step", 0,
		"Climb in even steps of this many clusters instead of doubling. Zero doubles, which is the shape "+
			"for a first run — it finds the neighbourhood of a ceiling cheaply. A step is the shape for the "+
			"run after it, spending rungs either side of a wall already roughly located rather than "+
			"everywhere below it. The last rung is always -capi-max-clusters, even when the step does not "+
			"divide the range evenly.")
	nodesPer      = flag.Int("capi-nodes-per-cluster", 10, "Nodes per cluster, control plane included.")
	controlPlanes = flag.Int("capi-control-plane-nodes", 3, "Of those, how many are control plane.")
	perNamespace  = flag.Int("capi-clusters-per-namespace", 10, "How many clusters share a namespace.")

	stepTimeout  = flag.Duration("capi-step-timeout", 45*time.Minute, "How long one rung may take to converge.")
	pollInterval = flag.Duration("capi-poll-interval", 15*time.Second, "How often to check a rung's progress.")
	soak         = flag.Duration("capi-soak", 30*time.Minute, "How long to hold the last rung that converged.")
	soakInterval = flag.Duration("capi-soak-interval", 5*time.Minute, "How often to sample during the soak.")

	outDir  = flag.String("capi-out", "bin", "Where the report is written.")
	outName = flag.String("capi-out-name", "capi-scale", "Its name.")
	keep    = flag.Bool("capi-keep", false, "Leave the fleet behind for inspection.")

	teardownTimeout = flag.Duration("capi-teardown-timeout", 30*time.Minute,
		"How long teardown may wait for the fleet's Clusters to go before it stops and says what remains.")

	settleTimeout = flag.Duration("capi-settle-timeout", 5*time.Minute,
		"How long to wait for the controllers' goroutine counts to stop moving before taking the baseline.")
	settleTolerance = flag.Float64("capi-settle-tolerance", 0.02,
		"How much a controller's goroutine count may move between samples and still count as settled.")

	// The API server's heap cannot be read after a forced collection here:
	// that needs profiling, and profiling cannot be turned on on this cluster.
	// The lowest of several reads is the sawtooth's floor instead. See
	// upstreamscale.LowestHeap.
	apiHeapSamples = flag.Int("capi-apiserver-heap-samples", 5,
		"How many times to read the API server's heap, taking the lowest as the floor.")
	apiHeapGap = flag.Duration("capi-apiserver-heap-gap", 2*time.Second,
		"How long between those reads.")

	// The driver's own throughput, which is not the subject of the run and
	// was silently capping it. See restConfig and create.
	createConcurrency = flag.Int("capi-create-concurrency", 16,
		"How many namespaces' objects to create at once.")
	clientQPS = flag.Float64("capi-client-qps", 200,
		"The driver's client-side rate limit. client-go defaults to 5, which throttles the run rather than the cluster.")
	clientBurst = flag.Int("capi-client-burst", 400, "Its burst.")
)

// TestStockClusterApiClimbsUntilSomethingGives is the whole run: a baseline, a
// doubling ladder, a defragmentation between rungs, and a soak of the largest
// fleet that converged.
//
// It skips rather than fails when there is no cluster. A done-condition that
// needed one would hold unrelated work hostage, which is the rule every other
// scale measurement here follows.
func TestStockClusterApiClimbsUntilSomethingGives(t *testing.T) {
	// controller-runtime routes the API server's warning headers through its
	// own logger, and prints a stack trace instead when none is set. The
	// warning is the part worth having — the API server has something to say
	// about objects this run creates, and without this it says it into a
	// goroutine dump.
	ctrl.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(os.Stderr, nil)))

	cfg, err := restConfig(*kubeconfig, *kubecontext)
	if err != nil {
		t.Skipf("could not run: no cluster (%v)", err)
	}
	// Every group the blueprint draws on, from beside the blueprint. Building
	// the scheme here by hand is what made the first run die on its first rung
	// with "no kind is registered for the type v1beta2.DevClusterTemplate".
	s, err := upstreamscale.Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Skipf("could not run: no client (%v)", err)
	}

	ctx := ctrl.SetupSignalHandler()

	sampler, err := upstreamscale.NewSampler(cfg)
	if err != nil {
		t.Fatalf("building a sampler: %v", err)
	}

	target := &upstreamscale.StockTarget{
		Client:  cl,
		Config:  cfg,
		Sampler: sampler,
		Shape: upstreamscale.FleetShape{
			ClustersPerNamespace: *perNamespace,
			ControlPlaneMachines: *controlPlanes,
			WorkerMachines:       *nodesPer - *controlPlanes,
		},
		NodesPerCluster: *nodesPer,
	}

	runner := &upstreamscale.Runner{
		Target:       target,
		Host:         cl,
		Sampler:      sampler,
		Defragmenter: upstreamscale.NewDefragmenter(cfg, mustRESTClient(t, cfg)),
		Logf:         t.Logf,
		Options: upstreamscale.RunOptions{
			StartClusters:     *startClusters,
			MaxClusters:       *maxClusters,
			RungStep:          *rungStep,
			NodesPerCluster:   *nodesPer,
			CreateConcurrency: *createConcurrency,
			SettleTolerance:   *settleTolerance,
			SettleTimeout:     *settleTimeout,
			StepTimeout:       *stepTimeout,
			PollInterval:      *pollInterval,
			Soak:              *soak,
			SoakInterval:      *soakInterval,
			TeardownTimeout:   *teardownTimeout,
			APIHeapSamples:    *apiHeapSamples,
			APIHeapGap:        *apiHeapGap,
			DriverFact: fmt.Sprintf("creates %d namespaces' objects at once, at %g QPS "+
				"(burst %d): client-go's default is 5 QPS, which times the driver rather than "+
				"the cluster", *createConcurrency, *clientQPS, *clientBurst),
		},
	}

	// Everything the run creates, torn down at the end unless asked not to.
	//
	// Registered before the run so that a half-built rung is removed too: what
	// a failed run leaves behind is what the next one measures as its baseline.
	t.Cleanup(func() {
		if *keep {
			t.Logf("NOTE: leaving %d namespaces behind (--capi-keep)", len(runner.Created))
			return
		}
		if err := runner.Teardown(context.Background()); err != nil {
			t.Errorf("teardown did not finish: %v", err)
		}
	})

	report, ceiling, runErr := runner.Run(ctx)
	if report != nil {
		if err := report.Write(*outDir, *outName); err != nil {
			t.Errorf("writing the report: %v", err)
		}
		t.Logf("report written to %s", filepath.Join(*outDir, *outName+".md"))
	}

	// A climb that measured nothing is a failure; one that found a ceiling is
	// a result, whichever rung it stopped at.
	if runErr != nil {
		t.Fatalf("%v", runErr)
	}
	_ = ceiling
}

func restConfig(kubeconfig, kubecontext string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	if _, err := os.Stat(rules.ExplicitPath); rules.ExplicitPath != "" && err != nil {
		return nil, err
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext}).ClientConfig()
	if err != nil {
		return nil, err
	}

	// client-go applies DefaultQPS 5 and DefaultBurst 10 whenever QPS is left
	// at zero, so a driver that never sets them is rate limited to five
	// requests a second. A 400 cluster rung is some 680 objects, which is over
	// two minutes of pure client-side throttling before the cluster is asked
	// to do anything — time that reads as the fleet being slow to create.
	cfg.QPS = float32(*clientQPS)
	cfg.Burst = *clientBurst
	return cfg, nil
}

func mustRESTClient(t *testing.T, cfg *rest.Config) rest.Interface {
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
