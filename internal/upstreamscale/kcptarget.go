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

	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"golang.org/x/sync/errgroup"
)

// Tenancy is how a kcp run makes a tenant and reaches inside it.
//
// # Why the target does not do this itself
//
// Making a workspace is several things that need a kcp: a Workspace object in
// :root, a wait for its initializers, a client rebased onto the logical cluster
// it became, and an APIBinding per provider with that provider's own computed
// permission claims. All of it needs a running shard, published exports and a
// reachable virtual workspace — which is a cluster, and which no unit test has.
//
// Behind this interface, the rest of the kcp side is ordinary code: it creates
// the same objects the stock side creates, counts them the same way, and is
// tested the same way.
//
// Ensure is called from several goroutines at once — a rung's workspaces are
// made concurrently — so an implementation that keeps state has to say so to
// itself.
type Tenancy interface {
	// Preflight checks the shard can serve what the run is about to create,
	// before anything is created. The kcp analogue of asking a cluster whether
	// the CRDs are installed.
	Preflight(ctx context.Context) error
	// Ensure makes the workspace and returns a client scoped to it.
	Ensure(ctx context.Context, name string) (client.Client, error)
	// Remove deletes it.
	Remove(ctx context.Context, name string) error
	// Gone reports whether it has finished going. Deleting a workspace is not
	// instant, and one still going is one the next run measures.
	Gone(ctx context.Context, name string) (bool, error)
}

// KcpTarget is this project's Cluster API on a kcp shard: the tenancy unit is a
// Workspace, the fleet lives in the shard rather than in the hosting cluster's
// API server, and the store is the etcd the run deployed.
//
// It is the other side of StockTarget, and everything the two do differently is
// on this file and that one. The ladder, the settle, the sampling, the
// defragmentation, the soak and the report are the Runner's, once.
type KcpTarget struct {
	// Tenancy makes workspaces. See Tenancy.
	Tenancy Tenancy
	// Shard reads one shard replica's metrics. Supplied rather than built here
	// because the shard serves them to an authenticated caller on its own
	// secure port, which the API server's pod proxy cannot be — see
	// Sampler.ControlPlanesVia.
	Shard ControlPlaneScraper
	// Sampler reads the processes on the hosting cluster.
	Sampler *Sampler

	// Namespace is where the shard, its store and the four managers run on the
	// hosting cluster. Not where the fleet lives: that is inside kcp.
	Namespace string

	// Shape is everything but the cluster count, which the ladder supplies.
	Shape FleetShape
	// NodesPerCluster is carried for the report.
	NodesPerCluster int

	// tenants is every workspace this run made and the client that reaches
	// into it, which is how the fleet is counted: there is no listing across
	// logical clusters, so a rung's convergence is the sum over workspaces.
	mu      sync.Mutex
	tenants map[string]client.Client
}

var _ Target = (*KcpTarget)(nil)

func (k *KcpTarget) Name() string { return "kcp" }

func (k *KcpTarget) Title(startClusters, nodes int) string {
	return fmt.Sprintf("Cluster API on kcp: climbing from %d clusters at %d nodes each",
		startClusters, nodes)
}

// Facts are the same facts the stock side reports, with this side's answers.
// A key one side has and the other does not is a report a reader cannot diff.
func (k *KcpTarget) Facts() map[string]string {
	return map[string]string{
		"side":              "this project's Cluster API on a kcp shard",
		"clusterApi":        "this repository's fork, as four workspace-aware managers",
		"devClusterBackend": "inMemory",
		"tenancy":           "Workspace",
		"clustersPerTenant": fmt.Sprint(k.Shape.ClustersPerNamespace),
	}
}

func (k *KcpTarget) Prepare(ctx context.Context) error { return k.Tenancy.Preflight(ctx) }

// Controllers is the four managers as this run deployed them: four Deployments
// in one namespace, rather than the four namespaces clusterctl chooses.
func (k *KcpTarget) Controllers() []Controller {
	components := deployedscale.Components()
	out := make([]Controller, 0, len(components))
	for _, c := range components {
		out = append(out, Controller{
			Name:      c.Name,
			Namespace: k.Namespace,
			// The Deployment, the container inside it and the name the report
			// attributes cost to are all the binary's name here. See
			// deployedscale.Component.
			Deployment: c.Name,
			Container:  c.Name,
		})
	}
	return out
}

// Store is the etcd this run deployed, not the hosting cluster's own — which is
// kubeadm's, belongs to the cluster kcp happens to be running on, and holds
// none of this fleet.
func (k *KcpTarget) Store() StoreLocation { return DeployedStore(k.Namespace) }

// ShardLocation is where the shard's replicas are, for sampling.
func (k *KcpTarget) ShardLocation() ControlPlaneLocation {
	return ControlPlaneLocation{
		Namespace: k.Namespace,
		Labels:    map[string]string{deployedscale.ComponentLabel: deployedscale.KcpName},
		Component: deployedscale.KcpName,
		Scheme:    "https",
		Port:      deployedscale.KcpPort,
	}
}

// ControlPlane samples every shard replica.
//
// Every one, because each holds its own watch cache and pays for the fleet in
// full — the same reason the stock side reads its three API servers apart.
func (k *KcpTarget) ControlPlane(ctx context.Context, host client.Client,
	heapSamples int, heapGap time.Duration,
) ([]deployedscale.ComponentSample, string, error) {
	loc := k.ShardLocation()
	reading, err := k.Sampler.ControlPlanesVia(ctx, host, loc, k.Shard, heapSamples, heapGap)
	if err != nil {
		return nil, "", err
	}
	return reading.Samples(loc.Component), reading.Describe(), nil
}

func (k *KcpTarget) Plan(clusters int) (Fleet, error) {
	shape := k.Shape
	shape.Clusters = clusters
	if err := shape.Validate(); err != nil {
		return Fleet{}, err
	}
	return PlanFleet(shape), nil
}

// Create makes a rung's workspaces and fills each with the blueprint and its
// clusters, several workspaces at once.
//
// The same objects the stock side creates in a namespace, in the workspace's
// own default namespace — a workspace has one, which is why the demo puts
// everything there and why nothing here creates a Namespace.
//
// The workspaces created are returned even when one fails, so that a half-built
// rung is torn down rather than left for the next run to measure as its
// baseline.
func (k *KcpTarget) Create(ctx context.Context, fleet Fleet, concurrency int) ([]string, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu      sync.Mutex
		created []string
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)
	for _, ws := range fleet.Namespaces {
		group.Go(func() error {
			// A rung re-creates the whole fleet, so most of its workspaces
			// already exist. Making one again is idempotent and not free: it
			// re-binds every export in every existing workspace at every rung,
			// which is the driver's own work showing up as the rung being slow
			// to create. Only what this rung actually added is returned, so
			// that a name is torn down once and a rung's timing is its own.
			cl, known := k.tenant(ws.Name)
			if !known {
				made, err := k.Tenancy.Ensure(groupCtx, ws.Name)
				if err != nil {
					return fmt.Errorf("creating workspace %s: %w", ws.Name, err)
				}
				cl = made
				mu.Lock()
				created = append(created, ws.Name)
				mu.Unlock()
				k.remember(ws.Name, cl)
			}

			// In dependency order, the class last, so that by the time it
			// exists everything it refers to does.
			blueprint := Blueprint(demo.Namespace)
			for _, obj := range blueprint {
				what := fmt.Sprintf("creating %T in %s", obj, ws.Name)
				if err := createRetrying(groupCtx, cl, obj, what); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			// The same wait as the stock side, and for the same reason: both
			// instruments have to ask the fleet for the same amount of
			// admission work. See WaitForBlueprint.
			if class, ok := ClassOf(blueprint); ok {
				if err := WaitForBlueprint(groupCtx, cl, demo.Namespace, class.Name); err != nil {
					return fmt.Errorf("%s: %w", ws.Name, err)
				}
			}
			// No class namespace: the class is in this workspace's one
			// namespace alongside its Clusters, which is what a workspace
			// being an isolation boundary means. See Blueprint.
			for _, cluster := range Clusters(demo.Namespace, "", ws.Clusters, fleet.Shape) {
				what := fmt.Sprintf("creating cluster %s in %s", cluster.Name, ws.Name)
				if err := createRetrying(groupCtx, cl, cluster, what); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			return nil
		})
	}
	err := group.Wait()
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), created...), err
}

// Converged counts the fleet across every workspace.
//
// One list per workspace and summed. There is no listing across logical
// clusters from outside, and counting one workspace and multiplying would
// report a fleet nobody looked at.
func (k *KcpTarget) Converged(ctx context.Context, wantClusters, wantMachines int) (Convergence, error) {
	var (
		clusters []clusterv1.Cluster
		machines []clusterv1.Machine
	)
	for name, cl := range k.tenantClients() {
		var inWorkspace clusterv1.ClusterList
		if err := cl.List(ctx, &inWorkspace); err != nil {
			return Convergence{}, fmt.Errorf("listing clusters in %s: %w", name, err)
		}
		clusters = append(clusters, inWorkspace.Items...)

		var theirMachines clusterv1.MachineList
		if err := cl.List(ctx, &theirMachines); err != nil {
			return Convergence{}, fmt.Errorf("listing machines in %s: %w", name, err)
		}
		machines = append(machines, theirMachines.Items...)
	}
	return Converged(clusters, machines, wantClusters, wantMachines), nil
}

// Teardown deletes every workspace the run made and waits for them to go.
//
// Deleting a workspace deletes everything in it, so there is no ordering to get
// right here as there is on the stock side — but the wait is the same
// requirement: a workspace still going is one the next run measures on its way
// out.
func (k *KcpTarget) Teardown(ctx context.Context, created []string, timeout, poll time.Duration,
	logf func(string, ...any),
) error {
	log := func(format string, args ...any) {
		if logf != nil {
			logf(format, args...)
		}
	}
	if len(created) == 0 {
		return nil
	}

	log("tearing down %d workspaces", len(created))
	var failed []string
	for _, name := range created {
		if err := k.Tenancy.Remove(ctx, name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
		k.forget(name)
	}

	deadline := time.Now().Add(timeout)
	for {
		var remaining []string
		for _, name := range created {
			gone, err := k.Tenancy.Gone(ctx, name)
			if err != nil {
				remaining = append(remaining, fmt.Sprintf("%s (%v)", name, err))
				continue
			}
			if !gone {
				remaining = append(remaining, name)
			}
		}
		if len(remaining) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d workspace(s) had not gone after %s: %v",
				len(remaining), timeout, remaining)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("could not delete %d workspace(s): %v", len(failed), failed)
	}
	return nil
}

func (k *KcpTarget) remember(name string, cl client.Client) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.tenants == nil {
		k.tenants = map[string]client.Client{}
	}
	k.tenants[name] = cl
}

// tenant is the client for a workspace this run already made, if it did.
func (k *KcpTarget) tenant(name string) (client.Client, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cl, ok := k.tenants[name]
	return cl, ok
}

func (k *KcpTarget) forget(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.tenants, name)
}

// tenantClients is a copy, so that counting a fleet does not hold the lock
// against a rung still creating workspaces.
func (k *KcpTarget) tenantClients() map[string]client.Client {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]client.Client, len(k.tenants))
	for name, cl := range k.tenants {
		out[name] = cl
	}
	return out
}
