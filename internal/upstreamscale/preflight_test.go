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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// resources builds an APIResourceList index of the form Preflight consumes.
func resources(gvs ...string) map[string][]string {
	out := map[string][]string{}
	for _, gv := range gvs {
		out[gv] = []string{"ClusterClass", "Cluster", "DevCluster", "DevClusterTemplate",
			"DevMachineTemplate", "KubeadmControlPlaneTemplate", "KubeadmConfigTemplate", "Machine"}
	}
	return out
}

// TestPreflightCatchesTheVersionThisRunIsBuiltAgainst is the one risk this
// harness carries that no unit test of its own code can find.
//
// The objects come from this repository, whose Cluster API is a fork off the
// v1.15 line. The CRDs come from whatever clusterctl installed, which is stock
// v1.14. If the group version the objects are built against is not one the
// cluster serves, every create fails — or worse, succeeds with fields pruned
// and a fleet that never converges for a reason nothing reports.
//
// So it is checked before a rung is created, against the cluster, by name.
func TestPreflightCatchesTheVersionThisRunIsBuiltAgainst(t *testing.T) {
	ok := Preflight(resources(
		"cluster.x-k8s.io/v1beta2",
		"infrastructure.cluster.x-k8s.io/v1beta2",
		"controlplane.cluster.x-k8s.io/v1beta2",
		"bootstrap.cluster.x-k8s.io/v1beta2",
	))
	if ok != nil {
		t.Fatalf("a cluster serving every needed version was refused: %v", ok)
	}

	// The version is served but the kind is not: a provider is missing rather
	// than out of date, and the message should say which.
	partial := resources("cluster.x-k8s.io/v1beta2", "controlplane.cluster.x-k8s.io/v1beta2",
		"bootstrap.cluster.x-k8s.io/v1beta2")
	err := Preflight(partial)
	if err == nil {
		t.Fatal("a cluster with no infrastructure provider was accepted")
	}
	if !strings.Contains(err.Error(), "DevCluster") {
		t.Errorf("the message does not name what is missing: %v", err)
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("the message does not say which provider serves it: %v", err)
	}

	// The kinds exist at a version this run does not build against, which is
	// the fork-versus-release case and the one worth an explicit sentence.
	old := resources("cluster.x-k8s.io/v1beta1", "infrastructure.cluster.x-k8s.io/v1beta1",
		"controlplane.cluster.x-k8s.io/v1beta1", "bootstrap.cluster.x-k8s.io/v1beta1")
	err = Preflight(old)
	if err == nil {
		t.Fatal("a cluster serving only v1beta1 was accepted")
	}
	if !strings.Contains(err.Error(), "v1beta2") {
		t.Errorf("the message does not name the version this run needs: %v", err)
	}
}

// TestPreflightReadsWhatTheClusterSays, so that the discovery shape it is given
// is the one client-go hands back.
func TestPreflightIndexesAnAPIResourceList(t *testing.T) {
	index := IndexResources([]*metav1.APIResourceList{
		{GroupVersion: "cluster.x-k8s.io/v1beta2", APIResources: []metav1.APIResource{
			{Kind: "Cluster"}, {Kind: "ClusterClass"}, {Kind: "Machine"},
		}},
		{GroupVersion: "infrastructure.cluster.x-k8s.io/v1beta2", APIResources: []metav1.APIResource{
			{Kind: "DevCluster"},
		}},
	})
	if got := index["cluster.x-k8s.io/v1beta2"]; len(got) != 3 {
		t.Errorf("indexed %v", got)
	}
	if got := index["infrastructure.cluster.x-k8s.io/v1beta2"]; len(got) != 1 || got[0] != "DevCluster" {
		t.Errorf("indexed %v", got)
	}
}
