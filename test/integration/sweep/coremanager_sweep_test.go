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

package sweep_test

import (
	"context"
	"fmt"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"slices"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// The CRDs the real reconciler set needs bound in every workspace. This is not
// a matter of taste: cluster.Reconciler and machine.Reconciler watch
// MachineSet, MachineDeployment and MachineHealthCheck whether or not this
// sweep creates any, and controller-runtime blocks a controller's startup on
// every registered source's cache sync — including sources for kinds the API
// server does not serve. Leaving one out does not make a reconciler skip it,
// it makes the controller hang (ADR-0001, Phase 1 results).
var (
	coreReconcilerCoreCRDs = []string{
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
		// The ClusterClass controller is wired whenever ClusterTopology is on,
		// which is this project's default, and it watches this type. Publishing
		// it is not optional for the same reason the rest of this list is not:
		// an unserved watched type hangs the controller rather than skipping it.
		"core/config/crd/bases/cluster.x-k8s.io_clusterclasses.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
		// Read by the topology reconciler on every reconcile of a managed
		// topology, whatever the MachinePool gate says. Published, not enabled.
		"core/config/crd/bases/cluster.x-k8s.io_machinepools.yaml",
	}
	coreReconcilerDevCRDs = []string{
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
		// Both templates, because a ClusterClass names one of each: nothing
		// watches or reconciles them, and the topology controller reads them to
		// stamp the DevCluster and each Machine's DevMachine.
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclustertemplates.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachinetemplates.yaml",
	}
	coreReconcilerBootstrapCRDs = []string{
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigs.yaml",
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigtemplates.yaml",
	}
	coreReconcilerControlPlaneCRDs = []string{
		"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanes.yaml",
		"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanetemplates.yaml",
	}
)

// What one workspace's worth of the core deployment watches, measured from a
// run's own stream inventory and handler accounting rather than counted off
// the source by eye.
//
// The two numbers differ, and the difference is the point. Four published
// types are watched — Cluster, Machine, MachineSet and MachineDeployment;
// MachineHealthCheck is published because it must be bound for the controllers
// to start, but nothing here watches it. More handlers than types are
// registered against those informers, because five controllers each register
// their own and several watch the same type. It is the registrations, not the
// types, that decide what a departing workspace fails to give back.
const (
	coreReconcilerWatchedTypes  = 4
	coreReconcilerEventHandlers = 12
)

// TestCoreDeploymentWorkspaceSweep measures what one deployment pays per
// workspace: the reconciler set cmd/core-manager wires, and only that —
// ClusterCache, Cluster, Machine, MachineSet and MachineDeployment.
//
// One deployment, because that is the unit now. Each Cluster API provider runs
// as its own process against its own APIExport, so a workspace is engaged once
// per deployment and the fleet's cost is the sum. This measures the largest of
// those terms; TestFleetWorkspaceSweep measures every provider's controllers
// together, which bounds the sum from the other side.
//
// It differs from TestActiveWorkspaceSweep in the workload and nothing else:
// same instrument, same settling, same assertions. The point of running both
// is that the difference between them is attributable. One controller on one
// type is what the wiring costs; this is what the wiring plus a real provider
// costs, and only the second number sizes a deployment.
//
// # The set is wired once, not once per workspace
//
// It used to be per workspace, and this test used to say so. It is not any
// more: every controller serves every workspace, and each resolves the
// workspace from the context of the reconcile it is running. So the wiring is
// installed once, before the manager starts, and what the per-workspace
// columns of this report measure is what a workspace adds to a process already
// serving others — which is the number that sizes a shard.
//
// # Why the in-memory backend
//
// The dev provider has two backends, and the choice here is deliberate. The
// docker backend provisions real containers per cluster, which would make this
// measure Docker's cost and image pulls rather than the manager's, need a
// container runtime everywhere it runs, and take minutes per workspace. The
// in-memory backend runs the same reconcilers through the same code paths
// against an in-process workload cluster, so what is measured is the
// controller machinery per workspace, which is the thing that multiplies.
//
// coremanager.NewDevInfrastructure still constructs a Docker client, because
// the reconcilers take one whichever backend an object selects. Constructing
// it does not contact a daemon — moby's client.New(FromEnv) is lazy — so this
// sweep runs without a container runtime, and no object here selects the
// docker backend.
func TestCoreDeploymentWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))
	must(t, infrav1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (the core provider's deployment)",
		reportName: "sweep-report-core",
		exportName: "cluster-api-sweep-core",

		// Its own environment variables, and smaller defaults. Five
		// controllers per workspace against a real provider is minutes of
		// wall clock at the widths the single-type sweep runs at, and sharing
		// SWEEP_WORKSPACES would mean widening the cheap sweep silently
		// widened this one too.
		workspacesEnv:     "SWEEP_CORE_WORKSPACES",
		objectsEnv:        "SWEEP_CORE_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  coreReconcilerWatchedTypes,
		eventHandlers: coreReconcilerEventHandlers,
		facts: map[string]string{
			"shape":           "coremanager.SetupCoreControllers: ClusterCache, Cluster, Machine, MachineSet, MachineDeployment — one controller each for the whole shard",
			"deploymentName":  "core-manager",
			"deployment":      "core-manager, one of four provider deployments",
			"reconciledTypes": "cluster.x-k8s.io/clusters",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			// Resolved from the pinned Cluster API modules rather than copied
			// here, so they cannot disagree with the version this compiles
			// against.
			paths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreReconcilerCoreCRDs...)
			must(t, err)
			return paths
		},
		crdTransform: keepStorageVersion,

		// The index the topology controllers list through, on the cache they
		// read through. See coremanager.FleetCacheIndexes.
		cacheIndexes: coremanager.FleetCacheIndexes,

		// Nothing is wired per workspace any more, so this contributes no
		// reconcilers. The seam stays registered because engagement is what the
		// sweep waits on before measuring a point, and because a deployment
		// still registers it — for the engagement telemetry, which is all it
		// does now.
		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry) {
			t.Helper()

			// Process-global, installed once: the contract-metadata and
			// conversion resolvers the core reconcilers need to resolve
			// contract-versioned cross-references against a type that a
			// workspace consumes via APIBinding. See ADR-0001's "Known gaps".
			coremanager.SetupProcessGlobals()

			// Kept for the diagnostic below, which needs to ask the two caches
			// what they can see.
			fleetManager = mgr

			// The production wiring itself, unmodified — not a
			// reimplementation of it. Anything this sweep measures that a
			// deployment would not pay would make the numbers a fiction.
			fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
				// The shard, not the manager's config: the ClusterCache reads
				// kubeconfig Secrets, which live in the workspaces themselves
				// and not in the virtual workspace the manager addresses.
				ShardConfig: shardCfg,

				// Two shapes in one test binary means two ClusterCaches in one
				// process, which controller-runtime's global controller-name
				// registry rejects. A deployment builds one; only a co-located
				// process like this one, or the demo, needs the exemption.
				SkipControllerNameValidation: true,
			})
			must(t, err)
			// Nil dev infrastructure: this is the core provider's deployment,
			// and that provider is a deployment of its own now.
			must(t, coremanager.SetupCoreControllers(ctx, mgr, fleet, nil))
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				// A Cluster alone: the infrastructure provider is a separate
				// deployment, so this process has nothing that would act on a
				// DevCluster, and creating one would measure an object nobody
				// reconciles.
				//
				// Paused is set explicitly, and not for its meaning: ClusterSpec
				// is tagged omitzero, so an entirely zero spec is omitted from
				// the serialised object and the server rejects it with
				// "spec: Required value".
				cluster := &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{Name: objectName(tn, n), Namespace: "default"},
					Spec:       clusterv1.ClusterSpec{Paused: ptr.To(false)},
				}
				if err := tn.directClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %s in workspace %s: %v", cluster.Name, tn.name, err)
				}
			}
		},

		// Active means the Cluster reconciler ran for this workspace and its
		// write landed here: the finalizer is the first thing it adds to every
		// Cluster, and it is this deployment's work rather than another's.
		//
		// Provisioned infrastructure is not available as a signal any more, and
		// that is the split showing through: no single deployment takes a
		// cluster from created to provisioned.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				var cluster clusterv1.Cluster
				key := client.ObjectKey{Namespace: "default", Name: objectName(tn, n)}
				if err := tn.directClient.Get(ctx, key, &cluster); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading Cluster %s in workspace %s: %v", key.Name, tn.name, err)
				}
				if !slices.Contains(cluster.Finalizers, clusterv1.ClusterFinalizer) {
					return false
				}
			}
			return true
		},

		// What the chain looked like when it stopped. Owner references first:
		// the DevCluster reconciler refuses to act until the Cluster reconciler
		// has claimed it, so their absence and their presence point at
		// different halves of the chain.
		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				key := client.ObjectKey{Namespace: "default", Name: objectName(tn, n)}

				var cluster clusterv1.Cluster
				if err := tn.directClient.Get(ctx, key, &cluster); err != nil {
					t.Logf("diagnose: reading Cluster %s in %s: %v", key.Name, tn.name, err)
				} else {
					t.Logf("diagnose: Cluster %s in %s: rv=%s infraRef=%s/%s initialization=%+v conditions=%s",
						key.Name, tn.name, cluster.ResourceVersion,
						cluster.Spec.InfrastructureRef.Kind, cluster.Spec.InfrastructureRef.Name,
						cluster.Status.Initialization, conditionSummary(cluster.Status.Conditions))
				}

				var devCluster infrav1.DevCluster
				if err := tn.directClient.Get(ctx, key, &devCluster); err != nil {
					t.Logf("diagnose: reading DevCluster %s in %s: %v", key.Name, tn.name, err)
					continue
				}
				owners := make([]string, 0, len(devCluster.OwnerReferences))
				for _, o := range devCluster.OwnerReferences {
					owners = append(owners, fmt.Sprintf("%s/%s(uid=%s)", o.Kind, o.Name, o.UID))
				}
				t.Logf("diagnose: DevCluster %s in %s: rv=%s owners=%v initialization=%+v conditions=%s",
					key.Name, tn.name, devCluster.ResourceVersion, owners,
					devCluster.Status.Initialization, conditionSummary(devCluster.Status.Conditions))

				cacheViews(t, ctx, tn, key)
			}
		},
	})
}

// objectName names the Cluster and DevCluster a workspace holds.
//
// The same name in every workspace by default, and that is the point: identical
// names are how a cross-workspace confusion becomes visible rather than
// plausible, and every tenancy assertion in this sweep rests on it.
//
// SWEEP_CORE_UNIQUE_NAMES=1 makes them unique instead, which is a diagnostic
// rather than a mode. The dev provider's in-memory backend used to key its
// process-global workload-cluster listeners by namespace and name alone, so
// every workspace's default/sweep-00 shared one listener on one port — one
// tenant's control plane serving another's, which is a collision that works
// rather than one that fails. The fork keys them by management cluster as well
// (workloadClusterKey, carried in DRIFT.md), so that is fixed; this knob stays
// because it is how a name collision is told apart from a fault in the
// workspace-aware wiring. A failure that survives unique names is not a
// collision.
func objectName(tn *tenant, n int) string {
	if os.Getenv("SWEEP_CORE_UNIQUE_NAMES") == "1" {
		return fmt.Sprintf("sweep-%s-%02d", strings.ToLower(string(tn.name)), n)
	}
	return fmt.Sprintf("sweep-%02d", n)
}

// fleetManager is the manager the wiring was installed on, kept so the
// diagnostic can ask each cache what it sees.
//
// A package-level variable because the sweep harness hands the diagnostic a
// tenant and nothing else, and this is a diagnostic rather than part of the
// measurement. One sweep runs at a time.
var fleetManager mcmanager.Manager

// cacheViews reports what each of the process's two views of an object says
// about it, against the workspace's own API server.
//
// The three are meant to agree and are not guaranteed to. Watches are
// registered on the local manager's wildcard cache; reads through the
// cluster-aware client go to the provider's, which is a different informer over
// the same endpoint with its own lag. A reconcile woken by one and reading the
// other can act on state older than the event that woke it — and because the
// event is consumed, nothing re-fires. This prints all three so that a
// disagreement is visible rather than inferred.
func cacheViews(t *testing.T, ctx context.Context, tn *tenant, key client.ObjectKey) {
	t.Helper()

	if fleetManager == nil {
		return
	}

	var direct infrav1.DevCluster
	if err := tn.directClient.Get(ctx, key, &direct); err != nil {
		t.Logf("diagnose:   API server: %v", err)
	} else {
		t.Logf("diagnose:   API server:            rv=%s deletionTimestamp=%v owners=%d finalizers=%v",
			direct.ResourceVersion, direct.DeletionTimestamp, len(direct.OwnerReferences), direct.Finalizers)
	}

	if cl, err := fleetManager.GetCluster(ctx, tn.name); err != nil {
		t.Logf("diagnose:   provider cache: %v", err)
	} else {
		var scoped infrav1.DevCluster
		if err := cl.GetClient().Get(ctx, key, &scoped); err != nil {
			t.Logf("diagnose:   provider cache (reads):  %v", err)
		} else {
			t.Logf("diagnose:   provider cache (reads):  rv=%s deletionTimestamp=%v owners=%d finalizers=%v",
				scoped.ResourceVersion, scoped.DeletionTimestamp, len(scoped.OwnerReferences), scoped.Finalizers)
		}
	}

	// The local manager's cache spans every workspace, so it is asked for the
	// whole fleet and filtered rather than keyed.
	//
	// The count is the point, not the match. A controller-runtime cache keys its
	// store with MetaNamespaceKeyFunc, which has no room for a logical cluster —
	// so if this cache is not kcp-aware, every workspace's default/sweep-00
	// occupies one entry and overwrites the last. That would deliver events
	// naming the wrong workspace and lose the rest, which is a fault that looks
	// exactly like a lost event. One entry per workspace says the store keys are
	// cluster-aware; fewer says they are not.
	var all infrav1.DevClusterList
	if err := fleetManager.GetLocalManager().GetCache().List(ctx, &all); err != nil {
		t.Logf("diagnose:   local cache (watches):   %v", err)
		return
	}
	byCluster := map[string]int{}
	for i := range all.Items {
		byCluster[logicalcluster.From(&all.Items[i]).String()]++
	}
	t.Logf("diagnose:   local cache (watches):   %d DevClusters across %d logical clusters %v",
		len(all.Items), len(byCluster), byCluster)
	for i := range all.Items {
		item := &all.Items[i]
		if logicalcluster.From(item).String() != string(tn.name) || item.Name != key.Name || item.Namespace != key.Namespace {
			continue
		}
		t.Logf("diagnose:   local cache (watches):   rv=%s deletionTimestamp=%v owners=%d finalizers=%v",
			item.ResourceVersion, item.DeletionTimestamp, len(item.OwnerReferences), item.Finalizers)
	}
}

func conditionSummary(conditions []metav1.Condition) string {
	if len(conditions) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(conditions))
	for _, c := range conditions {
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", c.Type, c.Status, c.Reason))
	}
	return strings.Join(parts, ",")
}
