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

package upstreamscale

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"golang.org/x/sync/errgroup"
)

// StockTarget is stock Cluster API on an ordinary Kubernetes cluster: the
// tenancy unit is a Namespace, the fleet lives in the hosting cluster's own
// API server, and the store is kubeadm's etcd.
//
// It is the side the recorded figures came from, and its behaviour here is
// exactly what test/integration/capiscale did before there were two sides —
// moved rather than rewritten, so that the numbers stay comparable with the
// ones already taken.
type StockTarget struct {
	// Client addresses the fleet, which on this side is the hosting cluster
	// itself. Sampling uses the Runner's own client, which is the same one —
	// they are separate on the kcp side and named separately here so that the
	// distinction is visible on both.
	Client client.Client
	// Config is the hosting cluster's, for the preflight's discovery.
	Config *rest.Config
	// Sampler reads the API server, which on this side is the control plane.
	Sampler *Sampler

	// Shape is everything but the cluster count, which the ladder supplies.
	Shape FleetShape
	// NodesPerCluster is carried for the report.
	NodesPerCluster int

	// fleet is where Converged reads from once Prepare has started a watch:
	// an informer cache of every Cluster and Machine, so a poll is a walk
	// over memory rather than a list of the whole fleet through the API
	// server. Nil until then, and Converged reads through Client. See watch.
	fleet client.Reader
}

var _ Target = (*StockTarget)(nil)

func (s *StockTarget) Name() string { return "stock" }

func (s *StockTarget) Title(startClusters, nodes int) string {
	return fmt.Sprintf("Stock Cluster API: climbing from %d clusters at %d nodes each",
		startClusters, nodes)
}

func (s *StockTarget) Facts() map[string]string {
	return map[string]string{
		"side":              "stock Cluster API on its own Kubernetes API server",
		"clusterApi":        "stock upstream, installed by clusterctl",
		"devClusterBackend": "inMemory",
		"tenancy":           "Namespace",
		// Per tenant rather than per namespace, in the same words the kcp side
		// uses for its workspaces: a fact one side reports and the other does
		// not is a fact a reader cannot diff the two reports on.
		"clustersPerTenant": fmt.Sprint(s.Shape.ClustersPerNamespace),
		// How the poll reads the fleet, because it decides how much of the
		// load on the API server is the harness's own. See watch.
		"convergenceRead": "an informer cache of every Cluster and Machine: one paged list per kind " +
			"and then a watch, so each poll reads memory and puts nothing on the API server",
	}
}

// Prepare checks the cluster serves every kind the run is about to create,
// then starts the watch Converged reads from.
//
// The one risk no unit test can find: the objects come from this repository's
// fork of Cluster API and the CRDs from whatever clusterctl installed, and a
// disagreement between them surfaces one namespace into a climb as an
// admission error naming the object rather than the installation.
func (s *StockTarget) Prepare(ctx context.Context) error {
	dc, err := discovery.NewDiscoveryClientForConfig(s.Config)
	if err != nil {
		return fmt.Errorf("building a discovery client: %w", err)
	}
	served := map[string][]string{}
	for _, gv := range NeededGroupVersions() {
		list, err := dc.ServerResourcesForGroupVersion(gv)
		if err != nil {
			continue
		}
		served[gv] = IndexResources([]*metav1.APIResourceList{list})[gv]
	}
	if err := Preflight(served); err != nil {
		return err
	}
	return s.watch(ctx)
}

// watch starts the informer cache Converged reads from, for the life of ctx.
//
// # Why the poll stopped listing
//
// Converged listed every Cluster and every Machine as full objects every
// fifteen seconds. At 15,000 Machines that is about 90 MB of JSON per poll,
// and all of it went through the VIP, so all of it landed on one API server —
// the one whose node's etcd member kube-vip's own lease has to write through.
// That instance served two and a half times the lists and nine times the GETs
// of its peers, and its member was the one that stalled, twice. A measurement
// tool that puts its own load on the thing it is measuring, and on precisely
// the point that fails, is measuring itself.
//
// An informer costs one list per kind, paged by the reflector, and then a
// watch carrying only what changes — which is what every controller on the
// cluster already does. The poll then reads memory. The cache holds the whole
// fleet as typed objects, which at 20,000 Machines is a few hundred megabytes
// in the harness and nothing on the cluster.
func (s *StockTarget) watch(ctx context.Context) error {
	if s.fleet != nil || s.Config == nil {
		return nil
	}
	c, err := cache.New(s.Config, cache.Options{Scheme: s.Client.Scheme()})
	if err != nil {
		return fmt.Errorf("building a cache of the fleet: %w", err)
	}
	// Both informers before the cache starts, so that WaitForCacheSync waits
	// for them rather than for nothing.
	for _, obj := range []client.Object{&clusterv1.Cluster{}, &clusterv1.Machine{}} {
		if _, err := c.GetInformer(ctx, obj); err != nil {
			return fmt.Errorf("watching %T: %w", obj, err)
		}
	}
	go func() {
		if err := c.Start(ctx); err != nil {
			// Reported once at the end of the run: the cache stops with the
			// context, and Converged falls back to the client if it never
			// started.
			fmt.Fprintf(os.Stderr, "the fleet watch stopped: %v\n", err)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		return errors.New("the fleet watch did not sync before the context ended")
	}
	s.fleet = c
	return nil
}

func (s *StockTarget) Controllers() []Controller { return Controllers() }

func (s *StockTarget) Store() StoreLocation { return KubeadmStore() }

// ControlPlane is every process on the control plane's nodes, plus the API
// server's own metrics for the one instance that can be read with credentials.
//
// # Two reads, because neither answers on its own
//
// The **node** read is the resource one, and it is the only one that can cover
// the whole control plane. The API server's /metrics needs credentials and the
// pod proxy strips them — a request through it arrives as system:anonymous and
// is refused, which is what reduced every recorded control-plane figure to one
// arbitrary instance behind the VIP. The kubelet's cAdvisor endpoint goes
// through the *node* proxy, which carries the caller's identity, and reports
// every container on the node: all three API servers, all three etcd members,
// the controller manager, the scheduler, and whatever else is up there. That is
// where the memory and CPU come from now.
//
// The **endpoint** read is the behavioural one: stored objects, requests in
// flight, requests rejected, how long the store is taking. Those exist nowhere
// else, and they come from whichever instance the VIP picked — which is fine,
// because they are about the cluster rather than about a process.
//
// A failure of the node read is fatal to the sample; a failure of the endpoint
// read is not, because the resource question survives without it.
func (s *StockTarget) ControlPlane(ctx context.Context, host client.Client,
	heapSamples int, heapGap time.Duration,
) ([]deployedscale.ComponentSample, string, error) {
	readout, err := s.Sampler.ControlPlaneNodeUsage(ctx, host)
	if len(readout.Samples) == 0 {
		return nil, "", err
	}
	described := readout.Describe()

	// One instance's own metrics, for what cAdvisor cannot see: stored objects,
	// requests in flight, requests shed, how long the store is taking. Marked
	// as one instance's, because that is what it is — those are facts about the
	// cluster rather than about a process, so one answering for them is fine
	// as long as nobody reads them as the control plane's cost.
	if api, apiErr := s.Sampler.APIServer(ctx, heapSamples, heapGap); apiErr == nil {
		described += "; one API server instance reports " + api.Describe()
	}
	return readout.Samples, described, nil
}

func (s *StockTarget) Plan(clusters int) (Fleet, error) {
	shape := s.Shape
	shape.Clusters = clusters
	if err := shape.Validate(); err != nil {
		return Fleet{}, err
	}
	return PlanFleet(shape), nil
}

// Create applies a rung's blueprint and Clusters, several namespaces at once.
//
// Namespaces run in parallel and each namespace's objects stay in order, since
// a Cluster names a ClusterClass in its own namespace and the class has to
// exist first. The namespaces created are returned even when one fails, so
// that teardown removes what a half-built rung left behind.
func (s *StockTarget) Create(ctx context.Context, fleet Fleet, concurrency int) ([]string, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu      sync.Mutex
		created []string
	)

	// One ClusterClass for the whole fleet, before anything references it.
	//
	// Serially and up front rather than inside the group, because every tenant
	// needs it and a hundred goroutines racing to create the same objects is a
	// hundred AlreadyExists conflicts to no purpose. It is also the only part
	// of a rung that cannot be done in parallel with anything, being what the
	// rest depends on.
	if err := s.blueprint(ctx); err != nil {
		return []string{BlueprintNamespace}, err
	}
	created = append(created, BlueprintNamespace)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)
	for _, ns := range fleet.Namespaces {
		group.Go(func() error {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}}
			what := fmt.Sprintf("creating namespace %s", ns.Name)
			if err := createRetrying(groupCtx, s.Client, namespace, what); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			mu.Lock()
			created = append(created, ns.Name)
			mu.Unlock()

			for _, cluster := range Clusters(ns.Name, BlueprintNamespace, ns.Clusters, fleet.Shape) {
				what := fmt.Sprintf("creating cluster %s/%s", ns.Name, cluster.Name)
				if err := createRetrying(groupCtx, s.Client, cluster, what); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			return nil
		})
	}
	return created, group.Wait()
}

// blueprint puts the fleet's one ClusterClass in place and waits for it.
//
// Idempotent, because the ladder is cumulative: every rung re-applies the whole
// fleet and only the new tenants are actually created. The wait runs every time
// regardless — a class that was reconciled for the last rung is still
// reconciled for this one, so it costs a single Get.
func (s *StockTarget) blueprint(ctx context.Context) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: BlueprintNamespace}}
	what := fmt.Sprintf("creating namespace %s", BlueprintNamespace)
	if err := createRetrying(ctx, s.Client, namespace, what); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}

	// In dependency order, the class last, so that by the time it exists
	// everything it refers to does.
	objects := Blueprint(BlueprintNamespace)
	for _, obj := range objects {
		what := fmt.Sprintf("creating %T in %s", obj, BlueprintNamespace)
		if err := createRetrying(ctx, s.Client, obj, what); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}

	// Existing is not reconciled, and a Cluster created against an unreconciled
	// class is admitted without its topology being validated. One class means
	// one wait for the whole fleet rather than one per tenant, which is the
	// other thing sharing it buys. See WaitForBlueprint.
	class, ok := ClassOf(objects)
	if !ok {
		return nil
	}
	return WaitForBlueprint(ctx, s.Client, BlueprintNamespace, class.Name)
}

// Converged counts every Cluster and Machine on the cluster against what the
// rung asked for, from the watch where there is one. See watch.
func (s *StockTarget) Converged(ctx context.Context, wantClusters, wantMachines int) (Convergence, error) {
	var reader client.Reader = s.Client
	if s.fleet != nil {
		reader = s.fleet
	}
	var clusters clusterv1.ClusterList
	if err := reader.List(ctx, &clusters); err != nil {
		return Convergence{}, fmt.Errorf("listing clusters: %w", err)
	}
	var machines clusterv1.MachineList
	if err := reader.List(ctx, &machines); err != nil {
		return Convergence{}, fmt.Errorf("listing machines: %w", err)
	}
	return Converged(clusters.Items, machines.Items, wantClusters, wantMachines), nil
}

func (s *StockTarget) Teardown(ctx context.Context, created []string, timeout, poll time.Duration,
	logf func(string, ...any),
) error {
	return Teardown(ctx, s.Client, created, timeout, poll, logf)
}
