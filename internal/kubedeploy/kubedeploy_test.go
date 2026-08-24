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

package kubedeploy

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// One deployment per provider is the topology this project has, and the demo
// running them in one process is the exception rather than the shape. A
// provider that lost its deployment would leave a shard serving types nothing
// reconciles, which looks like a stuck cluster rather than like a missing
// manager.
func TestObjectsDeploysOneManagerPerProvider(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{})

	for _, export := range []string{
		capiexports.CoreExport,
		capiexports.BootstrapExport,
		capiexports.ControlPlaneExport,
		capiexports.InfraExport,
	} {
		deployment := find[*appsv1.Deployment](t, objects, export)
		args := deployment.Spec.Template.Spec.Containers[0].Args
		if !slices.Contains(args, "--endpoint-slice-name="+export) {
			t.Errorf("%s does not discover workspaces through its own export: %v", export, args)
		}
	}
	// The workspace manager is not a provider - it serves the WorkspaceType a
	// tenant onboards with - so it is deployed by name rather than derived
	// from the export list.
	find[*appsv1.Deployment](t, objects, WorkspaceManagerName)
}

// The Nutanix provider is published so its types can be bound, and its manager
// is a separate module that needs a Prism Central. Deploying a manager for it
// out of this image is not possible, and doing it silently - a shard with an
// export nothing runs - is the failure worth being loud about.
func TestManagersRefusesAProviderItCannotRun(t *testing.T) {
	t.Parallel()

	_, err := Managers(append(capiexports.All(), capiexports.NutanixInfrastructure()))
	if err == nil {
		t.Fatal("a provider with no manager in this image was deployed anyway")
	}
	if !strings.Contains(err.Error(), capiexports.NutanixInfraExport) {
		t.Errorf("error %q does not name the provider it cannot run", err)
	}
}

// Two kubeconfigs, because the two kinds of manager address kcp differently
// and controller-runtime's --kubeconfig comes without a --context: a provider
// manager reads an endpoint slice out of the workspace the exports live in,
// and the workspace manager scopes itself and so needs the cluster-unaware
// endpoint. Handing either one the other's file produces a manager that starts
// and then finds nothing.
func TestObjectsGivesEachManagerTheKubeconfigItCanUse(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{})

	for name, want := range map[string]string{
		capiexports.CoreExport: ProviderKubeconfigKey,
		WorkspaceManagerName:   BaseKubeconfigKey,
	} {
		deployment := find[*appsv1.Deployment](t, objects, name)
		volume := deployment.Spec.Template.Spec.Volumes[0]
		if got := volume.Secret.Items[0].Key; got != want {
			t.Errorf("%s mounts %q, want %q", name, got, want)
		}
	}

	secret := find[*corev1.Secret](t, objects, KubeconfigSecretName)
	for _, key := range []string{ProviderKubeconfigKey, BaseKubeconfigKey} {
		if len(secret.Data[key]) == 0 {
			t.Errorf("the kubeconfig Secret has no %s", key)
		}
	}
}

// The dev infrastructure provider advertises its in-memory workload clusters
// at its own address. Without POD_IP it advertises ":20000", the DevCluster
// reports that as its endpoint, and the control plane provider waits forever
// for a cluster whose API server nothing can reach.
func TestObjectsGivesTheInfrastructureProviderItsPodIP(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{})

	infra := find[*appsv1.Deployment](t, objects, capiexports.InfraExport)
	var found bool
	for _, env := range infra.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "POD_IP" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil {
			found = env.ValueFrom.FieldRef.FieldPath == "status.podIP"
		}
	}
	if !found {
		t.Error("the dev infrastructure provider does not take its address from the pod")
	}

	// And no other manager asks for it, because no other one advertises
	// anything.
	core := find[*appsv1.Deployment](t, objects, capiexports.CoreExport)
	if len(core.Spec.Template.Spec.Containers[0].Env) != 0 {
		t.Errorf("the core manager takes environment it has no use for: %v",
			core.Spec.Template.Spec.Containers[0].Env)
	}
}

// Every URL kcp hands out comes from these three flags, and the one that is
// easiest to forget is the virtual workspace URL: it ends up in the
// APIExportEndpointSlice, which is the address every manager connects to. Left
// to kcp's own detection they name the pod's IP, which changes on restart and
// is on no certificate.
func TestStatefulSetPinsEveryURLToTheService(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{Namespace: "capi"})
	set := find[*appsv1.StatefulSet](t, objects, KcpName)
	args := set.Spec.Template.Spec.Containers[0].Args

	server := ServerURL("capi")
	for _, flag := range []string{"--shard-base-url=", "--shard-external-url=", "--shard-virtual-workspace-url="} {
		if !slices.Contains(args, flag+server) {
			t.Errorf("%s is not pinned to %s: %v", flag, server, args)
		}
	}
	// The serving certificate and the client CA are what make the Service
	// name usable at all: kcp's own certificate names localhost and nothing
	// else, and its own credentials are tokens written inside the pod.
	for _, flag := range []string{"--tls-cert-file=", "--tls-private-key-file=", "--client-ca-file="} {
		if !slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, flag) }) {
			t.Errorf("kcp is not given %s: %v", flag, args)
		}
	}
	if set.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Scheme != corev1.URISchemeHTTPS {
		t.Error("the readiness probe speaks HTTP to a server that only serves HTTPS")
	}
}

// The demo Job is the same run as `task demo` with its manager half switched
// off: the controllers are the deployments above. Everything else it does -
// the exports, the WorkspaceType, the tenants, the isolation checks - is the
// same code, which is why there is one description of what a demo is.
func TestDemoJobRunsTheDemoWithoutItsManagers(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{Demo: &DemoJob{
		Workspaces:           4,
		Users:                []string{"alice", "bob"},
		Clusters:             1,
		ControlPlaneMachines: 1,
		WorkerMachines:       1,
		Timeout:              "10m",
	}})

	job := find[*batchv1.Job](t, objects, DemoJobName)
	args := job.Spec.Template.Spec.Containers[0].Args
	for _, want := range []string{
		"--no-manager",
		"--kcp-kubeconfig-context=" + demo.BaseContext,
		"--workspaces=4",
		"--users=alice,bob",
		"--backend=" + string(demo.BackendInMemory),
		"--timeout=10m",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("the demo is not run with %s: %v", want, args)
		}
	}

	// The demo writes a kubeconfig per audience beside the one it was given,
	// and the one it was given is a read-only Secret mount. Left to default it
	// fails after every cluster is ready, which is the worst moment to fail.
	if !slices.Contains(args, "--workspace-kubeconfig-dir=/tmp") {
		t.Errorf("the demo would write its kubeconfigs into a read-only mount: %v", args)
	}
	if !slices.ContainsFunc(job.Spec.Template.Spec.Volumes, func(v corev1.Volume) bool {
		return v.Name == "tmp" && v.EmptyDir != nil
	}) {
		t.Error("there is nowhere writable for the demo to write them")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("a failed demo would be run again, reporting the second run over the first one's failure")
	}
}

// Nothing creates the demo run unless it was asked for: deploying the shard
// and the managers and leaving them empty is a legitimate installation, and is
// what somebody onboarding their own workspaces wants.
func TestObjectsWithoutADemoCreatesNoJob(t *testing.T) {
	t.Parallel()

	for _, obj := range build(t, Options{}) {
		if _, ok := obj.(*batchv1.Job); ok {
			t.Errorf("a Job was created for an installation that asked for no demo: %s", obj.GetName())
		}
	}
}

// The order objects are applied in is not cosmetic: a manager mounts a Secret
// that has to exist, and everything lives in a namespace that has to exist
// first.
func TestObjectsAreOrderedForApplying(t *testing.T) {
	t.Parallel()

	objects := build(t, Options{Namespace: "capi", Demo: &DemoJob{Workspaces: 1}})

	if _, ok := objects[0].(*corev1.Namespace); !ok {
		t.Fatalf("the first object is a %T, want the namespace", objects[0])
	}
	if _, ok := objects[len(objects)-1].(*batchv1.Job); !ok {
		t.Fatalf("the last object is a %T, want the demo run", objects[len(objects)-1])
	}

	position := func(name string) int {
		return slices.IndexFunc(objects, func(obj client.Object) bool { return obj.GetName() == name })
	}
	if position(KubeconfigSecretName) > position(capiexports.CoreExport) {
		t.Error("a manager is applied before the Secret it mounts")
	}

	for _, obj := range objects[1:] {
		if obj.GetNamespace() != "capi" {
			t.Errorf("%s %s is in namespace %q, want capi",
				obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), obj.GetNamespace())
		}
	}
}

func build(t *testing.T, opts Options) []client.Object {
	t.Helper()

	if opts.Credentials.ServingCA == nil {
		creds, err := NewCredentials(KcpName, ServerNames(DefaultNamespace), nil, time.Hour)
		if err != nil {
			t.Fatalf("issuing the credentials: %v", err)
		}
		opts.Credentials = creds
	}
	objects, err := Objects(opts)
	if err != nil {
		t.Fatalf("building the objects: %v", err)
	}
	return objects
}

func find[T client.Object](t *testing.T, objects []client.Object, name string) T {
	t.Helper()

	for _, obj := range objects {
		if typed, ok := obj.(T); ok && obj.GetName() == name {
			return typed
		}
	}
	var zero T
	t.Fatalf("no %T named %q", zero, name)
	return zero
}
