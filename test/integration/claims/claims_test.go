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

// Package claims_test answers the question ADR-0001 left open in D3: whether
// a permission claim makes core v1 Secrets reachable through the APIExport's
// virtual workspace, and so whether a fleet-wide controller can read and
// write them through the same cluster-aware client it uses for everything
// else.
//
// It matters because the bootstrap provider is made of Secrets. A
// KubeadmConfig produces a bootstrap data Secret and the cluster's
// certificate Secrets, all in the workspace being reconciled. If the virtual
// workspace cannot serve them, every one of those reads and writes needs a
// second, shard-scoped client - the shape internal/coremanager already
// carries for the ClusterCache's kubeconfig Secret, and would then have to
// carry for the whole bootstrap provider.
package claims_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

const (
	exportName  = "cluster-api-claims"
	bindingName = "cluster-api-claims"
	workspace   = "claims-demo"
)

// secretClaim is the claim under test: every Secret in the consuming
// workspace. Narrower selectors exist, and a deployment may want them; what
// is being established here is whether the mechanism serves the resource at
// all.
var secretClaim = apisv1alpha1.PermissionClaim{
	GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
	All:           true,
}

// configMapClaim is the other half of what the bootstrap provider needs: the
// control plane init lock is a ConfigMap.
var configMapClaim = apisv1alpha1.PermissionClaim{
	GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
	All:           true,
}

func TestClaimedSecretsAreServedThroughTheVirtualWorkspace(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	scheme, err := demo.ManagerScheme()
	if err != nil {
		t.Fatalf("building scheme: %v", err)
	}

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a root client: %v", err)
	}

	// One published type is enough: what is under test is the claim, not the
	// export's contents.
	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI,
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml")
	if err != nil {
		t.Fatalf("resolving CRD manifests: %v", err)
	}

	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:       exportName,
		SchemaPrefix:     "v1",
		CRDPaths:         crdPaths,
		PermissionClaims: []apisv1alpha1.PermissionClaim{secretClaim, configMapClaim},
		CRDTransform:     kcpfixtures.KeepStorageVersion,
	}); err != nil {
		t.Fatalf("publishing the APIExport: %v", err)
	}

	clusterName, err := kcpfixtures.EnsureWorkspace(ctx, rootClient, workspace, time.Minute)
	if err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}

	wsPath := logicalcluster.NewPath("root").Join(workspace)
	wsCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
	wsClient, err := client.New(wsCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a workspace client: %v", err)
	}

	if err := kcpfixtures.BindExport(ctx, wsClient, kcpfixtures.BindExportOptions{
		BindingName:      bindingName,
		ExportPath:       "root",
		ExportName:       exportName,
		PermissionClaims: []apisv1alpha1.PermissionClaim{secretClaim, configMapClaim},
		ReadyTimeout:     time.Minute,
	}); err != nil {
		t.Fatalf("binding the APIExport: %v", err)
	}

	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, time.Minute); err != nil {
		t.Fatalf("waiting for the endpoint slice: %v", err)
	}

	// A Secret written the ordinary way, into the workspace on the shard.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "written-on-the-shard", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte("shard")},
	}
	if err := wsClient.Create(ctx, secret); err != nil {
		t.Fatalf("creating a Secret in the workspace: %v", err)
	}

	// The same address the manager uses: the export's virtual workspace,
	// scoped to this one logical cluster.
	virtualCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, rootClient, exportName, baseCfg, time.Minute)
	if err != nil {
		t.Fatalf("resolving the virtual workspace: %v", err)
	}
	// VirtualWorkspaceConfig hands back the wildcard path - /clusters/* - which
	// is what a fleet-wide manager wants and what a client scoped to one
	// workspace must not keep: appending a second /clusters/ segment addresses
	// nothing, and discovery against it fails with "the server could not find
	// the requested resource", which reads like the claim not working.
	scopedCfg := rest.CopyConfig(virtualCfg)
	scopedCfg.Host = strings.TrimSuffix(virtualCfg.Host, "/clusters/*")
	scopedCfg = kcpclient.SetCluster(scopedCfg, logicalcluster.Name(clusterName).Path())

	var virtualClient client.Client
	// The claim becomes effective asynchronously: the binding reports Bound
	// before the virtual workspace's API surface has grown the claimed
	// resource, and a client built in that window has no REST mapping for it.
	if err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		cl, err := client.New(scopedCfg, client.Options{Scheme: scheme})
		if err != nil {
			return false, nil
		}
		got := &corev1.Secret{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(secret), got); err != nil {
			t.Logf("claimed Secret not readable through the virtual workspace yet: %v", err)
			return false, nil
		}
		virtualClient = cl
		return true, nil
	}); err != nil {
		t.Fatalf("a claimed Secret never became readable through the virtual workspace: %v", err)
	}

	// Writing matters as much as reading: the bootstrap provider creates the
	// data Secret and the cluster's certificates.
	written := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "written-through-the-export", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte("virtual")},
	}
	if err := virtualClient.Create(ctx, written); err != nil {
		t.Fatalf("creating a Secret through the virtual workspace: %v", err)
	}

	// The init lock is a ConfigMap, so the claim has to cover creating one of
	// those through the same client.
	lock := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "written-lock", Namespace: "default"},
		Data:       map[string]string{"value": "virtual"},
	}
	if err := virtualClient.Create(ctx, lock); err != nil {
		t.Errorf("creating a ConfigMap through the virtual workspace: %v", err)
	}

	// The bootstrap provider's lock carries an owner reference to the Cluster -
	// an object served by the export rather than claimed - so the claim has to
	// cover that shape too, not just a bare ConfigMap.
	owned := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "written-owned-lock",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "cluster.x-k8s.io/v1beta2",
				Kind:       "Cluster",
				Name:       "not-a-real-cluster",
				UID:        "00000000-0000-0000-0000-000000000000",
			}},
		},
		Data: map[string]string{"value": "virtual"},
	}
	if err := virtualClient.Create(ctx, owned); err != nil {
		t.Errorf("creating an owned ConfigMap through the virtual workspace: %v", err)
	}

	// And it has to have landed in this workspace on the shard, not somewhere
	// the virtual workspace alone would report back to us.
	readBack := &corev1.Secret{}
	if err := wsClient.Get(ctx, client.ObjectKeyFromObject(written), readBack); err != nil {
		t.Fatalf("a Secret written through the virtual workspace is not in the workspace: %v", err)
	}
	if got := string(readBack.Data["value"]); got != "virtual" {
		t.Errorf("Secret data = %q, want %q", got, "virtual")
	}
}
