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
	"net"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/secret"
)

// A fixture is one backend on free ports, a fake management cluster, and
// the two reconcilers over both.
type fixture struct {
	backend  *Backend
	client   client.Client
	clusters *ClusterReconciler
	machines *MachineReconciler
	port     int32
}

func newFixture(t *testing.T, generate bool, objects ...client.Object) *fixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	debug, port := freePort(t), freePort(t)
	backend, err := NewBackend(ctx, "127.0.0.1", Ports{Min: port, Max: port + 100, Debug: debug})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()

	return &fixture{
		backend:  backend,
		client:   c,
		clusters: &ClusterReconciler{Client: c, Backend: backend, GenerateClusterSecrets: generate},
		machines: &MachineReconciler{Client: c, Backend: backend},
		port:     port,
	}
}

// freePort is a port nothing is listening on at the moment of asking, which
// is as good as it gets without a reservation.
func freePort(t *testing.T) int32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return int32(l.Addr().(*net.TCPAddr).Port) //nolint:errcheck,forcetypeassert,gosec // a TCP listener has a TCP address, and it is a port.
}

func newCluster(name string, endpoint clusterv1.APIEndpoint) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID("uid-" + name)},
		Spec:       clusterv1.ClusterSpec{ControlPlaneEndpoint: endpoint},
	}
}

func newMachine(name, cluster, providerID string) *clusterv1.Machine {
	return &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster,
			ProviderID:  providerID,
			Version:     "v1.33.0",
		},
		Status: clusterv1.MachineStatus{
			Addresses: clusterv1.MachineAddresses{{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"}},
		},
	}
}

func request(namespace, name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
}

// workloadClientset is what the core Machine controller would build from the
// kubeconfig secret: a client-go clientset against the fake API server, trusting
// the CA in the secret and authenticating with the client certificate in it.
func workloadClientset(t *testing.T, c client.Client, cluster *clusterv1.Cluster) *kubernetes.Clientset {
	t.Helper()
	s, err := secret.Get(context.Background(), c, client.ObjectKeyFromObject(cluster), secret.Kubeconfig)
	if err != nil {
		t.Fatalf("reading the kubeconfig secret: %v", err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(s.Data[secret.KubeconfigDataName])
	if err != nil {
		t.Fatalf("parsing the kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestClusterReconciler_ServesTheEndpointFromTheGeneratedSecrets(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	if err := f.client.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, purpose := range []secret.Purpose{secret.ClusterCA, secret.EtcdCA, secret.Kubeconfig} {
		if _, err := secret.Get(ctx, f.client, client.ObjectKeyFromObject(cluster), purpose); err != nil {
			t.Errorf("secret %q was not generated: %v", secret.Name(cluster.Name, purpose), err)
		}
	}

	// The proof that matters: the kubeconfig written for the Cluster reaches
	// an API server that answers, which is the whole chain the core Machine
	// controller walks to find a Node.
	nodes, err := workloadClientset(t, f.client, cluster).CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes through the Cluster's kubeconfig: %v", err)
	}
	if len(nodes.Items) != 0 {
		t.Fatalf("a fresh cluster has %d nodes, want none", len(nodes.Items))
	}

	// A second reconcile is a no-op, not a second API server.
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := len(f.backend.Mux.ListListeners()); got != 1 {
		t.Fatalf("listeners after two reconciles = %d, want 1", got)
	}
}

func TestClusterReconciler_WaitsForAnEndpoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true, newCluster("c1", clusterv1.APIEndpoint{}))

	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(f.backend.Mux.ListListeners()); got != 0 {
		t.Fatalf("listeners = %d, want none before the Cluster declares an endpoint", got)
	}
	if _, err := secret.Get(ctx, f.client, client.ObjectKey{Namespace: "default", Name: "c1"}, secret.ClusterCA); err == nil {
		t.Fatal("a CA was generated for a Cluster with no endpoint")
	}
}

func TestClusterReconciler_RefusesAnEndpointOnAnotherHost(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	if err := f.client.Create(ctx, newCluster("c1", clusterv1.APIEndpoint{Host: "10.10.255.1", Port: f.port})); err != nil {
		t.Fatal(err)
	}

	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(f.backend.Mux.ListListeners()); got != 0 {
		t.Fatalf("listeners = %d, want none for an endpoint this backend cannot serve", got)
	}
}

func TestClusterReconciler_WaitsForACAItDoesNotGenerate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, false)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	if err := f.client.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	res, err := f.clusters.Reconcile(ctx, request("default", "c1"))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("a Cluster with no CA should be requeued to look again")
	}
	if _, err := secret.Get(ctx, f.client, client.ObjectKeyFromObject(cluster), secret.ClusterCA); err == nil {
		t.Fatal("a CA was generated with generation off")
	}
	if f.backend.Mux.HasAPIServer("default/c1", apiServerName(cluster)) {
		t.Fatal("an API server was started with no CA to sign for it")
	}
}

func TestClusterReconciler_TearsDownAClusterThatIsGone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	if err := f.client.Create(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := f.client.Delete(ctx, cluster); err != nil {
		t.Fatal(err)
	}

	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Reconcile after deletion: %v", err)
	}
	if got := len(f.backend.Mux.ListListeners()); got != 0 {
		t.Fatalf("listeners = %d after the Cluster went, want none", got)
	}
	// And the port is free again for the next Cluster to use.
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(f.port)))
	if err != nil {
		t.Fatalf("the listener's port is still held: %v", err)
	}
	_ = l.Close()
}

func TestMachineReconciler_WritesTheNodeTheCoreControllerLooksFor(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	machine := newMachine("m1", "c1", "nutanix://0f6c5e2a")
	machine.Labels = map[string]string{clusterv1.MachineControlPlaneLabel: ""}
	for _, obj := range []client.Object{cluster, machine} {
		if err := f.client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Cluster Reconcile: %v", err)
	}

	if _, err := f.machines.Reconcile(ctx, request("default", "m1")); err != nil {
		t.Fatalf("Machine Reconcile: %v", err)
	}

	// Read it the way the core Machine controller does: through the
	// Cluster's kubeconfig, matching on providerID.
	nodes, err := workloadClientset(t, f.client, cluster).CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes.Items))
	}
	node := nodes.Items[0]
	if node.Name != "m1" || node.Spec.ProviderID != "nutanix://0f6c5e2a" {
		t.Errorf("node = %s providerID=%q, want m1 with the Machine's providerID", node.Name, node.Spec.ProviderID)
	}
	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; !ok {
		t.Error("a control plane Machine's Node is not labelled as one")
	}
	if len(node.Status.Addresses) != 1 || node.Status.Addresses[0].Type != corev1.NodeInternalIP || node.Status.Addresses[0].Address != "10.0.0.7" {
		t.Errorf("node addresses = %+v, want the Machine's", node.Status.Addresses)
	}
	if node.Status.NodeInfo.KubeletVersion != "v1.33.0" {
		t.Errorf("kubelet version = %q, want the Machine's", node.Status.NodeInfo.KubeletVersion)
	}
	var ready bool
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		t.Error("the Node is not Ready")
	}
}

func TestMachineReconciler_WaitsForAProviderID(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	for _, obj := range []client.Object{cluster, newMachine("m1", "c1", "")} {
		if err := f.client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Cluster Reconcile: %v", err)
	}

	res, err := f.machines.Reconcile(ctx, request("default", "m1"))
	if err != nil {
		t.Fatalf("Machine Reconcile: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatal("a Machine with no providerID is woken by its next update, not by a timer")
	}
	nodes, err := workloadClientset(t, f.client, cluster).CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	if len(nodes.Items) != 0 {
		t.Fatalf("nodes = %d for a Machine with no providerID, want none", len(nodes.Items))
	}
}

func TestMachineReconciler_WaitsForItsClusterToBeServed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true, newMachine("m1", "c1", "nutanix://0f6c5e2a"))

	res, err := f.machines.Reconcile(ctx, request("default", "m1"))
	if err != nil {
		t.Fatalf("Machine Reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("a Machine whose Cluster is not served yet should be requeued to look again")
	}
}

func TestMachineReconciler_RemovesTheNodeOfAMachineThatIsGone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, true)
	cluster := newCluster("c1", clusterv1.APIEndpoint{Host: "127.0.0.1", Port: f.port})
	machine := newMachine("m1", "c1", "nutanix://0f6c5e2a")
	for _, obj := range []client.Object{cluster, machine} {
		if err := f.client.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.clusters.Reconcile(ctx, request("default", "c1")); err != nil {
		t.Fatalf("Cluster Reconcile: %v", err)
	}
	if _, err := f.machines.Reconcile(ctx, request("default", "m1")); err != nil {
		t.Fatalf("Machine Reconcile: %v", err)
	}
	if err := f.client.Delete(ctx, machine); err != nil {
		t.Fatal(err)
	}

	if _, err := f.machines.Reconcile(ctx, request("default", "m1")); err != nil {
		t.Fatalf("Machine Reconcile after deletion: %v", err)
	}
	nodes, err := workloadClientset(t, f.client, cluster).CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes: %v", err)
	}
	if len(nodes.Items) != 0 {
		t.Fatalf("nodes = %d after the Machine went, want none", len(nodes.Items))
	}
	// Reconciling a Machine that was never seen is harmless.
	if _, err := f.machines.Reconcile(ctx, request("default", "never")); err != nil {
		t.Fatalf("Reconcile of an unknown Machine: %v", err)
	}
}

func itoa(p int32) string { return strconv.Itoa(int(p)) }
