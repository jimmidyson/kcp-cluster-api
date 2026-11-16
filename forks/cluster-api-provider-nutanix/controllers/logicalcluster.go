/*
Copyright 2026 Nutanix

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

package controllers

import (
	"fmt"

	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
)

// logicalClusterAnnotation is where kcp records the logical cluster an object
// was read from. It is on the object rather than in configuration, which is
// why the helpers here take the object and derive the scope from it rather
// than being told.
//
// The constant itself lives in api/v1beta1, because pkg/client needs it too
// and cannot import this package.
const logicalClusterAnnotation = infrav1.LogicalClusterAnnotation

// logicalClusterOf returns the kcp logical cluster the object was read from,
// or the empty string when it was not read from kcp.
//
// Empty is the ordinary Cluster API case, and every helper below treats it as
// "do not qualify". A fork carrying this therefore behaves exactly as upstream
// does wherever kcp is not involved, which is what keeps the change safe for
// an existing installation and reviewable as an addition rather than a change
// of behaviour.
// The parameter is the concrete Cluster type rather than metav1.Object on
// purpose. A nil *Cluster stored in a metav1.Object is not equal to nil, so an
// interface-typed version silently passes its own nil guard and then panics on
// the first field access — which is exactly what a reconciler unit test that
// builds a context without a Cluster does.
func logicalClusterOf(c *capiv1beta2.Cluster) string {
	if c == nil {
		return ""
	}
	return infrav1.LogicalClusterFrom(c.Annotations)
}

// scopedName qualifies a name with the logical cluster it belongs to.
//
// # Why a Cluster API name is not enough on Prism Central
//
// Names that CAPX derives from Kubernetes objects are unique only within the
// API server that served them. That is sufficient where Cluster API normally
// runs: one management cluster names one set of objects, and one Prism Central
// serves that one management cluster.
//
// It is not sufficient under kcp. A workspace is a whole API server, two
// workspaces routinely hold a Cluster or a Machine of the same name, and the
// controllers serving them share one Prism Central. Unqualified, Prism cannot
// tell the two apart.
//
// The scope is derived from the Cluster in both call sites rather than from
// the nearest object, so that a VM and the categories describing it agree on
// which workspace they belong to even though they are named from different
// objects.
func scopedName(logicalCluster, name string) string {
	if logicalCluster == "" {
		return name
	}
	return fmt.Sprintf("%s-%s", logicalCluster, name)
}
