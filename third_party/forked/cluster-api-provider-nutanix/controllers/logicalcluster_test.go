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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func cluster(name string, annotations map[string]string) *capiv1beta2.Cluster {
	return &capiv1beta2.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: annotations},
	}
}

func TestLogicalClusterOf(t *testing.T) {
	t.Run("a nil Cluster is unscoped rather than a panic", func(t *testing.T) {
		// A reconciler unit test can build a context with no Cluster, and a
		// delete path still has to name the VM it is deleting.
		var absent *capiv1beta2.Cluster
		assert.Equal(t, "", logicalClusterOf(absent))
	})

	t.Run("an object with no annotations is unscoped", func(t *testing.T) {
		assert.Equal(t, "", logicalClusterOf(cluster("demo", nil)))
	})

	t.Run("an object outside kcp is unscoped", func(t *testing.T) {
		assert.Equal(t, "", logicalClusterOf(cluster("demo", map[string]string{"other": "value"})))
	})

	t.Run("an object read from kcp carries its logical cluster", func(t *testing.T) {
		c := cluster("demo", map[string]string{logicalClusterAnnotation: "krqce2ecrfartlw3"})
		assert.Equal(t, "krqce2ecrfartlw3", logicalClusterOf(c))
	})
}

func TestScopedName(t *testing.T) {
	t.Run("no logical cluster leaves the name exactly as it was", func(t *testing.T) {
		// This is the property that keeps an installation without kcp
		// behaving as upstream does, so it is asserted rather than assumed.
		assert.Equal(t, "demo-md-0-abcde", scopedName("", "demo-md-0-abcde"))
	})

	t.Run("a logical cluster qualifies the name", func(t *testing.T) {
		assert.Equal(t, "krqce2ecrfartlw3-demo-md-0-abcde",
			scopedName("krqce2ecrfartlw3", "demo-md-0-abcde"))
	})
}

// TestScopedNameSeparatesWorkspaces is the collision this exists to stop,
// stated as a test: two workspaces holding a Machine of the same name must not
// ask Prism Central for one VM name.
//
// Unqualified they do, and the failure is not a clean rejection at create time
// — FindVMByName resolves by a Prism-wide `name eq` query and refuses to
// resolve at all once two VMs share a name, so the second tenant breaks the
// first tenant's reconcile as well as its own.
func TestScopedNameSeparatesWorkspaces(t *testing.T) {
	const machineName = "demo-md-0-abcde"

	alpha := cluster("demo", map[string]string{logicalClusterAnnotation: "workspace-alpha"})
	beta := cluster("demo", map[string]string{logicalClusterAnnotation: "workspace-beta"})

	alphaVM := scopedName(logicalClusterOf(alpha), machineName)
	betaVM := scopedName(logicalClusterOf(beta), machineName)

	assert.NotEqual(t, alphaVM, betaVM,
		"two workspaces holding a same-named Machine must not name one VM")
	assert.Equal(t, "workspace-alpha-"+machineName, alphaVM)
	assert.Equal(t, "workspace-beta-"+machineName, betaVM)
}
