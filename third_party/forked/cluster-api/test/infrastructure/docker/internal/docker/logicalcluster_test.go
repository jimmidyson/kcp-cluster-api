/*
Copyright 2026 The Kubernetes Authors.

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

package docker

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
)

// clusterIn returns a Cluster named name, read from the given kcp logical
// cluster. An empty logical cluster is the ordinary Cluster API case.
func clusterIn(name, logicalCluster string) *clusterv1.Cluster {
	c := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	if logicalCluster != "" {
		c.Annotations = map[string]string{logicalClusterAnnotation: logicalCluster}
	}
	return c
}

// Two workspaces routinely hold a Cluster with the same name — the fleet tests
// in kcp-cluster-api do it deliberately, so that a leak cannot hide behind
// names that happen not to collide. Their containers must not.
func TestTwoWorkspacesDoNotShareAContainerName(t *testing.T) {
	g := NewWithT(t)

	first := scopedContainerName(logicalClusterOf(clusterIn("demo-00", "2yqfrtuq4cjeh3n5")), "demo-00-lb")
	second := scopedContainerName(logicalClusterOf(clusterIn("demo-00", "2pes8qc13ri2fa4y")), "demo-00-lb")

	g.Expect(first).ToNot(Equal(second),
		"two workspaces' load balancers asked for the same container name; a container name is unique per daemon, so the second cluster would adopt the first's load balancer")
	g.Expect(first).To(ContainSubstring("demo-00-lb"))
}

// Outside kcp nothing is qualified, so a fork carrying this is indistinguishable
// from upstream wherever no logical cluster is involved.
func TestOutsideKcpNothingIsQualified(t *testing.T) {
	g := NewWithT(t)

	plain := clusterIn("demo-00", "")
	g.Expect(logicalClusterOf(plain)).To(BeEmpty())
	g.Expect(scopedContainerName("", "demo-00-lb")).To(Equal("demo-00-lb"))

	filters := clusterContainerFilters("demo-00", "")
	g.Expect(filters["label"]).To(HaveKeyWithValue(clusterLabelKey, []string{"demo-00"}))
	g.Expect(filters["label"]).ToNot(HaveKey(logicalClusterLabelKey),
		"a filter outside kcp gained a label no container carries, which would match nothing")
}

// The filter is what stops one workspace's load balancer from listing another
// workspace's control planes as its backend servers — which is the defect this
// whole change is about.
func TestFilterSelectsOneWorkspacesContainers(t *testing.T) {
	g := NewWithT(t)

	filters := clusterContainerFilters("demo-00", "2yqfrtuq4cjeh3n5")

	g.Expect(filters["label"]).To(HaveKeyWithValue(clusterLabelKey, []string{"demo-00"}))
	g.Expect(filters["label"]).To(HaveKeyWithValue(logicalClusterLabelKey, []string{"2yqfrtuq4cjeh3n5"}),
		"without this the filter selects every cluster named demo-00 on the daemon, whichever workspace it belongs to")

	other := clusterContainerFilters("demo-00", "2pes8qc13ri2fa4y")
	g.Expect(other["label"][logicalClusterLabelKey]).ToNot(Equal(filters["label"][logicalClusterLabelKey]))
}

// The label is what every container lookup selects on, so a container created
// without it cannot be told apart from a same-named one in another workspace.
func TestLogicalClusterLabelIsAddedWithoutMutatingTheCaller(t *testing.T) {
	g := NewWithT(t)

	caller := map[string]string{"io.x-k8s.cluster.failureDomain": "fd1"}

	got := withLogicalClusterLabel(caller, "2yqfrtuq4cjeh3n5")
	g.Expect(got).To(HaveKeyWithValue(logicalClusterLabelKey, "2yqfrtuq4cjeh3n5"))
	g.Expect(got).To(HaveKeyWithValue("io.x-k8s.cluster.failureDomain", "fd1"))
	g.Expect(caller).ToNot(HaveKey(logicalClusterLabelKey),
		"the caller's map was mutated; these maps come from reconciler state and are reused")

	g.Expect(withLogicalClusterLabel(caller, "")).To(Equal(caller),
		"outside kcp the labels must be handed on untouched")
}

// A nil Cluster must not panic: callers reach these helpers before they have
// validated their inputs.
func TestLogicalClusterOfToleratesNil(t *testing.T) {
	NewWithT(t).Expect(logicalClusterOf(nil)).To(BeEmpty())
}

// The entry points, not just the helpers: a call site that stopped using them
// would leave the helpers passing and the collision back.
func TestNewLoadBalancerNamesItsContainerPerWorkspace(t *testing.T) {
	g := NewWithT(t)
	ctx := container.RuntimeInto(context.Background(), &container.FakeRuntime{})

	first, err := NewLoadBalancer(ctx, clusterIn("demo-00", "2yqfrtuq4cjeh3n5"), "", "", "6443")
	g.Expect(err).ToNot(HaveOccurred())
	second, err := NewLoadBalancer(ctx, clusterIn("demo-00", "2pes8qc13ri2fa4y"), "", "", "6443")
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(first.containerName()).ToNot(Equal(second.containerName()),
		"both workspaces' load balancers would be the same container, so one cluster's endpoint would front the other's control plane")

	plain, err := NewLoadBalancer(ctx, clusterIn("demo-00", ""), "", "", "6443")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(plain.containerName()).To(Equal("demo-00-lb"),
		"outside kcp the name must be exactly what upstream produces")
}

func TestNewMachineCarriesItsWorkspace(t *testing.T) {
	g := NewWithT(t)
	ctx := container.RuntimeInto(context.Background(), &container.FakeRuntime{})

	m, err := NewMachine(ctx, clusterIn("demo-00", "2yqfrtuq4cjeh3n5"), "demo-00-cp-vzm47", nil)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(m.logicalCluster).To(Equal("2yqfrtuq4cjeh3n5"),
		"a Machine that does not know its workspace cannot label the container it creates, and every later lookup selects on that label")

	plain, err := NewMachine(ctx, clusterIn("demo-00", ""), "demo-00-cp-vzm47", nil)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(plain.logicalCluster).To(BeEmpty())
}
