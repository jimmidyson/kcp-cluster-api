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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// fakeTenancy is workspaces without a kcp: each one a client of its own, which
// is the only thing about a workspace this target depends on.
type fakeTenancy struct {
	failAt  int
	made    []string
	removed []string
	clients map[string]client.Client
}

func (f *fakeTenancy) Preflight(context.Context) error { return nil }

func newFakeTenancy(t *testing.T) *fakeTenancy {
	t.Helper()
	return &fakeTenancy{clients: map[string]client.Client{}}
}

func (f *fakeTenancy) Ensure(_ context.Context, name string) (client.Client, error) {
	if f.failAt != 0 && len(f.made) == f.failAt {
		return nil, errors.New("the shard said no")
	}
	s, err := Scheme()
	if err != nil {
		return nil, err
	}
	// The status subresource, because a workspace's clusters become ready by
	// their conditions and the fake client refuses a status write to a type it
	// was not told has one.
	cl := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&clusterv1.Cluster{}, &clusterv1.Machine{}).Build()
	f.made = append(f.made, name)
	f.clients[name] = cl
	return cl, nil
}

func (f *fakeTenancy) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	delete(f.clients, name)
	return nil
}

func (f *fakeTenancy) Gone(_ context.Context, name string) (bool, error) {
	_, still := f.clients[name]
	return !still, nil
}

func testKcpTarget(t *testing.T, tenancy *fakeTenancy) *KcpTarget {
	t.Helper()
	return &KcpTarget{
		Tenancy:   tenancy,
		Namespace: "kcp-scale",
		Shape: FleetShape{
			ClustersPerNamespace: 4, ControlPlaneMachines: 1, WorkerMachines: 2,
		},
		NodesPerCluster: 3,
	}
}

// TestBothSidesPlanTheSameFleet.
//
// This is the comparison at its narrowest: given the same shape, the two sides
// must resolve it to the same tenants holding the same clusters. A difference
// here would be a difference in what was created rather than in what it cost,
// and every figure downstream would carry it silently.
func TestBothSidesPlanTheSameFleet(t *testing.T) {
	shape := FleetShape{ClustersPerNamespace: 4, ControlPlaneMachines: 1, WorkerMachines: 2}
	stock := &StockTarget{Shape: shape}
	kcp := testKcpTarget(t, newFakeTenancy(t))

	stockFleet, err := stock.Plan(10)
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	kcpFleet, err := kcp.Plan(10)
	if err != nil {
		t.Fatalf("kcp: %v", err)
	}

	if len(stockFleet.Namespaces) != len(kcpFleet.Namespaces) {
		t.Fatalf("%d tenants against %d", len(stockFleet.Namespaces), len(kcpFleet.Namespaces))
	}
	for i := range stockFleet.Namespaces {
		a, b := stockFleet.Namespaces[i], kcpFleet.Namespaces[i]
		if a.Name != b.Name {
			t.Errorf("tenant %d is %q on one side and %q on the other", i, a.Name, b.Name)
		}
		if strings.Join(a.Clusters, ",") != strings.Join(b.Clusters, ",") {
			t.Errorf("tenant %d holds %v on one side and %v on the other", i, a.Clusters, b.Clusters)
		}
	}
	if stockFleet.Machines() != kcpFleet.Machines() {
		t.Errorf("%d Machines against %d", stockFleet.Machines(), kcpFleet.Machines())
	}
}

// TestBothSidesSayTheSameThingsAboutThemselves, in the same words. A report
// carrying a fact one side does not is a report a reader cannot diff.
func TestBothSidesSayTheSameThingsAboutThemselves(t *testing.T) {
	stock := (&StockTarget{Shape: FleetShape{ClustersPerNamespace: 4}}).Facts()
	kcp := testKcpTarget(t, newFakeTenancy(t)).Facts()

	for key := range stock {
		if _, ok := kcp[key]; !ok {
			t.Errorf("the stock side reports %q and the kcp side does not", key)
		}
	}
	for key := range kcp {
		if _, ok := stock[key]; !ok {
			t.Errorf("the kcp side reports %q and the stock side does not", key)
		}
	}
	if kcp["tenancy"] != "Workspace" {
		t.Errorf("the kcp side calls its tenant a %q", kcp["tenancy"])
	}
	if stock["tenancy"] == kcp["tenancy"] {
		t.Error("the two sides do not say what a tenant is on each")
	}
}

// TestEveryWorkspaceGetsTheBlueprintAndItsClusters.
//
// The same objects the stock side puts in a namespace, in the workspace's own
// default namespace — a workspace has one, which is why the demo puts
// everything there.
func TestEveryWorkspaceGetsTheBlueprintAndItsClusters(t *testing.T) {
	tenancy := newFakeTenancy(t)
	target := testKcpTarget(t, tenancy)

	fleet, err := target.Plan(6)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	made, err := target.Create(context.Background(), fleet, 2)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if len(made) != len(fleet.Namespaces) {
		t.Fatalf("made %v, want %d workspaces", made, len(fleet.Namespaces))
	}

	for _, ws := range fleet.Namespaces {
		cl, ok := tenancy.clients[ws.Name]
		if !ok {
			t.Fatalf("no workspace %s", ws.Name)
		}
		var classes clusterv1.ClusterClassList
		if err := cl.List(context.Background(), &classes); err != nil {
			t.Fatalf("listing classes in %s: %v", ws.Name, err)
		}
		if len(classes.Items) != 1 {
			t.Errorf("%s holds %d ClusterClasses, want the one every Cluster names", ws.Name, len(classes.Items))
		} else if classes.Items[0].Namespace != demo.Namespace {
			t.Errorf("the class is in namespace %q, want the workspace's own %q",
				classes.Items[0].Namespace, demo.Namespace)
		}

		var clusters clusterv1.ClusterList
		if err := cl.List(context.Background(), &clusters); err != nil {
			t.Fatalf("listing clusters in %s: %v", ws.Name, err)
		}
		if len(clusters.Items) != len(ws.Clusters) {
			t.Errorf("%s holds %d clusters, want %d", ws.Name, len(clusters.Items), len(ws.Clusters))
		}
	}
}

// TestAHalfBuiltFleetStillNamesTheWorkspacesItMade, so teardown removes them.
// A workspace left behind is measured as the next run's baseline.
func TestAHalfBuiltFleetStillNamesTheWorkspacesItMade(t *testing.T) {
	tenancy := newFakeTenancy(t)
	tenancy.failAt = 2
	target := testKcpTarget(t, tenancy)

	fleet, err := target.Plan(12)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	made, err := target.Create(context.Background(), fleet, 1)
	if err == nil {
		t.Fatal("a creation that failed reported success")
	}
	if len(made) != 2 {
		t.Errorf("made %v, want the two workspaces that were created", made)
	}
}

// TestTheFleetIsCountedAcrossEveryWorkspace.
//
// There is no listing across logical clusters here: a workspace is asked what
// it holds, and the rung's convergence is the sum. Counting one workspace and
// multiplying would report a fleet nobody looked at.
func TestTheFleetIsCountedAcrossEveryWorkspace(t *testing.T) {
	tenancy := newFakeTenancy(t)
	target := testKcpTarget(t, tenancy)
	ctx := context.Background()

	fleet, err := target.Plan(8)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if _, err := target.Create(ctx, fleet, 2); err != nil {
		t.Fatalf("creating: %v", err)
	}

	// Nothing is ready yet.
	got, err := target.Converged(ctx, 8, 24)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if got.Done || got.ControlPlanesReady != 0 {
		t.Errorf("an unconverged fleet counted as %s", got.Describe())
	}

	// Make every cluster's control plane available, in every workspace.
	for _, ws := range fleet.Namespaces {
		cl := tenancy.clients[ws.Name]
		var clusters clusterv1.ClusterList
		if err := cl.List(ctx, &clusters); err != nil {
			t.Fatalf("listing: %v", err)
		}
		for i := range clusters.Items {
			c := &clusters.Items[i]
			c.Status.Conditions = []metav1.Condition{{
				Type: clusterv1.ClusterControlPlaneAvailableCondition, Status: metav1.ConditionTrue,
				Reason: "Ready", LastTransitionTime: metav1.Now(),
			}}
			if err := cl.Status().Update(ctx, c); err != nil {
				t.Fatalf("marking %s ready: %v", c.Name, err)
			}
		}
	}

	got, err = target.Converged(ctx, 8, 0)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if got.ControlPlanesReady != 8 {
		t.Errorf("counted %d control planes over %d workspaces, want 8",
			got.ControlPlanesReady, len(fleet.Namespaces))
	}
}

// TestTeardownRemovesEveryWorkspaceAndWaitsForIt. A workspace deleted and not
// waited for is one the next run measures on its way out.
func TestTeardownRemovesEveryWorkspaceAndWaitsForIt(t *testing.T) {
	tenancy := newFakeTenancy(t)
	target := testKcpTarget(t, tenancy)
	ctx := context.Background()

	fleet, err := target.Plan(8)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	made, err := target.Create(ctx, fleet, 2)
	if err != nil {
		t.Fatalf("creating: %v", err)
	}

	if err := target.Teardown(ctx, made, time.Second, time.Millisecond, nil); err != nil {
		t.Fatalf("tearing down: %v", err)
	}
	if len(tenancy.removed) != len(made) {
		t.Errorf("removed %v of %v", tenancy.removed, made)
	}
	if len(tenancy.clients) != 0 {
		t.Errorf("%d workspaces are still there", len(tenancy.clients))
	}
}

// TestTheManagersAreWhereTheRunPutThem. On this side they are four Deployments
// in the run's own namespace, not four namespaces clusterctl chose.
func TestTheManagersAreWhereTheRunPutThem(t *testing.T) {
	target := testKcpTarget(t, newFakeTenancy(t))
	controllers := target.Controllers()

	if len(controllers) != len(deployedscale.Components()) {
		t.Fatalf("%d controllers, want one per manager", len(controllers))
	}
	for _, c := range controllers {
		if c.Namespace != "kcp-scale" {
			t.Errorf("%s is looked for in %s", c.Deployment, c.Namespace)
		}
		if c.Container == "" {
			t.Errorf("%s names no container, so its readiness and limit read as zero", c.Deployment)
		}
	}
}

// TestTheStoreIsTheOneTheRunDeployed, not kubeadm's — which is the store of the
// cluster kcp happens to be running on, and has nothing to do with this fleet.
func TestTheStoreIsTheOneTheRunDeployed(t *testing.T) {
	target := testKcpTarget(t, newFakeTenancy(t))
	store := target.Store()
	if store.Namespace != "kcp-scale" {
		t.Errorf("the store is looked for in %s", store.Namespace)
	}
	if store.MetricsPort != EtcdMetricsPort {
		t.Errorf("metrics port = %d", store.MetricsPort)
	}
}

// TestTheShardIsSampledReplicaByReplica.
//
// Three replicas, each holding its own watch cache. Sampling one and calling it
// the control plane is the mistake the stock side made for six runs.
func TestTheShardIsSampledReplicaByReplica(t *testing.T) {
	target := testKcpTarget(t, newFakeTenancy(t))
	target.Shard = &fakeShard{}

	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	loc := target.ShardLocation()
	host := fake.NewClientBuilder().WithScheme(s).WithObjects(
		shardPod("kcp-abc-2", loc), shardPod("kcp-abc-0", loc), shardPod("kcp-abc-1", loc),
	).Build()
	target.Sampler = &Sampler{}

	samples, described, err := target.ControlPlane(context.Background(), host, 1, 0)
	if err != nil {
		t.Fatalf("sampling the shard: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("%d samples for three replicas", len(samples))
	}
	seen := map[string]bool{}
	for _, s := range samples {
		if seen[s.Component] {
			t.Errorf("two replicas are called %q", s.Component)
		}
		seen[s.Component] = true
	}
	if !strings.Contains(described, "3 instances") {
		t.Errorf("the line does not say this is three processes: %q", described)
	}
}

func shardPod(name string, loc ControlPlaneLocation) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: loc.Namespace, Labels: loc.Labels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// fakeShard is an exposition without a shard behind it.
type fakeShard struct{}

func (f *fakeShard) Metrics(_ context.Context, pod string) ([]byte, error) {
	return []byte(fmt.Sprintf(`go_goroutines %d
go_memstats_heap_alloc_bytes 1.0e+09
process_resident_memory_bytes 2.0e+09
apiserver_storage_objects{resource="clusters.cluster.x-k8s.io"} 8
`, 100+len(pod))), nil
}

func (f *fakeShard) ForceCollection(context.Context, string) error { return nil }
