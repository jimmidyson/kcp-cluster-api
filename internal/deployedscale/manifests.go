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
	KcpName = "kcp"
	KcpPort = 6443
	// KcpEtcdPort is the embedded etcd's client port, where its own metrics
	// are served. kcp defaults to this and the run does not override it.
	KcpEtcdPort = 2379
	// RootWorkspace is where the exports are published and where every
	// manager looks for its APIExportEndpointSlice.
	RootWorkspace         = "root"
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

	// ImagePullPolicy is how the kubelet fetches the manager images. Empty
	// means IfNotPresent — see DefaultImagePullPolicy for why that default
	// rather than Kubernetes' own.
	ImagePullPolicy corev1.PullPolicy

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

// DefaultImagePullPolicy is IfNotPresent, which is not what Kubernetes would
// choose and is deliberate.
//
// Kubernetes defaults the policy from the tag: `:latest`, or no tag at all,
// gets Always. That is right for an image a registry serves and wrong for one
// that was loaded straight onto the nodes, which is how the local path works —
// `KO_DOCKER_REPO=kind.local` builds and loads without a registry existing at
// all. Under Always the kubelet then tries to pull `kind.local/core-manager`,
// finds no such registry, and the deployment sits in ImagePullBackOff until
// the run times out.
//
// So the policy is stated rather than inherited from a tag. A run against a
// real registry with a moving tag should set it to Always, which is what the
// option is for.
const DefaultImagePullPolicy = corev1.PullIfNotPresent

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

// KcpBaseURL is kcp's address inside the cluster, with no logical cluster on
// it. This is what kcp itself is told to advertise.
func (o Options) KcpBaseURL() string {
	return fmt.Sprintf("https://%s.%s.svc:%d", KcpName, o.Namespace, KcpPort)
}

// KcpServerURL is what a client's kubeconfig addresses: the base with a
// logical cluster on it.
//
// Named explicitly rather than left off. A bare base URL resolves to a
// workspace by kcp's own default, which is a thing to remember rather than a
// thing to read, and the managers look for their APIExportEndpointSlice in
// "the workspace targeted by the kubeconfig" — so the workspace they look in
// should be written down where somebody can see it.
func (o Options) KcpServerURL() string {
	return o.KcpBaseURL() + "/clusters/" + RootWorkspace
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
// # Headless, and this is load-bearing
//
// kcp is told to advertise this Service's name as its shard URL, and it has to
// be able to reach that address *itself* — its apibinder initializer resolves
// the APIExports it binds through the advertised address. A virtual IP does
// not satisfy that: a pod connecting to a ClusterIP whose only endpoint is
// itself is the hairpin case, and where it does not work kcp cannot reach its
// own advertised URL.
//
// The failure that produces is silent and looks like somebody else's. The
// default APIBindings never bind, the system:apibindings initializer is never
// removed, and every workspace sits in Initializing for ever — reported
// against whatever created the workspace. Confirmed by starting kcp with an
// advertised address it could not reach and watching workspaces hang exactly
// that way.
//
// Headless removes the virtual IP: the name resolves straight to the pod, so
// kcp reaches itself at its own address and the managers reach it directly
// too. The certificate covers the name either way.
//
// Not a NodePort, for the reason it never was: a node-addressed Service is the
// sort of thing that works on a single-node cluster and shapes a harness
// around it. The driver outside the cluster reaches kcp through a forwarded
// port, which works the same on one node and on fifty.
func (o Options) KcpService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: KcpName, Namespace: o.Namespace, Labels: labels(KcpName)},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels(KcpName),
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       KcpPort,
				TargetPort: intstr.FromInt32(KcpPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// KcpArgs are the flags kcp is started with.
//
// A function rather than a literal inside the Deployment so that the exact
// flag set can be started and exercised without a cluster. It is worth that:
// kcp's own controllers run inside it, and a flag combination that stops one
// of them does not fail loudly — it produces workspaces that never leave
// Initializing, which looks like a problem with whatever created them.
//
// baseURL is what kcp advertises itself as. dataDir and credentialsDir are
// where it keeps its state and finds its serving certificate and token file,
// which differ between a container and a local process.
func KcpArgs(baseURL, dataDir, credentialsDir string, port int) []string {
	return []string{
		"start",
		"--root-directory=" + dataDir,
		fmt.Sprintf("--secure-port=%d", port),
		"--bind-address=0.0.0.0",
		// Supplied rather than self-signed, so the certificate covers the
		// Service name a pod on another node resolves. See Credentials.
		fmt.Sprintf("--tls-cert-file=%s/tls.crt", credentialsDir),
		fmt.Sprintf("--tls-private-key-file=%s/tls.key", credentialsDir),
		fmt.Sprintf("--token-auth-file=%s/tokens.csv", credentialsDir),
		// What kcp writes into APIExportEndpointSlices and hands to clients.
		// Without these it advertises the address it detected for itself,
		// which is a pod IP that changes on every restart and is not what the
		// serving certificate covers.
		"--shard-base-url=" + baseURL,
		"--shard-external-url=" + baseURL,
		"--audit-log-path=-",
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
						Name:            KcpName,
						Image:           o.KcpImage,
						ImagePullPolicy: o.imagePullPolicy(),
						Args:            KcpArgs(o.KcpBaseURL(), "/data", CredentialsMountPath, KcpPort),
						Env:             memoryLimitEnvFor(o.kcpResources()),
						Ports:           []corev1.ContainerPort{{Name: "https", ContainerPort: KcpPort, Protocol: corev1.ProtocolTCP}},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "credentials", MountPath: CredentialsMountPath, ReadOnly: true},
							{Name: "data", MountPath: "/data"},
						},
						Resources:                o.kcpResources(),
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
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

func (o Options) imagePullPolicy() corev1.PullPolicy {
	if o.ImagePullPolicy == "" {
		return DefaultImagePullPolicy
	}
	return o.ImagePullPolicy
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

	// The same ceiling the shard gets, and for the same reason: a limit the
	// collector does not know about is a limit it will walk past.
	env = append(env, memoryLimitEnvFor(o.managerResources())...)

	replicas := int32(1)
	podLabels := labels(c.Name)

	spec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:            c.Name,
			Image:           o.Images[c.Name],
			ImagePullPolicy: o.imagePullPolicy(),
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
			// So a container that dies carries its last words in its own
			// status. Nothing here writes to the default termination log, so
			// without this a crash reports a reason and no message, which is
			// most of the way to saying nothing.
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
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

// InfrastructureObjects are what a run creates first: the namespace, the
// secrets a pod mounts, and kcp itself.
//
// Ordered rather than sorted, because a Deployment whose Secret does not exist
// yet does not fail — it starts a pod that cannot mount and waits, which is a
// run that hangs rather than one that reports what is wrong.
func (o Options) InfrastructureObjects(creds *Credentials) ([]client.Object, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	kubeconfig, err := o.KubeconfigSecret(creds)
	if err != nil {
		return nil, err
	}
	kcp := o.KcpDeployment()
	annotateCredentials(&kcp.Spec.Template, creds)
	return []client.Object{
		o.NamespaceObject(),
		o.CredentialsSecret(creds),
		kubeconfig,
		o.KcpService(),
		kcp,
	}, nil
}

// CredentialsAnnotation carries a fingerprint of the credentials a pod was
// built for.
//
// # Why a Deployment has to name its credentials
//
// A Deployment whose pods mount a Secret does not restart when that Secret
// changes, and kcp reads its serving certificate once at startup. So a run that
// mints new credentials into an existing namespace updates the Secret, leaves
// the old pod running, and then cannot talk to it:
//
//	tls: failed to verify certificate: x509: certificate signed by unknown
//	authority ... "kcp-cluster-api-scale-ca"
//
// which reads as a mistake in how the certificate was built rather than as a
// server still serving the previous one. Putting the fingerprint in the pod
// template makes new credentials a new template, so the Deployment rolls and
// the pod that comes back is the one the client can verify.
const CredentialsAnnotation = "scale.kcp-cluster-api/credentials"

func annotateCredentials(tmpl *corev1.PodTemplateSpec, creds *Credentials) {
	if creds == nil {
		return
	}
	if tmpl.Annotations == nil {
		tmpl.Annotations = map[string]string{}
	}
	tmpl.Annotations[CredentialsAnnotation] = creds.Fingerprint()
}

// ManagerObjects are the four Deployments, created separately and later.
//
// # Why they are not created with the rest
//
// A manager resolves its APIExport's virtual workspace at startup, by polling
// for its APIExportEndpointSlice, and exits when it does not find one — see
// providerwiring.VirtualWorkspaceConfig. The slice carries no endpoints until
// the export is published *and* a workspace has bound it, so a manager
// deployed alongside kcp starts into a world where neither has happened yet,
// exits, and enters CrashLoopBackOff.
//
// It would eventually recover, which is what makes this worth writing down
// rather than fixing quietly: the run does not fail because the manager is
// broken, it fails because the kubelet's backoff grows to minutes while the
// harness waits, and what a measurement wants is a process that started once
// and cleanly. So the exports are published and the first workspace bound
// before any of this is created.
func (o Options) ManagerObjects() ([]client.Object, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	objects := make([]client.Object, 0, len(o.components()))
	for _, c := range o.components() {
		objects = append(objects, o.ManagerDeployment(c))
	}
	return objects, nil
}

// Objects is everything a run creates, in creation order. Callers that need
// the two phases apart use InfrastructureObjects and ManagerObjects.
func (o Options) Objects(creds *Credentials) ([]client.Object, error) {
	infrastructure, err := o.InfrastructureObjects(creds)
	if err != nil {
		return nil, err
	}
	managers, err := o.ManagerObjects()
	if err != nil {
		return nil, err
	}
	return append(infrastructure, managers...), nil
}

// MetricsURL is where the harness scrapes one manager's process metrics.
func MetricsURL(podIP string) string {
	return fmt.Sprintf("http://%s:%d/metrics", podIP, MetricsPort)
}

// MemoryLimitEnv gives a Go process a heap ceiling matched to its container's.
//
// # Why a container memory limit is not enough on its own
//
// Go's collector runs when the heap reaches a multiple of the live set — twice
// it, by default — and knows nothing about the cgroup it is in. A process whose
// live data is comfortably inside its limit will still grow past that limit and
// be killed, because nothing told the runtime the limit exists.
//
// kcp did exactly that. At 250 Machines its live heap was 1.63 GiB against a
// 4 GiB limit, while the runtime had taken 3.02 GiB from the OS and was still
// climbing — the ratio rose from 1.44x to 1.85x as allocation churned during
// provisioning. It was then OOM killed with well under half the limit in use,
// and the run recorded that as the fleet size the shard could not hold. It was
// not: it was the fleet size at which an untuned collector overran a limit
// nobody had told it about.
//
// GOMEMLIMIT is a soft limit: the collector works harder as the heap approaches
// it rather than failing, which turns an OOM kill into CPU. That is the right
// trade for a measurement — a slower shard is a data point, a dead one is not.
//
// Set below the container's limit, not equal to it. The limit covers the whole
// process, and stacks, the binary and anything mapped live outside the heap.
func MemoryLimitEnv(limit resource.Quantity) []corev1.EnvVar {
	headroom := limit.Value() / 10
	if max := int64(512 << 20); headroom > max {
		headroom = max
	}
	return []corev1.EnvVar{{
		Name:  "GOMEMLIMIT",
		Value: fmt.Sprintf("%dB", limit.Value()-headroom),
	}}
}

// memoryLimitEnvFor reads the limit off a container's requirements. A container
// with no memory limit gets no ceiling, because there is none to respect.
func memoryLimitEnvFor(r corev1.ResourceRequirements) []corev1.EnvVar {
	limit, ok := r.Limits[corev1.ResourceMemory]
	if !ok || limit.IsZero() {
		return nil
	}
	return MemoryLimitEnv(limit)
}
