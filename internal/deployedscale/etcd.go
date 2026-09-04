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

package deployedscale

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const (
	// EtcdName is the StatefulSet, the headless Service, and the prefix of
	// every member's stable hostname.
	EtcdName = "etcd"
	// EtcdClientPort, EtcdPeerPort and EtcdMetricsPort are etcd's own
	// defaults. The metrics port matters: kubeadm points --listen-metrics-urls
	// at 127.0.0.1 and the stock run's ClusterClass patch opens it, so this
	// one is opened the same way and for the same reason.
	EtcdClientPort  = 2379
	EtcdPeerPort    = 2380
	EtcdMetricsPort = 2381

	// EtcdDataDir is where a member keeps its backend file. It is the mount
	// point of the volume claim, not a path inside the image.
	EtcdDataDir = "/var/lib/etcd"

	// DefaultEtcdImage is a released etcd, pinned. The version is the one
	// Kubernetes 1.32 ships with, so the store under kcp is the same software
	// at the same version as the store under the stock API server.
	DefaultEtcdImage = "registry.k8s.io/etcd:3.5.16-0"
)

// EtcdOptions is the store kcp is given, when it is given one.
//
// # Why kcp does not use its own
//
// kcp starts an embedded single-member etcd inside the shard's own container
// unless told otherwise. That is right for a laptop and wrong for this
// measurement in three ways at once: it shares the shard's memory limit, so the
// shard's figures include the store's; it is one member, against the stock
// side's three; and it has no quota and no reachable metrics, so nothing can
// say how close it is to running out — which is the finding the stock runs went
// looking for first.
//
// Zero Members means no store is deployed and kcp keeps its embedded one, so
// the local demo and the kind-based runs are unchanged.
type EtcdOptions struct {
	// Members is how many, and should be 3 to match the stock side. Zero
	// deploys nothing.
	Members int32
	// StorageClass and StorageSize are the volume each member gets. A
	// PersistentVolume rather than an emptyDir because a member that loses its
	// data directory cannot rejoin the cluster: it comes back as an unknown
	// peer, and the run's store quietly becomes two members with a third
	// crash-looping.
	StorageClass string
	StorageSize  string
	// QuotaBytes is --quota-backend-bytes, the cliff. Match the stock side's,
	// or a run that reaches it is reporting a different ceiling.
	QuotaBytes int64
	// Image is the etcd to run. Empty means DefaultEtcdImage.
	Image string
	// Resources sizes the members, as the managers and the shard are sized.
	Resources corev1.ResourceRequirements
	// NodeSelector pins the members to the pool the comparison gives the
	// control plane under test.
	NodeSelector map[string]string
}

// Enabled reports whether a run deploys its own store.
func (e EtcdOptions) Enabled() bool { return e.Members > 0 }

// EtcdEndpoints is what the shard is told to talk to: every member by its
// stable name, which is what a StatefulSet exists to provide.
//
// Nil when no store is deployed, so that KcpArgs emits no flag at all rather
// than an empty one.
func (o Options) EtcdEndpoints() []string {
	if !o.Etcd.Enabled() {
		return nil
	}
	out := make([]string, 0, o.Etcd.Members)
	for i := range int(o.Etcd.Members) {
		out = append(out, fmt.Sprintf("http://%s:%d", o.etcdMemberHost(i), EtcdClientPort))
	}
	return out
}

// etcdMemberHost is one member's stable DNS name, which the headless Service
// gives it: <statefulset>-<ordinal>.<service>.<namespace>.svc.
func (o Options) etcdMemberHost(i int) string {
	return fmt.Sprintf("%s-%d.%s.%s.svc", EtcdName, i, EtcdName, o.Namespace)
}

// EtcdService is headless, which is what gives each member a resolvable name
// of its own. A ClusterIP would give the set one address and the members no
// way to find each other.
func (o Options) EtcdService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: EtcdName, Namespace: o.Namespace, Labels: labels(EtcdName)},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: corev1.ClusterIPNone,
			Selector:  labels(EtcdName),
			// Members have to resolve each other before any of them is ready,
			// because none of them becomes ready until the quorum forms.
			PublishNotReadyAddresses: true,
			Ports: []corev1.ServicePort{
				{Name: "client", Port: EtcdClientPort, Protocol: corev1.ProtocolTCP},
				{Name: "peer", Port: EtcdPeerPort, Protocol: corev1.ProtocolTCP},
				{Name: "metrics", Port: EtcdMetricsPort, Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// EtcdArgs are the flags a member starts with.
//
// $(POD_NAME) is expanded by the kubelet from the container's own environment,
// which is how one manifest produces three members that each know which they
// are. Everything else is identical across them, including the initial cluster
// — every member must list every other before any of them starts, or the
// quorum refuses the ones it was not told about.
func (o Options) EtcdArgs() []string {
	peers := make([]string, 0, o.Etcd.Members)
	for i := range int(o.Etcd.Members) {
		peers = append(peers, fmt.Sprintf("%s-%d=http://%s:%d", EtcdName, i, o.etcdMemberHost(i), EtcdPeerPort))
	}
	return []string{
		"--name=$(POD_NAME)",
		"--data-dir=" + EtcdDataDir,
		fmt.Sprintf("--listen-client-urls=http://0.0.0.0:%d", EtcdClientPort),
		fmt.Sprintf("--listen-peer-urls=http://0.0.0.0:%d", EtcdPeerPort),
		// Open, for the same reason the stock ClusterClass opens it: this run
		// expects the store to be what runs out, and that is hard to establish
		// about a store nothing outside the node can see.
		fmt.Sprintf("--listen-metrics-urls=http://0.0.0.0:%d", EtcdMetricsPort),
		fmt.Sprintf("--advertise-client-urls=http://$(POD_NAME).%s.%s.svc:%d", EtcdName, o.Namespace, EtcdClientPort),
		fmt.Sprintf("--initial-advertise-peer-urls=http://$(POD_NAME).%s.%s.svc:%d", EtcdName, o.Namespace, EtcdPeerPort),
		"--initial-cluster=" + strings.Join(peers, ","),
		"--initial-cluster-state=new",
		"--initial-cluster-token=" + o.Namespace + "-" + EtcdName,
		fmt.Sprintf("--quota-backend-bytes=%d", o.Etcd.QuotaBytes),
		// And no --auto-compaction-*. kcp inherits kube-apiserver's compactor
		// (--etcd-compaction-interval, default 5m) and compacts its own store
		// exactly as the API server compacts the stock one, which is why
		// kubeadm sets no auto-compaction on its etcd either. Setting it here
		// would give this side two compactors against the stock side's one:
		// not corrupting, since whoever compacts to the later revision wins,
		// but it changes when revisions are released — and revision retention
		// is most of what the backend size measures.
	}
}

// EtcdStatefulSet is the store: one member per node, each with its own volume.
func (o Options) EtcdStatefulSet() *appsv1.StatefulSet {
	image := o.Etcd.Image
	if image == "" {
		image = DefaultEtcdImage
	}
	size := o.Etcd.StorageSize
	if size == "" {
		size = "100Gi"
	}

	var storageClass *string
	if o.Etcd.StorageClass != "" {
		storageClass = ptr.To(o.Etcd.StorageClass)
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: EtcdName, Namespace: o.Namespace, Labels: labels(EtcdName)},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: EtcdName,
			Replicas:    ptr.To(o.Etcd.Members),
			Selector:    &metav1.LabelSelector{MatchLabels: labels(EtcdName)},
			// Members come up together rather than one after another: none is
			// ready until the quorum forms, and OrderedReady would wait for a
			// readiness that cannot arrive until the member after it exists.
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels(EtcdName)},
				Spec: corev1.PodSpec{
					NodeSelector: o.Etcd.NodeSelector,
					Affinity: &corev1.Affinity{
						// Required, not preferred: three members on one node is
						// one disk's latency reported three times, and a single
						// node's failure taking the whole store with it.
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
								LabelSelector: &metav1.LabelSelector{MatchLabels: labels(EtcdName)},
								TopologyKey:   "kubernetes.io/hostname",
							}},
						},
					},
					Containers: []corev1.Container{{
						Name:      EtcdName,
						Image:     image,
						Command:   []string{"etcd"},
						Args:      o.EtcdArgs(),
						Resources: o.Etcd.Resources,
						Env: []corev1.EnvVar{{
							Name: "POD_NAME",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						}},
						Ports: []corev1.ContainerPort{
							{Name: "client", ContainerPort: EtcdClientPort},
							{Name: "peer", ContainerPort: EtcdPeerPort},
							{Name: "metrics", ContainerPort: EtcdMetricsPort},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: EtcdDataDir}},
						// Against the metrics port, which serves /health
						// without touching the client API.
						ReadinessProbe: etcdProbe(),
						LivenessProbe:  etcdProbe(),
					}},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: labels(EtcdName)},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: storageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
					},
				},
			}},
		},
	}
}

func etcdProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(EtcdMetricsPort)},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		FailureThreshold:    6,
	}
}
