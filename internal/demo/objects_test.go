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
	cluster := NewCluster(name, BackendInMemory)
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
