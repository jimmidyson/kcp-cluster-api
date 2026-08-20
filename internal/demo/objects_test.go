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

package demo

import (
	"testing"

	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

func TestBackendValidate(t *testing.T) {
	for _, tc := range []struct {
		backend Backend
		wantErr bool
	}{
		{BackendInMemory, false},
		{BackendDocker, false},
		{Backend(""), true},
		{Backend("kind"), true},
	} {
		if err := tc.backend.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("Backend(%q).Validate() = %v, wantErr %v", tc.backend, err, tc.wantErr)
		}
	}
}

func TestNewDevClusterBackend(t *testing.T) {
	inMemory := NewDevCluster("demo-00", BackendInMemory)
	if inMemory.Spec.Backend.InMemory == nil || inMemory.Spec.Backend.Docker != nil {
		t.Errorf("in-memory DevCluster backend = %+v, want only InMemory set", inMemory.Spec.Backend)
	}

	docker := NewDevCluster("demo-00", BackendDocker)
	if docker.Spec.Backend.Docker == nil || docker.Spec.Backend.InMemory != nil {
		t.Errorf("docker DevCluster backend = %+v, want only Docker set", docker.Spec.Backend)
	}
}

// The Cluster's infrastructureRef has to name the DevCluster this demo
// creates alongside it, by group and kind as well as name: the Cluster
// reconciler resolves it through the contract-metadata resolver, and a
// reference that does not resolve leaves the cluster stuck with nothing
// visibly wrong in either object.
func TestNewClusterReferencesItsDevCluster(t *testing.T) {
	name := ClusterName(0)
	cluster := NewCluster(name, BackendInMemory, false)
	devCluster := NewDevCluster(name, BackendInMemory)

	ref := cluster.Spec.InfrastructureRef
	if ref.Name != devCluster.Name {
		t.Errorf("infrastructureRef.Name = %q, want %q", ref.Name, devCluster.Name)
	}
	if ref.Kind != "DevCluster" {
		t.Errorf("infrastructureRef.Kind = %q, want DevCluster", ref.Kind)
	}
	if ref.APIGroup != infrav1.GroupVersion.Group {
		t.Errorf("infrastructureRef.APIGroup = %q, want %q", ref.APIGroup, infrav1.GroupVersion.Group)
	}
	if cluster.Namespace != devCluster.Namespace {
		t.Errorf("Cluster namespace %q != DevCluster namespace %q", cluster.Namespace, devCluster.Namespace)
	}
}

// A cluster with a control plane refers to it, and one without does not: the
// reference is what makes the control plane provider take the cluster on, and
// what makes the bootstrap provider stop generating certificates itself.
func TestNewClusterControlPlaneReference(t *testing.T) {
	without := NewCluster(ClusterName(0), BackendInMemory, false)
	if without.Spec.ControlPlaneRef.IsDefined() {
		t.Errorf("Cluster without a control plane has controlPlaneRef %+v", without.Spec.ControlPlaneRef)
	}

	with := NewCluster(ClusterName(0), BackendInMemory, true)
	ref := with.Spec.ControlPlaneRef
	if ref.Kind != "KubeadmControlPlane" {
		t.Errorf("controlPlaneRef.Kind = %q, want KubeadmControlPlane", ref.Kind)
	}
	if want := ControlPlaneName(ClusterName(0)); ref.Name != want {
		t.Errorf("controlPlaneRef.Name = %q, want %q", ref.Name, want)
	}
}

// The control plane stamps its Machines from the infrastructure template, so
// the reference has to name the one the demo creates alongside it.
func TestKubeadmControlPlaneReferencesItsTemplate(t *testing.T) {
	cluster := ClusterName(0)
	kcp := NewKubeadmControlPlane(cluster, 3, DefaultKubernetesVersion)
	template := NewDevMachineTemplate(cluster, BackendInMemory)

	ref := kcp.Spec.MachineTemplate.Spec.InfrastructureRef
	if ref.Name != template.Name {
		t.Errorf("machineTemplate infrastructureRef.Name = %q, want %q", ref.Name, template.Name)
	}
	if ref.Kind != "DevMachineTemplate" {
		t.Errorf("machineTemplate infrastructureRef.Kind = %q, want DevMachineTemplate", ref.Kind)
	}
	if got := *kcp.Spec.Replicas; got != 3 {
		t.Errorf("replicas = %d, want 3", got)
	}
	if kcp.Spec.Version != DefaultKubernetesVersion {
		t.Errorf("version = %q, want %q", kcp.Spec.Version, DefaultKubernetesVersion)
	}
}

// Every workspace holds the same cluster names, which is what makes a
// cross-workspace leak visible in a demo rather than plausible.
func TestClusterNameIsWorkspaceIndependent(t *testing.T) {
	if got, want := ClusterName(0), "demo-00"; got != want {
		t.Errorf("ClusterName(0) = %q, want %q", got, want)
	}
	if ClusterName(1) == ClusterName(0) {
		t.Error("ClusterName is not unique within a workspace")
	}
}

// TestDevClusterIsFullySpecifiedForEveryBackend is the demo's stated contract,
// enforced rather than asserted in prose.
//
// The demo serves no webhooks, so nothing defaults what it creates. That is
// written down in the design doc and was not true of the docker backend: the
// control plane port came from the admission webhook, the demo left it zero,
// and the docker backend sets only the host. The resulting endpoint fails
// APIEndpoint.IsValid, and the control plane provider returns early on every
// reconcile without ever creating a Machine - while the DevCluster reports
// itself provisioned, so nothing else looks wrong.
func TestDevClusterIsFullySpecifiedForEveryBackend(t *testing.T) {
	for _, backend := range []Backend{BackendInMemory, BackendDocker} {
		t.Run(string(backend), func(t *testing.T) {
			devCluster := NewDevCluster(ClusterName(0), backend)

			switch backend {
			case BackendDocker:
				// The docker backend fills in the host from the load balancer
				// it creates and takes the port as given, so the port has to
				// be here or the endpoint is never valid.
				if devCluster.Spec.ControlPlaneEndpoint.Port == 0 {
					t.Error("a docker-backed DevCluster has no control plane port, so its endpoint can never become valid and no control plane machine is ever created")
				}
			case BackendInMemory:
				// The in-memory backend assigns the port of the listener it
				// starts, so setting one here would be overwritten and would
				// suggest the demo had a say in it.
				if devCluster.Spec.ControlPlaneEndpoint.Port != 0 {
					t.Errorf("an in-memory DevCluster specifies port %d, but the backend assigns the listener's own port",
						devCluster.Spec.ControlPlaneEndpoint.Port)
				}
			}

			if devCluster.Spec.Backend.Docker == nil && devCluster.Spec.Backend.InMemory == nil {
				t.Error("neither backend is set, which the webhook would have defaulted")
			}
		})
	}
}
