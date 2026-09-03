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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// TestABlueprintIsNamespacedWherever it is asked for. The demo builds its
// blueprint in one namespace because a workspace only ever has one; a fleet
// spread over namespaces needs the same objects in each, and a ClusterClass
// that stayed in "default" while its Clusters went elsewhere would resolve to
// nothing.
func TestABlueprintIsNamespacedWhereverItIsAskedFor(t *testing.T) {
	objects := Blueprint("scale-0007")
	if len(objects) < 5 {
		t.Fatalf("blueprint has %d objects, want the class and its templates", len(objects))
	}
	var sawClass bool
	for _, o := range objects {
		if got := o.GetNamespace(); got != "scale-0007" {
			t.Errorf("%T %s is in namespace %q", o, o.GetName(), got)
		}
		// Type assertion rather than TypeMeta: objects built in Go carry an
		// empty Kind until a client sets it, so matching on the string would
		// pass for a blueprint with no class in it at all.
		if _, ok := o.(*clusterv1.ClusterClass); ok {
			sawClass = true
		}
	}
	if !sawClass {
		t.Error("no ClusterClass in the blueprint: the Clusters have nothing to name")
	}

	// Built fresh each time. Handing out one shared slice would let a caller
	// that re-namespaced a second fleet silently move the first one.
	again := Blueprint("scale-0008")
	if objects[0].GetNamespace() != "scale-0007" {
		t.Errorf("building a second blueprint moved the first: %s", objects[0].GetNamespace())
	}
	if again[0].GetNamespace() != "scale-0008" {
		t.Errorf("the second blueprint is in %s", again[0].GetNamespace())
	}
}

func TestAFleetIsNamedPredictably(t *testing.T) {
	// Zero-padded, because a run creates these in a loop and a report that
	// sorts them lexically should sort them in the order they were made.
	if got := NamespaceName(7); got != "capi-scale-0007" {
		t.Errorf("namespace = %q", got)
	}
	if got := ClusterName(3); got != "c0003" {
		t.Errorf("cluster = %q", got)
	}
}

// TestAFleetSpreadsClustersOverNamespaces the way the kcp runs spread them over
// workspaces, so the same shape argument applies to both instruments.
func TestAFleetSpreadsClustersOverNamespaces(t *testing.T) {
	fleet := PlanFleet(FleetShape{Clusters: 10, ClustersPerNamespace: 5, ControlPlaneMachines: 3, WorkerMachines: 7})

	if len(fleet.Namespaces) != 2 {
		t.Fatalf("namespaces = %d, want 2", len(fleet.Namespaces))
	}
	total := 0
	for _, ns := range fleet.Namespaces {
		total += len(ns.Clusters)
	}
	if total != 10 {
		t.Errorf("clusters = %d, want 10", total)
	}
	if got := fleet.Machines(); got != 100 {
		t.Errorf("machines = %d, want 100 (10 clusters x 10 nodes)", got)
	}

	// Names are unique across the whole fleet, not just within a namespace: a
	// report that attributed two clusters to one name would be counting one of
	// them twice.
	seen := map[string]bool{}
	for _, ns := range fleet.Namespaces {
		for _, c := range ns.Clusters {
			key := ns.Name + "/" + c
			if seen[key] {
				t.Errorf("duplicate cluster %s", key)
			}
			seen[key] = true
		}
	}

	// A remainder is not dropped. Asking for 10 clusters and getting 8 would
	// be a fleet two smaller than the rung claims.
	odd := PlanFleet(FleetShape{Clusters: 10, ClustersPerNamespace: 3, ControlPlaneMachines: 1, WorkerMachines: 0})
	got := 0
	for _, ns := range odd.Namespaces {
		got += len(ns.Clusters)
	}
	if got != 10 {
		t.Errorf("clusters = %d with 3 per namespace, want 10", got)
	}
}

func TestAShapeWithNoControlPlaneIsRefused(t *testing.T) {
	err := FleetShape{Clusters: 1, ClustersPerNamespace: 1, ControlPlaneMachines: 0}.Validate()
	if err == nil {
		t.Fatal("a cluster with no control plane machines was accepted")
	}
	if !strings.Contains(err.Error(), "control plane") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if err := (FleetShape{Clusters: 1, ClustersPerNamespace: 1, ControlPlaneMachines: 1}).Validate(); err != nil {
		t.Errorf("a one-node cluster was refused: %v", err)
	}
	// The port range is the provider's, and a shape past it cannot run.
	if err := (FleetShape{Clusters: 20000, ClustersPerNamespace: 1, ControlPlaneMachines: 1}).Validate(); err == nil {
		t.Error("a fleet past the in-memory port range was accepted")
	}
}

// TestEveryBlueprintObjectHasAKindInTheScheme is the test the first real run
// earned.
//
// The driver registered the core Cluster API types and none of the four other
// groups the blueprint draws on, so the first rung died on
// "no kind is registered for the type v1beta2.DevClusterTemplate" — after
// creating a namespace, before creating anything worth measuring. The knowledge
// of which schemes a blueprint needs belongs beside the blueprint rather than in
// whatever happens to apply it, and this is what keeps the two in step: add an
// object to Blueprint whose group Scheme does not carry, and this fails.
func TestEveryBlueprintObjectHasAKindInTheScheme(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	objects := Blueprint("capi-scale-0000")
	for _, cluster := range Clusters("capi-scale-0000", []string{"c0000"}, FleetShape{ControlPlaneMachines: 1}) {
		objects = append(objects, cluster)
	}

	if len(objects) < 6 {
		t.Fatalf("only %d objects: this test is not exercising the blueprint", len(objects))
	}
	for _, obj := range objects {
		kinds, _, err := s.ObjectKinds(obj)
		if err != nil {
			t.Errorf("%T is not in the scheme: %v", obj, err)
			continue
		}
		if len(kinds) == 0 {
			t.Errorf("%T has no kind", obj)
		}
	}

	// And the types the run reads back while waiting, which are not in the
	// blueprint and are just as necessary.
	for _, list := range []runtime.Object{&clusterv1.ClusterList{}, &clusterv1.MachineList{}} {
		if _, _, err := s.ObjectKinds(list); err != nil {
			t.Errorf("%T is not in the scheme: %v", list, err)
		}
	}
}
