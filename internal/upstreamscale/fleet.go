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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// KubernetesVersion is what the fleet's clusters claim to run.
//
// Nothing is installed, so this is a string the in-memory backend reports back
// rather than a version anything has to be compatible with — but it is stated
// rather than left to a default, because it appears in the report and a fleet
// whose version nobody chose is a fleet nobody can reproduce.
const KubernetesVersion = "v1.32.0"

// FleetShape is what a rung of the ladder asks for.
//
// The same three knobs as the kcp runs, with namespaces where those had
// workspaces: a cluster is a Cluster and the objects a ClusterClass stamps from
// it, and a node is a Machine.
type FleetShape struct {
	Clusters             int
	ClustersPerNamespace int
	ControlPlaneMachines int
	WorkerMachines       int
}

// Validate refuses a shape before a cluster is touched.
func (s FleetShape) Validate() error {
	var errs []error
	if s.Clusters < 1 {
		errs = append(errs, fmt.Errorf("clusters = %d, want at least 1", s.Clusters))
	}
	if s.ClustersPerNamespace < 1 {
		errs = append(errs, fmt.Errorf("clusters per namespace = %d, want at least 1", s.ClustersPerNamespace))
	}
	if s.ControlPlaneMachines < 1 {
		// A ClusterClass always names a control plane template, so a cluster
		// built from one always has a control plane object. Asking for none
		// leaves a cluster that can never report every control plane ready,
		// which is the end state the whole run is waiting for.
		errs = append(errs, errors.New("control plane machines = 0: a cluster with no control plane "+
			"never reaches the end state this run waits for"))
	}
	if s.WorkerMachines < 0 {
		errs = append(errs, fmt.Errorf("worker machines = %d", s.WorkerMachines))
	}
	if err := CheckFleetFits(s.Clusters); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// NodesPerCluster is the whole node count of one cluster, control plane
// included — the same meaning the kcp runs' nodes-per-cluster had.
func (s FleetShape) NodesPerCluster() int { return s.ControlPlaneMachines + s.WorkerMachines }

// Namespace is one namespace's worth of the fleet.
type Namespace struct {
	Name     string
	Clusters []string
}

// Fleet is a shape resolved into the names a run will create.
type Fleet struct {
	Shape      FleetShape
	Namespaces []Namespace
}

// Machines is how many Machines the whole fleet holds when it has converged.
func (f Fleet) Machines() int {
	total := 0
	for _, ns := range f.Namespaces {
		total += len(ns.Clusters) * f.Shape.NodesPerCluster()
	}
	return total
}

// PlanFleet resolves a shape into namespaces and cluster names.
//
// The remainder gets its own namespace rather than being dropped: a rung that
// asked for ten clusters and created eight is a rung reporting a fleet size it
// did not have.
func PlanFleet(shape FleetShape) Fleet {
	fleet := Fleet{Shape: shape}
	if shape.ClustersPerNamespace < 1 {
		return fleet
	}
	for created := 0; created < shape.Clusters; {
		ns := Namespace{Name: NamespaceName(len(fleet.Namespaces))}
		for i := 0; i < shape.ClustersPerNamespace && created < shape.Clusters; i++ {
			ns.Clusters = append(ns.Clusters, ClusterName(created))
			created++
		}
		fleet.Namespaces = append(fleet.Namespaces, ns)
	}
	return fleet
}

// NamespaceName and ClusterName are zero-padded so that a report listing them
// sorts them in the order they were created.
func NamespaceName(i int) string { return fmt.Sprintf("capi-scale-%04d", i) }
func ClusterName(i int) string   { return fmt.Sprintf("c%04d", i) }

// Blueprint is the ClusterClass and the templates it refers to, in one
// namespace.
//
// The objects come from internal/demo, which is where this repository's
// ClusterClass already lives — the same class the kcp runs are built from, so a
// difference between the two instruments is not a difference in what they
// asked for. What is added here is the namespace it goes in.
//
// # One class per API server, which is not one class per side
//
// Stock puts a single ClusterClass in BlueprintNamespace and every Cluster in
// the fleet references it across namespaces, because Topology.ClassRef carries
// a Namespace. kcp puts one in each workspace, because a workspace is an
// isolation boundary and a class in one is not visible in another.
//
// That asymmetry is the arrangement rather than a flaw in it. Both sides run
// what an operator would actually run, and the duplication on the kcp side is
// what tenant API isolation costs — a real property of the architecture, and
// one of the things worth measuring rather than one to be engineered away
// before measuring. Recording it as an equal number of classes on both sides
// would have hidden exactly the difference the run exists to find.
//
// In dependency order, the class last, so that by the time it exists everything
// it refers to does.
func Blueprint(namespace string) []client.Object {
	objects := demo.Blueprint(demo.BackendInMemory)
	for _, o := range objects {
		o.SetNamespace(namespace)
	}
	return objects
}

// BlueprintNamespace holds the stock side's one ClusterClass.
//
// Its own namespace rather than a tenant's: the class outlives any single rung
// and every tenant refers to it, so putting it in capi-scale-0000 would make
// one arbitrary tenant's teardown everyone else's problem.
const BlueprintNamespace = "capi-scale-blueprint"

// Clusters builds one namespace's Clusters.
//
// classNamespace is where the ClusterClass lives. Empty means the Cluster's own
// namespace, which is what kcp wants — a workspace has one namespace and the
// class is in it, so an explicit reference would say the same thing at greater
// length and diverge from the objects every recorded kcp run was taken with.
func Clusters(namespace, classNamespace string, names []string, shape FleetShape) []*clusterv1.Cluster {
	out := make([]*clusterv1.Cluster, 0, len(names))
	for _, name := range names {
		c := demo.NewCluster(name, shape.ControlPlaneMachines, shape.WorkerMachines, KubernetesVersion)
		c.Namespace = namespace
		if classNamespace != "" {
			c.Spec.Topology.ClassRef.Namespace = classNamespace
		}
		out = append(out, c)
	}
	return out
}

// Scheme is every API group this run creates or reads.
//
// # Why this is not the caller's business
//
// The first real run died on its first rung with "no kind is registered for the
// type v1beta2.DevClusterTemplate": the driver had registered the core Cluster
// API types and none of the four other groups a blueprint draws on. It had
// already created a namespace by then, and nothing else.
//
// Which schemes a blueprint needs is a property of the blueprint, so it lives
// here, and a test walks every object the blueprint produces to check that this
// carries its kind. Add an object whose group is missing and the test fails
// rather than the run.
func Scheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		clusterv1.AddToScheme,
		bootstrapv1.AddToScheme,
		controlplanev1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, fmt.Errorf("registering types: %w", err)
		}
	}
	return s, nil
}
