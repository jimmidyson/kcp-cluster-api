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
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
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
	}
}

// Prepare checks the cluster serves every kind the run is about to create.
//
// The one risk no unit test can find: the objects come from this repository's
// fork of Cluster API and the CRDs from whatever clusterctl installed, and a
// disagreement between them surfaces one namespace into a climb as an
// admission error naming the object rather than the installation.
func (s *StockTarget) Prepare(_ context.Context) error {
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
	return Preflight(served)
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

			// The blueprint in dependency order, the class last, so that by
			// the time it exists everything it refers to does.
			for _, obj := range Blueprint(ns.Name) {
				what := fmt.Sprintf("creating %T in %s", obj, ns.Name)
				if err := createRetrying(groupCtx, s.Client, obj, what); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			for _, cluster := range Clusters(ns.Name, ns.Clusters, fleet.Shape) {
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

// Converged counts every Cluster and Machine on the cluster against what the
// rung asked for.
func (s *StockTarget) Converged(ctx context.Context, wantClusters, wantMachines int) (Convergence, error) {
	var clusters clusterv1.ClusterList
	if err := s.Client.List(ctx, &clusters); err != nil {
		return Convergence{}, fmt.Errorf("listing clusters: %w", err)
	}
	var machines clusterv1.MachineList
	if err := s.Client.List(ctx, &machines); err != nil {
		return Convergence{}, fmt.Errorf("listing machines: %w", err)
	}
	return Converged(clusters.Items, machines.Items, wantClusters, wantMachines), nil
}

func (s *StockTarget) Teardown(ctx context.Context, created []string, timeout, poll time.Duration,
	logf func(string, ...any),
) error {
	return Teardown(ctx, s.Client, created, timeout, poll, logf)
}
