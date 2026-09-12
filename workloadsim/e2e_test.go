//go:build e2e

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

package workloadsim_test

// The end-to-end proof: stock Cluster API's real core, kubeadm bootstrap and
// KubeadmControlPlane controllers, as the binaries a user would deploy, take a
// KubeadmControlPlane to its declared replica count and worker Machines to
// Running against workloadsim and a fake infrastructure provider.
//
// The fake provider does exactly what CAPX does against ntnx-sim, and nothing
// more: it marks the infrastructure cluster provisioned, and once a Machine
// has bootstrap data it gives the infrastructure machine a providerID and an
// address and marks it provisioned. Swapping it for CAPX changes nothing on
// the workloadsim side, which is the point of the test.
//
// It needs envtest binaries (KUBEBUILDER_ASSETS) and builds the three
// managers from the module cache on first run; see the docs page.

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/secret"

	"github.com/jimmidyson/kcp-cluster-api/workloadsim"
)

const (
	infraGroup   = "infrastructure.cluster.x-k8s.io"
	infraVersion = "v1beta2"
	namespace    = "default"
	k8sVersion   = "v1.33.0"

	controlPlaneReplicas = 3
	workers              = 2
)

var (
	fakeClusterGVK         = schema.GroupVersionKind{Group: infraGroup, Version: infraVersion, Kind: "FakeCluster"}
	fakeMachineGVK         = schema.GroupVersionKind{Group: infraGroup, Version: infraVersion, Kind: "FakeMachine"}
	fakeMachineTemplateGVK = schema.GroupVersionKind{Group: infraGroup, Version: infraVersion, Kind: "FakeMachineTemplate"}
)

func TestKubeadmControlPlaneReachesItsReplicasAgainstAFakeProvider(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("could not run: KUBEBUILDER_ASSETS is unset, so there are no envtest binaries")
	}
	timeout := 10 * time.Minute
	if v := os.Getenv("WORKLOADSIM_E2E_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("WORKLOADSIM_E2E_TIMEOUT: %v", err)
		}
		timeout = d
	}
	ctrl.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(false)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logDir := artifactDir(t)

	capi := capiModule(t)
	managers := buildManagers(t, capi)

	env := startEnvtest(t, capi.dir)
	kubeconfig := writeKubeconfig(t, env.Config)
	certDir := webhookCerts(t)
	for _, name := range []string{"core", "bootstrap", "controlplane"} {
		startManager(t, ctx, logDir, name, managers[name], kubeconfig, certDir)
	}

	// The two things under test, in-process: workloadsim, and the fake
	// infrastructure provider standing where CAPX goes.
	endpointPort := freePort(t)
	backend, err := workloadsim.NewBackend(ctx, "127.0.0.1", workloadsim.Ports{Min: endpointPort, Max: endpointPort + 200, Debug: freePort(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, bootstrapv1.AddToScheme, controlplanev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := ctrl.NewManager(env.Config, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workloadsim.Setup(mgr, backend, workloadsim.Options{MaxConcurrentReconciles: 4}); err != nil {
		t.Fatal(err)
	}
	if err := setupFakeProvider(mgr); err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()

	c, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	createCluster(t, ctx, c, endpointPort)
	t.Logf("cluster objects created; waiting up to %s for %d control plane replicas and %d workers", timeout, controlPlaneReplicas, workers)

	deadline := time.Now().Add(timeout)
	if err := waitFor(ctx, deadline, 2*time.Second, func() (bool, string, error) { return clusterReady(ctx, c) }); err != nil {
		diagnose(t, ctx, c, logDir)
		t.Fatalf("the cluster did not come up: %v", err)
	}
	t.Logf("control plane initialized with %d ready replicas and %d workers Running after %s", controlPlaneReplicas, workers, time.Since(start).Round(time.Second))

	// And down again: the Machines drain and delete their Nodes through the
	// fake API server, the replicas leave etcd, and the Cluster goes.
	start = time.Now()
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "c1"}}
	if err := c.Delete(ctx, cluster); err != nil {
		t.Fatal(err)
	}
	if err := waitFor(ctx, time.Now().Add(timeout), 2*time.Second, func() (bool, string, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
			if apierrors.IsNotFound(err) {
				return true, "", nil
			}
			return false, "", err
		}
		return false, fmt.Sprintf("cluster phase=%s", cluster.Status.Phase), nil
	}); err != nil {
		diagnose(t, ctx, c, logDir)
		t.Fatalf("the cluster did not go: %v", err)
	}
	t.Logf("cluster deleted after %s", time.Since(start).Round(time.Second))
}

// clusterReady is the end state: KubeadmControlPlane reports initialized
// with every replica ready, and every Machine is Running.
func clusterReady(ctx context.Context, c client.Client) (bool, string, error) {
	kcp := &controlplanev1.KubeadmControlPlane{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "c1-cp"}, kcp); err != nil {
		return false, "", err
	}
	machines := &clusterv1.MachineList{}
	if err := c.List(ctx, machines, client.InNamespace(namespace)); err != nil {
		return false, "", err
	}
	running := 0
	phases := make([]string, 0, len(machines.Items))
	for _, m := range machines.Items {
		phases = append(phases, m.Name+"="+m.Status.Phase)
		if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
			running++
		}
	}
	status := fmt.Sprintf("initialized=%v replicas=%d ready=%d machines=[%s]",
		ptr.Deref(kcp.Status.Initialization.ControlPlaneInitialized, false),
		ptr.Deref(kcp.Status.Replicas, 0), ptr.Deref(kcp.Status.ReadyReplicas, 0), strings.Join(phases, " "))
	done := ptr.Deref(kcp.Status.Initialization.ControlPlaneInitialized, false) &&
		ptr.Deref(kcp.Status.ReadyReplicas, 0) == controlPlaneReplicas &&
		running == controlPlaneReplicas+workers
	return done, status, nil
}

// createCluster writes what a user would: the infrastructure cluster with its
// endpoint in workloadsim's range, the Cluster, a KubeadmControlPlane at three
// replicas, and two worker Machines with their bootstrap configs.
func createCluster(t *testing.T, ctx context.Context, c client.Client, endpointPort int32) {
	t.Helper()
	fakeCluster := newUnstructured(fakeClusterGVK, "c1")
	mustSet(t, fakeCluster, map[string]any{"host": "127.0.0.1", "port": int64(endpointPort)}, "spec", "controlPlaneEndpoint")

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "c1"},
		Spec: clusterv1.ClusterSpec{
			ClusterNetwork: clusterv1.ClusterNetwork{
				Pods:     clusterv1.NetworkRanges{CIDRBlocks: []string{"192.168.0.0/16"}},
				Services: clusterv1.NetworkRanges{CIDRBlocks: []string{"10.128.0.0/12"}},
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: infraGroup, Kind: "FakeCluster", Name: "c1"},
			ControlPlaneRef:   clusterv1.ContractVersionedObjectReference{APIGroup: controlplanev1.GroupVersion.Group, Kind: "KubeadmControlPlane", Name: "c1-cp"},
		},
	}

	template := newUnstructured(fakeMachineTemplateGVK, "c1-cp")
	mustSet(t, template, map[string]any{"spec": map[string]any{}}, "spec", "template")

	kcp := &controlplanev1.KubeadmControlPlane{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "c1-cp"},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Replicas: ptr.To(int32(controlPlaneReplicas)),
			Version:  k8sVersion,
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: infraGroup, Kind: "FakeMachineTemplate", Name: "c1-cp"},
				},
			},
			// What the defaulting webhook would set, since none runs here.
			Rollout: controlplanev1.KubeadmControlPlaneRolloutSpec{
				Strategy: controlplanev1.KubeadmControlPlaneRolloutStrategy{
					Type:          controlplanev1.RollingUpdateStrategyType,
					RollingUpdate: controlplanev1.KubeadmControlPlaneRolloutStrategyRollingUpdate{MaxSurge: ptr.To(intstr.FromInt32(1))},
				},
			},
		},
	}

	objects := []client.Object{fakeCluster, cluster, template, kcp}
	for i := 1; i <= workers; i++ {
		name := fmt.Sprintf("c1-w-%d", i)
		objects = append(objects,
			newUnstructured(fakeMachineGVK, name),
			&bootstrapv1.KubeadmConfig{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}},
			&clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{clusterv1.ClusterNameLabel: "c1"}},
				Spec: clusterv1.MachineSpec{
					ClusterName:       "c1",
					Version:           k8sVersion,
					Bootstrap:         clusterv1.Bootstrap{ConfigRef: clusterv1.ContractVersionedObjectReference{APIGroup: bootstrapv1.GroupVersion.Group, Kind: "KubeadmConfig", Name: name}},
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{APIGroup: infraGroup, Kind: "FakeMachine", Name: name},
				},
			},
		)
	}
	for _, obj := range objects {
		if err := c.Create(ctx, obj); err != nil {
			t.Fatalf("creating %T %s: %v", obj, obj.GetName(), err)
		}
	}
}

// The fake infrastructure provider.

// setupFakeProvider wires the two reconcilers a minimal infrastructure
// provider has. They speak the v1beta2 contract through unstructured objects,
// so the test carries no provider types of its own.
func setupFakeProvider(mgr ctrl.Manager) error {
	fakeCluster := &unstructured.Unstructured{}
	fakeCluster.SetGroupVersionKind(fakeClusterGVK)
	if err := ctrl.NewControllerManagedBy(mgr).Named("fake-cluster").For(fakeCluster).
		Complete(&fakeClusterReconciler{Client: mgr.GetClient()}); err != nil {
		return err
	}

	fakeMachine := &unstructured.Unstructured{}
	fakeMachine.SetGroupVersionKind(fakeMachineGVK)
	return ctrl.NewControllerManagedBy(mgr).Named("fake-machine").For(fakeMachine).
		// A Machine's bootstrap data arriving is what lets its infrastructure
		// machine proceed, so Machine events map to it.
		Watches(&clusterv1.Machine{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []ctrl.Request {
			m, ok := o.(*clusterv1.Machine)
			if !ok || m.Spec.InfrastructureRef.Kind != fakeMachineGVK.Kind {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: m.Namespace, Name: m.Spec.InfrastructureRef.Name}}}
		})).
		Complete(&fakeMachineReconciler{Client: mgr.GetClient()})
}

type fakeClusterReconciler struct{ client.Client }

// Reconcile marks the infrastructure cluster provisioned. Its endpoint is
// user input, as it is on a NutanixCluster.
func (r *fakeClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(fakeClusterGVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if provisioned, _, _ := unstructured.NestedBool(obj.Object, "status", "initialization", "provisioned"); provisioned {
		return ctrl.Result{}, nil
	}
	_ = unstructured.SetNestedField(obj.Object, true, "status", "initialization", "provisioned")
	_ = unstructured.SetNestedField(obj.Object, true, "status", "ready")
	return ctrl.Result{}, r.Status().Update(ctx, obj)
}

type fakeMachineReconciler struct {
	client.Client
	addresses atomic.Int32
}

// Reconcile does what CAPX does once a Machine has bootstrap data: gives the
// infrastructure machine a providerID and an address, and marks it
// provisioned. Core copies the providerID to the Machine, which is where
// workloadsim reads it.
func (r *fakeMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(fakeMachineGVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}

	var machine *clusterv1.Machine
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == "Machine" {
			machine = &clusterv1.Machine{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: ref.Name}, machine); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
	}
	if machine == nil || machine.Spec.Bootstrap.DataSecretName == nil {
		// Owned and bootstrapped are both Machine events, which are watched.
		return ctrl.Result{}, nil
	}

	if providerID, _, _ := unstructured.NestedString(obj.Object, "spec", "providerID"); providerID == "" {
		_ = unstructured.SetNestedField(obj.Object, "fake://"+obj.GetName(), "spec", "providerID")
		if err := r.Update(ctx, obj); err != nil {
			return ctrl.Result{}, err
		}
	}
	if provisioned, _, _ := unstructured.NestedBool(obj.Object, "status", "initialization", "provisioned"); provisioned {
		return ctrl.Result{}, nil
	}
	n := r.addresses.Add(1)
	_ = unstructured.SetNestedSlice(obj.Object, []any{map[string]any{"type": "InternalIP", "address": fmt.Sprintf("10.0.%d.%d", n/256, n%256)}}, "status", "addresses")
	_ = unstructured.SetNestedField(obj.Object, true, "status", "initialization", "provisioned")
	_ = unstructured.SetNestedField(obj.Object, true, "status", "ready")
	return ctrl.Result{}, r.Status().Update(ctx, obj)
}

// The management cluster and the stock managers.

type capiModuleInfo struct{ dir, version string }

// capiModule is where stock Cluster API's source and CRDs are, at the version
// this module pins: the module cache.
func capiModule(t *testing.T) capiModuleInfo {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}} {{.Version}}", "sigs.k8s.io/cluster-api").Output()
	if err != nil {
		t.Fatalf("locating sigs.k8s.io/cluster-api: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		t.Fatalf("locating sigs.k8s.io/cluster-api: unexpected %q", out)
	}
	return capiModuleInfo{dir: fields[0], version: fields[1]}
}

// buildManagers compiles the three stock managers from the module cache,
// once per Cluster API version, into WORKLOADSIM_E2E_BINDIR or bin/ at the
// repository root. Built from a throwaway module so that this module's go.sum
// stays what its own imports need.
func buildManagers(t *testing.T, capi capiModuleInfo) map[string]string {
	t.Helper()
	dir := os.Getenv("WORKLOADSIM_E2E_BINDIR")
	if dir == "" {
		dir = filepath.Join("..", "bin", "workloadsim-e2e")
	}
	dir = filepath.Join(dir, "cluster-api-"+capi.version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	packages := map[string]string{
		"core":         "sigs.k8s.io/cluster-api",
		"bootstrap":    "sigs.k8s.io/cluster-api/bootstrap/kubeadm",
		"controlplane": "sigs.k8s.io/cluster-api/controlplane/kubeadm",
	}
	bins := map[string]string{}
	for name, pkg := range packages {
		bin, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		bins[name] = bin
		if _, err := os.Stat(bin); err == nil {
			continue
		}
		t.Logf("building %s from %s (once per Cluster API version)", name, pkg)
		mod := t.TempDir()
		if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module e2emanagers\n\ngo 1.25.0\n\nrequire sigs.k8s.io/cluster-api "+capi.version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("go", "build", "-mod=mod", "-o", bin, pkg)
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", name, err, out)
		}
	}
	return bins
}

// startEnvtest starts kube-apiserver and etcd with Cluster API's CRDs and
// the fake provider's, every provider CRD carrying the contract label that
// kustomize would add and core resolves versions through.
func startEnvtest(t *testing.T, capiDir string) *envtest.Environment {
	t.Helper()
	crds := []*apiextensionsv1.CustomResourceDefinition{
		fakeCRD("FakeCluster", "fakeclusters"),
		fakeCRD("FakeMachine", "fakemachines"),
		fakeCRD("FakeMachineTemplate", "fakemachinetemplates"),
	}
	for _, sub := range []struct {
		dir      string
		contract bool
	}{
		{"config/crd/bases", false},
		{"bootstrap/kubeadm/config/crd/bases", true},
		{"controlplane/kubeadm/config/crd/bases", true},
	} {
		files, err := filepath.Glob(filepath.Join(capiDir, sub.dir, "*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := yaml.Unmarshal(raw, crd); err != nil {
				t.Fatalf("parsing %s: %v", file, err)
			}
			if sub.contract {
				crd.Labels = contractLabels(crd.Labels)
			}
			crds = append(crds, crd)
		}
	}

	env := &envtest.Environment{CRDs: crds}
	if _, err := env.Start(); err != nil {
		t.Fatalf("starting envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	return env
}

func contractLabels(labels map[string]string) map[string]string {
	if labels == nil {
		labels = map[string]string{}
	}
	labels["cluster.x-k8s.io/v1beta1"] = "v1beta1"
	labels["cluster.x-k8s.io/v1beta2"] = "v1beta2"
	return labels
}

// fakeCRD is a provider CRD with a free-form spec and status and a status
// subresource, which is all the contract needs.
func fakeCRD(kind, plural string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + infraGroup, Labels: contractLabels(nil)},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: infraGroup,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, Plural: plural, Singular: strings.ToLower(kind)},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:         infraVersion,
				Served:       true,
				Storage:      true,
				Subresources: &apiextensionsv1.CustomResourceSubresources{Status: &apiextensionsv1.CustomResourceSubresourceStatus{}},
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec":   {Type: "object", XPreserveUnknownFields: ptr.To(true)},
						"status": {Type: "object", XPreserveUnknownFields: ptr.To(true)},
					},
				}},
			}},
		},
	}
}

func writeKubeconfig(t *testing.T, cfg *rest.Config) string {
	t.Helper()
	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData, Token: cfg.BearerToken}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest"}
	kc.CurrentContext = "envtest"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// webhookCerts is a serving certificate for the managers' webhook servers,
// which have to start even though nothing here calls them.
func webhookCerts(t *testing.T) string {
	t.Helper()
	ca := secret.NewCertificatesForInitialControlPlane(nil).GetByPurpose(secret.ClusterCA)
	if err := ca.Generate(); err != nil {
		t.Fatal(err)
	}
	key, err := certs.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := certs.Config{
		CommonName: "localhost",
		AltNames:   certs.AltNames{DNSNames: []string{"localhost"}, IPs: []net.IP{net.ParseIP("127.0.0.1")}},
		Usages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	caCert, err := certs.DecodeCertPEM(ca.KeyPair.Cert)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := certs.DecodePrivateKeyPEM(ca.KeyPair.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := cfg.NewSignedCert(key, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), certs.EncodeCertPEM(cert), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), certs.EncodePrivateKeyPEM(key), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startManager runs one stock manager as a user would, and waits for it to
// report ready. Its log lands in logDir.
func startManager(t *testing.T, ctx context.Context, logDir, name, bin, kubeconfig, certDir string) {
	t.Helper()
	health := freePort(t)
	args := []string{
		"--leader-elect=false",
		fmt.Sprintf("--webhook-port=%d", freePort(t)),
		"--webhook-cert-dir=" + certDir,
		fmt.Sprintf("--health-addr=127.0.0.1:%d", health),
		fmt.Sprintf("--diagnostics-address=127.0.0.1:%d", freePort(t)),
		"--insecure-diagnostics=true",
		"--v=2",
	}
	logFile, err := os.Create(filepath.Join(logDir, name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = logFile.Close()
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/readyz", health)
	if err := waitFor(ctx, time.Now().Add(90*time.Second), time.Second, func() (bool, string, error) {
		resp, err := http.Get(url) //nolint:noctx // a readiness poll.
		if err != nil {
			return false, err.Error(), nil
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK, resp.Status, nil
	}); err != nil {
		t.Fatalf("%s did not become ready: %v (see %s)", name, err, logFile.Name())
	}
	t.Logf("%s ready", name)
}

// Helpers.

func newUnstructured(gvk schema.GroupVersionKind, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetNamespace(namespace)
	obj.SetName(name)
	return obj
}

func mustSet(t *testing.T, obj *unstructured.Unstructured, value any, fields ...string) {
	t.Helper()
	if err := unstructured.SetNestedField(obj.Object, value, fields...); err != nil {
		t.Fatal(err)
	}
}

func freePort(t *testing.T) int32 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return int32(l.Addr().(*net.TCPAddr).Port) //nolint:errcheck,forcetypeassert,gosec // a TCP listener has a TCP address, and it is a port.
}

func artifactDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("ARTIFACT_DIR"); dir != "" {
		dir = filepath.Join(dir, "workloadsim-e2e")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	return t.TempDir()
}

// waitFor polls check until it reports done, the deadline passes, or it
// errors. The last status is in the error, so a timeout says what it saw.
func waitFor(ctx context.Context, deadline time.Time, every time.Duration, check func() (bool, string, error)) error {
	var last string
	for {
		done, status, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		last = status
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out; last seen: %s", last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// diagnose says what the management cluster looked like when a wait gave up,
// and where the managers' logs are.
func diagnose(t *testing.T, ctx context.Context, c client.Client, logDir string) {
	t.Helper()
	kcp := &controlplanev1.KubeadmControlPlane{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "c1-cp"}, kcp); err == nil {
		for _, cond := range kcp.Status.Conditions {
			t.Logf("KubeadmControlPlane condition %s=%s %s: %s", cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
	machines := &clusterv1.MachineList{}
	if err := c.List(ctx, machines, client.InNamespace(namespace)); err == nil {
		for _, m := range machines.Items {
			t.Logf("Machine %s phase=%s providerID=%q nodeRef=%q", m.Name, m.Status.Phase, m.Spec.ProviderID, m.Status.NodeRef.Name)
			for _, cond := range m.Status.Conditions {
				if cond.Status != metav1.ConditionTrue {
					t.Logf("  %s=%s %s: %s", cond.Type, cond.Status, cond.Reason, cond.Message)
				}
			}
		}
	}
	t.Logf("manager logs are in %s", logDir)
}
