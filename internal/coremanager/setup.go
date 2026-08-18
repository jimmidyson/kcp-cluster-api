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
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	coreadmission "sigs.k8s.io/cluster-api/core/webhooks/admission"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
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

	// DefaultMaxConcurrentReconciles is the per-controller worker count a
	// workspace gets unless an operator raises it.
	//
	// Upstream's core/main.go uses 10. That is the right order of magnitude
	// when it is the whole process's budget; here it is paid once per
	// controller *per workspace*, and controller-runtime starts every worker
	// eagerly rather than on demand. At five controllers, 10 means fifty
	// goroutines per workspace before a single object exists — a cost paid by
	// every idle tenant.
	//
	// # Chosen against measurement, not intuition
	//
	// This was 2, with a comment saying so was reasoned rather than measured
	// and that the sweep should set it. The sweep now has
	// (evidence/reconcile-throughput.md), and it says two things.
	//
	// Throughput is **linear** in this number: one worker retires 4 reconciles
	// per second per workspace at a 250 ms reconcile, two retire 8.0 — 100% of
	// linear — and the relationship holds within 9% to eight workers. So the
	// return on raising it is exact rather than hoped for.
	//
	// And the cost is exactly 1 goroutine and under 1 KiB per worker per
	// controller per workspace. At the wired census of five controllers, 4
	// costs 85 goroutines per workspace against 2's 75 — 13% more — and halves
	// the worst case a single tenant can hit.
	//
	// Four rather than upstream's 10 because the remaining gap is bought at 53%
	// more goroutines, and because raising this partition is the *expensive*
	// way to buy burst capacity: these workers are statically partitioned per
	// workspace, so a bursting tenant cannot use the thousands sitting idle in
	// other workspaces. Pooling them behind fleet-wide controllers raises burst
	// capacity and lowers total goroutines at once, and that is the fix this
	// number is standing in for.
	DefaultMaxConcurrentReconciles = 4
)

// SetupOptions configures what SetupReconcilers wires.
type SetupOptions struct {
	// MaxConcurrentReconciles is the per-controller worker count for each
	// workspace. Zero means DefaultMaxConcurrentReconciles.
	MaxConcurrentReconciles int

	// FleetMaxConcurrentReconciles is the worker count for each controller that
	// serves every workspace. Zero means DefaultFleetMaxConcurrentReconciles.
	//
	// Separate from MaxConcurrentReconciles because the two size different
	// things: one is multiplied by the number of workspaces and the other is
	// not. One knob meaning both would make raising the shared pool — which is
	// cheap — also raise every workspace's private pool, which is not.
	FleetMaxConcurrentReconciles int

	// ShardConfig addresses the kcp shard the workspaces live on, as opposed to
	// the APIExport virtual workspace the multi-cluster manager is built
	// against.
	//
	// Required by SetupFleetControllers, and only by it. The two endpoints
	// describe different API surfaces: the virtual workspace serves exactly
	// what the export serves, and the ClusterCache has to read a core
	// v1.Secret, which it does not. See NewWorkspaceSecretReader.
	ShardConfig *rest.Config
}

func (o SetupOptions) maxConcurrentReconciles() int {
	if o.MaxConcurrentReconciles <= 0 {
		return DefaultMaxConcurrentReconciles
	}
	return o.MaxConcurrentReconciles
}

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
