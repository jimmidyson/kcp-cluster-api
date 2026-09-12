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

package workloadsim

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	cloudv1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/cloud/api/v1alpha1"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	"sigs.k8s.io/cluster-api/util/secret"
)

// What a control plane Machine's Node has to carry for a KubeadmControlPlane
// to count it healthy and dare to add the next replica. Every object here is
// the shape KubeadmControlPlane inspects on a real cluster, copied from the
// docker/dev provider's in-memory backend, which is where these shapes were
// worked out against KubeadmControlPlane in the first place:
//
//   - an etcd Pod, and an etcd member behind it that answers the member list
//     and health calls KubeadmControlPlane makes through a port-forward;
//   - the kube-apiserver, kube-scheduler and kube-controller-manager Pods
//     whose readiness is the "static pod" health per node;
//   - kubeadm's ConfigMap and the RBAC kubeadm leaves behind, which
//     KubeadmControlPlane reads and updates on upgrade;
//   - kube-proxy and CoreDNS, which KubeadmControlPlane's upgrade path
//     reconciles and would otherwise fail to find.

func etcdPodName(machine string) string              { return "etcd-" + machine }
func apiServerPodName(machine string) string         { return "kube-apiserver-" + machine }
func schedulerPodName(machine string) string         { return "kube-scheduler-" + machine }
func controllerManagerPodName(machine string) string { return "kube-controller-manager-" + machine }

// reconcileControlPlane brings up everything a control plane Machine's Node
// has to carry, in the order a real node would: etcd, then the API server,
// then the components that need one.
func (r *MachineReconciler) reconcileControlPlane(ctx context.Context, cluster client.ObjectKey, machine *clusterv1.Machine) error {
	key := cluster.String()
	c := r.Backend.Manager.GetResourceGroup(key).GetClient()

	if err := r.reconcileEtcd(ctx, c, cluster, machine); err != nil {
		return fmt.Errorf("etcd: %w", err)
	}
	if err := r.reconcileAPIServer(ctx, c, cluster, machine); err != nil {
		return fmt.Errorf("API server: %w", err)
	}
	for _, name := range []string{schedulerPodName(machine.Name), controllerManagerPodName(machine.Name)} {
		if err := create(ctx, c, staticPod(name, machine.Name, componentOf(name))); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := reconcileKubeadmObjects(ctx, c); err != nil {
		return fmt.Errorf("kubeadm objects: %w", err)
	}
	if err := reconcileKubeProxy(ctx, c, machine.Spec.Version); err != nil {
		return fmt.Errorf("kube-proxy: %w", err)
	}
	if err := reconcileCoreDNS(ctx, c); err != nil {
		return fmt.Errorf("CoreDNS: %w", err)
	}
	return nil
}

// deleteControlPlane takes down what reconcileControlPlane brought up for one
// Machine. The kubeadm objects, kube-proxy and CoreDNS stay: they belong to
// the cluster, not to a node.
func (r *MachineReconciler) deleteControlPlane(ctx context.Context, cluster client.ObjectKey, machine string) error {
	key := cluster.String()
	c := r.Backend.Manager.GetResourceGroup(key).GetClient()

	if err := deletePod(ctx, c, etcdPodName(machine)); err != nil {
		return err
	}
	if err := r.Backend.Mux.DeleteEtcdMember(key, etcdPodName(machine)); err != nil {
		return fmt.Errorf("removing etcd member %s: %w", etcdPodName(machine), err)
	}
	if err := deletePod(ctx, c, apiServerPodName(machine)); err != nil {
		return err
	}
	if err := r.Backend.Mux.DeleteAPIServer(key, apiServerPodName(machine)); err != nil {
		return fmt.Errorf("removing API server %s: %w", apiServerPodName(machine), err)
	}
	for _, name := range []string{schedulerPodName(machine), controllerManagerPodName(machine)} {
		if err := deletePod(ctx, c, name); err != nil {
			return err
		}
	}
	return nil
}

// reconcileEtcd adds the Machine's etcd member: a Pod carrying the member's
// identity in annotations, which is what the fake etcd computes the member
// list and the leader from, and a serving certificate on the mux so the
// member answers over TLS. The first member of a cluster mints the cluster ID
// and is the leader; every later one joins it.
func (r *MachineReconciler) reconcileEtcd(ctx context.Context, c inmemoryruntime.Client, cluster client.ObjectKey, machine *clusterv1.Machine) error {
	pod := staticPod(etcdPodName(machine.Name), machine.Name, "etcd")
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("reading Pod %s: %w", pod.Name, err)
		}
		info, err := etcdInfo(ctx, c)
		if err != nil {
			return err
		}
		if info.clusterID == "" {
			info.clusterID = nonZeroID(sets.New[string]())
		}
		pod.Annotations = map[string]string{
			cloudv1.EtcdClusterIDAnnotationName: info.clusterID,
			cloudv1.EtcdMemberIDAnnotationName:  nonZeroID(info.members),
		}
		if info.leaderID == "" {
			pod.Annotations[cloudv1.EtcdLeaderFromAnnotationName] = time.Now().Format(time.RFC3339)
		}
		if err := create(ctx, c, pod); err != nil {
			return err
		}
	}

	if r.Backend.Mux.HasEtcdMember(cluster.String(), pod.Name) {
		return nil
	}
	cert, key, err := loadCA(ctx, r.Client, cluster, secret.EtcdCA)
	if err != nil {
		return err
	}
	if err := r.Backend.Mux.AddEtcdMember(cluster.String(), pod.Name, cert, key); err != nil {
		return fmt.Errorf("adding etcd member %s: %w", pod.Name, err)
	}
	return nil
}

// reconcileAPIServer adds the Machine's kube-apiserver: a Pod, and an
// instance on the mux behind the cluster's endpoint.
func (r *MachineReconciler) reconcileAPIServer(ctx context.Context, c inmemoryruntime.Client, cluster client.ObjectKey, machine *clusterv1.Machine) error {
	pod := staticPod(apiServerPodName(machine.Name), machine.Name, "kube-apiserver")
	if err := create(ctx, c, pod); err != nil {
		return err
	}
	if r.Backend.Mux.HasAPIServer(cluster.String(), pod.Name) {
		return nil
	}
	cert, key, err := loadCA(ctx, r.Client, cluster, secret.ClusterCA)
	if err != nil {
		return err
	}
	if err := r.Backend.Mux.AddAPIServer(cluster.String(), pod.Name, cert, key); err != nil {
		return fmt.Errorf("adding API server %s: %w", pod.Name, err)
	}
	return nil
}

// etcdClusterInfo is what the existing etcd Pods of a cluster say about it.
type etcdClusterInfo struct {
	clusterID string
	leaderID  string
	members   sets.Set[string]
}

// etcdInfo reads the etcd cluster's identity back from its Pods' annotations,
// the same way the fake etcd does when it answers a member list.
func etcdInfo(ctx context.Context, c inmemoryruntime.Client) (etcdClusterInfo, error) {
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods,
		client.InNamespace(metav1.NamespaceSystem),
		client.MatchingLabels{"component": "etcd", "tier": "control-plane"},
	); err != nil {
		return etcdClusterInfo{}, fmt.Errorf("listing etcd Pods: %w", err)
	}

	info := etcdClusterInfo{members: sets.New[string]()}
	var leaderFrom time.Time
	for _, pod := range pods.Items {
		if _, removed := pod.Annotations[cloudv1.EtcdMemberRemoved]; removed {
			continue
		}
		clusterID := pod.Annotations[cloudv1.EtcdClusterIDAnnotationName]
		switch {
		case info.clusterID == "":
			info.clusterID = clusterID
		case clusterID != info.clusterID:
			return etcdClusterInfo{}, fmt.Errorf("etcd Pods disagree about the cluster ID: %q and %q", info.clusterID, clusterID)
		}
		memberID := pod.Annotations[cloudv1.EtcdMemberIDAnnotationName]
		info.members.Insert(memberID)
		if t, err := time.Parse(time.RFC3339, pod.Annotations[cloudv1.EtcdLeaderFromAnnotationName]); err == nil && t.After(leaderFrom) {
			info.leaderID = memberID
			leaderFrom = t
		}
	}
	return info, nil
}

// nonZeroID is a random etcd identifier not already taken and not zero,
// which etcd reserves.
func nonZeroID(taken sets.Set[string]) string {
	for {
		id := fmt.Sprintf("%d", rand.Uint32()) //nolint:gosec // an identifier, not a secret.
		if id != "0" && !taken.Has(id) {
			return id
		}
	}
}

// staticPod is a control plane component's Pod as kubeadm would run it: in
// kube-system, pinned to its node, Running and Ready.
func staticPod(name, node, component string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceSystem,
			Name:      name,
			Labels:    map[string]string{"component": component, "tier": "control-plane"},
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// componentOf is the component label of a static Pod named after it.
func componentOf(podName string) string {
	for _, component := range []string{"kube-scheduler", "kube-controller-manager", "kube-apiserver", "etcd"} {
		if len(podName) > len(component) && podName[:len(component)+1] == component+"-" {
			return component
		}
	}
	return podName
}

// reconcileKubeadmObjects writes what kubeadm init leaves behind and
// KubeadmControlPlane reads: its ConfigMap, and the RBAC that lets joining
// nodes read Nodes.
func reconcileKubeadmObjects(ctx context.Context, c inmemoryruntime.Client) error {
	objects := []client.Object{
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "kubeadm:get-nodes"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"nodes"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "kubeadm:get-nodes"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kubeadm:get-nodes"},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.GroupKind, Name: "system:bootstrappers:kubeadm:default-node-token"}},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceSystem, Name: "kubeadm-config"},
			Data:       map[string]string{"ClusterConfiguration": ""},
		},
	}
	for _, obj := range objects {
		if err := create(ctx, c, obj); err != nil {
			return err
		}
	}
	return nil
}

// reconcileKubeProxy writes the kube-proxy DaemonSet at the cluster's
// Kubernetes version, which KubeadmControlPlane's upgrade path updates.
func reconcileKubeProxy(ctx context.Context, c inmemoryruntime.Client, version string) error {
	return create(ctx, c, &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: metav1.NamespaceSystem,
			Name:      "kube-proxy",
			Labels:    map[string]string{"component": "kube-proxy"},
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "kube-proxy", Image: "registry.k8s.io/kube-proxy:" + version}},
				},
			},
		},
	})
}

// reconcileCoreDNS writes CoreDNS's ConfigMap and Deployment, which
// KubeadmControlPlane's upgrade path updates.
func reconcileCoreDNS(ctx context.Context, c inmemoryruntime.Client) error {
	if err := create(ctx, c, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceSystem, Name: "coredns"},
		Data:       map[string]string{"Corefile": "ANG"},
	}); err != nil {
		return err
	}
	return create(ctx, c, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceSystem, Name: "coredns"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "coredns", Image: "registry.k8s.io/coredns/coredns:v1.10.1"}},
				},
			},
		},
	})
}

// create writes an object that may already exist.
func create(ctx context.Context, c inmemoryruntime.Client, obj client.Object) error {
	if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}

// deletePod removes a kube-system Pod that may already be gone.
func deletePod(ctx context.Context, c inmemoryruntime.Client, name string) error {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: metav1.NamespaceSystem, Name: name}}
	if err := c.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Pod %s: %w", name, err)
	}
	return nil
}
