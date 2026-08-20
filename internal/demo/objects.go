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

// A demo cluster is a ClusterClass based cluster.
//
// The Cluster this package creates names a class and a shape - so many control
// plane machines, so many workers, this Kubernetes version - and nothing else.
// Everything under it, the infrastructure cluster, the control plane, the
// worker MachineDeployment and the templates each is stamped from, is created
// by the topology controller from the ClusterClass.
//
// # Why the demo is built this way
//
// Because it is the shape this project has to hold up under. A workspace is a
// tenant, and a tenant handed a class writes eight lines to get a cluster
// instead of six objects that have to agree with each other. That is also the
// shape a fleet's operator maintains: a class is where a fix or a version bump
// is made once for every cluster built from it.
//
// It is also the harder case to serve, which is the point of demonstrating it.
// A managed topology adds four reconcilers to the core provider, a server-side
// apply of every object under the Cluster on every reconcile, and a
// cross-object read - Cluster to ClusterClass to five templates - that has to
// resolve inside one workspace and never across two.
//
// # The names are pinned, and that is a choice
//
// A ClusterClass names the objects it creates `{{ .cluster.name }}-{{ .random
// }}` by default. The naming templates below pin them to what this demo used
// to create by hand: `demo-00` for the infrastructure cluster, `demo-00-cp` for
// the control plane, `demo-00-md` for the worker deployment.
//
// That is for the reader, not for the reconcilers. What a demo is for is the
// `kubectl get` that follows it, and a name with five random characters in it
// is one the walkthrough cannot print and a person cannot predict. Nothing in
// this project depends on the pinning; a class that omits it works exactly as
// well and is what a real tenant would write.

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
// demo does not run that webhook; see NewDevClusterTemplate.
const defaultAPIServerPort = 6443

// DemoLabel marks every object the demo creates, so a workspace it ran
// against can be cleaned out again without guessing at names.
const DemoLabel = "kcp-cluster-api.jimmidyson.github.io/demo"

// ClassName is the ClusterClass every cluster in a workspace is built from,
// and the name of the templates it refers to that there is only one of.
//
// One class per workspace, shared by every cluster in it: a class is a
// blueprint, and a demo that gave each cluster its own would be demonstrating
// copying rather than classes.
const ClassName = "demo"

// WorkerClass names the ClusterClass's one machine deployment class. It is what
// a Cluster's topology asks for by name when it wants workers.
const WorkerClass = "default-worker"

// WorkerTopologyName names the one worker deployment in a Cluster's topology.
// It is short because it becomes part of the MachineDeployment's name.
const WorkerTopologyName = "md"

// ControlPlaneTemplateName, WorkerBootstrapTemplateName and the two machine
// template names are the objects the ClusterClass refers to. The control plane
// and the workers get separate infrastructure templates even though this demo
// gives them identical contents: a class that shared one would be a class that
// cannot change a worker's shape without changing the control plane's, which is
// the first thing anybody does with one.
const (
	ControlPlaneTemplateName        = ClassName
	InfrastructureTemplateName      = ClassName
	ControlPlaneMachineTemplateName = ClassName + "-control-plane"
	WorkerMachineTemplateName       = ClassName + "-" + WorkerClass
	WorkerBootstrapTemplateName     = ClassName + "-" + WorkerClass
)

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
//
// It is what the ClusterClass's control plane naming template produces, not an
// independent convention: change one and the other is wrong.
func ControlPlaneName(cluster string) string { return cluster + "-cp" }

// InfraClusterName names a cluster's DevCluster, as the ClusterClass's
// infrastructure naming template produces it.
func InfraClusterName(cluster string) string { return cluster }

// WorkerDeploymentName names a cluster's worker MachineDeployment, as the
// ClusterClass's machine deployment naming template produces it.
func WorkerDeploymentName(cluster string) string { return cluster + "-" + WorkerTopologyName }

// classLabels marks a blueprint object: the demo label, so a workspace can be
// cleaned out, and nothing else. A template is not owned by any one cluster.
func classLabels() map[string]string {
	return map[string]string{DemoLabel: ClassName}
}

// NewDevClusterTemplate builds the infrastructure cluster template a
// ClusterClass stamps each cluster's DevCluster from.
func NewDevClusterTemplate(backend Backend) *infrav1.DevClusterTemplate {
	spec := infrav1.DevClusterSpec{}
	switch backend {
	case BackendDocker:
		spec.Backend.Docker = &infrav1.DockerClusterBackendSpec{}
		// The port the admission webhook would have defaulted.
		//
		// The demo serves no webhooks - they are single-workspace by
		// construction until G4 lands - so everything it creates has to be
		// fully specified. The docker backend takes the control plane port
		// from the spec and only sets the host itself, so without this the
		// endpoint is {host, 0}, which APIEndpoint.IsValid rejects. The
		// control plane provider then returns early from initControlPlaneScope
		// on every reconcile and never creates a Machine, forever, with the
		// DevCluster reporting itself provisioned throughout.
		//
		// The in-memory backend does not need it because it assigns the port
		// of the listener it started, which is why only the docker path was
		// affected.
		spec.ControlPlaneEndpoint.Port = defaultAPIServerPort
	default:
		spec.Backend.InMemory = &infrav1.InMemoryClusterBackendSpec{}
	}

	return &infrav1.DevClusterTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      InfrastructureTemplateName,
			Namespace: Namespace,
			Labels:    classLabels(),
		},
		Spec: infrav1.DevClusterTemplateSpec{
			Template: infrav1.DevClusterTemplateResource{Spec: spec},
		},
	}
}

// NewDevMachineTemplate builds an infrastructure machine template. The
// ClusterClass names one for the control plane and one per machine deployment
// class.
func NewDevMachineTemplate(name string, backend Backend) *infrav1.DevMachineTemplate {
	spec := infrav1.DevMachineBackendSpec{}
	switch backend {
	case BackendDocker:
		spec.Docker = &infrav1.DockerMachineBackendSpec{}
	default:
		spec.InMemory = &infrav1.InMemoryMachineBackendSpec{}
	}

	return &infrav1.DevMachineTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    classLabels(),
		},
		Spec: infrav1.DevMachineTemplateSpec{
			Template: infrav1.DevMachineTemplateResource{
				Spec: infrav1.DevMachineSpec{Backend: spec},
			},
		},
	}
}

// NewKubeadmControlPlaneTemplate builds the control plane template a
// ClusterClass stamps each cluster's KubeadmControlPlane from.
//
// The kubeadmConfigSpec is left at its zero value: for the first control plane
// machine the bootstrap provider fills in what kubeadm needs, and a demo that
// spelled out an init configuration would be demonstrating the spelling. The
// replicas, the version and the infrastructure machine template are not here
// either - the first two come from the Cluster's topology and the third from
// the class, which is what makes this template the part that is the same for
// every cluster.
func NewKubeadmControlPlaneTemplate() *controlplanev1.KubeadmControlPlaneTemplate {
	return &controlplanev1.KubeadmControlPlaneTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControlPlaneTemplateName,
			Namespace: Namespace,
			Labels:    classLabels(),
		},
	}
}

// NewKubeadmConfigTemplate builds the bootstrap template a worker
// MachineDeployment stamps each of its Machines from.
//
// The spec is left at its zero value, as the control plane's is: the bootstrap
// provider fills in what kubeadm needs to join a worker to an initialized
// control plane.
func NewKubeadmConfigTemplate(name string) *bootstrapv1.KubeadmConfigTemplate {
	return &bootstrapv1.KubeadmConfigTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: Namespace,
			Labels:    classLabels(),
		},
	}
}

// NewClusterClass builds the blueprint every demo cluster in a workspace is
// created from.
func NewClusterClass(backend Backend) *clusterv1.ClusterClass {
	return &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ClassName,
			Namespace: Namespace,
			Labels:    classLabels(),
		},
		Spec: clusterv1.ClusterClassSpec{
			Infrastructure: clusterv1.InfrastructureClass{
				TemplateRef: clusterv1.ClusterClassTemplateReference{
					APIVersion: infrav1.GroupVersion.String(),
					Kind:       "DevClusterTemplate",
					Name:       InfrastructureTemplateName,
				},
				Naming: clusterv1.InfrastructureClassNamingSpec{Template: "{{ .cluster.name }}"},
			},
			ControlPlane: clusterv1.ControlPlaneClass{
				TemplateRef: clusterv1.ClusterClassTemplateReference{
					APIVersion: controlplanev1.GroupVersion.String(),
					Kind:       "KubeadmControlPlaneTemplate",
					Name:       ControlPlaneTemplateName,
				},
				// A machine-based control plane: the KubeadmControlPlane this
				// class stamps creates Machines, and each of them needs an
				// infrastructure object stamped from this template. Without it
				// the control plane provider has nothing to create a Machine's
				// DevMachine from.
				MachineInfrastructure: clusterv1.ControlPlaneClassMachineInfrastructureTemplate{
					TemplateRef: clusterv1.ClusterClassTemplateReference{
						APIVersion: infrav1.GroupVersion.String(),
						Kind:       "DevMachineTemplate",
						Name:       ControlPlaneMachineTemplateName,
					},
				},
				Naming: clusterv1.ControlPlaneClassNamingSpec{Template: "{{ .cluster.name }}-cp"},
			},
			Workers: clusterv1.WorkersClass{
				MachineDeployments: []clusterv1.MachineDeploymentClass{{
					Class: WorkerClass,
					Bootstrap: clusterv1.MachineDeploymentClassBootstrapTemplate{
						TemplateRef: clusterv1.ClusterClassTemplateReference{
							APIVersion: bootstrapv1.GroupVersion.String(),
							Kind:       "KubeadmConfigTemplate",
							Name:       WorkerBootstrapTemplateName,
						},
					},
					Infrastructure: clusterv1.MachineDeploymentClassInfrastructureTemplate{
						TemplateRef: clusterv1.ClusterClassTemplateReference{
							APIVersion: infrav1.GroupVersion.String(),
							Kind:       "DevMachineTemplate",
							Name:       WorkerMachineTemplateName,
						},
					},
					// Spelled out because nothing defaults it: the rollout
					// strategy is normally filled in by the MachineDeployment
					// admission webhook, and this demo serves no webhooks. The
					// topology controller copies what it finds here onto the
					// MachineDeployment it creates, so an empty strategy here
					// is an empty strategy there, and the MachineDeployment
					// reconciler fails with "unexpected deployment strategy
					// type: ".
					Rollout: clusterv1.MachineDeploymentClassRolloutSpec{
						Strategy: clusterv1.MachineDeploymentClassRolloutStrategy{
							Type: clusterv1.RollingUpdateMachineDeploymentStrategyType,
							RollingUpdate: clusterv1.MachineDeploymentClassRolloutStrategyRollingUpdate{
								MaxSurge:       ptr.To(intstr.FromInt32(1)),
								MaxUnavailable: ptr.To(intstr.FromInt32(0)),
							},
						},
					},
					Naming: clusterv1.MachineDeploymentClassNamingSpec{
						Template: "{{ .cluster.name }}-{{ .machineDeployment.topologyName }}",
					},
				}},
			},
		},
	}
}

// NewCluster builds one demo cluster: a name, a class, a version and a shape.
//
// There is no infrastructureRef and no controlPlaneRef. Those are the topology
// controller's to fill in, from the class - and a Cluster that set them itself
// would not be a ClusterClass based cluster, it would be a hand-built one with
// a topology attached.
func NewCluster(name string, controlPlaneMachines, workerMachines int, version string) *clusterv1.Cluster {
	topology := clusterv1.Topology{
		ClassRef: clusterv1.ClusterClassRef{Name: ClassName},
		Version:  version,
	}

	if controlPlaneMachines > 0 {
		topology.ControlPlane = clusterv1.ControlPlaneTopology{
			Replicas: ptr.To(int32(controlPlaneMachines)), //nolint:gosec // a replica count from a flag, not arithmetic on untrusted input.
		}
	}

	// Workers need a control plane to join, so a run that asks for them asks
	// for a control plane too - see Options.validate.
	if workerMachines > 0 {
		topology.Workers = clusterv1.WorkersTopology{
			MachineDeployments: []clusterv1.MachineDeploymentTopology{{
				Class:    WorkerClass,
				Name:     WorkerTopologyName,
				Replicas: ptr.To(int32(workerMachines)), //nolint:gosec // as above.
			}},
		}
	}

	return &clusterv1.Cluster{
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
			Topology: topology,
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
