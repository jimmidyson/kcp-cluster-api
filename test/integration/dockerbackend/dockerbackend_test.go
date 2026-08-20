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

// Package dockerbackend_test exercises the dev infrastructure provider's
// *docker* backend - real containers through a real container runtime - which
// is why it is the one suite that cannot run everywhere and has a task target
// of its own.
//
// It began as the Phase 1 exit-criteria test from docs/conversion-plan.md, and
// still asserts what that criterion asked for: a Cluster->Machine reconcile
// loop working end to end inside a KCP workspace through entirely unmodified
// upstream reconciler and webhook code, including an admission webhook and the
// conversion webhook, with each reconciler write landing in the engaged
// workspace rather than a wildcard (D4's flagged open question - see
// ADR-0001).
//
// It wires the core and dev infrastructure providers onto one fleet, in one
// process. No deployment does that any more - they are separate deployments
// with separate APIExports - so read this as the reconcile loop under test
// rather than as the shape anything runs. What makes them co-located here is
// that this suite predates the split and proves something orthogonal to it.
package dockerbackend_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/go-logr/logr/testr"
	dockerclient "github.com/moby/moby/client"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1beta1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta1"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

const (
	exportName  = "cluster-api"
	bindingName = "cluster-api"
)

// crdPaths are the CRDs Phase 1 publishes, per ADR-0001's D3 scope: core
// Cluster/Machine/MachineSet/MachineDeployment/MachineHealthCheck plus the
// docker/dev infrastructure provider's DevCluster/DevMachine, resolved
// relative to this test file's location.
//
// MachineSet/MachineDeployment aren't reconciled by this Phase 1 skeleton
// (see kcp/internal/coremanager/setup.go), but they still have to be bound:
// cluster.Reconciler and machine.Reconciler both unconditionally register
// watches on them (as event sources that can trigger a Cluster/Machine
// reconcile), and controller-runtime's cache blocks that controller's
// startup on every registered source's cache sync - including ones for
// kinds the API server doesn't serve at all. Leaving them out doesn't make
// the reconciler skip them, it just makes it hang.
// coreCRDs and dockerCRDs are relative to their respective modules. They are
// resolved from the pinned dependency at run time rather than copied into this
// repository, so they cannot disagree with the version the code is built
// against - see kcpfixtures.ManifestPath.
var (
	coreCRDs = []string{
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
		// Watched by the ClusterClass controller, which the core provider wires
		// whenever ClusterTopology is on - this project's default. This test
		// creates no ClusterClass; publishing the type is what lets the
		// controller start at all.
		"core/config/crd/bases/cluster.x-k8s.io_clusterclasses.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
		// As above: read by the topology reconciler whatever the MachinePool
		// gate says.
		"core/config/crd/bases/cluster.x-k8s.io_machinepools.yaml",
	}
	dockerCRDs = []string{
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
	}
)

// resolveManifests returns the CRD and webhook manifest paths for this test,
// resolved from the pinned Cluster API modules.
func resolveManifests(t *testing.T) (crdPaths, webhookPaths []string) {
	t.Helper()

	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreCRDs...)
	if err != nil {
		t.Fatalf("resolving core CRD manifests: %v", err)
	}
	dockerPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest, dockerCRDs...)
	if err != nil {
		t.Fatalf("resolving docker CRD manifests: %v", err)
	}
	crdPaths = append(crdPaths, dockerPaths...)

	coreWebhook, err := kcpfixtures.ManifestPath(kcpfixtures.ModuleClusterAPI, "core/config/webhook/manifests.yaml")
	if err != nil {
		t.Fatalf("resolving core webhook manifests: %v", err)
	}
	dockerWebhook, err := kcpfixtures.ManifestPath(kcpfixtures.ModuleClusterAPITest, "infrastructure/docker/config/webhook/manifests.yaml")
	if err != nil {
		t.Fatalf("resolving docker webhook manifests: %v", err)
	}

	return crdPaths, []string{coreWebhook, dockerWebhook}
}

func TestCoreManagerClusterToMachine(t *testing.T) {
	// Skipping is reasonable on a developer machine with no container
	// runtime. It is a defect under `task verify`, which checks for one
	// before starting this step: there, a skip would report the project's
	// only end-to-end reconcile as passing without having run it, and the
	// only trace would be the step finishing in a fraction of a second.
	if err := verify.ContainerRuntimeAvailable(); err != nil {
		if verify.CapabilityAsserted(verify.CapabilityContainerRuntime) {
			t.Fatalf("verification asserted a container runtime is available, but this test cannot reach one: %v", err)
		}
		t.Skipf("no container runtime in this environment: %v", err)
	}
	ensureKindDockerNetwork(t)

	// The gates a deployment runs with: ClusterTopology on, MachinePool off.
	// The second is the one this test would hang without - the core reconcilers
	// watch MachinePool when it is on, and this fixture publishes no
	// MachinePool CRD, so their cache sync would never complete.
	if err := coremanager.SetFeatureGateDefaults(); err != nil {
		t.Fatalf("failed to set feature gate defaults: %v", err)
	}

	// Installs the process-wide contract-metadata and conversion resolvers -
	// see ADR-0001's "Known gaps" section and
	// kcp/internal/coremanager/contractmetadata.go.
	coremanager.SetupProcessGlobals()

	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	// Resolved from the pinned Cluster API modules, not from a co-located
	// source tree: they come from the same version this test compiles
	// against, so they cannot disagree with it.
	crdPaths, webhookManifestPaths := resolveManifests(t)

	// --- Start a real kcp server, and derive a client scoped to its root
	// workspace: PublishAPIExport runs there, publishing the CRDs this test
	// needs (see crdPaths above).
	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	fixtureScheme := runtime.NewScheme()
	must(t, clientgoscheme.AddToScheme(fixtureScheme))
	must(t, apiextensionsv1.AddToScheme(fixtureScheme))
	must(t, apisv1alpha1.AddToScheme(fixtureScheme))
	must(t, apisv1alpha2.AddToScheme(fixtureScheme))

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: fixtureScheme})
	if err != nil {
		t.Fatalf("failed to build root workspace client: %v", err)
	}

	// --- Prep the webhook serving CA/host/port and parse+patch the real
	// generated MutatingWebhookConfiguration/ValidatingWebhookConfiguration
	// manifests (converting their Service-based clientConfig to a direct
	// URL at our local serving address), before publishing the APIExport:
	// the CRD conversion clientConfig needs the same host/port/CA.
	whOpts := &envtest.WebhookInstallOptions{Paths: webhookManifestPaths}
	if err := whOpts.PrepWithoutInstalling(); err != nil {
		t.Fatalf("failed to prep webhook install options: %v", err)
	}
	t.Cleanup(func() { _ = whOpts.Cleanup() })

	conversionURL := fmt.Sprintf("https://%s:%d/convert", whOpts.LocalServingHost, whOpts.LocalServingPort)

	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   exportName,
		SchemaPrefix: "v1",
		CRDPaths:     crdPaths,
		ConversionWebhookClientConfig: &apiextensionsv1.WebhookClientConfig{
			URL:      &conversionURL,
			CABundle: whOpts.LocalServingCAData,
		},
		CRDTransform: dropV1Beta1ForCoreTypes,
	}); err != nil {
		t.Fatalf("failed to publish APIExport: %v", err)
	}

	// --- Create a child workspace and bind it to the export.
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
	clusterName := multicluster.ClusterName(ws.Spec.Cluster)

	wsCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
	wsFixtureClient, err := client.New(wsCfg, client.Options{Scheme: fixtureScheme})
	if err != nil {
		t.Fatalf("failed to build workspace client: %v", err)
	}
	if err := kcpfixtures.BindExport(ctx, wsFixtureClient, kcpfixtures.BindExportOptions{
		BindingName: bindingName,
		ExportPath:  "root",
		ExportName:  exportName,
	}); err != nil {
		t.Fatalf("failed to bind APIExport into workspace: %v", err)
	}

	// Only now does kcp's apiexportendpointsliceurls controller populate the
	// endpoint slice: it deliberately leaves status.endpoints empty until at
	// least one APIBinding consumes the export (see PublishAPIExport's doc
	// comment).
	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 30*time.Second); err != nil {
		t.Fatalf("APIExportEndpointSlice never got an endpoint: %v", err)
	}

	// Install the (already-prepped, already-patched) admission webhook
	// configurations into the workspace, and wait for them to appear.
	if err := whOpts.Install(wsCfg); err != nil {
		t.Fatalf("failed to install admission webhooks: %v", err)
	}

	// --- Start the multicluster manager against the root workspace's
	// endpoint slice, and wire the Phase 1 reconcilers/webhooks onto the
	// engaged child workspace exactly as kcp/cmd/core-manager/main.go does.
	mgrScheme := runtime.NewScheme()
	must(t, clientgoscheme.AddToScheme(mgrScheme))
	must(t, apiextensionsv1.AddToScheme(mgrScheme))
	must(t, apisv1alpha1.AddToScheme(mgrScheme))
	must(t, apisv1alpha2.AddToScheme(mgrScheme))
	must(t, clusterv1beta1.AddToScheme(mgrScheme))
	must(t, clusterv1.AddToScheme(mgrScheme))
	must(t, infrav1beta1.AddToScheme(mgrScheme))
	must(t, infrav1.AddToScheme(mgrScheme))

	wildcardRegistry := &capicontrollerutil.WildcardRegistry{}
	provider, err := providerwiring.NewAPIExportProvider(rootCfg, exportName, mgrScheme, wildcardRegistry,
		providerwiring.WithCacheIndexes(ctx, coremanager.FleetCacheIndexes()...))
	if err != nil {
		t.Fatalf("failed to construct kcp APIExport provider: %v", err)
	}

	// The local manager is addressed at the APIExport's virtual workspace, not
	// at the workspace holding the export — exactly as cmd/core-manager does,
	// and for a reason this test exists to catch: the local RESTMapper answers
	// every question a fleet-wide controller asks that has no cluster to
	// resolve from, and setup asks several before any workspace has engaged.
	// util.ClusterToTypedObjectsMapper asks whether MachineList is namespaced,
	// and the exporting workspace does not bind what it exports, so pointing
	// the manager at rootCfg fails setup with
	//
	//	failed to get restmapping: no matches for kind "MachineList"
	//
	// The endpoint slice is already populated at this point, because the
	// binding above is what populates it.
	localCfg, err := providerwiring.VirtualWorkspaceConfig(ctx, rootClient, exportName, baseCfg, 30*time.Second)
	if err != nil {
		t.Fatalf("failed to resolve the APIExport's virtual workspace: %v", err)
	}

	mgr, err := mcmanager.New(localCfg, provider, ctrl.Options{
		Scheme: mgrScheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    whOpts.LocalServingHost,
			Port:    whOpts.LocalServingPort,
			CertDir: whOpts.LocalServingCertDir,
		}),
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		t.Fatalf("failed to set up multicluster manager: %v", err)
	}

	mgrCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	// The dev infrastructure provider's backend binds a fixed port, so it is
	// created once per process and shared - see coremanager.DevInfrastructure.
	dev, err := coremanager.NewDevInfrastructure(mgrCtx, "127.0.0.1")
	if err != nil {
		t.Fatalf("failed to set up the dev infrastructure provider backend: %v", err)
	}

	// Wired once, before Start, for every workspace this provider will ever
	// engage - including ones created later in this test. That ordering is not
	// stylistic: multicluster-runtime hands each engagement to the components
	// registered at that moment and never replays earlier ones.
	if err := coremanager.SetupFleetControllers(mgrCtx, mgr, wildcardRegistry, dev, coremanager.SetupOptions{
		// The shard, not the manager's config: the ClusterCache reads
		// kubeconfig Secrets, which live in the workspaces themselves.
		ShardConfig: baseCfg,
	}); err != nil {
		t.Fatalf("failed to set up fleet-wide reconcilers: %v", err)
	}

	go func() {
		if err := mgr.Start(mgrCtx); err != nil {
			t.Logf("manager exited: %v", err)
		}
	}()

	wsMgr, err := coremanager.WaitForManager(mgrCtx, mgr, clusterName, 250*time.Millisecond, 30*time.Second)
	if err != nil {
		t.Fatalf("workspace was never engaged by the provider: %v", err)
	}

	coremanager.ResetWebhookWorkspaceForTest()
	t.Cleanup(coremanager.ResetWebhookWorkspaceForTest)
	if err := coremanager.SetupWebhooks(clusterName, wsMgr); err != nil {
		t.Fatalf("failed to set up webhooks: %v", err)
	}

	// --- A second workspace, served by the same controllers.
	//
	// It is wired by nobody: the controllers above already serve it, and that is
	// the thing this asserts. Under per-workspace wiring this was a regression
	// test for controller-runtime's process-global registry of controller names,
	// which rejected the second workspace's controller called "cluster" and had
	// to be disabled with SkipNameValidation. A fleet-wide controller registers
	// each name once, so the collision is gone rather than suppressed - and the
	// check is back on, which is what would fail here if a controller were
	// somehow wired twice.
	secondWsPath, secondWs := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
	secondClusterName := multicluster.ClusterName(secondWs.Spec.Cluster)

	secondWsCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), secondWsPath)
	secondFixtureClient, err := client.New(secondWsCfg, client.Options{Scheme: fixtureScheme})
	if err != nil {
		t.Fatalf("failed to build second workspace client: %v", err)
	}
	if err := kcpfixtures.BindExport(ctx, secondFixtureClient, kcpfixtures.BindExportOptions{
		BindingName: bindingName,
		ExportPath:  "root",
		ExportName:  exportName,
	}); err != nil {
		t.Fatalf("failed to bind APIExport into the second workspace: %v", err)
	}

	secondWsMgr, err := coremanager.WaitForManager(mgrCtx, mgr, secondClusterName, 250*time.Millisecond, 60*time.Second)
	if err != nil {
		t.Fatalf("second workspace was never engaged by the provider: %v", err)
	}

	// Webhooks, by contrast, are deliberately not multi-workspace: there is one
	// webhook server for the process, and controller-runtime skips a path that
	// is already registered rather than rejecting it, so a second workspace's
	// handlers would be silently dropped and the first workspace's client would
	// answer for everyone. That has to be an error until G4 exists.
	if err := coremanager.SetupWebhooks(secondClusterName, secondWsMgr); !errors.Is(err, providerwiring.ErrWebhooksAlreadyWired) {
		t.Errorf("SetupWebhooks for a second workspace = %v, want it to wrap %v", err, providerwiring.ErrWebhooksAlreadyWired)
	}

	// --- Exercise it: create a Cluster/DevCluster/Machine/DevMachine
	// through the engaged workspace's own scoped client (proving reads are
	// workspace-scoped), and separately through an independent client built
	// directly against wsCfg (bypassing the manager and its cache entirely)
	// to prove writes actually landed in *this* workspace and not a
	// wildcard/no-op (D4's flagged open question).
	directClient, err := client.New(wsCfg, client.Options{Scheme: mgrScheme})
	if err != nil {
		t.Fatalf("failed to build direct workspace client: %v", err)
	}

	testCluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevCluster",
				Name:     "test-cluster",
			},
		},
	}
	devCluster := &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec: infrav1.DevClusterSpec{
			Backend: infrav1.DevClusterBackendSpec{Docker: &infrav1.DockerClusterBackendSpec{}},
		},
	}

	// Cluster API's admission webhook defaults/validates cross-references
	// between a Cluster and its infrastructureRef; creating both proves the
	// admission webhook actually ran (an unpatched Cluster without a valid
	// reference is rejected by coreadmission.Cluster).
	if err := directClient.Create(ctx, devCluster); err != nil {
		t.Fatalf("failed to create DevCluster directly: %v", err)
	}
	if err := directClient.Create(ctx, testCluster); err != nil {
		t.Fatalf("failed to create Cluster directly (admission webhook may have rejected it): %v", err)
	}

	// Conversion webhook proof: read DevCluster back through its v1beta1
	// spoke version. If /convert weren't reachable/working, this Get would
	// fail. This deliberately uses DevCluster, not Cluster: Cluster's own
	// v1beta1<->v1beta2 conversion (core/webhooks/conversion/cluster.go)
	// resolves infrastructureRef's contract version via
	// internal/contract.GetAPIVersion, which looks up a
	// CustomResourceDefinition object by name - a mechanism that doesn't
	// exist for APIs a workspace only consumes via APIBinding (there is no
	// local CRD object to look up). That's a real, separate architectural
	// gap this Phase 1 spike surfaced, not a bug in this test; see
	// ADR-0001's "Known gaps" section. DevCluster's conversion
	// (test/infrastructure/docker/api/v1beta1/conversion.go) has no such
	// cross-reference lookup, so it isn't affected and still proves the
	// conversion webhook mechanism itself (routing, CA/URL, /convert
	// registration) works end to end under kcp.
	spokeDevCluster := &infrav1beta1.DevCluster{}
	if err := directClient.Get(ctx, client.ObjectKeyFromObject(devCluster), spokeDevCluster); err != nil {
		t.Fatalf("failed to read DevCluster back via v1beta1 (conversion webhook not working?): %v", err)
	}

	bootstrapSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "test-machine-bootstrap", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte("#!/bin/bash\necho bootstrapped\n")},
	}
	if err := directClient.Create(ctx, bootstrapSecret); err != nil {
		t.Fatalf("failed to create bootstrap secret directly: %v", err)
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "test-machine", Namespace: "default"},
		Spec: clusterv1.MachineSpec{
			ClusterName: testCluster.Name,
			Version:     "v1.31.0",
			Bootstrap: clusterv1.Bootstrap{
				DataSecretName: ptr.To(bootstrapSecret.Name),
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevMachine",
				Name:     "test-machine",
			},
		},
	}
	devMachine := &infrav1.DevMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-machine",
			Namespace: "default",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: testCluster.Name},
		},
		Spec: infrav1.DevMachineSpec{
			Backend: infrav1.DevMachineBackendSpec{Docker: &infrav1.DockerMachineBackendSpec{}},
		},
	}

	// Broadens the admission webhook proof to Machine/DevMachine too. Their
	// reconcile progress isn't asserted below: Machine.spec.infrastructureRef
	// resolution hits the same known gap documented below, and DevMachine's
	// own reconciler waits on Cluster/DevCluster to become ready first, which
	// (per that gap) never happens in this environment.
	if err := directClient.Create(ctx, devMachine); err != nil {
		t.Fatalf("failed to create DevMachine directly: %v", err)
	}
	if err := directClient.Create(ctx, machine); err != nil {
		t.Fatalf("failed to create Machine directly (admission webhook may have rejected it): %v", err)
	}

	// Write-path routing proof (D4's flagged open question): directClient
	// bypasses our manager and its cache entirely, talking straight to
	// wsCfg. cluster.Reconciler's very first write on every Cluster is
	// adding ClusterFinalizer (before it ever gets to the infrastructureRef
	// resolution that's blocked below), so seeing it here - a write made by
	// the reconciler, not by this test - confirms the reconciler's writes
	// land in *this* workspace specifically, not a wildcard/no-op.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got := &clusterv1.Cluster{}
		if err := directClient.Get(ctx, client.ObjectKeyFromObject(testCluster), got); err != nil {
			return false, err.Error()
		}
		hasFinalizer := slices.Contains(got.Finalizers, clusterv1.ClusterFinalizer)
		return hasFinalizer, fmt.Sprintf("finalizers=%v", got.Finalizers)
	}, 30*time.Second, time.Second, "Cluster reconciler never wrote its finalizer back into this workspace")

	// Phase 1's actual exit criterion: the Cluster reconciler resolves
	// Cluster.spec.infrastructureRef (DevCluster) via
	// controllers/external.GetObjectFromContractVersionedRef ->
	// internal/contract.GetGKMetadataFunc - the fork's one deliberate,
	// tracked exception to the upstream-is-read-only invariant (see
	// AGENTS.md, ADR-0001's "Known gaps" section, and
	// kcp/internal/coremanager.SetupContractMetadata) - and the
	// docker/dev infrastructure provider reaches real docker daemon calls
	// to provision a container, driven entirely by unmodified upstream
	// reconciler code running against this one engaged KCP workspace.
	//
	// This asserts reaching that point, not full readiness: provisioning a
	// working node also needs to pull kindest/node and kindest/haproxy
	// images from Docker Hub, which this sandbox's network policy blocks
	// outright (confirmed independently of this test - even a bare `docker
	// pull kindest/haproxy:...` here gets a 403 from Docker Hub's CDN). In
	// an environment with normal internet access (e.g. this repo's own
	// kcp-tests.yaml CI runner), the same reconcile loop is expected to
	// reach DevMachine Ready; that's a network property of this sandbox,
	// not something the KCP-workspace-aware mechanism affects.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got := &clusterv1.Cluster{}
		if err := directClient.Get(ctx, client.ObjectKeyFromObject(testCluster), got); err != nil {
			return false, err.Error()
		}
		cond := meta.FindStatusCondition(got.Status.Conditions, string(clusterv1.ClusterInfrastructureReadyCondition))
		// Reason != InternalError is the signal that matters here: it means
		// reconciliation got past resolving the infrastructureRef (this
		// fork's known gap) and into real docker/dev provider logic, which
		// now reports its own (here, network-gated) NotReady reason instead.
		reachedDockerLogic := cond != nil && cond.Reason != "InternalError"
		return reachedDockerLogic, fmt.Sprintf("InfrastructureReady condition = %+v", cond)
	}, 2*time.Minute, time.Second, "Cluster's InfrastructureReady condition never got past the contract-metadata gap")

	t.Logf("Cluster %s and Machine %s reconciled past the contract-metadata gap into real docker/dev provider logic in workspace %s (%s); full readiness needs network access to pull kindest/* images that this sandbox blocks", testCluster.Name, machine.Name, wsPath, clusterName)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("scheme registration failed: %v", err)
	}
}

// ensureKindDockerNetwork creates the "kind" docker network the docker/dev
// infrastructure provider's load balancer and machine containers attach to
// (test/infrastructure/docker/internal/docker.DefaultNetwork - unexported,
// so duplicated here as a literal; it's a stable convention shared with the
// kind project, not an implementation detail of that package). Normally
// `kind create cluster` creates this as a side effect of setting up a local
// management cluster; this walking skeleton doesn't use kind, so nothing
// else creates it.
func ensureKindDockerNetwork(t *testing.T) {
	t.Helper()

	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	if _, err := cli.NetworkInspect(t.Context(), "kind", dockerclient.NetworkInspectOptions{}); err == nil {
		return // already exists.
	} else if !errdefs.IsNotFound(err) {
		t.Fatalf("failed to inspect the \"kind\" docker network: %v", err)
	}
	if _, err := cli.NetworkCreate(t.Context(), "kind", dockerclient.NetworkCreateOptions{}); err != nil {
		t.Fatalf("failed to create the \"kind\" docker network: %v", err)
	}
}

// dropV1Beta1ForCoreTypes trims Cluster and Machine down to their v1beta2
// version only, so PublishAPIExport never needs a conversion strategy for
// them at all. This sidesteps a real, separate gap this Phase 1 spike
// surfaced (not a bug in this test): Cluster's v1beta1<->v1beta2 conversion
// (core/webhooks/conversion/cluster.go) resolves a cross-referenced type's
// (e.g. DevCluster's) contract version via internal/contract.GetAPIVersion,
// which looks up a CustomResourceDefinition object by name - a mechanism
// that has no equivalent for APIs a workspace only consumes via APIBinding,
// where no such object exists. DevCluster/DevMachine are left with both
// versions (their conversion has no such cross-reference lookup), so the
// conversion webhook mechanism itself is still proven end to end. See
// ADR-0001's "Known gaps" section.
func dropV1Beta1ForCoreTypes(crd *apiextensionsv1.CustomResourceDefinition) {
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
