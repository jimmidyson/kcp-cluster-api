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

// Package coremanager holds the Phase 1 walking skeleton's reconciler and
// webhook wiring, importable both by kcp/cmd/core-manager's thin main() and
// by the Phase 1 integration test.
package coremanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/core/reconcilers/cluster"
	"sigs.k8s.io/cluster-api/core/reconcilers/machine"
	coreadmission "sigs.k8s.io/cluster-api/core/webhooks/admission"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers"
	infrawebhooks "sigs.k8s.io/cluster-api/test/infrastructure/docker/webhooks/admission"
	cloudv1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/cloud/api/v1alpha1"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
)

const (
	// controllerName identifies this binary's clients to the API server
	// (e.g. in the User-Agent header), matching core/main.go and
	// test/infrastructure/docker/main.go's own controllerName convention.
	controllerName = "kcp-core-manager"

	// defaultRemoteConnectionGracePeriod and defaultRemoteConditionsGracePeriod
	// are core/main.go's own flag defaults (--remote-connection-grace-period,
	// --remote-conditions-grace-period), hardcoded here since the walking
	// skeleton doesn't expose the full flag surface core/main.go does.
	defaultRemoteConnectionGracePeriod = 50 * time.Second
	defaultRemoteConditionsGracePeriod = 5 * time.Minute
)

// inmemoryScheme is the scheme for the docker/dev infrastructure provider's
// in-memory workload-cluster backend (its own apiserver-like resources), kept
// separate from the main scheme per test/infrastructure/docker/main.go.
var inmemoryScheme = runtime.NewScheme()

func init() {
	_ = cloudv1.AddToScheme(inmemoryScheme)
	_ = corev1.AddToScheme(inmemoryScheme)
	_ = appsv1.AddToScheme(inmemoryScheme)
	_ = rbacv1.AddToScheme(inmemoryScheme)
	_ = storagev1.AddToScheme(inmemoryScheme)
	_ = apiextensionsv1.AddToScheme(inmemoryScheme)
	_ = policyv1.AddToScheme(inmemoryScheme)
}

// DevInfrastructure is the docker/dev infrastructure provider's backend,
// created once per process and shared by every workspace.
//
// It is not per-workspace because it cannot be: NewWorkloadClustersMux binds a
// fixed debug port at construction, so a second one in the same process fails
// with "address already in use". Sharing it has a consequence worth stating
// plainly — the in-memory workload-cluster backend keys its listeners by
// cluster name, so two workspaces each holding a Cluster with the same
// namespace and name collide there. That is a limitation of upstream's *test*
// infrastructure provider, which exists for development and e2e rather than
// for production, and it does not extend to the core reconcilers or to real
// infrastructure providers, which hold nothing process-wide. Fixing it belongs
// with the conversion plan's P3, the real docker-infrastructure provider port.
type DevInfrastructure struct {
	containerRuntime container.Runtime
	inMemoryManager  inmemoryruntime.Manager
	apiServerMux     *inmemoryserver.WorkloadClustersMux
}

// NewDevInfrastructure connects to the container runtime and starts the
// in-memory workload-cluster backend. Call it once, before any workspace is
// set up, and pass the result to every SetupReconcilers call.
func NewDevInfrastructure(ctx context.Context) (*DevInfrastructure, error) {
	runtimeClient, err := container.NewDockerClient()
	if err != nil {
		return nil, fmt.Errorf("establishing container runtime connection: %w", err)
	}

	inMemoryManager := inmemoryruntime.NewManager(inmemoryScheme)
	if err := inMemoryManager.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting in-memory manager: %w", err)
	}

	apiServerMux, err := inmemoryserver.NewWorkloadClustersMux(inMemoryManager, os.Getenv("POD_IP"))
	if err != nil {
		return nil, fmt.Errorf("creating workload clusters mux: %w", err)
	}

	return &DevInfrastructure{
		containerRuntime: runtimeClient,
		inMemoryManager:  inMemoryManager,
		apiServerMux:     apiServerMux,
	}, nil
}

// SetupReconcilers wires the walking skeleton's reconciler set onto one
// workspace's manager: the core Cluster/Machine reconcilers and the docker/dev
// infrastructure provider's DevCluster/DevMachine reconcilers, all unmodified
// upstream exported types, per ADR-0001's D3 scope. Everything else
// core/main.go and test/infrastructure/docker/main.go wire up
// (ClusterClass/topology, RuntimeSDK, MachineSet/MachineDeployment/MachinePool,
// ClusterResourceSet, MachineHealthCheck, CRD migration) is intentionally out
// of scope: proving the KCP-workspace-aware mechanism holds for a real
// Cluster->Machine loop is a different job from reaching feature parity with
// core/main.go (that's Phase 3).
//
// It is a providerwiring.SetupFunc in all but signature, and is called once per
// engaged workspace. Everything it creates is derived from mgr and so is scoped
// to that workspace; the only shared argument is dev, whose sharing is
// explained on DevInfrastructure.
//
// CRDMigrator is skipped entirely and deliberately, not just deferred: it
// operates on CustomResourceDefinition objects directly, but a workspace
// consuming a bound API via APIBinding has no such object to migrate - the
// CRD-shaped source of truth (the APIResourceSchema) lives in the exporting
// workspace instead. Running it here would be reconciling a concept that
// doesn't apply under kcp's APIBinding model.
func SetupReconcilers(ctx context.Context, mgr ctrl.Manager, dev *DevInfrastructure) error {
	if dev == nil {
		return errors.New("DevInfrastructure must not be nil: create it once per process with NewDevInfrastructure")
	}

	secretCachingClient, err := client.New(mgr.GetConfig(), client.Options{
		HTTPClient: mgr.GetHTTPClient(),
		Cache:      &client.CacheOptions{Reader: mgr.GetCache()},
	})
	if err != nil {
		return fmt.Errorf("creating secret caching client: %w", err)
	}

	clusterCache, err := clustercache.SetupWithManager(ctx, mgr, clustercache.Options{
		SecretClient: secretCachingClient,
		Client: clustercache.ClientOptions{
			UserAgent: remote.DefaultClusterAPIUserAgent(controllerName),
		},
	}, controllerOptions(10))
	if err != nil {
		return fmt.Errorf("creating ClusterCache: %w", err)
	}

	if err := (&cluster.Reconciler{
		Client:                      mgr.GetClient(),
		APIReader:                   mgr.GetAPIReader(),
		ClusterCache:                clusterCache,
		RemoteConnectionGracePeriod: defaultRemoteConnectionGracePeriod,
	}).SetupWithManager(ctx, mgr, controllerOptions(10)); err != nil {
		return fmt.Errorf("creating Cluster controller: %w", err)
	}

	if err := (&machine.Reconciler{
		Client:                      mgr.GetClient(),
		APIReader:                   mgr.GetAPIReader(),
		ClusterCache:                clusterCache,
		RemoteConditionsGracePeriod: defaultRemoteConditionsGracePeriod,
	}).SetupWithManager(ctx, mgr, controllerOptions(10)); err != nil {
		return fmt.Errorf("creating Machine controller: %w", err)
	}

	if err := (&reconcilers.DevCluster{
		Client:           mgr.GetClient(),
		ContainerRuntime: dev.containerRuntime,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithManager(ctx, mgr, controllerOptions(10)); err != nil {
		return fmt.Errorf("creating DevCluster controller: %w", err)
	}

	if err := (&reconcilers.DevMachine{
		Client:           mgr.GetClient(),
		ContainerRuntime: dev.containerRuntime,
		ClusterCache:     clusterCache,
		InMemoryManager:  dev.inMemoryManager,
		APIServerMux:     dev.apiServerMux,
	}).SetupWithManager(ctx, mgr, controllerOptions(10)); err != nil {
		return fmt.Errorf("creating DevMachine controller: %w", err)
	}

	return nil
}

// webhookWorkspace records the workspace whose webhooks are being served, so a
// second workspace is refused rather than silently ignored.
var webhookWorkspace struct {
	sync.Mutex
	name multicluster.ClusterName
	set  bool
}

// SetupWebhooks wires the core Cluster/Machine admission webhooks and the
// docker/dev infrastructure provider's DevCluster/DevMachine admission
// webhooks onto one workspace's manager, which also registers the shared
// "/convert" endpoint (see sigs.k8s.io/controller-runtime/pkg/builder's
// webhook builder) serving the core Cluster v1beta1<->v1beta2 conversion
// webhook.
//
// It serves exactly one workspace, and returns
// providerwiring.ErrWebhooksAlreadyWired if asked for a second. That is a real
// limitation, not a defensive check: the webhook server is process-wide, and
// controller-runtime's builder skips a path that is already registered instead
// of rejecting it, so wiring a second workspace would leave the first
// workspace's handlers - holding the first workspace's client - answering
// every workspace's admission requests, with nothing logged. Resolving an
// admission request to its own workspace is the conversion plan's G4, which is
// unbuilt and carries a required human review checkpoint.
func SetupWebhooks(workspace multicluster.ClusterName, mgr ctrl.Manager) error {
	webhookWorkspace.Lock()
	defer webhookWorkspace.Unlock()
	if webhookWorkspace.set {
		if webhookWorkspace.name != workspace {
			return fmt.Errorf("cannot wire webhooks for workspace %s, already wired for %s: %w",
				workspace, webhookWorkspace.name, providerwiring.ErrWebhooksAlreadyWired)
		}
		// Same workspace, already done. Repeating it would be a no-op anyway,
		// since controller-runtime skips a path it has already registered.
		return nil
	}

	if err := (&coreadmission.Cluster{Client: mgr.GetClient()}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating Cluster webhook: %w", err)
	}
	if err := (&coreadmission.Machine{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating Machine webhook: %w", err)
	}
	if err := (&infrawebhooks.DevCluster{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating DevCluster webhook: %w", err)
	}
	if err := (&infrawebhooks.DevMachine{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating DevMachine webhook: %w", err)
	}

	webhookWorkspace.name = workspace
	webhookWorkspace.set = true
	return nil
}

// ResetWebhookWorkspaceForTest clears the record of which workspace's webhooks
// are wired. Tests in one process wire webhooks more than once; a running
// manager never does.
func ResetWebhookWorkspaceForTest() {
	webhookWorkspace.Lock()
	defer webhookWorkspace.Unlock()
	webhookWorkspace.name = ""
	webhookWorkspace.set = false
}

// controllerOptions is the per-controller configuration every reconciler in a
// workspace gets.
//
// SkipNameValidation is required rather than preferred. controller-runtime
// records controller names in a process-global set it never empties, so the
// second workspace to wire a controller named "cluster" fails outright, as
// does the second engagement of any one workspace. The validation exists to
// stop two controllers reporting the same metric, and disabling it means
// exactly that: reconcile metrics are aggregated across workspaces rather than
// attributable to one. That is a reporting limitation rather than an isolation
// one - no workspace's objects become visible to another - and partitioning
// metrics per workspace is the conversion plan's P9.
func controllerOptions(c int) controller.Options {
	return controller.Options{
		MaxConcurrentReconciles: c,
		SkipNameValidation:      ptr.To(true),
	}
}
