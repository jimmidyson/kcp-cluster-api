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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// ControlPlane samples every API server by name.
//
// kubeadm's static pods, one per control plane node, rather than one read
// through the cluster's own kubeconfig: that addresses the VIP and lands on
// whichever instance the load balancer picked, which is what every stock figure
// recorded before this was. Where the pod proxy cannot reach them the reading
// falls back to the endpoint and carries the caveat — see Sampler.ControlPlanes.
func (s *StockTarget) ControlPlane(ctx context.Context, host client.Client,
	heapSamples int, heapGap time.Duration,
) ([]deployedscale.ComponentSample, string, error) {
	loc := KubeAPIServers()
	reading, err := s.Sampler.ControlPlanes(ctx, host, loc, heapSamples, heapGap)
	if err != nil {
		return nil, "", err
	}
	return reading.Samples(loc.Component), reading.Describe(), nil
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
			if err := s.Client.Create(groupCtx, namespace); err != nil && !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating namespace %s: %w", ns.Name, err)
			}
			mu.Lock()
			created = append(created, ns.Name)
			mu.Unlock()

			// The blueprint in dependency order, the class last, so that by
			// the time it exists everything it refers to does.
			for _, obj := range Blueprint(ns.Name) {
				if err := s.Client.Create(groupCtx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("creating %T in %s: %w", obj, ns.Name, err)
				}
			}
			for _, cluster := range Clusters(ns.Name, ns.Clusters, fleet.Shape) {
				if err := s.Client.Create(groupCtx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					return fmt.Errorf("creating cluster %s/%s: %w", ns.Name, cluster.Name, err)
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
