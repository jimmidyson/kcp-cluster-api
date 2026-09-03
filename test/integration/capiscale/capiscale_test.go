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
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

var (
	kubeconfig  = flag.String("capi-kubeconfig", "", "Kubeconfig of the cluster under test.")
	kubecontext = flag.String("capi-context", "", "Context within it.")

	startClusters = flag.Int("capi-start-clusters", 25, "The first rung.")
	maxClusters   = flag.Int("capi-max-clusters", 400, "The last rung the ladder will offer.")
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
)

// TestStockClusterApiClimbsUntilSomethingGives is the whole run: a baseline, a
// doubling ladder, a defragmentation between rungs, and a soak of the largest
// fleet that converged.
//
// It skips rather than fails when there is no cluster. A done-condition that
// needed one would hold unrelated work hostage, which is the rule every other
// scale measurement here follows.
func TestStockClusterApiClimbsUntilSomethingGives(t *testing.T) {
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

	// Preflight before anything is created. This is the one risk no test of
	// this code can find: the objects come from this repository's fork and the
	// CRDs from whatever clusterctl installed.
	if err := preflight(ctx, cfg); err != nil {
		t.Fatalf("this cluster cannot serve what the run creates:\n%v", err)
	}

	shape := upstreamscale.FleetShape{
		ClustersPerNamespace: *perNamespace,
		ControlPlaneMachines: *controlPlanes,
		WorkerMachines:       *nodesPer - *controlPlanes,
	}
	report := &deployedscale.Report{Title: fmt.Sprintf(
		"Stock Cluster API: climbing from %d clusters at %d nodes each",
		*startClusters, *nodesPer)}
	report.AddFact("clusterApi", "stock upstream, installed by clusterctl")
	report.AddFact("devClusterBackend", "inMemory")
	report.AddFact("nodesPerCluster", fmt.Sprint(*nodesPer))
	report.AddFact("clustersPerNamespace", fmt.Sprint(*perNamespace))
	report.AddFact("endState", "every control plane ready and every Machine Ready")
	report.AddFact("heapSample", "after a forced collection, so live heap is the retained set")

	sampler, err := upstreamscale.NewSampler(cfg)
	if err != nil {
		t.Fatalf("building a sampler: %v", err)
	}
	controllers := upstreamscale.Controllers()
	defragmenter := upstreamscale.NewDefragmenter(cfg, mustRESTClient(t, cfg))

	// Everything this run creates, torn down at the end unless asked not to.
	//
	// Clusters first, then namespaces, and never the other way round: stock
	// Cluster API cannot finish deleting a namespace whose objects were all
	// stamped at once. The first run left every namespace Terminating that
	// way. See upstreamscale.Teardown. A teardown that does not finish is a
	// failure of the run, after the report is written: what it leaves behind
	// is what the next run would measure as its baseline.
	var created []string
	t.Cleanup(func() {
		if *keep {
			t.Logf("NOTE: leaving %d namespaces behind (--capi-keep)", len(created))
			return
		}
		if err := upstreamscale.Teardown(context.Background(), cl, created, *teardownTimeout, *pollInterval, t.Logf); err != nil {
			t.Errorf("teardown did not finish: %v", err)
		}
	})

	sample := func(label string, clusters, machines int) {
		components, throttling, err := sampler.Sample(ctx, cl, controllers)
		if err != nil {
			t.Logf("NOTE: could not sample the controllers at %s: %v", label, err)
			return
		}
		if api, err := sampler.APIServer(ctx); err == nil {
			components = append(components, deployedscale.ComponentSample{
				Component: "kube-apiserver",
				Process:   api.Process,
				Pod:       deployedscale.PodFacts{Name: "kube-apiserver", Ready: true},
			})
			report.AddFact("apiserver@"+label, api.Describe())
		} else {
			t.Logf("NOTE: could not sample the API server at %s: %v", label, err)
		}
		if etcd, pod, err := sampler.Etcd(ctx, cl); err == nil {
			report.AddFact("etcd@"+label, etcd.Describe())
			if etcd.NearQuota() {
				t.Logf("WARNING at %s: %s", label, etcd.Describe())
			}
		} else {
			t.Logf("NOTE: could not sample etcd (%s) at %s: %v", pod, label, err)
		}
		for name, th := range throttling {
			if th.Significant() {
				report.AddFact("throttling@"+label+"/"+name, th.Describe())
			}
		}
		report.Add(deployedscale.Sample{
			Label: label, Workspaces: clusters, Clusters: clusters, Nodes: machines,
			Components: components,
		})
	}

	// The baseline, before any fleet exists. Every slope this run reports is a
	// difference between two large numbers, and without this the smaller of
	// them is still a fleet.
	sample("baseline (no clusters)", 0, 0)

	var rungs []upstreamscale.RungResult
	for i, clusters := range upstreamscale.Ladder(*startClusters, *maxClusters) {
		shape.Clusters = clusters
		if err := shape.Validate(); err != nil {
			t.Fatalf("rung of %d clusters: %v", clusters, err)
		}
		fleet := upstreamscale.PlanFleet(shape)
		machines := fleet.Machines()

		// Between rungs, never inside one: a defrag is a stop-the-world
		// rewrite on the member, and the quota counts the file rather than the
		// live data in it. See upstreamscale.Defragmenter.
		if i > 0 {
			if results, err := defragmenter.All(ctx, cl, sampler); err == nil {
				report.AddFact(fmt.Sprintf("defrag@%d", clusters), upstreamscale.DescribeDefrag(results))
				t.Logf("%s", upstreamscale.DescribeDefrag(results))
			} else {
				t.Logf("NOTE: could not defragment before %d clusters: %v", clusters, err)
			}
		}

		t.Logf("=== rung: %d clusters, %d Machines", clusters, machines)
		if err := create(ctx, cl, fleet, &created); err != nil {
			rungs = append(rungs, upstreamscale.RungResult{
				Clusters: clusters, Machines: machines,
				Failure: "the fleet could not be created: " + err.Error(),
			})
			break
		}

		converged, why := wait(ctx, t, cl, sampler, controllers, clusters, machines)
		if !converged {
			sample(fmt.Sprintf("%d clusters (did not converge)", clusters), clusters, machines)
			rungs = append(rungs, upstreamscale.RungResult{
				Clusters: clusters, Machines: machines, Failure: why,
			})
			break
		}
		sample(fmt.Sprintf("%d clusters", clusters), clusters, machines)
		rungs = append(rungs, upstreamscale.RungResult{
			Clusters: clusters, Machines: machines, Converged: true,
		})
	}

	ceiling := upstreamscale.Summarise(rungs)
	report.AddFact("ceiling", ceiling.Describe())
	t.Logf("%s", ceiling.Describe())

	// Then hold it. Reaching a fleet and holding it are different questions.
	if ceiling.LastGood != nil && *soak > 0 {
		holdAndReport(ctx, t, cl, report, sample, ceiling, *soak, *soakInterval)
	}

	if err := report.Write(*outDir, *outName); err != nil {
		t.Errorf("writing the report: %v", err)
	}
	t.Logf("report written to %s", filepath.Join(*outDir, *outName+".md"))

	// A climb that measured nothing is a failure; one that found a ceiling is
	// a result, whichever rung it stopped at.
	if ceiling.LastGood == nil {
		t.Fatalf("measured nothing: %s", ceiling.Describe())
	}
}

// create applies one rung's blueprint and Clusters.
func create(ctx context.Context, cl client.Client, fleet upstreamscale.Fleet, created *[]string) error {
	for _, ns := range fleet.Namespaces {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
		if err := cl.Create(ctx, namespace); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %s: %w", ns.Name, err)
		}
		*created = append(*created, ns.Name)

		// The blueprint in dependency order, the class last, so that by the
		// time it exists everything it refers to does.
		for _, obj := range upstreamscale.Blueprint(ns.Name) {
			if err := cl.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating %T in %s: %w", obj, ns.Name, err)
			}
		}
		for _, cluster := range upstreamscale.Clusters(ns.Name, ns.Clusters, fleet.Shape) {
			if err := cl.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating cluster %s/%s: %w", ns.Name, cluster.Name, err)
			}
		}
	}
	return nil
}

// wait polls until the rung reaches the end state, or a component dies, or time
// runs out — and says which.
func wait(ctx context.Context, t *testing.T, cl client.Client, sampler *upstreamscale.Sampler,
	controllers []upstreamscale.Controller, clusters, machines int,
) (bool, string) {
	deadline := time.Now().Add(*stepTimeout)
	var last upstreamscale.Convergence

	for {
		var clusterList clusterv1.ClusterList
		var machineList clusterv1.MachineList
		if err := cl.List(ctx, &clusterList); err != nil {
			return false, "listing clusters: " + err.Error()
		}
		if err := cl.List(ctx, &machineList); err != nil {
			return false, "listing machines: " + err.Error()
		}
		last = upstreamscale.Converged(clusterList.Items, machineList.Items, clusters, machines)
		if last.Done {
			return true, ""
		}

		// A component that died is why the fleet has not arrived, rather than
		// a second thing that went wrong. Checked every poll so that a kill is
		// reported promptly rather than after the step timeout.
		if components, _, err := sampler.Sample(ctx, cl, controllers); err == nil {
			if why := upstreamscale.Classify(components, false); why != "" {
				return false, why
			}
		}

		if time.Now().After(deadline) {
			return false, fmt.Sprintf("%s (%s)",
				upstreamscale.Classify(nil, true), last.Describe())
		}
		t.Logf("    %s", last.Describe())
		select {
		case <-ctx.Done():
			return false, "interrupted: " + last.Describe()
		case <-time.After(*pollInterval):
		}
	}
}

// holdAndReport soaks the largest fleet that converged.
func holdAndReport(ctx context.Context, t *testing.T, cl client.Client, report *deployedscale.Report,
	sample func(string, int, int), ceiling upstreamscale.Ceiling, duration, interval time.Duration,
) {
	t.Logf("=== soak: holding %d clusters for %s", ceiling.LastGood.Clusters, duration)
	before := len(report.Samples)
	deadline := time.Now().Add(duration)
	for n := 0; ; n++ {
		sample(fmt.Sprintf("soak %s", time.Duration(n)*interval), ceiling.LastGood.Clusters,
			ceiling.LastGood.Machines)
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			t.Log("NOTE: the soak was interrupted")
			return
		case <-time.After(interval):
		}
	}

	// Ready at the end, which no process metric shows.
	ready := 0
	var clusterList clusterv1.ClusterList
	if err := cl.List(ctx, &clusterList); err == nil {
		for i := range clusterList.Items {
			c := upstreamscale.Converged(clusterList.Items[i:i+1], nil, 1, 0)
			ready += c.ControlPlanesReady
		}
	}
	drift := upstreamscale.Drift(report.Samples[before:], ceiling.LastGood.Clusters, ready)
	report.AddFact("soak", drift.Describe())
	t.Logf("%s", drift.Describe())
}

func preflight(ctx context.Context, cfg *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building a discovery client: %w", err)
	}
	served := map[string][]string{}
	for _, gv := range upstreamscale.NeededGroupVersions() {
		list, err := dc.ServerResourcesForGroupVersion(gv)
		if err != nil {
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
	if _, err := os.Stat(rules.ExplicitPath); rules.ExplicitPath != "" && err != nil {
		return nil, err
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{CurrentContext: kubecontext}).ClientConfig()
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
