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
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// needed is every group version this run builds objects against, the kinds it
// needs from each, and the provider that installs them.
//
// Written out rather than derived from the blueprint, because the point is to
// state what this run assumes: derive it from the objects and a preflight would
// agree with whatever the objects happen to be, which is the assumption it
// exists to check.
var needed = []struct {
	groupVersion string
	kinds        []string
	provider     string
}{
	{"cluster.x-k8s.io/v1beta2", []string{"Cluster", "ClusterClass", "Machine"}, "core (cluster-api)"},
	{"controlplane.cluster.x-k8s.io/v1beta2", []string{"KubeadmControlPlaneTemplate"}, "control plane (kubeadm)"},
	{"bootstrap.cluster.x-k8s.io/v1beta2", []string{"KubeadmConfigTemplate"}, "bootstrap (kubeadm)"},
	{"infrastructure.cluster.x-k8s.io/v1beta2",
		[]string{"DevCluster", "DevClusterTemplate", "DevMachineTemplate"},
		"infrastructure (docker — DevCluster is served by the Docker provider, in-memory backend included)"},
}

// IndexResources turns discovery's answer into group version -> kinds.
func IndexResources(lists []*metav1.APIResourceList) map[string][]string {
	index := map[string][]string{}
	for _, list := range lists {
		if list == nil {
			continue
		}
		for _, r := range list.APIResources {
			// Subresources arrive as "cluster/status" and are not kinds this
			// run creates.
			if strings.Contains(r.Name, "/") {
				continue
			}
			index[list.GroupVersion] = append(index[list.GroupVersion], r.Kind)
		}
	}
	return index
}

// Preflight refuses to start a run against a cluster that cannot serve what it
// is about to create.
//
// # The one risk this harness carries that its own tests cannot find
//
// The objects come from this repository, whose Cluster API is a fork off the
// v1.15 line. The CRDs come from whatever clusterctl installed, which is stock
// v1.14. Those two agreeing is an assumption, not a fact, and the ways it fails
// are unpleasant in proportion to how quiet they are: a group version the
// cluster does not serve fails every create loudly, which is fine, but a
// version it does serve with a schema that prunes a field the objects rely on
// produces a fleet that never converges and nothing that says why.
//
// So the assumption is checked, by name, before a rung is created — and the
// message names the provider to install rather than the version string alone,
// because "no matches for kind DevCluster" has sent people looking for an
// in-memory provider that does not exist as a provider.
func Preflight(served map[string][]string) error {
	var problems []string
	for _, want := range needed {
		have, ok := served[want.groupVersion]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"%s is not served: install the %s provider", want.groupVersion, want.provider))
			continue
		}
		index := map[string]bool{}
		for _, kind := range have {
			index[kind] = true
		}
		var missing []string
		for _, kind := range want.kinds {
			if !index[kind] {
				missing = append(missing, kind)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			problems = append(problems, fmt.Sprintf("%s does not serve %s: install the %s provider",
				want.groupVersion, strings.Join(missing, ", "), want.provider))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("this cluster cannot serve what this run creates:\n  - %s\n\n"+
		"Every object here is built against v1beta2. A cluster serving only v1beta1 is running a "+
		"Cluster API older than this harness was written for, and the fix is the provider versions "+
		"rather than anything in the run", strings.Join(problems, "\n  - "))
}

// NeededGroupVersions is what a caller has to ask the cluster about before
// [Preflight] can judge the answer.
func NeededGroupVersions() []string {
	out := make([]string, 0, len(needed))
	for _, n := range needed {
		out = append(out, n.groupVersion)
	}
	return out
}
