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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/managermetrics"
)

func testOptions() Options {
	images := map[string]string{}
	for _, c := range Components() {
		images[c.Name] = "example.test/" + c.Name + ":test"
	}
	return Options{
		Namespace: "scale",
		KcpImage:  "example.test/kcp:test",
		Images:    images,
	}
}

func testObjects(t *testing.T, o Options) []client.Object {
	t.Helper()
	creds, err := NewCredentials(ServiceNames(KcpName, o.Namespace), LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	objects, err := o.Objects(creds)
	if err != nil {
		t.Fatalf("building objects: %v", err)
	}
	return objects
}

func podSpecs(objects []client.Object) map[string]corev1.PodSpec {
	out := map[string]corev1.PodSpec{}
	for _, obj := range objects {
		if d, ok := obj.(*appsv1.Deployment); ok {
			out[d.Name] = d.Spec.Template.Spec
		}
	}
	return out
}

// TestNothingAssumesOneNode is the requirement the whole package is shaped by
// (FR-002). Each of these would work on a single-node cluster and fail on a
// real one, which is the failure mode that would be discovered last.
func TestNothingAssumesOneNode(t *testing.T) {
	objects := testObjects(t, testOptions())

	for name, spec := range podSpecs(objects) {
		if spec.HostNetwork {
			t.Errorf("%s uses host networking", name)
		}
		for _, v := range spec.Volumes {
			if v.HostPath != nil {
				t.Errorf("%s mounts hostPath %s", name, v.HostPath.Path)
			}
		}
		for _, c := range spec.Containers {
			for _, arg := range c.Args {
				if strings.Contains(arg, "127.0.0.1") || strings.Contains(arg, "localhost") {
					t.Errorf("%s passes %q, which only resolves to a peer on one node", name, arg)
				}
			}
			for _, e := range c.Env {
				if strings.Contains(e.Value, "127.0.0.1") || strings.Contains(e.Value, "localhost") {
					t.Errorf("%s sets %s=%q, which only resolves to a peer on one node", name, e.Name, e.Value)
				}
			}
		}
	}

	for _, obj := range objects {
		if svc, ok := obj.(*corev1.Service); ok && svc.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Errorf("Service %s is a %s; a node-addressed Service shapes the harness around one node",
				svc.Name, svc.Spec.Type)
		}
	}
}

// TestDevInfrastructureAdvertisesItsPodIP is the one piece of wiring that
// makes a split deployment work at all: the in-memory workload clusters live
// in this pod and the core manager's ClusterCache has to reach them from
// another one. An unset POD_IP does not fail loudly — it produces DevClusters
// whose endpoint is ":20000" and control planes that wait forever.
func TestDevInfrastructureAdvertisesItsPodIP(t *testing.T) {
	specs := podSpecs(testObjects(t, testOptions()))

	spec, ok := specs[ComponentDevInfrastructure]
	if !ok {
		t.Fatalf("no %s deployment", ComponentDevInfrastructure)
	}
	var found *corev1.EnvVar
	for i, e := range spec.Containers[0].Env {
		if e.Name == "POD_IP" {
			found = &spec.Containers[0].Env[i]
		}
	}
	if found == nil {
		t.Fatal("POD_IP is not set, so the in-memory backend would advertise no address")
	}
	if found.ValueFrom == nil || found.ValueFrom.FieldRef == nil || found.ValueFrom.FieldRef.FieldPath != "status.podIP" {
		t.Errorf("POD_IP is not taken from status.podIP: %+v", found)
	}

	// And no other component sets it, because no other component serves
	// anything its peers dial directly.
	for name, s := range specs {
		if name == ComponentDevInfrastructure {
			continue
		}
		for _, e := range s.Containers[0].Env {
			if e.Name == "POD_IP" {
				t.Errorf("%s sets POD_IP but serves nothing at it", name)
			}
		}
	}
}

// TestDevInfrastructureIsSingleReplica guards the constraint its own doc
// comment states: the in-memory mux binds a fixed port range, so two on one
// node collide with "address already in use".
func TestDevInfrastructureIsSingleReplica(t *testing.T) {
	for _, c := range Components() {
		if c.Name == ComponentDevInfrastructure && !c.SingleReplica {
			t.Error("the dev infrastructure component is not marked single-replica")
		}
	}
	for _, obj := range testObjects(t, testOptions()) {
		d, ok := obj.(*appsv1.Deployment)
		if !ok || d.Name != ComponentDevInfrastructure {
			continue
		}
		if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
			t.Errorf("%s has %v replicas", d.Name, d.Spec.Replicas)
		}
	}
}

// TestEveryManagerReachesKcpByItsServiceName: the kubeconfig a pod mounts must
// address the Service, which is the name the serving certificate covers and
// the only one that resolves from another node.
func TestEveryManagerReachesKcpByItsServiceName(t *testing.T) {
	o := testOptions()
	want := "https://kcp.scale.svc:6443/clusters/root"
	if got := o.KcpServerURL(); got != want {
		t.Fatalf("KcpServerURL = %q, want %q", got, want)
	}
	// kcp is told its own address without a logical cluster on it; it appends
	// its own paths, and a shard URL carrying /clusters/root would produce
	// endpoint URLs with two of them.
	if got := o.KcpBaseURL(); got != "https://kcp.scale.svc:6443" {
		t.Fatalf("KcpBaseURL = %q", got)
	}

	objects := testObjects(t, o)
	var kubeconfig *corev1.Secret
	for _, obj := range objects {
		if s, ok := obj.(*corev1.Secret); ok && s.Name == KubeconfigSecretName {
			kubeconfig = s
		}
	}
	if kubeconfig == nil {
		t.Fatal("no kubeconfig secret")
	}
	if !strings.Contains(string(kubeconfig.Data["kubeconfig"]), want) {
		t.Errorf("the managers' kubeconfig does not address %s", want)
	}

	for name, spec := range podSpecs(objects) {
		if name == KcpName {
			continue
		}
		var hasEnv, hasMount bool
		for _, e := range spec.Containers[0].Env {
			if e.Name == "KUBECONFIG" && e.Value == KubeconfigMountPath+"/kubeconfig" {
				hasEnv = true
			}
		}
		for _, m := range spec.Containers[0].VolumeMounts {
			if m.MountPath == KubeconfigMountPath {
				hasMount = true
			}
		}
		if !hasEnv {
			t.Errorf("%s does not set KUBECONFIG; controller-runtime would fall back to in-cluster config and address the wrong API server", name)
		}
		if !hasMount {
			t.Errorf("%s does not mount the kubeconfig it is told to read", name)
		}
	}
}

// TestKcpIsToldTheNameItIsAddressedBy. Without the shard URLs kcp advertises
// the address it detected for itself — a pod IP that changes on restart and is
// not covered by the serving certificate — into every APIExportEndpointSlice.
func TestKcpIsToldTheNameItIsAddressedBy(t *testing.T) {
	o := testOptions()
	args := strings.Join(o.KcpDeployment().Spec.Template.Spec.Containers[0].Args, " ")

	for _, want := range []string{
		"--shard-base-url=" + o.KcpBaseURL(),
		"--shard-external-url=" + o.KcpBaseURL(),
		"--tls-cert-file=" + CredentialsMountPath + "/tls.crt",
		"--tls-private-key-file=" + CredentialsMountPath + "/tls.key",
		"--token-auth-file=" + CredentialsMountPath + "/tokens.csv",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("kcp is not started with %s", want)
		}
	}
}

// TestEveryContainerHasAMemoryLimit. The limit is what makes an OOMKill
// possible, and an OOMKill at a stated fleet size is the capacity finding this
// measurement exists to produce.
func TestEveryContainerHasAMemoryLimit(t *testing.T) {
	for name, spec := range podSpecs(testObjects(t, testOptions())) {
		for _, c := range spec.Containers {
			if c.Resources.Limits.Memory().IsZero() {
				t.Errorf("%s/%s has no memory limit, so it can never produce a capacity finding", name, c.Name)
			}
			if c.Resources.Requests.Cpu().IsZero() {
				t.Errorf("%s/%s has no CPU request, so the scheduler cannot place it meaningfully", name, c.Name)
			}
			// No CPU limit: throttling makes a slow run look like a slow
			// system, which measures the limit rather than the fleet.
			if !c.Resources.Limits.Cpu().IsZero() {
				t.Errorf("%s/%s has a CPU limit; throttling would be measured as the system being slower", name, c.Name)
			}
		}
	}
}

// TestMetricsPortMatchesWhatTheManagerServes: the container port, the flag the
// deployment passes, and the manager's own default must agree, or the harness
// scrapes a port nothing is listening on.
func TestMetricsPortMatchesWhatTheManagerServes(t *testing.T) {
	if want := fmt.Sprintf(":%d", MetricsPort); managermetrics.DefaultBindAddress != want {
		t.Errorf("this package scrapes %s but a manager defaults to %s", want, managermetrics.DefaultBindAddress)
	}

	for name, spec := range podSpecs(testObjects(t, testOptions())) {
		if name == KcpName {
			continue
		}
		c := spec.Containers[0]

		var declared int32
		for _, p := range c.Ports {
			if p.Name == MetricsPortName {
				declared = p.ContainerPort
			}
		}
		if declared != MetricsPort {
			t.Errorf("%s declares metrics port %d, want %d", name, declared, MetricsPort)
		}
		if want := fmt.Sprintf("--metrics-bind-address=:%d", MetricsPort); !strings.Contains(strings.Join(c.Args, " "), want) {
			t.Errorf("%s is not started with %s", name, want)
		}
	}
}

// TestEachManagerDiscoversItsOwnExport. The endpoint slice name is the one
// flag that differs between the four, and getting it wrong produces a manager
// that engages nothing and reports no error.
func TestEachManagerDiscoversItsOwnExport(t *testing.T) {
	o := testOptions()
	seen := map[string]string{}
	for _, c := range Components() {
		args := strings.Join(o.ManagerDeployment(c).Spec.Template.Spec.Containers[0].Args, " ")
		want := "--endpoint-slice-name=" + c.ExportName
		if !strings.Contains(args, want) {
			t.Errorf("%s is not started with %s", c.Name, want)
		}
		if other, dup := seen[c.ExportName]; dup {
			t.Errorf("%s and %s both discover through %s", c.Name, other, c.ExportName)
		}
		seen[c.ExportName] = c.Name
	}
}

func TestSpreadAcrossNodesIsRequiredNotPreferred(t *testing.T) {
	o := testOptions()
	o.SpreadAcrossNodes = true

	for name, spec := range podSpecs(testObjects(t, o)) {
		if name == KcpName {
			continue
		}
		if spec.Affinity == nil || spec.Affinity.PodAntiAffinity == nil {
			t.Errorf("%s has no anti-affinity when the run asked to be spread", name)
			continue
		}
		anti := spec.Affinity.PodAntiAffinity
		if len(anti.RequiredDuringSchedulingIgnoredDuringExecution) == 0 {
			t.Errorf("%s's anti-affinity is only preferred: it could be co-scheduled and reported as spread", name)
		}
		if len(anti.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
			t.Errorf("%s uses preferred anti-affinity, which permits the silent co-scheduling this option exists to prevent", name)
		}
	}
}

func TestNoAntiAffinityByDefault(t *testing.T) {
	// A single-node cluster is the common first case, and required
	// anti-affinity would make every pod unschedulable there.
	for name, spec := range podSpecs(testObjects(t, testOptions())) {
		if spec.Affinity != nil {
			t.Errorf("%s has affinity rules without the run asking to be spread", name)
		}
	}
}

// TestObjectsAreOrderedForCreation: a Deployment created before the Secret it
// mounts does not fail, it hangs. Ordering is how that is avoided.
func TestObjectsAreOrderedForCreation(t *testing.T) {
	objects := testObjects(t, testOptions())

	index := map[string]int{}
	for i, obj := range objects {
		index[fmt.Sprintf("%T/%s", obj, obj.GetName())] = i
	}

	ns := index["*v1.Namespace/scale"]
	creds := index["*v1.Secret/"+CredentialsSecretName]
	kubeconfig := index["*v1.Secret/"+KubeconfigSecretName]
	kcp := index["*v1.Deployment/"+KcpName]

	if ns != 0 {
		t.Error("the namespace is not created first")
	}
	if creds > kcp {
		t.Error("kcp is created before the secret it mounts, so its pod would hang rather than fail")
	}
	for _, c := range Components() {
		if kubeconfig > index["*v1.Deployment/"+c.Name] {
			t.Errorf("%s is created before the kubeconfig it mounts", c.Name)
		}
	}
}

func TestComponentsNamedSelectsMilestones(t *testing.T) {
	// M1 is core-manager alone, reconciled against an in-process run.
	one, err := ComponentsNamed(ComponentCore)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if len(one) != 1 || one[0].Name != ComponentCore {
		t.Fatalf("selected %v", one)
	}

	all, err := ComponentsNamed(ComponentDevInfrastructure, ComponentCore)
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	// Order follows Components(), not the argument order, so a report's rows
	// are in the same order whatever the caller asked for.
	if all[0].Name != ComponentCore {
		t.Errorf("selection did not preserve the canonical order: %v", all)
	}

	if _, err := ComponentsNamed(); err == nil {
		t.Error("a run with no components was accepted")
	}
	if _, err := ComponentsNamed("not-a-manager"); err == nil {
		t.Error("an unknown component was accepted")
	}
}

func TestValidateRejectsAnIncompleteRun(t *testing.T) {
	creds, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Options)
		wants  string
	}{
		{"no namespace", func(o *Options) { o.Namespace = "" }, "namespace"},
		{"no kcp image", func(o *Options) { o.KcpImage = "" }, "kcp image"},
		{"a manager with no image", func(o *Options) { delete(o.Images, ComponentCore) }, ComponentCore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := testOptions()
			tc.mutate(&o)
			_, err := o.Objects(creds)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not mention %q", err, tc.wants)
			}
		})
	}
}

func TestMetricsURL(t *testing.T) {
	if got, want := MetricsURL("10.244.1.7"), "http://10.244.1.7:8080/metrics"; got != want {
		t.Errorf("MetricsURL = %q, want %q", got, want)
	}
}

// TestKcpDeploymentRollsWhenCredentialsChange is the regression test for a run
// that failed with
//
//	tls: failed to verify certificate: x509: certificate signed by unknown
//	authority ... "kcp-cluster-api-scale-ca"
//
// against a namespace an interrupted run had left behind. The credentials Secret
// was updated, but a Deployment does not restart for a changed Secret and kcp
// reads its certificate once, so the pod kept serving the previous run's while
// the client trusted the new CA. The error names the CA, which reads as a
// mistake in building it rather than as a server still serving the old one.
func TestKcpDeploymentRollsWhenCredentialsChange(t *testing.T) {
	options := testOptions()

	first, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	second, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}

	fingerprintOf := func(creds *Credentials) string {
		objects, err := options.InfrastructureObjects(creds)
		if err != nil {
			t.Fatalf("building the manifests: %v", err)
		}
		for _, obj := range objects {
			if d, ok := obj.(*appsv1.Deployment); ok && d.Name == KcpName {
				return d.Spec.Template.Annotations[CredentialsAnnotation]
			}
		}
		t.Fatal("no kcp Deployment in the infrastructure objects")
		return ""
	}

	a, b := fingerprintOf(first), fingerprintOf(second)
	if a == "" {
		t.Fatalf("the kcp pod template carries no %s annotation, so it will not roll when the "+
			"credentials change", CredentialsAnnotation)
	}
	if a == b {
		t.Error("two different sets of credentials produced the same pod template, so a re-minted " +
			"certificate would leave the old kcp pod running and serving the old one")
	}
	if again := fingerprintOf(first); again != a {
		t.Error("the same credentials produced two different pod templates, which would roll kcp on " +
			"every apply and restart the process a run is measuring")
	}
}

// TestFingerprintDoesNotLeakTheToken pins that the annotation is derived from
// public material only: it lands on a pod, readable by anything that can read
// pods.
func TestFingerprintDoesNotLeakTheToken(t *testing.T) {
	creds, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if strings.Contains(creds.Fingerprint(), creds.Token) {
		t.Error("the fingerprint contains the bearer token")
	}
}
