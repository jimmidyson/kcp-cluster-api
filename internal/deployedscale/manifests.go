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

// Package deployedscale builds and reads a measurement in which the four
// managers run as four Deployments in a Kubernetes cluster, rather than as
// four sets of controllers in one process.
//
// # What it is for
//
// Every resource figure this repository publishes carries the same caveat,
// written on each report: "four deployments co-located, so one engagement per
// workspace rather than four". That is not a measurement error to refine away.
// It is a property of measuring four deployments inside one process, and the
// only way to remove it is to run them as four deployments.
//
// See `specs/20260831-210000-deployed-fleet-scale/spec.md`.
//
// # The cluster is a kubeconfig, not kind
//
// Nothing here may assume the components share a node. kind is one way to get
// a cluster and the only one this repository automates; a real multi-node
// cluster is where the figures are worth quoting from, and a harness that
// assumed one node could not produce them. So: no host networking, no
// hostPath, no loopback address between components, and kcp inside the cluster
// rather than beside it.
package deployedscale

import (
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

// Names of the objects a run creates. Fixed rather than generated: a run is
// scoped by its namespace, and a stable name inside it is what makes a failed
// run inspectable with a command somebody can type.
const (
	KcpName               = "kcp"
	KcpPort               = 6443
	CredentialsSecretName = "kcp-credentials"
	KubeconfigSecretName  = "kcp-kubeconfig"

	MetricsPort     = 8080
	MetricsPortName = "metrics"
	HealthPort      = 9440

	// CredentialsMountPath is where kcp finds its serving certificate and
	// token file; KubeconfigMountPath is where a manager finds its kubeconfig.
	CredentialsMountPath = "/etc/kcp-credentials"
	KubeconfigMountPath  = "/etc/kcp"

	// ComponentLabel marks everything a run creates, so a namespace can be
	// read back component by component and torn down as a unit.
	ComponentLabel = "kcp-cluster-api.jimmidyson.github.io/scale-component"
)

// Component names. These are the binary names, which is also what the report
// attributes cost to — the whole point being that a figure names the
// deployment it belongs to.
const (
	ComponentCore              = "core-manager"
	ComponentBootstrap         = "kubeadm-bootstrap-manager"
	ComponentControlPlane      = "kubeadm-control-plane-manager"
	ComponentDevInfrastructure = "dev-infrastructure-manager"
)

// Component is one manager, deployed on its own.
type Component struct {
	// Name is the binary's name, the Deployment's name, and the name its cost
	// is reported under.
	Name string
	// ExportName is the APIExportEndpointSlice this manager discovers its
	// workspaces through — the one flag that differs between them.
	ExportName string
	// NeedsPodIP is true for the manager that serves in-memory workload
	// clusters. It has to advertise an address other pods can reach, and the
	// pod IP is the one it has. See coremanager.NewDevInfrastructure, whose
	// host parameter exists for exactly this.
	NeedsPodIP bool
	// SingleReplica is true where more than one replica on a node would
	// collide. The in-memory backend's mux binds a fixed port range, so two of
	// them on one node fail with "address already in use".
	SingleReplica bool
}

// Components are the four managers, in the order a reader wants them: the one
// every workspace engages first, then the providers.
func Components() []Component {
	return []Component{
		{Name: ComponentCore, ExportName: capiexports.CoreExport},
		{Name: ComponentBootstrap, ExportName: capiexports.BootstrapExport},
		{Name: ComponentControlPlane, ExportName: capiexports.ControlPlaneExport},
		{Name: ComponentDevInfrastructure, ExportName: capiexports.InfraExport, NeedsPodIP: true, SingleReplica: true},
	}
}

// ComponentsNamed selects components by name, preserving Components' order.
//
// Selecting is how the specification's milestones are expressed: M1 deploys
// core-manager alone and reconciles it against an in-process run of the same
// shape, and only once that agrees does M2 deploy all four. A milestone is a
// configuration of one harness rather than a second harness.
func ComponentsNamed(names ...string) ([]Component, error) {
	if len(names) == 0 {
		return nil, errors.New("no components named: a run with no managers measures nothing")
	}
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}

	var out []Component
	for _, c := range Components() {
		if wanted[c.Name] {
			out = append(out, c)
			delete(wanted, c.Name)
		}
	}
	for n := range wanted {
		return nil, fmt.Errorf("unknown component %q", n)
	}
	return out, nil
}

// Options is everything a run needs that it cannot work out for itself.
type Options struct {
	// Namespace holds every object the run creates, and is what tearing it
	// down deletes.
	Namespace string
	// KcpImage and Images are the containers to run. Images is keyed by
	// component name; a component with no image is an error rather than a
	// default, because a wrong image silently measures the wrong build.
	KcpImage string
	Images   map[string]string

	// Components to deploy. Empty means all four.
	Components []Component

	// SpreadAcrossNodes adds anti-affinity so no two components share a node.
	// It is required for the figures to be about a real deployment and it is
	// not the default, because on a single-node cluster it makes every pod
	// unschedulable — which is a worse first experience than a labelled
	// co-located run.
	SpreadAcrossNodes bool

	// ManagerResources and KcpResources size the containers. A memory limit is
	// load-bearing rather than hygiene: an OOMKill at a given fleet size is
	// the capacity finding this whole measurement exists to produce, and a
	// container with no limit can never produce it.
	ManagerResources corev1.ResourceRequirements
	KcpResources     corev1.ResourceRequirements
}

// DefaultManagerResources are generous on the limit on purpose. The limit is
// what makes an OOMKill possible and therefore what makes a capacity finding
// possible, but a limit set too low turns every run into one and measures the
// limit rather than the fleet.
func DefaultManagerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
	}
}

// DefaultKcpResources size the server the whole fleet talks to.
func DefaultKcpResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}
}

// No CPU limit is set on either. CPU is throttled rather than killed, so a
// limit does not produce a capacity finding — it produces a slower run that
// looks like a slower system, which is a measurement of the limit. Throttling
// is recorded from the container metrics instead.

func (o Options) components() []Component {
	if len(o.Components) > 0 {
		return o.Components
	}
	return Components()
}

func (o Options) validate() error {
	var errs []error
	if o.Namespace == "" {
		errs = append(errs, errors.New("no namespace: a run must be scoped to one so it can be torn down as a unit"))
	}
	if o.KcpImage == "" {
		errs = append(errs, errors.New("no kcp image"))
	}
	for _, c := range o.components() {
		if o.Images[c.Name] == "" {
			errs = append(errs, fmt.Errorf("no image for %s: a missing image would measure a different build than the one asked for", c.Name))
		}
	}
	return errors.Join(errs...)
}

// KcpServerURL is how a pod in the cluster addresses kcp.
func (o Options) KcpServerURL() string {
	return fmt.Sprintf("https://%s.%s.svc:%d", KcpName, o.Namespace, KcpPort)
}

func labels(component string) map[string]string {
	return map[string]string{ComponentLabel: component}
}

// Namespace is the object every other object in a run belongs to.
func (o Options) NamespaceObject() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: o.Namespace, Labels: labels("namespace")}}
}

// CredentialsSecret carries what kcp serves with and authenticates against.
func (o Options) CredentialsSecret(creds *Credentials) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: CredentialsSecretName, Namespace: o.Namespace, Labels: labels(KcpName)},
		Data: map[string][]byte{
			"tls.crt":    creds.ServingCertPEM,
			"tls.key":    creds.ServingKeyPEM,
			"tokens.csv": []byte(creds.TokenAuthCSV()),
		},
	}
}

// KubeconfigSecret is what every manager reads to reach kcp. It addresses the
// Service, which is the name the serving certificate covers and the only one
// reachable from another node.
func (o Options) KubeconfigSecret(creds *Credentials) (*corev1.Secret, error) {
	raw, err := creds.Kubeconfig(o.KcpServerURL())
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: KubeconfigSecretName, Namespace: o.Namespace, Labels: labels("managers")},
		Data:       map[string][]byte{"kubeconfig": raw},
	}, nil
}

// KcpService gives kcp a stable name inside the cluster.
//
// ClusterIP, deliberately. A NodePort would be reachable at a node's address,
// which is the sort of thing that works on a single-node cluster and shapes a
// harness around it; the driver outside the cluster reaches kcp through a
// forwarded port instead, which works the same on one node and on fifty.
func (o Options) KcpService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: KcpName, Namespace: o.Namespace, Labels: labels(KcpName)},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: labels(KcpName),
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       KcpPort,
				TargetPort: intstr.FromInt32(KcpPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// KcpDeployment runs the server the whole fleet talks to.
//
// One replica and an emptyDir. The state is one measurement's worth and dies
// with it, and a PersistentVolumeClaim would make the harness depend on a
// storage class — which is exactly the kind of assumption that works on the
// cluster it was written against and fails on the next one.
func (o Options) KcpDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: KcpName, Namespace: o.Namespace, Labels: labels(KcpName)},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels(KcpName)},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(KcpName)},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  KcpName,
						Image: o.KcpImage,
						Args: []string{
							"start",
							"--root-directory=/data",
							fmt.Sprintf("--secure-port=%d", KcpPort),
							"--bind-address=0.0.0.0",
							// Supplied rather than self-signed, so the
							// certificate covers the Service name a pod on
							// another node resolves. See Credentials.
							fmt.Sprintf("--tls-cert-file=%s/tls.crt", CredentialsMountPath),
							fmt.Sprintf("--tls-private-key-file=%s/tls.key", CredentialsMountPath),
							fmt.Sprintf("--token-auth-file=%s/tokens.csv", CredentialsMountPath),
							// What kcp writes into APIExportEndpointSlices and
							// hands to clients. Without these it advertises the
							// address it detected for itself, which is a pod IP
							// that changes on every restart and is not what the
							// serving certificate covers.
							"--shard-base-url=" + o.KcpServerURL(),
							"--shard-external-url=" + o.KcpServerURL(),
							"--audit-log-path=-",
						},
						Ports: []corev1.ContainerPort{{Name: "https", ContainerPort: KcpPort, Protocol: corev1.ProtocolTCP}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "credentials", MountPath: CredentialsMountPath, ReadOnly: true},
							{Name: "data", MountPath: "/data"},
						},
						Resources: o.kcpResources(),
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
								Path:   "/readyz",
								Port:   intstr.FromInt32(KcpPort),
								Scheme: corev1.URISchemeHTTPS,
							}},
							InitialDelaySeconds: 10,
							PeriodSeconds:       5,
							// kcp brings up a great deal before it is ready,
							// and a fleet-sized run has time to wait for it.
							FailureThreshold: 60,
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "credentials", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: CredentialsSecretName},
						}},
						{Name: "data", VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						}},
					},
				},
			},
		},
	}
}

func (o Options) kcpResources() corev1.ResourceRequirements {
	if o.KcpResources.Requests == nil && o.KcpResources.Limits == nil {
		return DefaultKcpResources()
	}
	return o.KcpResources
}

func (o Options) managerResources() corev1.ResourceRequirements {
	if o.ManagerResources.Requests == nil && o.ManagerResources.Limits == nil {
		return DefaultManagerResources()
	}
	return o.ManagerResources
}

// ManagerDeployment builds one manager's Deployment.
func (o Options) ManagerDeployment(c Component) *appsv1.Deployment {
	env := []corev1.EnvVar{{
		// controller-runtime's config loader reads KUBECONFIG, and these
		// binaries register no --kubeconfig flag of their own.
		Name:  "KUBECONFIG",
		Value: KubeconfigMountPath + "/kubeconfig",
	}}
	if c.NeedsPodIP {
		// What the in-memory backend advertises its workload clusters at. Its
		// host is a parameter precisely so a deployment can pass this; an
		// empty one does not fail, it produces DevClusters whose endpoint is
		// ":20000" and a control plane that waits forever.
		env = append(env, corev1.EnvVar{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		})
	}

	replicas := int32(1)
	podLabels := labels(c.Name)

	spec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  c.Name,
			Image: o.Images[c.Name],
			Args: []string{
				"--endpoint-slice-name=" + c.ExportName,
				fmt.Sprintf("--health-addr=:%d", HealthPort),
				fmt.Sprintf("--metrics-bind-address=:%d", MetricsPort),
			},
			Env: env,
			Ports: []corev1.ContainerPort{
				{Name: MetricsPortName, ContainerPort: MetricsPort, Protocol: corev1.ProtocolTCP},
				{Name: "health", ContainerPort: HealthPort, Protocol: corev1.ProtocolTCP},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "kubeconfig", MountPath: KubeconfigMountPath, ReadOnly: true},
			},
			Resources: o.managerResources(),
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
					Path: "/readyz", Port: intstr.FromInt32(HealthPort),
				}},
				InitialDelaySeconds: 5,
				PeriodSeconds:       5,
				FailureThreshold:    60,
			},
		}},
		Volumes: []corev1.Volume{{
			Name: "kubeconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: KubeconfigSecretName},
			},
		}},
	}

	if o.SpreadAcrossNodes {
		spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			// Required rather than preferred: a run asking to be spread and
			// quietly co-scheduled anyway would report a single-node figure
			// under a multi-node label, which is the one mistake this option
			// exists to prevent. Unschedulable is the honest failure.
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey: corev1.LabelHostname,
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      ComponentLabel,
						Operator: metav1.LabelSelectorOpExists,
					}},
				},
			}},
		}}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: o.Namespace, Labels: podLabels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec:       spec,
			},
		},
	}
}

// Objects is everything a run creates, in the order it must be created:
// namespace, then the secrets a pod mounts, then kcp, then the managers.
//
// Ordered rather than sorted, because a Deployment whose Secret does not exist
// yet does not fail — it starts a pod that cannot mount and waits, which is a
// run that hangs rather than one that reports what is wrong.
func (o Options) Objects(creds *Credentials) ([]client.Object, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	kubeconfig, err := o.KubeconfigSecret(creds)
	if err != nil {
		return nil, err
	}

	objects := []client.Object{
		o.NamespaceObject(),
		o.CredentialsSecret(creds),
		kubeconfig,
		o.KcpService(),
		o.KcpDeployment(),
	}
	for _, c := range o.components() {
		objects = append(objects, o.ManagerDeployment(c))
	}
	return objects, nil
}

// MetricsURL is where the harness scrapes one manager's process metrics.
func MetricsURL(podIP string) string {
	return fmt.Sprintf("http://%s:%d/metrics", podIP, MetricsPort)
}
