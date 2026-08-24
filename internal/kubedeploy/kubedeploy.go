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

// Package kubedeploy runs this project on Kubernetes: a kcp shard and one
// deployment per Cluster API provider, as pods rather than as processes on
// somebody's laptop.
//
// The demo (internal/demo) starts a kcp server and every provider's
// controllers in one process, which is the right shape for showing what the
// project does and the wrong shape for everything else: the managers share a
// process they would never share in an installation, the shard is a child
// process that dies with the terminal, and nothing about the run says whether
// the wiring survives being split across pods. This package is the same
// topology as a deployment: one pod per provider, each reaching kcp over the
// network, each holding only its own credentials.
//
// What it does not change is what the demo *does*. The workspaces, the
// exports, the ClusterClass and the clusters are created by the same
// internal/demo code, run as a Job with its manager half switched off - so
// there is one description of what a demo is, and this package decides only
// where it runs.
package kubedeploy

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// Names of the objects a deployment is made of. They are constants rather than
// derived from the namespace, so that two installations in two namespaces are
// described by the same names and a person reading a manifest recognises them.
const (
	// DefaultNamespace holds a whole installation: the shard and every
	// manager.
	DefaultNamespace = "kcp-demo"

	// KcpName is the shard's StatefulSet, its Service and its DNS name.
	KcpName = "kcp"

	// KcpPort is what the shard serves on, inside the cluster and through a
	// port-forward.
	KcpPort = 6443

	// ServingSecretName holds the shard's serving certificate and key.
	ServingSecretName = "kcp-serving-cert"

	// ClientCASecretName holds the authority the shard verifies client
	// certificates against.
	ClientCASecretName = "kcp-client-ca"

	// KubeconfigSecretName holds the kubeconfigs every other pod reaches the
	// shard with. Two of them: see ProviderKubeconfigKey.
	KubeconfigSecretName = "kcp-kubeconfig"

	// DemoJobName is the run that creates the workspaces and the clusters.
	DemoJobName = "capi-demo"

	// WorkspaceManagerName is the deployment behind the cluster-api
	// WorkspaceType: it holds no Cluster API export of its own, so it is named
	// here rather than derived from one.
	WorkspaceManagerName = "cluster-api-workspace-manager"
)

// The two kubeconfigs in KubeconfigSecretName, and why there are two.
//
// controller-runtime resolves --kubeconfig through client-go's loading rules
// and offers no --context to go with it, so a manager gets its file's current
// context or nothing. The provider managers read an APIExportEndpointSlice out
// of the workspace their config addresses and so need one scoped to it; the
// workspace manager scopes itself and so needs a cluster-unaware one. Same
// credentials, different current context.
const (
	ProviderKubeconfigKey = "provider.kubeconfig"
	BaseKubeconfigKey     = "base.kubeconfig"

	// KubeconfigMountPath is where both are mounted, one file per pod, always
	// under the same name so that every manager takes the same flag.
	KubeconfigMountPath = "/etc/kcp"
	kubeconfigFileName  = "kubeconfig"
)

// Where the shard keeps what it is given and what it writes.
const (
	kcpRootDirectory   = "/var/lib/kcp"
	kcpServingCertPath = "/etc/kcp/serving"
	kcpClientCAPath    = "/etc/kcp/client-ca"
)

// DefaultStartupTimeout is how long a manager waits for the things it cannot
// create for itself before giving up.
//
// Generous, because the thing it waits for is a demo run that Kubernetes
// starts alongside it rather than before it: kcp gives an APIExport an
// endpoint only once a workspace has bound it, and the workspaces are created
// by the Job. A manager that exited instead would back off exponentially and
// turn a wait of seconds into minutes of CrashLoopBackOff.
const DefaultStartupTimeout = 30 * time.Minute

// DefaultImage is this project's image: every binary in this repository, plus
// the pinned kcp server.
//
// One image rather than two because the shard's version is already pinned by
// the build - see the Dockerfile - and a second pin in a manifest is a second
// thing to keep in step. Override it with Options.Image for a registry of your
// own, and Options.KcpImage to run the upstream kcp image instead.
const DefaultImage = "ghcr.io/jimmidyson/kcp-cluster-api:latest"

// KcpCommand is where the pinned kcp binary lives in DefaultImage.
const KcpCommand = "/usr/local/bin/kcp"

// managerCommands maps an APIExport to the binary that reconciles it.
//
// A provider missing from this table has no deployment, which is the failure
// this map exists to make loud: Managers returns an error rather than quietly
// deploying a shard that serves types nothing reconciles.
var managerCommands = map[string]string{
	capiexports.CoreExport:         "/usr/local/bin/core-manager",
	capiexports.BootstrapExport:    "/usr/local/bin/kubeadm-bootstrap-manager",
	capiexports.ControlPlaneExport: "/usr/local/bin/kubeadm-control-plane-manager",
	capiexports.InfraExport:        "/usr/local/bin/dev-infrastructure-manager",
}

// Manager is one provider's deployment.
type Manager struct {
	// Name is the Deployment's name, which is the export's name: one
	// deployment per export is the topology this project has, so naming them
	// the same thing makes `kubectl get deploy` and `kubectl get apiexports`
	// read as the same list.
	Name string

	// Command is the binary in the image.
	Command string

	// EndpointSlice is the APIExportEndpointSlice the manager discovers
	// workspaces through.
	EndpointSlice string

	// PodIP asks for the pod's own address in POD_IP. The dev infrastructure
	// provider advertises its in-memory workload clusters there, and without
	// it the DevCluster reports an endpoint of ":20000" that the control plane
	// provider then waits on forever.
	PodIP bool
}

// Managers returns one deployment per provider, in the order they are applied.
func Managers(providers []capiexports.Provider) ([]Manager, error) {
	managers := make([]Manager, 0, len(providers))
	for _, provider := range providers {
		command, ok := managerCommands[provider.Export]
		if !ok {
			// The Nutanix export is the case this is written for: it is
			// published so its types can be bound, and its manager is a
			// separate module that needs a Prism Central. Deploying it here
			// would engage every workspace to watch nothing.
			return nil, fmt.Errorf(
				"no manager in this image reconciles the %s APIExport: publish it without deploying a manager, or add its binary to managerCommands",
				provider.Export)
		}
		managers = append(managers, Manager{
			Name:          provider.Export,
			Command:       command,
			EndpointSlice: provider.Export,
			PodIP:         provider.Export == capiexports.InfraExport,
		})
	}
	return managers, nil
}

// DemoJob is the run that creates the workspaces, the classes and the clusters
// once the managers are up. Its fields are the demo's own flags.
type DemoJob struct {
	Workspaces           int
	Users                []string
	Clusters             int
	ControlPlaneMachines int
	WorkerMachines       int

	// Timeout is how long the run waits for every cluster to be ready.
	Timeout string

	// ExtraArgs are passed to the demo binary after everything above, so a
	// caller can set a flag this struct does not name.
	ExtraArgs []string
}

// Options describes one installation.
type Options struct {
	// Namespace holds all of it. Created if it does not exist.
	Namespace string

	// Image is this project's image, running every manager and the demo.
	Image string

	// KcpImage runs the shard. Empty means Image, which carries the pinned kcp
	// binary.
	KcpImage string

	// KcpCommand is the shard's entrypoint. Empty means KcpCommand, which is
	// where this project's image puts it; the upstream kcp image needs none,
	// so set it to "-" to pass arguments to the image's own entrypoint.
	KcpCommand string

	// ImagePullPolicy defaults to IfNotPresent, which is what a locally built
	// image loaded into a kind cluster needs: Always would send Kubernetes
	// looking for a registry that has never heard of it.
	ImagePullPolicy corev1.PullPolicy

	// Parent is the workspace the APIExports are published in and the demo
	// workspaces are created under. It is where every provider manager's
	// kubeconfig points.
	Parent string

	// Providers get a manager deployment each.
	Providers []capiexports.Provider

	// StartupTimeout is how long a manager waits for the things it cannot
	// create for itself - its APIExportEndpointSlice having an endpoint, and
	// for the workspace manager the WorkspaceType itself. Generous by default:
	// these arrive when the demo run publishes them, which is after Kubernetes
	// has started the pods that wait for them.
	StartupTimeout time.Duration

	// StorageSize is the shard's volume. Its state directory holds etcd, so
	// this is the size of the whole control plane's data.
	StorageSize string

	// StorageClassName is the class that volume comes from. Empty means the
	// cluster's default.
	StorageClassName string

	// Credentials are the certificates and kubeconfigs. Generate them with
	// NewCredentials.
	Credentials Credentials

	// Demo is the run to create once everything is up. Nil deploys the shard
	// and the managers and creates nothing in them.
	Demo *DemoJob
}

func (o *Options) applyDefaults() {
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if o.Image == "" {
		o.Image = DefaultImage
	}
	if o.KcpImage == "" {
		o.KcpImage = o.Image
	}
	if o.KcpCommand == "" {
		o.KcpCommand = KcpCommand
	}
	if o.ImagePullPolicy == "" {
		o.ImagePullPolicy = corev1.PullIfNotPresent
	}
	if o.Parent == "" {
		o.Parent = demo.DefaultParent
	}
	if len(o.Providers) == 0 {
		o.Providers = capiexports.All()
	}
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = DefaultStartupTimeout
	}
	if o.StorageSize == "" {
		o.StorageSize = "2Gi"
	}
}

// ServerURL is how everything inside the cluster reaches the shard.
func ServerURL(namespace string) string {
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", KcpName, namespace, KcpPort)
}

// ServerNames are the names the shard's certificate has to carry: its Service,
// in every form Kubernetes resolves it, and localhost so that the same
// certificate serves a `kubectl port-forward` on somebody's machine.
func ServerNames(namespace string) []string {
	return []string{
		KcpName,
		fmt.Sprintf("%s.%s", KcpName, namespace),
		fmt.Sprintf("%s.%s.svc", KcpName, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", KcpName, namespace),
		"localhost",
	}
}

// Objects is everything an installation is made of, in the order it is
// applied: the namespace, then the credentials, then the shard, then the
// managers, then the demo run.
//
// Order is not a convenience here. The managers mount a Secret that has to
// exist, and the demo Job creates objects in workspaces the managers have to
// be watching - see Apply, which waits between the layers rather than trusting
// the ordering to be enough.
func Objects(opts Options) ([]client.Object, error) {
	opts.applyDefaults()

	managers, err := Managers(opts.Providers)
	if err != nil {
		return nil, err
	}
	kubeconfigs, err := kubeconfigSecret(opts)
	if err != nil {
		return nil, err
	}

	objects := []client.Object{
		namespace(opts),
		servingSecret(opts),
		clientCASecret(opts),
		kubeconfigs,
		service(opts),
		statefulSet(opts),
	}
	for _, manager := range managers {
		objects = append(objects, managerDeployment(opts, manager))
	}
	objects = append(objects, workspaceManagerDeployment(opts))
	if opts.Demo != nil {
		objects = append(objects, demoJob(opts))
	}
	return objects, nil
}

// componentLabels are what `kubectl -l` selects a whole installation with, and what
// Delete removes.
func componentLabels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/part-of":   "kcp-cluster-api",
		"app.kubernetes.io/component": component,
		"app.kubernetes.io/name":      component,
	}
}

// PartOfSelector matches every object in an installation.
const PartOfSelector = "app.kubernetes.io/part-of=kcp-cluster-api"

func objectMeta(name, ns, component string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: ns,
		Labels:    componentLabels(component),
	}
}

func namespace(opts Options) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: objectMeta(opts.Namespace, "", "namespace")}
	ns.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}
	ns.Name = opts.Namespace
	return ns
}

func servingSecret(opts Options) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: objectMeta(ServingSecretName, opts.Namespace, KcpName),
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       opts.Credentials.Serving.CertPEM,
			corev1.TLSPrivateKeyKey: opts.Credentials.Serving.KeyPEM,
		},
	}
	secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
	return secret
}

func clientCASecret(opts Options) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: objectMeta(ClientCASecretName, opts.Namespace, KcpName),
		Data:       map[string][]byte{"ca.crt": opts.Credentials.ClientCA},
	}
	secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
	return secret
}

func kubeconfigSecret(opts Options) (*corev1.Secret, error) {
	server := ServerURL(opts.Namespace)
	provider, err := Kubeconfig(server, opts.Parent, opts.Parent, opts.Credentials)
	if err != nil {
		return nil, err
	}
	base, err := Kubeconfig(server, opts.Parent, demo.BaseContext, opts.Credentials)
	if err != nil {
		return nil, err
	}

	secret := &corev1.Secret{
		ObjectMeta: objectMeta(KubeconfigSecretName, opts.Namespace, KcpName),
		Data: map[string][]byte{
			ProviderKubeconfigKey: provider,
			BaseKubeconfigKey:     base,
		},
	}
	secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
	return secret, nil
}

func service(opts Options) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: objectMeta(KcpName, opts.Namespace, KcpName),
		Spec: corev1.ServiceSpec{
			Selector: componentLabels(KcpName),
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       KcpPort,
				TargetPort: intstr.FromInt32(KcpPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	svc.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}
	return svc
}

// statefulSet is the shard.
//
// A StatefulSet rather than a Deployment because it holds etcd: the volume is
// the control plane, one replica owns it, and a rolling update that started a
// second pod against the same data would corrupt it. One replica, and
// horizontal sharding is a kcp Partition rather than a replica count - see
// docs/conversion-plan.md's D6.
func statefulSet(opts Options) *appsv1.StatefulSet {
	server := ServerURL(opts.Namespace)
	args := []string{
		"start",
		"--root-directory=" + kcpRootDirectory,
		fmt.Sprintf("--secure-port=%d", KcpPort),
		// All three, for the reason internal/demo/kcpserver.go gives at
		// length: the shard hands out URLs from these, and the one that is
		// easiest to forget - the virtual workspace URL in the
		// APIExportEndpointSlice - is the address every manager connects to.
		// Left to kcp's own detection they would name the pod's IP, which
		// changes on every restart and is on no certificate.
		"--shard-base-url=" + server,
		"--shard-external-url=" + server,
		"--shard-virtual-workspace-url=" + server,
		"--tls-cert-file=" + kcpServingCertPath + "/" + corev1.TLSCertKey,
		"--tls-private-key-file=" + kcpServingCertPath + "/" + corev1.TLSPrivateKeyKey,
		"--client-ca-file=" + kcpClientCAPath + "/ca.crt",
	}
	container := corev1.Container{
		Name:            KcpName,
		Image:           opts.KcpImage,
		ImagePullPolicy: opts.ImagePullPolicy,
		Args:            args,
		Ports:           []corev1.ContainerPort{{Name: "https", ContainerPort: KcpPort}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: kcpRootDirectory},
			{Name: "serving-cert", MountPath: kcpServingCertPath, ReadOnly: true},
			{Name: "client-ca", MountPath: kcpClientCAPath, ReadOnly: true},
		},
		// Every probe is unauthenticated on purpose and answers that way: kcp
		// serves /livez and /readyz to an anonymous client, so the kubelet
		// needs no credential to ask.
		StartupProbe:   httpsProbe("/livez", 5*time.Minute),
		ReadinessProbe: httpsProbe("/readyz", 0),
		LivenessProbe:  httpsProbe("/livez", 0),
		Resources:      resources("500m", "1Gi"),
	}
	if opts.KcpCommand != "" && opts.KcpCommand != "-" {
		container.Command = []string{opts.KcpCommand}
	}

	set := &appsv1.StatefulSet{
		ObjectMeta: objectMeta(KcpName, opts.Namespace, KcpName),
		Spec: appsv1.StatefulSetSpec{
			ServiceName: KcpName,
			Replicas:    ptr(int32(1)),
			Selector:    &metav1.LabelSelector{MatchLabels: componentLabels(KcpName)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: componentLabels(KcpName)},
				Spec: corev1.PodSpec{
					Containers:      []corev1.Container{container},
					SecurityContext: podSecurityContext(),
					Volumes: []corev1.Volume{
						secretVolume("serving-cert", ServingSecretName, nil),
						secretVolume("client-ca", ClientCASecretName, nil),
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: componentLabels(KcpName)},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse(opts.StorageSize),
						},
					},
				},
			}},
		},
	}
	if opts.StorageClassName != "" {
		set.Spec.VolumeClaimTemplates[0].Spec.StorageClassName = &opts.StorageClassName
	}
	set.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}
	return set
}

// managerDeployment is one provider's controllers.
//
// One replica each, and that is a correctness statement rather than a demo
// shortcut: these controllers hold no lease, so a second replica would
// reconcile every workspace twice. Leader election across a fleet of
// workspaces is not built.
func managerDeployment(opts Options, manager Manager) *appsv1.Deployment {
	container := corev1.Container{
		Name:            "manager",
		Image:           opts.Image,
		ImagePullPolicy: opts.ImagePullPolicy,
		Command:         []string{manager.Command},
		Args: []string{
			"--kubeconfig=" + KubeconfigMountPath + "/" + kubeconfigFileName,
			"--endpoint-slice-name=" + manager.EndpointSlice,
			"--startup-timeout=" + opts.StartupTimeout.String(),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "kubeconfig", MountPath: KubeconfigMountPath, ReadOnly: true},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Ports: []corev1.ContainerPort{
			{Name: "health", ContainerPort: 9440},
			// Named so that a ServiceMonitor or a scrape annotation has
			// something to name. Nothing here creates either: what an
			// installation collects is its own decision.
			{Name: "metrics", ContainerPort: 8080},
		},
		// A startup probe, and it is the one that matters here. The health
		// endpoint binds when the manager starts its controllers, and that is
		// after it has waited for its APIExport to have an endpoint - up to
		// --startup-timeout, which is minutes rather than seconds. A liveness
		// probe alone would kill a manager for waiting exactly as long as it
		// was told to.
		StartupProbe:   httpProbe("/healthz", 9440, opts.StartupTimeout+time.Minute),
		ReadinessProbe: httpProbe("/readyz", 9440, 0),
		LivenessProbe:  httpProbe("/healthz", 9440, 0),
		Resources:      resources("200m", "512Mi"),
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   ptr(true),
			AllowPrivilegeEscalation: ptr(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	if manager.PodIP {
		container.Env = append(container.Env, corev1.EnvVar{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		})
	}

	return deployment(opts, manager.Name, container, ProviderKubeconfigKey)
}

// workspaceManagerDeployment is the controller behind the cluster-api
// WorkspaceType: it initializes a tenant's workspace and keeps the permission
// claim list current.
//
// It is the one manager that takes a cluster-unaware kubeconfig, because it
// scopes itself to --provider-workspace, and the one with no probes: it binds
// no health address, so a probe would fail a pod that is working.
func workspaceManagerDeployment(opts Options) *appsv1.Deployment {
	container := corev1.Container{
		Name:            "manager",
		Image:           opts.Image,
		ImagePullPolicy: opts.ImagePullPolicy,
		Command:         []string{"/usr/local/bin/workspace-manager"},
		Args: []string{
			"--kubeconfig=" + KubeconfigMountPath + "/" + kubeconfigFileName,
			"--provider-workspace=" + opts.Parent,
			"--startup-timeout=" + opts.StartupTimeout.String(),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "kubeconfig", MountPath: KubeconfigMountPath, ReadOnly: true},
			{Name: "tmp", MountPath: "/tmp"},
		},
		Resources: resources("100m", "256Mi"),
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   ptr(true),
			AllowPrivilegeEscalation: ptr(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	return deployment(opts, WorkspaceManagerName, container, BaseKubeconfigKey)
}

func deployment(opts Options, name string, container corev1.Container, kubeconfigKey string) *appsv1.Deployment {
	dep := &appsv1.Deployment{
		ObjectMeta: objectMeta(name, opts.Namespace, name),
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: componentLabels(name)},
			// Recreate, not RollingUpdate: for the same reason there is one
			// replica, an update that ran the old pod and the new one together
			// would have both reconciling every workspace.
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: componentLabels(name)},
				Spec: corev1.PodSpec{
					Containers:      []corev1.Container{container},
					SecurityContext: podSecurityContext(),
					Volumes: []corev1.Volume{
						secretVolume("kubeconfig", KubeconfigSecretName, []corev1.KeyToPath{
							{Key: kubeconfigKey, Path: kubeconfigFileName},
						}),
						// Somewhere writable, because the root filesystem is
						// not. Nothing here is known to need it; it is here so
						// that something that does - a library writing a
						// temporary file - fails visibly rather than at the
						// moment it matters.
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
	dep.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	return dep
}

// demoJob creates the workspaces, the blueprints and the clusters, and waits
// for every one of them to be ready.
//
// --no-manager is the whole difference from `task demo`: the controllers are
// the deployments above rather than goroutines in this process. Everything
// else the run does - the exports, the WorkspaceType, the tenants, the
// isolation checks - is the same code.
func demoJob(opts Options) *batchv1.Job {
	args := []string{
		"--kcp-kubeconfig=" + KubeconfigMountPath + "/" + kubeconfigFileName,
		"--kcp-kubeconfig-context=" + demo.BaseContext,
		"--no-manager",
		"--parent=" + opts.Parent,
		// The in-memory backend, because the alternative provisions containers
		// with a container runtime this pod does not have. A deployment that
		// wants real containers runs the dev infrastructure provider somewhere
		// with a socket, which is a different thing to show than this.
		"--backend=" + string(demo.BackendInMemory),
		// Somewhere writable. The demo writes a kubeconfig per audience beside
		// the one it was given, and the one it was given is a read-only Secret
		// mount.
		"--workspace-kubeconfig-dir=/tmp",
	}
	if opts.Demo.Workspaces > 0 {
		args = append(args, fmt.Sprintf("--workspaces=%d", opts.Demo.Workspaces))
	}
	args = append(args, "--users="+strings.Join(opts.Demo.Users, ","))
	if opts.Demo.Clusters > 0 {
		args = append(args, fmt.Sprintf("--clusters=%d", opts.Demo.Clusters))
	}
	args = append(args,
		fmt.Sprintf("--control-plane-machines=%d", opts.Demo.ControlPlaneMachines),
		fmt.Sprintf("--worker-machines=%d", opts.Demo.WorkerMachines),
	)
	if opts.Demo.Timeout != "" {
		args = append(args, "--timeout="+opts.Demo.Timeout)
	}
	args = append(args, opts.Demo.ExtraArgs...)

	job := &batchv1.Job{
		ObjectMeta: objectMeta(DemoJobName, opts.Namespace, DemoJobName),
		Spec: batchv1.JobSpec{
			// One attempt. A demo that failed halfway and started again would
			// report the second run's tables over the first run's failure, and
			// the run is idempotent enough to be re-applied by hand when that
			// is what somebody wants.
			BackoffLimit: ptr(int32(0)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: componentLabels(DemoJobName)},
				Spec: corev1.PodSpec{
					RestartPolicy:   corev1.RestartPolicyNever,
					SecurityContext: podSecurityContext(),
					Containers: []corev1.Container{{
						Name:            "demo",
						Image:           opts.Image,
						ImagePullPolicy: opts.ImagePullPolicy,
						Command:         []string{"/usr/local/bin/demo"},
						Args:            args,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "kubeconfig", MountPath: KubeconfigMountPath, ReadOnly: true},
							{Name: "tmp", MountPath: "/tmp"},
						},
						Resources: resources("200m", "512Mi"),
						SecurityContext: &corev1.SecurityContext{
							ReadOnlyRootFilesystem:   ptr(true),
							AllowPrivilegeEscalation: ptr(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{
						secretVolume("kubeconfig", KubeconfigSecretName, []corev1.KeyToPath{
							{Key: BaseKubeconfigKey, Path: kubeconfigFileName},
						}),
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}
	job.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	return job
}

func secretVolume(name, secret string, items []corev1.KeyToPath) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secret,
				Items:      items,
			},
		},
	}
}

// podSecurityContext runs everything as the same unprivileged user the image's
// base layer defines, and gives the group ownership of mounted volumes so that
// the shard can write its state directory.
func podSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr(true),
		RunAsUser:      ptr(int64(65532)),
		RunAsGroup:     ptr(int64(65532)),
		FSGroup:        ptr(int64(65532)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// resources requests without limiting.
//
// A request is a scheduling promise and a limit is a ceiling, and the ceiling
// is the one that turns a slow demo into a failed one: a manager throttled or
// killed mid-reconcile looks like a wiring bug. The requests are enough for
// the shape the demo runs and are the number to raise for a larger fleet.
func resources(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(memory),
		},
	}
}

func httpsProbe(path string, startupBudget time.Duration) *corev1.Probe {
	probe := httpProbe(path, KcpPort, startupBudget)
	probe.HTTPGet.Scheme = corev1.URISchemeHTTPS
	return probe
}

// httpProbe builds a probe. A non-zero startupBudget makes it a startup probe:
// one that may fail for that long before the container is considered to have
// failed to start, which is how a container that legitimately waits for
// something is told apart from one that is wedged.
func httpProbe(path string, port int32, startupBudget time.Duration) *corev1.Probe {
	const period = 10
	probe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   path,
				Port:   intstr.FromInt32(port),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		PeriodSeconds:    period,
		TimeoutSeconds:   5,
		FailureThreshold: 3,
	}
	if startupBudget > 0 {
		probe.FailureThreshold = int32(startupBudget.Seconds()/period) + 1
	}
	return probe
}

func ptr[T any](v T) *T { return &v }
