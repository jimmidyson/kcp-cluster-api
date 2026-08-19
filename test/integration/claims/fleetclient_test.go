//go:build integration

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

package claims_test

import (
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

// TestFleetClientWritesClaimedResources asks the question the other test in
// this package does not: whether the *fleet's own* client - the one every
// reconciler holds, resolving the workspace from the context - can write a
// claimed resource, as opposed to a client built by hand against the same
// virtual workspace.
//
// The two are not the same question. The bootstrap provider's first write is a
// ConfigMap (its control plane init lock) and the rest are Secrets, and a
// deployment where kcp serves them but this client cannot reach them is a
// deployment where that provider does not work.
//
// It asks each provider through its *own* manager, which is the part that
// changed with the export split and with per-verb claims. Every provider used
// to claim every core type with every verb, so any manager would do and this
// test used core's for both writes. Now each claims what its own RBAC markers
// justify: the bootstrap provider owns ConfigMaps because the init lock is
// one, and core cannot write them at all - it declares no ConfigMap access
// upstream either. Asking core to create one tested nothing about the
// bootstrap provider and now fails, correctly.
func TestFleetClientWritesClaimedResources(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig:      server.BaseConfig(t),
		WorkspacePrefix: "claims-fleet",
		Workspaces:      1,
		// A control plane machine, so the bootstrap and control plane
		// providers are wired at all: a run that asks for no machines starts
		// neither, and this test asks the bootstrap provider a question.
		ControlPlaneMachines: 1,
		Backend:              demo.BackendInMemory,
		RunManager:           true,
		Timeout:              5 * time.Minute,
		Log:                  ctrl.Log.WithName("demo"),
	})
	if err != nil {
		t.Fatalf("demo run failed: %v", err)
	}
	if result.Managers == nil {
		t.Fatal("demo did not return any managers")
	}
	bootstrapManager, ok := result.Managers[capiexports.BootstrapExport]
	if !ok {
		t.Fatalf("demo returned no manager for %s", capiexports.BootstrapExport)
	}
	coreManager, ok := result.Managers[capiexports.CoreExport]
	if !ok {
		t.Fatalf("demo returned no manager for %s", capiexports.CoreExport)
	}

	workspace := result.Workspaces[0]
	// The context a reconcile runs with: the cluster is what the fleet's
	// client resolves on.
	ctx = mccontext.WithCluster(ctx, multicluster.ClusterName(workspace.LogicalCluster))
	// What the fleet actually talks to, printed rather than assumed: the
	// difference between this test and its neighbour is a URL somewhere.
	if cl, err := bootstrapManager.GetCluster(ctx, multicluster.ClusterName(workspace.LogicalCluster)); err != nil {
		t.Fatalf("getting the engaged cluster: %v", err)
	} else {
		t.Logf("fleet cluster host: %s", cl.GetConfig().Host)
	}

	// The init lock, through the provider that takes it.
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet-written-lock", Namespace: demo.Namespace},
		Data:       map[string]string{"value": "fleet"},
	}
	if err := capimulticluster.NewClusterAwareClient(bootstrapManager).Create(ctx, configMap); err != nil {
		t.Errorf("the bootstrap provider's fleet client cannot create a claimed ConfigMap: %v", err)
	}

	// A Secret through core, which claims them for the kubeconfigs it reads
	// and writes.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "fleet-written-secret", Namespace: demo.Namespace},
		Data:       map[string][]byte{"value": []byte("fleet")},
	}
	if err := capimulticluster.NewClusterAwareClient(coreManager).Create(ctx, secret); err != nil {
		t.Errorf("core's fleet client cannot create a claimed Secret: %v", err)
	}

	// The other half of least privilege, and the half a claim list cannot
	// assert on its own: a verb that was *not* granted is refused. Core
	// declares no ConfigMap access upstream, so it claims read only.
	stray := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "core-written-lock", Namespace: demo.Namespace},
		Data:       map[string]string{"value": "core"},
	}
	if err := capimulticluster.NewClusterAwareClient(coreManager).Create(ctx, stray); err == nil {
		t.Error("core's fleet client created a ConfigMap: its claim grants read only, so a narrowed verb is not being enforced")
	} else {
		t.Logf("core cannot create a ConfigMap, as claimed: %v", err)
	}

	// Whatever it wrote has to be in this workspace on the shard.
	for _, obj := range []client.Object{configMap, secret} {
		key := client.ObjectKeyFromObject(obj)
		readBack := obj.DeepCopyObject().(client.Object)
		if err := workspace.Client.Get(ctx, key, readBack); err != nil {
			t.Errorf("%T %s written by the fleet client is not in %s: %v", obj, key.Name, workspace.Path, err)
		}
	}
}
