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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
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

// defaultAPIServerPort is what the DevCluster admission webhook defaults a
// docker-backed cluster's control plane port to. Repeated here because the
// demo does not run that webhook; see NewDevCluster.
const defaultAPIServerPort = 6443

// NewDevCluster builds the infrastructure object for one demo cluster.
func NewDevCluster(name string, backend Backend) *infrav1.DevCluster {
	spec := infrav1.DevClusterSpec{}
	switch backend {
	case BackendDocker:
		spec.Backend.Docker = &infrav1.DockerClusterBackendSpec{}
		// The port the admission webhook would have defaulted.
		//
		// The demo serves no webhooks - they are single-workspace by
		// construction until G4 lands - so everything it creates has to be
		// fully specified. That is stated in the design doc and was not true
		// here: the docker backend takes the control plane port from the spec
		// and only sets the host itself, so without this the endpoint is
		// {host, 0}, which APIEndpoint.IsValid rejects. The control plane
		// provider then returns early from initControlPlaneScope on every
		// reconcile and never creates a Machine, forever, with the DevCluster
		// reporting itself provisioned throughout.
		//
		// The in-memory backend does not need it because it assigns the port
		// of the listener it started, which is why only the docker path was
		// affected.
		spec.ControlPlaneEndpoint.Port = defaultAPIServerPort
	default:
		spec.Backend.InMemory = &infrav1.InMemoryClusterBackendSpec{}
	}

	return &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    map[string]string{DemoLabel: name},
		},
		Spec: spec,
	}
}

// NewCluster builds the Cluster referring to the DevCluster of the same name.
//
// The reference is contract-versioned rather than a full object reference,
// which is what makes the Cluster reconciler resolve it through this
// project's kcp-aware contract-metadata resolver: a workspace that consumes
// a type through an APIBinding has no CustomResourceDefinition object for
// upstream's own resolver to read.
func NewCluster(name string, backend Backend, controlPlane bool) *clusterv1.Cluster {
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    map[string]string{DemoLabel: name},
		},
		Spec: clusterv1.ClusterSpec{
			// Stated rather than left to kubeadm's default, because on a real
			// container runtime something else has to agree with it: a CNI
			// allocates out of this range, and a CNI configured for a
			// different one leaves every Node NotReady. The kubeadm bootstrap
			// provider copies this into the ClusterConfiguration it renders
			// (kubeadmconfig_controller.go), so setting it here is what makes
			// the two sides the same value rather than two defaults that
			// happen to match.
			ClusterNetwork: clusterv1.ClusterNetwork{
				Pods: clusterv1.NetworkRanges{CIDRBlocks: []string{DefaultPodCIDR}},
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevCluster",
				Name:     name,
			},
		},
	}

	// The control plane reference is what makes the control plane provider
	// take the cluster on - and what makes the bootstrap provider stop
	// generating certificates itself, because the control plane provider owns
	// them once there is one.
	if controlPlane {
		cluster.Spec.ControlPlaneRef = clusterv1.ContractVersionedObjectReference{
			APIGroup: controlplanev1.GroupVersion.Group,
			Kind:     "KubeadmControlPlane",
			Name:     ControlPlaneName(name),
		}
	}

	return cluster
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

// ControlPlaneName names a cluster's KubeadmControlPlane. The control plane
// provider names the Machines it creates after it.
func ControlPlaneName(cluster string) string { return cluster + "-cp" }

// MachineTemplateName names the infrastructure template a cluster's Machines
// are built from. One per cluster, shared by the control plane and the worker
// deployment: they want the same backend, and a second identical template
// would be a thing to keep in step for no reason.
func MachineTemplateName(cluster string) string { return cluster }

// WorkerDeploymentName names a cluster's worker MachineDeployment, and the
// KubeadmConfigTemplate its Machines are bootstrapped from.
func WorkerDeploymentName(cluster string) string { return cluster + "-md" }

// NewDevMachineTemplate builds the infrastructure template a KubeadmControlPlane
// stamps each of its Machines from.
func NewDevMachineTemplate(cluster string, backend Backend) *infrav1.DevMachineTemplate {
	spec := infrav1.DevMachineBackendSpec{}
	switch backend {
	case BackendDocker:
		spec.Docker = &infrav1.DockerMachineBackendSpec{}
	default:
		spec.InMemory = &infrav1.InMemoryMachineBackendSpec{}
	}

	return &infrav1.DevMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MachineTemplateName(cluster),
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
		Spec: infrav1.DevMachineTemplateSpec{
			Template: infrav1.DevMachineTemplateResource{
				Spec: infrav1.DevMachineSpec{Backend: spec},
			},
		},
	}
}

// NewKubeadmControlPlane builds a cluster's control plane.
//
// The kubeadmConfigSpec is left at its zero value, as the KubeadmConfigs the
// demo used to create by hand were: for the first control plane machine the
// bootstrap provider fills in what kubeadm needs, and a demo that spelled out
// an init configuration would be demonstrating the spelling.
func NewKubeadmControlPlane(cluster string, replicas int, version string) *controlplanev1.KubeadmControlPlane {
	return &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControlPlaneName(cluster),
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Replicas: ptr.To(int32(replicas)), //nolint:gosec // a replica count from a flag, not arithmetic on untrusted input.
			Version:  version,
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "DevMachineTemplate",
						Name:     MachineTemplateName(cluster),
					},
				},
			},
		},
	}
}

// NewKubeadmConfigTemplate builds the bootstrap template a worker
// MachineDeployment stamps each of its Machines from.
//
// The spec is left at its zero value, as the control plane's is: the bootstrap
// provider fills in what kubeadm needs to join a worker to an initialized
// control plane.
func NewKubeadmConfigTemplate(cluster string) *bootstrapv1.KubeadmConfigTemplate {
	return &bootstrapv1.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkerDeploymentName(cluster),
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
	}
}

// NewMachineDeployment builds a cluster's worker pool.
//
// Workers need a control plane to join, so a run that asks for them asks for a
// control plane too - see Options.validate.
func NewMachineDeployment(cluster string, replicas int, version string) *clusterv1.MachineDeployment {
	return &clusterv1.MachineDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkerDeploymentName(cluster),
			Namespace: Namespace,
			Labels: map[string]string{
				DemoLabel:                  cluster,
				clusterv1.ClusterNameLabel: cluster,
			},
		},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: cluster,
			Replicas:    ptr.To(int32(replicas)), //nolint:gosec // a replica count from a flag, not arithmetic on untrusted input.
			// Spelled out because nothing defaults it: the rollout strategy is
			// normally filled in by the MachineDeployment admission webhook,
			// and this demo serves no webhooks. Without it the reconciler
			// fails with "unexpected deployment strategy type: ".
			Rollout: clusterv1.MachineDeploymentRolloutSpec{
				Strategy: clusterv1.MachineDeploymentRolloutStrategy{
					Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
					RollingUpdate: clusterv1.MachineDeploymentRolloutStrategyRollingUpdate{
						MaxSurge:       ptr.To(intstr.FromInt32(1)),
						MaxUnavailable: ptr.To(intstr.FromInt32(0)),
					},
				},
			},
			// The selector is required, and has to match the labels the
			// template stamps onto each Machine - the MachineSet the
			// deployment creates adopts Machines by it.
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{clusterv1.ClusterNameLabel: cluster},
			},
			Template: clusterv1.MachineTemplateSpec{
				ObjectMeta: clusterv1.ObjectMeta{
					Labels: map[string]string{clusterv1.ClusterNameLabel: cluster},
				},
				Spec: clusterv1.MachineSpec{
					ClusterName: cluster,
					Version:     version,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: bootstrapv1.GroupVersion.Group,
							Kind:     "KubeadmConfigTemplate",
							Name:     WorkerDeploymentName(cluster),
						},
					},
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "DevMachineTemplate",
						Name:     MachineTemplateName(cluster),
					},
				},
			},
		},
	}
}

// KubeconfigSecretName names the Secret holding a workload cluster's
// kubeconfig. The control plane provider writes it once the cluster's
// certificates exist, and it is what a person uses to talk to the cluster.
func KubeconfigSecretName(cluster string) string { return cluster + "-kubeconfig" }

// DefaultPodCIDR is the range demo clusters allocate pod addresses from.
//
// kind's own default, which is not a coincidence: the CNI that ships inside
// the kindest/node image is configured from whatever this is, and staying on
// the range that image was built around is one less thing to get wrong.
const DefaultPodCIDR = "10.244.0.0/16"

// DefaultKubernetesVersion is what demo clusters ask for. The bootstrap
// provider parses it to decide which kubeadm API version to marshal, so it has
// to be a real version rather than a placeholder.
const DefaultKubernetesVersion = "v1.34.0"
