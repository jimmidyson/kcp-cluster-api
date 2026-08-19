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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// TestCoreCanReachAnotherExportsTypes is the question a per-provider APIExport
// split turns on.
//
// Cluster API's core reconcilers do not merely coexist with the providers':
// the Cluster reconciler resolves spec.infrastructureRef, takes ownership of
// what it finds and reads its status, and the Machine reconciler does the same
// for spec.bootstrap.configRef. Publish those types from a separate export and
// they leave core's virtual workspace - so core reaches them, if at all,
// through a permission claim carrying the *other* export's identity hash.
//
// Everything else about splitting the exports is manifests and deployments.
// This is the part that either works or does not.
func TestCoreCanReachAnotherExportsTypes(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	const (
		infraExport = "dev-infrastructure"
		coreExport  = "core"
		workspace   = "split-exports"
	)

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

	// --- Two exports, one per provider, as a split deployment would have.
	// Both infrastructure types, though core claims only one of them. The
	// unclaimed one is this test's control: same export, same workspace, same
	// client, differing in nothing but the claim.
	infraCRDs, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest,
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml")
	if err != nil {
		t.Fatalf("resolving the infrastructure CRD: %v", err)
	}
	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   infraExport,
		SchemaPrefix: "v1",
		CRDPaths:     infraCRDs,
		CRDTransform: kcpfixtures.KeepStorageVersion,
	}); err != nil {
		t.Fatalf("publishing the infrastructure APIExport: %v", err)
	}

	// The identity hash is assigned by the server and is what a claim on
	// somebody else's exported resource has to name. It is per kcp instance,
	// so it can only be looked up at run time - which is why a deployment
	// needs a controller to maintain these claims rather than a manifest.
	identityHash := waitForIdentityHash(t, ctx, rootClient, infraExport)

	coreCRDs, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI,
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml")
	if err != nil {
		t.Fatalf("resolving the core CRD: %v", err)
	}
	devClusterClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: infrav1.GroupVersion.Group, Resource: "devclusters"},
		IdentityHash:  identityHash,
		Verbs:         []string{"*"},
	}
	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:       coreExport,
		SchemaPrefix:     "v1",
		CRDPaths:         coreCRDs,
		PermissionClaims: []apisv1alpha2.PermissionClaim{devClusterClaim},
		CRDTransform:     kcpfixtures.KeepStorageVersion,
	}); err != nil {
		t.Fatalf("publishing the core APIExport: %v", err)
	}

	// --- One workspace, bound to both, accepting core's claim on the
	// infrastructure type.
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

	for _, binding := range []kcpfixtures.BindExportOptions{
		{BindingName: infraExport, ExportPath: "root", ExportName: infraExport, ReadyTimeout: time.Minute},
		{
			BindingName:      coreExport,
			ExportPath:       "root",
			ExportName:       coreExport,
			PermissionClaims: []apisv1alpha2.PermissionClaim{devClusterClaim},
			ReadyTimeout:     time.Minute,
		},
	} {
		if err := kcpfixtures.BindExport(ctx, wsClient, binding); err != nil {
			t.Fatalf("binding %s: %v", binding.ExportName, err)
		}
	}

	// A DevCluster in the workspace, created the ordinary way through the
	// infrastructure provider's own API.
	devCluster := demo.NewDevCluster("split-00", demo.BackendInMemory)
	if err := wsClient.Create(ctx, devCluster); err != nil {
		t.Fatalf("creating the DevCluster: %v", err)
	}

	// --- Core's virtual workspace, scoped to that workspace: can it see and
	// own an object published by the other export?
	virtualCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, rootClient, coreExport, baseCfg, time.Minute)
	if err != nil {
		t.Fatalf("resolving core's virtual workspace: %v", err)
	}
	scopedCfg := rest.CopyConfig(virtualCfg)
	scopedCfg.Host = strings.TrimSuffix(virtualCfg.Host, "/clusters/*")
	scopedCfg = kcpclient.SetCluster(scopedCfg, logicalcluster.Name(clusterName).Path())

	var coreView client.Client
	if err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		cl, err := client.New(scopedCfg, client.Options{Scheme: scheme})
		if err != nil {
			return false, nil
		}
		got := &infrav1.DevCluster{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(devCluster), got); err != nil {
			t.Logf("the other export's DevCluster is not readable from core's virtual workspace yet: %v", err)
			return false, nil
		}
		coreView = cl
		return true, nil
	}); err != nil {
		t.Fatalf("core could never read a DevCluster published by another export: %v", err)
	}

	// Reading is not enough. The Cluster reconciler writes an owner reference
	// onto the infrastructure object, which is what starts the infrastructure
	// provider working, so the claim has to cover a write as well.
	owned := &infrav1.DevCluster{}
	if err := coreView.Get(ctx, client.ObjectKeyFromObject(devCluster), owned); err != nil {
		t.Fatalf("re-reading the DevCluster: %v", err)
	}
	owned.OwnerReferences = append(owned.OwnerReferences, metav1.OwnerReference{
		APIVersion: "cluster.x-k8s.io/v1beta2",
		Kind:       "Cluster",
		Name:       "split-00",
		UID:        "11111111-1111-1111-1111-111111111111",
	})
	if err := coreView.Update(ctx, owned); err != nil {
		t.Fatalf("core cannot take ownership of another export's DevCluster: %v", err)
	}

	// And the write has to have landed in the workspace, under the
	// infrastructure provider's own API rather than in some view of it.
	readBack := &infrav1.DevCluster{}
	if err := wsClient.Get(ctx, client.ObjectKeyFromObject(devCluster), readBack); err != nil {
		t.Fatalf("reading the DevCluster back from the workspace: %v", err)
	}
	if len(readBack.OwnerReferences) != 1 || readBack.OwnerReferences[0].Name != "split-00" {
		t.Errorf("owner references in the workspace = %v, want the one core wrote", readBack.OwnerReferences)
	}

	assertWildcardWatchSeesClaimedType(t, ctx, virtualCfg, scheme, wsClient, clusterName)
	assertUnclaimedTypeStaysOutOfReach(t, ctx, virtualCfg, scheme, coreView)
}

// assertUnclaimedTypeStaysOutOfReach is the control for everything above.
//
// DevMachine is published by the same export as DevCluster and bound in the
// same workspace; the only difference is that core does not claim it. If core
// could reach it anyway, then none of the claims in this test were doing any
// work and the split would be resting on an authorisation that is not there.
func assertUnclaimedTypeStaysOutOfReach(
	t *testing.T,
	ctx context.Context,
	virtualCfg *rest.Config,
	scheme *runtime.Scheme,
	coreView client.Client,
) {
	t.Helper()

	wildcard, err := client.NewWithWatch(virtualCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a wildcard client: %v", err)
	}
	if watcher, err := wildcard.Watch(ctx, &infrav1.DevMachineList{}); err == nil {
		watcher.Stop()
		t.Error("core can watch an unclaimed type from another export: the claims prove nothing")
	}

	if err := coreView.List(ctx, &infrav1.DevMachineList{}); err == nil {
		t.Error("core can list an unclaimed type from another export: the claims prove nothing")
	} else {
		t.Logf("unclaimed type is out of reach, as it should be: %v", err)
	}
}

// assertWildcardWatchSeesClaimedType is the half of the split that reading and
// writing does not cover.
//
// Reads and writes above went to one workspace at a time. A fleet-wide
// controller does neither for its *events*: it watches
// /clusters/* on the export's virtual workspace once per type, and
// demultiplexes each event to the workspace it came from. The Cluster
// reconciler's watch on an infrastructure object is not even static - it is
// added by external.ObjectTracker the first time a Cluster references one - so
// a claim that serves Get and Update but not Watch would leave every
// cross-export reconcile firing once and never again.
//
// Two things are asserted: that the event arrives at all, and that it carries
// the logical cluster. The second is what the wildcard registry keys on, and an
// event without it is one no controller can route.
func assertWildcardWatchSeesClaimedType(
	t *testing.T,
	ctx context.Context,
	virtualCfg *rest.Config,
	scheme *runtime.Scheme,
	wsClient client.Client,
	clusterName string,
) {
	t.Helper()

	// The wildcard path, exactly as VirtualWorkspaceConfig hands it over and as
	// the provider's cache uses it.
	wildcard, err := client.NewWithWatch(virtualCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a wildcard client on core's virtual workspace: %v", err)
	}

	watcher, err := wildcard.Watch(ctx, &infrav1.DevClusterList{})
	if err != nil {
		t.Fatalf("core cannot watch another export's DevClusters across workspaces: %v", err)
	}
	defer watcher.Stop()

	// Created after the watch is established, so what arrives is an event
	// rather than a replay.
	watched := demo.NewDevCluster("split-watched", demo.BackendInMemory)
	if err := wsClient.Create(ctx, watched); err != nil {
		t.Fatalf("creating the watched DevCluster: %v", err)
	}

	deadline := time.After(time.Minute)
	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				t.Fatal("the wildcard watch closed before the DevCluster arrived")
			}
			obj, isDevCluster := event.Object.(*infrav1.DevCluster)
			if !isDevCluster || obj.Name != watched.Name {
				continue
			}
			if got := string(logicalcluster.From(obj)); got != clusterName {
				t.Errorf("watch event for %s names logical cluster %q, want %q: an event no controller can route",
					obj.Name, got, clusterName)
			}
			return
		case <-deadline:
			t.Fatal("core never saw a watch event for another export's DevCluster")
		case <-ctx.Done():
			t.Fatalf("context done while waiting for the watch event: %v", ctx.Err())
		}
	}
}

// waitForIdentityHash returns the export's server-assigned identity, which a
// claim from another export must carry.
func waitForIdentityHash(t *testing.T, ctx context.Context, cl client.Client, name string) string {
	t.Helper()

	var hash string
	if err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		export := &apisv1alpha1.APIExport{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name}, export); err != nil {
			return false, nil
		}
		hash = export.Status.IdentityHash
		return hash != "", nil
	}); err != nil {
		t.Fatalf("APIExport %s never got an identity hash: %v", name, err)
	}
	return hash
}
