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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// Backend selects which of DevCluster's backends the demo provisions with.
type Backend string

const (
	// BackendInMemory provisions the dev provider's in-memory backend: no
	// container runtime, no images, nothing outside the manager process. It
	// is the default because it is the only one that runs anywhere the
	// integration tests run.
	BackendInMemory Backend = "inmemory"

	// BackendDocker provisions real containers through a container runtime,
	// and pulls kindest images to do it.
	BackendDocker Backend = "docker"
)

// Validate reports whether b names a backend this demo can provision.
func (b Backend) Validate() error {
	switch b {
	case BackendInMemory, BackendDocker:
		return nil
	default:
		return fmt.Errorf("unknown backend %q: want %q or %q", b, BackendInMemory, BackendDocker)
	}
}

// Namespace holds every object the demo creates. One namespace per workspace
// keeps `kubectl get` in a demo short, and the tenancy boundary being
// demonstrated is the workspace, not the namespace.
const Namespace = "default"

// NewDevCluster builds the infrastructure object for one demo cluster.
func NewDevCluster(name string, backend Backend) *infrav1.DevCluster {
	spec := infrav1.DevClusterBackendSpec{}
	switch backend {
	case BackendDocker:
		spec.Docker = &infrav1.DockerClusterBackendSpec{}
	default:
		spec.InMemory = &infrav1.InMemoryClusterBackendSpec{}
	}

	return &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    map[string]string{DemoLabel: name},
		},
		Spec: infrav1.DevClusterSpec{Backend: spec},
	}
}

// NewCluster builds the Cluster referring to the DevCluster of the same name.
//
// The reference is contract-versioned rather than a full object reference,
// which is what makes the Cluster reconciler resolve it through this
// project's kcp-aware contract-metadata resolver: a workspace that consumes
// a type through an APIBinding has no CustomResourceDefinition object for
// upstream's own resolver to read.
func NewCluster(name string, backend Backend) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    map[string]string{DemoLabel: name},
		},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevCluster",
				Name:     name,
			},
		},
	}
}

// DemoLabel marks every object the demo creates, so a workspace it ran
// against can be cleaned out again without guessing at names.
const DemoLabel = "kcp-cluster-api.jimmidyson.github.io/demo"

// ClusterName names the nth cluster in a workspace.
//
// The name repeats across workspaces deliberately: identical names in every
// workspace are what makes a cross-workspace confusion visible rather than
// plausible, which is the property a multi-workspace demo exists to show.
func ClusterName(n int) string {
	return fmt.Sprintf("demo-%02d", n)
}

// MachineName names the nth control plane machine of a cluster. Like
// ClusterName, it repeats across workspaces on purpose.
func MachineName(cluster string, n int) string {
	return fmt.Sprintf("%s-cp-%d", cluster, n)
}

// NewKubeadmConfig builds the bootstrap configuration for a control plane
// machine.
//
// The spec is left at its zero value deliberately: for the first control plane
// machine the provider fills in what kubeadm needs, and a demo that spelled
// out an init configuration would be demonstrating the spelling rather than
// the provider. What makes this the *init* path is the owning Machine being a
// control plane machine of a Cluster whose control plane is not yet
// initialized - see the bootstrap provider's handleClusterNotInitialized.
func NewKubeadmConfig(cluster, name string) *bootstrapv1.KubeadmConfig {
	return &bootstrapv1.KubeadmConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
	}
}

// NewControlPlaneMachine builds a control plane Machine referring to the
// KubeadmConfig and DevMachine of the same name.
//
// It is a standalone control plane machine: the Cluster has no
// controlPlaneRef, which is what makes the bootstrap provider generate the
// cluster's certificates itself rather than waiting for a control plane
// provider to do it. That is the conversion plan's P2, and until it lands this
// is how a Machine gets bootstrap data at all.
func NewControlPlaneMachine(cluster, name, version string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                          cluster,
				clusterv1.ClusterNameLabel:         cluster,
				clusterv1.MachineControlPlaneLabel: "",
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster,
			Version:     version,
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: bootstrapv1.GroupVersion.Group,
					Kind:     "KubeadmConfig",
					Name:     name,
				},
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevMachine",
				Name:     name,
			},
		},
	}
}

// NewDevMachine builds the infrastructure object for one machine.
func NewDevMachine(cluster, name string, backend Backend) *infrav1.DevMachine {
	spec := infrav1.DevMachineBackendSpec{}
	switch backend {
	case BackendDocker:
		spec.Docker = &infrav1.DockerMachineBackendSpec{}
	default:
		spec.InMemory = &infrav1.InMemoryMachineBackendSpec{}
	}

	return &infrav1.DevMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
		Spec: infrav1.DevMachineSpec{Backend: spec},
	}
}

// DefaultKubernetesVersion is what demo machines ask for. The bootstrap
// provider parses it to decide which kubeadm API version to marshal, so it has
// to be a real version rather than a placeholder.
const DefaultKubernetesVersion = "v1.34.0"
