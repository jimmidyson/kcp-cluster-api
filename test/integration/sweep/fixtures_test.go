//go:build integration

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

package sweep_test

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// Stand-ins for the objects a provider this sweep does not run would have
// written.
//
// A sweep that measures one provider's deployment runs that provider and
// nothing else, so the objects its reconcilers depend on have to be put there
// by hand: a Cluster for the control plane provider to find, a DevCluster with
// the owner reference the core Cluster reconciler would have added. They are
// deliberately *not* built from internal/demo's ClusterClass based blueprint.
// That blueprint is what a cluster is in this project, and it needs the core
// provider's topology controllers to turn into anything - which is exactly the
// deployment these shapes exclude.
//
// The fleet shape, which runs every provider together, uses the blueprint. It
// is the one that measures what an installation actually pays.

// bareCluster is a Cluster with no topology and no references.
//
// Paused is set explicitly, and not for its meaning: ClusterSpec is tagged
// omitzero, so an entirely zero spec is omitted from the serialised object and
// the server rejects it with "spec: Required value".
func bareCluster(name string) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: demo.Namespace},
		Spec:       clusterv1.ClusterSpec{Paused: ptr.To(false)},
	}
}

// clusterWithControlPlane is a Cluster naming a KubeadmControlPlane, which is
// what makes the control plane provider take it on.
func clusterWithControlPlane(name string) *clusterv1.Cluster {
	cluster := bareCluster(name)
	cluster.Spec.ControlPlaneRef = clusterv1.ContractVersionedObjectReference{
		APIGroup: controlplanev1.GroupVersion.Group,
		Kind:     "KubeadmControlPlane",
		Name:     demo.ControlPlaneName(name),
	}
	return cluster
}

// newDevCluster is an in-memory DevCluster, as the topology controller would
// have stamped one from the class.
func newDevCluster(name string) *infrav1.DevCluster {
	return &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: demo.Namespace},
		Spec: infrav1.DevClusterSpec{
			Backend: infrav1.DevClusterBackendSpec{InMemory: &infrav1.InMemoryClusterBackendSpec{}},
		},
	}
}

// newKubeadmControlPlane is a control plane object as the topology controller
// would have stamped one from the class's KubeadmControlPlaneTemplate: the
// replicas and version from the Cluster's topology, the infrastructure machine
// template from the class.
func newKubeadmControlPlane(cluster string, replicas int32, version string) *controlplanev1.KubeadmControlPlane {
	return &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      demo.ControlPlaneName(cluster),
			Namespace: demo.Namespace,
			Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster},
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Replicas: ptr.To(replicas),
			Version:  version,
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "DevMachineTemplate",
						Name:     demo.ControlPlaneMachineTemplateName,
					},
				},
			},
		},
	}
}
