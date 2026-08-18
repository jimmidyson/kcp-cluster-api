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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
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
		"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
	}
	coreReconcilerDevCRDs = []string{
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
	}
)

// What one workspace's worth of this reconciler set watches, measured from a
// run's own stream inventory and handler accounting rather than counted off
// the source by eye.
//
// The two numbers differ, and the difference is the point. Six published types
// are watched — Cluster, Machine, MachineSet, MachineDeployment, DevCluster
// and DevMachine; MachineHealthCheck is published because it must be bound for
// the controllers to start, but nothing here watches it. Fifteen event-handler
// registrations are made against those six informers, because five controllers
// each register their own handlers, several of them on the same type. It is
// the registrations, not the types, that decide what a departing workspace
// fails to give back.
const (
	coreReconcilerWatchedTypes  = 6
	coreReconcilerEventHandlers = 15
)

// TestCoreReconcilerWorkspaceSweep measures what a deployment actually pays:
// the whole reconciler set cmd/core-manager wires — ClusterCache, the core
// Cluster and Machine reconcilers, and the dev infrastructure provider's
// DevCluster and DevMachine reconcilers.
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
func TestCoreReconcilerWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))
	must(t, infrav1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (core-manager reconciler set, in-memory backend)",
		reportName: "sweep-report-coremanager",
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
			"shape":             "coremanager.SetupReconcilers: ClusterCache, Cluster, Machine, DevCluster, DevMachine",
			"reconciledTypes":   "cluster.x-k8s.io/clusters + infrastructure.cluster.x-k8s.io/devclusters",
			"devClusterBackend": "inMemory",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			// Resolved from the pinned Cluster API modules rather than copied
			// here, so they cannot disagree with the version this compiles
			// against.
			paths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreReconcilerCoreCRDs...)
			must(t, err)
			devPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest, coreReconcilerDevCRDs...)
			must(t, err)
			return append(paths, devPaths...)
		},
		crdTransform: keepStorageVersion,

		// Nothing is wired per workspace any more, so this contributes no
		// reconcilers. The seam stays registered because engagement is what the
		// sweep waits on before measuring a point, and because a deployment
		// still registers it — for the engagement telemetry, which is all it
		// does now.
		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager) {
			t.Helper()

			// MachinePool defaults to enabled upstream, and the core
			// reconcilers watch it as an event source when it is on — which
			// would stall their cache sync here, because this sweep publishes
			// the ADR-0001 D3 scope and MachinePool is not in it.
			must(t, feature.MutableGates.Set("MachinePool=false"))

			// Process-global, installed once: the contract-metadata and
			// conversion resolvers the core reconcilers need to resolve
			// contract-versioned cross-references against a type that a
			// workspace consumes via APIBinding. See ADR-0001's "Known gaps".
			coremanager.SetupProcessGlobals()

			dev, err := coremanager.NewDevInfrastructure(ctx)
			must(t, err)

			// The production wiring itself, unmodified — not a
			// reimplementation of it. Anything this sweep measures that a
			// deployment would not pay would make the numbers a fiction.
			must(t, coremanager.SetupFleetControllers(ctx, mgr, dev, coremanager.SetupOptions{}))
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := fmt.Sprintf("sweep-%02d", n)

				// The infrastructure object first: the Cluster reconciler
				// resolves spec.infrastructureRef and sets an owner reference
				// on what it finds, which is what starts the DevCluster
				// reconciler working.
				devCluster := &infrav1.DevCluster{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: infrav1.DevClusterSpec{
						Backend: infrav1.DevClusterBackendSpec{
							InMemory: &infrav1.InMemoryClusterBackendSpec{},
						},
					},
				}
				if err := tn.directClient.Create(ctx, devCluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating DevCluster %s in workspace %s: %v", name, tn.name, err)
				}

				cluster := &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
					Spec: clusterv1.ClusterSpec{
						InfrastructureRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: infrav1.GroupVersion.Group,
							Kind:     "DevCluster",
							Name:     name,
						},
					},
				}
				if err := tn.directClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		// Active means provisioned, not merely created. Waiting for the
		// infrastructure to come up is what proves the whole chain ran in this
		// workspace — the Cluster reconciler resolved a contract-versioned
		// reference, the DevCluster reconciler acted on it, and the status it
		// wrote landed back in this workspace and not another.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				var cluster clusterv1.Cluster
				key := client.ObjectKey{Namespace: "default", Name: fmt.Sprintf("sweep-%02d", n)}
				if err := tn.directClient.Get(ctx, key, &cluster); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading Cluster %s in workspace %s: %v", key.Name, tn.name, err)
				}
				provisioned := cluster.Status.Initialization.InfrastructureProvisioned
				if provisioned == nil || !*provisioned {
					return false
				}
			}
			return true
		},
	})
}
