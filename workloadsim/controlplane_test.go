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
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	cloudv1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/cloud/api/v1alpha1"
	inmemoryproxy "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server/proxy"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/secret"
)

// controlPlaneFixture is a served Cluster and however many control plane
// Machines a test asks for, reconciled.
func controlPlaneFixture(t *testing.T, machines ...string) (*fixture, *clusterv1.Cluster) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	if err := f.client.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Cluster Reconcile: %v", err)
	}
	for _, name := range machines {
		machine := newMachine(name, "c1", "nutanix://"+name)
		machine.Labels = map[string]string{clusterv1.MachineControlPlaneLabel: ""}
		if err := f.client.Create(ctx, machine); err != nil {
			t.Fatal(err)
		}
		if _, err := f.machines.Reconcile(ctx, request("default", name)); err != nil {
			t.Fatalf("Machine Reconcile of %s: %v", name, err)
		}
	}
	return f, cluster
}

func restConfigFor(t *testing.T, c client.Client, cluster *clusterv1.Cluster) *rest.Config {
	t.Helper()
	s, err := secret.Get(context.Background(), c, client.ObjectKeyFromObject(cluster), secret.Kubeconfig)
	if err != nil {
		t.Fatalf("reading the kubeconfig secret: %v", err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(s.Data[secret.KubeconfigDataName])
	if err != nil {
		t.Fatalf("parsing the kubeconfig: %v", err)
	}
	return cfg
}

// etcdMembers asks the cluster's etcd for its member list the way
// KubeadmControlPlane does: a port-forward through the API server to one
// etcd Pod, then etcd's own client over it, authenticated with a certificate
// signed by the cluster's etcd CA.
func etcdMembers(t *testing.T, c client.Client, cluster *clusterv1.Cluster, viaPod string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	caCert, caKey, err := loadCA(ctx, c, client.ObjectKeyFromObject(cluster), secret.EtcdCA)
	if err != nil {
		t.Fatalf("loading the etcd CA: %v", err)
	}
	clientKey, err := certs.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientCfg := certs.Config{CommonName: "apiserver-etcd-client", Organization: []string{"system:masters"}, Usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	clientCert, err := clientCfg.NewSignedCert(clientKey, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(certs.EncodeCertPEM(clientCert), certs.EncodePrivateKeyPEM(clientKey))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	dialer, err := inmemoryproxy.NewDialer(inmemoryproxy.Proxy{
		Kind:       "pods",
		Namespace:  metav1.NamespaceSystem,
		KubeConfig: restConfigFor(t, c, cluster),
		Port:       2379,
	})
	if err != nil {
		t.Fatal(err)
	}
	etcd, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{viaPod},
		DialTimeout: 5 * time.Second,
		DialOptions: []grpc.DialOption{grpc.WithContextDialer(dialer.DialContextWithAddr)},
		TLS:         &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
	})
	if err != nil {
		t.Fatalf("etcd client: %v", err)
	}
	defer etcd.Close()

	list, err := etcd.MemberList(ctx)
	if err != nil {
		t.Fatalf("etcd member list through %s: %v", viaPod, err)
	}
	names := make([]string, 0, len(list.Members))
	for _, m := range list.Members {
		names = append(names, m.Name)
	}
	return names
}

func TestControlPlane_WhatKubeadmControlPlaneInspectsIsThere(t *testing.T) {
	ctx := context.Background()
	f, cluster := controlPlaneFixture(t, "cp-1")
	cs, err := kubernetes.NewForConfig(restConfigFor(t, f.client, cluster))
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"etcd-cp-1", "kube-apiserver-cp-1", "kube-scheduler-cp-1", "kube-controller-manager-cp-1"} {
		pod, err := cs.CoreV1().Pods(metav1.NamespaceSystem).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Errorf("static Pod %s: %v", name, err)
			continue
		}
		if pod.Spec.NodeName != "cp-1" || pod.Status.Phase != "Running" {
			t.Errorf("Pod %s: node=%q phase=%q, want on cp-1 and Running", name, pod.Spec.NodeName, pod.Status.Phase)
		}
	}
	if _, err := cs.CoreV1().ConfigMaps(metav1.NamespaceSystem).Get(ctx, "kubeadm-config", metav1.GetOptions{}); err != nil {
		t.Errorf("kubeadm-config: %v", err)
	}
	if _, err := cs.RbacV1().ClusterRoleBindings().Get(ctx, "kubeadm:get-nodes", metav1.GetOptions{}); err != nil {
		t.Errorf("kubeadm:get-nodes binding: %v", err)
	}
	ds, err := cs.AppsV1().DaemonSets(metav1.NamespaceSystem).Get(ctx, "kube-proxy", metav1.GetOptions{})
	if err != nil {
		t.Errorf("kube-proxy: %v", err)
	} else if got := ds.Spec.Template.Spec.Containers[0].Image; got != "registry.k8s.io/kube-proxy:v1.33.0" {
		t.Errorf("kube-proxy image = %q, want the Machine's version", got)
	}
	if _, err := cs.AppsV1().Deployments(metav1.NamespaceSystem).Get(ctx, "coredns", metav1.GetOptions{}); err != nil {
		t.Errorf("coredns: %v", err)
	}
	for _, ns := range []string{"default", "kube-public", "kube-system"} {
		if _, err := cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
			t.Errorf("namespace %s: %v", ns, err)
		}
	}
	if !f.backend.Mux.HasEtcdMember("default/c1", "etcd-cp-1") || !f.backend.Mux.HasAPIServer("default/c1", "kube-apiserver-cp-1") {
		t.Error("the mux does not carry the Machine's etcd member and API server")
	}

	if got := etcdMembers(t, f.client, cluster, "etcd-cp-1"); len(got) != 1 {
		t.Errorf("etcd members = %v, want exactly the one", got)
	}
}

func TestControlPlane_EveryReplicaJoinsOneEtcdCluster(t *testing.T) {
	ctx := context.Background()
	f, cluster := controlPlaneFixture(t, "cp-1", "cp-2", "cp-3")
	cs, err := kubernetes.NewForConfig(restConfigFor(t, f.client, cluster))
	if err != nil {
		t.Fatal(err)
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{LabelSelector: "component=etcd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 3 {
		t.Fatalf("etcd Pods = %d, want 3", len(pods.Items))
	}
	clusterIDs, memberIDs, leaders := map[string]bool{}, map[string]bool{}, 0
	for _, pod := range pods.Items {
		clusterIDs[pod.Annotations[cloudv1.EtcdClusterIDAnnotationName]] = true
		memberIDs[pod.Annotations[cloudv1.EtcdMemberIDAnnotationName]] = true
		if _, ok := pod.Annotations[cloudv1.EtcdLeaderFromAnnotationName]; ok {
			leaders++
		}
	}
	if len(clusterIDs) != 1 || len(memberIDs) != 3 || leaders != 1 {
		t.Errorf("clusterIDs=%d memberIDs=%d leaders=%d, want one cluster, three members, one leader", len(clusterIDs), len(memberIDs), leaders)
	}

	// The member list is the same whichever member answers, which is what
	// KubeadmControlPlane relies on when it picks one to ask.
	for _, via := range []string{"etcd-cp-1", "etcd-cp-3"} {
		if got := etcdMembers(t, f.client, cluster, via); len(got) != 3 {
			t.Errorf("etcd members through %s = %v, want 3", via, got)
		}
	}

	// Three replicas, three control plane nodes: the count is the Machines'.
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/control-plane"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes.Items) != 3 {
		t.Errorf("control plane nodes = %d, want 3", len(nodes.Items))
	}
}

func TestControlPlane_AWorkerCarriesNoneOfIt(t *testing.T) {
	ctx := context.Background()
	f, cluster := controlPlaneFixture(t, "cp-1")
	worker := newMachine("w-1", "c1", "nutanix://w-1")
	if err := f.client.Create(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.machines.Reconcile(ctx, request("default", "w-1")); err != nil {
		t.Fatalf("Machine Reconcile: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restConfigFor(t, f.client, cluster))
	if err != nil {
		t.Fatal(err)
	}

	pods, err := cs.CoreV1().Pods(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{LabelSelector: "tier=control-plane"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "w-1" {
			t.Errorf("a worker's node carries control plane Pod %s", pod.Name)
		}
	}
	if f.backend.Mux.HasEtcdMember("default/c1", "etcd-w-1") {
		t.Error("a worker was added as an etcd member")
	}
	node, err := cs.CoreV1().Nodes().Get(ctx, "w-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the worker's Node: %v", err)
	}
	if _, labelled := node.Labels["node-role.kubernetes.io/control-plane"]; labelled || len(node.Spec.Taints) != 0 {
		t.Error("a worker's Node is labelled or tainted as control plane")
	}
}

func TestControlPlane_AReplicaThatGoesLeavesEtcd(t *testing.T) {
	ctx := context.Background()
	f, cluster := controlPlaneFixture(t, "cp-1", "cp-2")
	machine := &clusterv1.Machine{}
	if err := f.client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "cp-2"}, machine); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(ctx, machine); err != nil {
		t.Fatal(err)
	}

	if _, err := f.machines.Reconcile(ctx, request("default", "cp-2")); err != nil {
		t.Fatalf("Machine Reconcile after deletion: %v", err)
	}

	if f.backend.Mux.HasEtcdMember("default/c1", "etcd-cp-2") || f.backend.Mux.HasAPIServer("default/c1", "kube-apiserver-cp-2") {
		t.Error("the gone replica is still an etcd member or API server on the mux")
	}
	if got := etcdMembers(t, f.client, cluster, "etcd-cp-1"); len(got) != 1 {
		t.Errorf("etcd members after the replica went = %v, want 1", got)
	}
	cs, err := kubernetes.NewForConfig(restConfigFor(t, f.client, cluster))
	if err != nil {
		t.Fatal(err)
	}
	pods, err := cs.CoreV1().Pods(metav1.NamespaceSystem).List(ctx, metav1.ListOptions{LabelSelector: "tier=control-plane"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "cp-2" {
			t.Errorf("Pod %s of the gone replica is still there", pod.Name)
		}
	}
	// And the surviving replica still answers: the endpoint did not go
	// with the replica.
	if _, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{}); err != nil {
		t.Errorf("the cluster stopped answering when a replica went: %v", err)
	}
}
