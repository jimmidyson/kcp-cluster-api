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

// Package providerwiring_test is the acceptance test for engaging every bound
// workspace: the conversion plan's Phase 2 G2, specified in
// specs/20260815-185524-per-workspace-wiring.
//
// It runs against a real kcp server and deliberately does not need a container
// runtime. What is under test is the wiring — whether every workspace that
// binds gets set up, whether each one's client writes to its own workspace,
// and whether a workspace that unbinds stops — none of which involves an
// infrastructure provider. Keeping it independent of Docker means the property
// this project's whole design rests on is verifiable in any environment that
// can run kcp at all, rather than only where images can be pulled.
package providerwiring_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	exportName  = "cluster-api-wiring"
	bindingName = "cluster-api-wiring"

	// wiredClusterName is created by the setup function in whichever
	// workspace it is setting up. The same name in every workspace is the
	// point: if two workspaces' clients were in fact the same workspace's
	// client, the second create would collide instead of succeeding, and the
	// per-workspace counts below would not come out at one each.
	wiredClusterName = "wired-by-setup"

	// poll bounds every wait in this test. kcp, the provider's discovery
	// watch and the wiring all move asynchronously, so every assertion about
	// them is a bounded wait rather than an immediate read.
	pollInterval = 250 * time.Millisecond
	pollTimeout  = 90 * time.Second
)

// coreCRDs is the smallest set that exercises the wiring: one type, bound in
// each workspace, that the setup function can write.
var coreCRDs = []string{"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml"}

// keepOneVersion trims the published CRD to its v1beta2 version alone.
//
// A multi-version CRD needs a conversion strategy before kcp will accept it as
// an APIResourceSchema, and a conversion strategy means a webhook server —
// which this test deliberately does not have, since webhooks are the one thing
// the wiring under test does not do. Conversion itself is covered by the Phase
// 1 test, which serves webhooks for its single workspace.
func keepOneVersion(crd *apiextensionsv1.CustomResourceDefinition) {
	if crd.Spec.Group != clusterv1.GroupVersion.Group {
		return
	}
	kept := crd.Spec.Versions[:0]
	for _, v := range crd.Spec.Versions {
		if v.Name == clusterv1.GroupVersion.Version {
			kept = append(kept, v)
		}
	}
	crd.Spec.Versions = kept
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// setupRecord is what one call of the setup function did, so the test can
// assert on it afterwards.
type setupRecord struct {
	calls   int
	stopped chan struct{}
}

// recordingSetup is a providerwiring.SetupFunc that records the workspaces it
// was called for, writes an object through each workspace's own client, and
// registers a runnable whose stopping is observable.
type recordingSetup struct {
	t *testing.T

	mu      sync.Mutex
	records map[multicluster.ClusterName]*setupRecord
}

func newRecordingSetup(t *testing.T) *recordingSetup {
	return &recordingSetup{t: t, records: map[multicluster.ClusterName]*setupRecord{}}
}

func (s *recordingSetup) setup(ctx context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error {
	s.mu.Lock()
	record, ok := s.records[workspace]
	if !ok {
		record = &setupRecord{}
		s.records[workspace] = record
	}
	record.calls++
	record.stopped = make(chan struct{})
	stopped := record.stopped
	s.mu.Unlock()

	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return nil
	})); err != nil {
		return fmt.Errorf("registering the marker runnable: %w", err)
	}

	// Write through this workspace's own client. Where this object lands is
	// the whole question: a client that was not workspace-scoped would put
	// every workspace's copy in one place.
	err := mgr.GetClient().Create(ctx, &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: wiredClusterName, Namespace: "default"},
		// spec is required by the schema, and every field in it is optional,
		// so paused is here to make the object valid rather than to mean
		// anything. Nothing reconciles it: this test wires no reconcilers.
		Spec: clusterv1.ClusterSpec{Paused: ptr.To(true)},
	})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating the marker Cluster in workspace %s: %w", workspace, err)
	}
	return nil
}

func (s *recordingSetup) workspaces() []multicluster.ClusterName {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := make([]multicluster.ClusterName, 0, len(s.records))
	for workspace := range s.records {
		got = append(got, workspace)
	}
	slices.Sort(got)
	return got
}

func (s *recordingSetup) calls(workspace multicluster.ClusterName) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[workspace]; ok {
		return record.calls
	}
	return 0
}

func (s *recordingSetup) stopped(workspace multicluster.ClusterName) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[workspace].stopped
}

// eventually polls condition until it holds or the timeout expires, failing
// with describe. It is the only waiting primitive here: nothing in this test
// is synchronous enough to assert on directly.
func eventually(t *testing.T, describe string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", pollTimeout, describe)
		}
		time.Sleep(pollInterval)
	}
}

// boundWorkspace is one tenant: its path, its logical cluster name, and a
// client that talks to it directly, bypassing the manager entirely.
type boundWorkspace struct {
	name         multicluster.ClusterName
	path         logicalcluster.Path
	cfg          *rest.Config
	directClient client.Client
}

// TestEveryBoundWorkspaceIsWired is the acceptance condition for user stories
// 1 and 2 of the specification: two workspaces bind, both are set up without
// either being named in configuration, each one's writes land in itself, and
// one unbinding stops that workspace's work while leaving the other running.
func TestEveryBoundWorkspaceIsWired(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreCRDs...)
	must(t, err)

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	must(t, err)

	must(t, kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   exportName,
		SchemaPrefix: "v1",
		CRDPaths:     crdPaths,
		CRDTransform: keepOneVersion,
	}))

	// --- Two tenants, bound to the same export.
	tenants := make([]*boundWorkspace, 0, 2)
	for range 2 {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		cfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
		directClient, err := client.New(cfg, client.Options{Scheme: scheme})
		must(t, err)

		must(t, kcpfixtures.BindExport(ctx, directClient, kcpfixtures.BindExportOptions{
			BindingName: bindingName,
			ExportPath:  "root",
			ExportName:  exportName,
		}))

		tenants = append(tenants, &boundWorkspace{
			name:         multicluster.ClusterName(ws.Spec.Cluster),
			path:         wsPath,
			cfg:          cfg,
			directClient: directClient,
		})
	}

	must(t, kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 30*time.Second))

	// --- One manager, one provider, no workspace named anywhere.
	provider, err := apiexport.New(rootCfg, exportName, apiexport.Options{Scheme: scheme})
	must(t, err)

	mgr, err := mcmanager.New(rootCfg, provider, ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	must(t, err)

	setup := newRecordingSetup(t)

	// Before Start, which is the contract: the coordinator does not replay
	// engagements to components registered later.
	wiring, err := providerwiring.AddToManager(mgr, setup.setup, providerwiring.Options{
		Log: ctrl.Log.WithName("providerwiring"),
	})
	must(t, err)

	mgrCtx, stopManager := context.WithCancel(ctx)
	t.Cleanup(stopManager)
	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()

	// --- SC-001: both workspaces set up, neither named.
	want := []multicluster.ClusterName{tenants[0].name, tenants[1].name}
	slices.Sort(want)
	eventually(t, fmt.Sprintf("both workspaces to be set up (want %v)", want), func() bool {
		return slices.Equal(setup.workspaces(), want)
	})
	eventually(t, "both workspaces to be reported as engaged", func() bool {
		return slices.Equal(wiring.Engaged(), want)
	})

	// --- FR-007: each workspace's client wrote to its own workspace. Read it
	// back with a client built straight from each workspace's config, so the
	// manager's cache cannot be what makes this look right.
	for _, tenant := range tenants {
		eventually(t, fmt.Sprintf("the marker Cluster to exist in workspace %s", tenant.name), func() bool {
			var got clusterv1.Cluster
			err := tenant.directClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: wiredClusterName}, &got)
			return err == nil
		})

		var clusters clusterv1.ClusterList
		must(t, tenant.directClient.List(ctx, &clusters))
		if len(clusters.Items) != 1 {
			t.Errorf("workspace %s holds %d Clusters, want exactly 1: each workspace's setup writes one, to itself",
				tenant.name, len(clusters.Items))
		}
	}

	// --- SC-002: unbinding one workspace stops its work, and only its work.
	departing, remaining := tenants[0], tenants[1]
	departingStopped := setup.stopped(departing.name)
	remainingStopped := setup.stopped(remaining.name)

	must(t, departing.directClient.Delete(ctx, &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName},
	}))

	select {
	case <-departingStopped:
	case <-time.After(pollTimeout):
		t.Fatalf("workspace %s unbound but its runnable is still running", departing.name)
	}

	select {
	case <-remainingStopped:
		t.Fatalf("workspace %s stopped when a different workspace unbound", remaining.name)
	default:
	}

	eventually(t, fmt.Sprintf("only workspace %s to remain engaged", remaining.name), func() bool {
		return slices.Equal(wiring.Engaged(), []multicluster.ClusterName{remaining.name})
	})

	// --- SC-003: the same workspace binds again and is set up again. This is
	// the case that fails outright if per-engagement state accumulates, which
	// is what controller-runtime's never-emptied controller-name set would do
	// to a real reconciler set.
	must(t, kcpfixtures.BindExport(ctx, departing.directClient, kcpfixtures.BindExportOptions{
		BindingName: bindingName,
		ExportPath:  "root",
		ExportName:  exportName,
	}))

	eventually(t, fmt.Sprintf("workspace %s to be set up a second time", departing.name), func() bool {
		return setup.calls(departing.name) == 2
	})
	eventually(t, "both workspaces to be engaged again", func() bool {
		return slices.Equal(wiring.Engaged(), want)
	})
}
