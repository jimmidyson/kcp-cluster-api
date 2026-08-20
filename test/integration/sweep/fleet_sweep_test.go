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
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/bootstrapmanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// What one workspace's worth of every provider's controllers watches. Declared
// rather than inferred, so a shape whose wiring changed underneath the number
// fails rather than reporting the new shape under the old label.
const (
	fleetWatchedTypes  = 8
	fleetEventHandlers = 27
)

// TestFleetWorkspaceSweep measures what every provider's controllers together
// cost per workspace: core, bootstrap, control plane and dev infrastructure,
// taking a cluster all the way to an initialized control plane.
//
// # What this is, and what it is not
//
// It is the upper bound on the controller half of a fleet's per-workspace cost,
// and the only shape that reaches the end state a person cares about - a
// control plane that answers.
//
// It is not a deployment. Cluster API deploys one process per provider, and
// this wires all four onto one fleet in one process, so it pays *one*
// engagement per workspace where four deployments pay four, and shares one
// ClusterCache where four deployments have one each. Read it with
// TestCoreDeploymentWorkspaceSweep, which measures a single deployment: the
// fleet's real cost per workspace is each deployment's engagement plus its own
// controllers, and these two bound that from either side.
//
// The in-memory backend for the same reason the core sweep gives: the docker
// backend would measure Docker rather than the manager.
func TestFleetWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))
	must(t, bootstrapv1.AddToScheme(scheme))
	must(t, controlplanev1.AddToScheme(scheme))
	must(t, infrav1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (every provider's controllers, one process)",
		reportName: "sweep-report-fleet",
		exportName: "cluster-api-sweep-fleet",

		// Smaller defaults than the core sweep's: every workspace here runs a
		// control plane to completion, which is the slowest thing this
		// repository measures.
		//
		// Three rather than two, though, because two is the one count at which
		// the retention figure cannot be checked against itself. Retention is
		// measured by comparing a teardown sample with the sample taken at the
		// same workspace count on the way up, so two workspaces give exactly
		// one such pair and the figure is a single subtraction of two
		// integers — which is how a transient at either end came to be
		// reported as retention three times over. Three gives two independent
		// pairs, and a shape that genuinely retains per departure retains it
		// in both.
		workspacesEnv:     "SWEEP_FLEET_WORKSPACES",
		objectsEnv:        "SWEEP_FLEET_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  fleetWatchedTypes,
		eventHandlers: fleetEventHandlers,
		facts: map[string]string{
			"shape":             "every provider's controllers on one fleet: core, bootstrap, control plane, dev infrastructure",
			"deployment":        "none — four deployments co-located, so one engagement per workspace rather than four",
			"reconciledTypes":   "clusterclasses, clusters, machines, machinesets, machinedeployments, kubeadmconfigs, kubeadmcontrolplanes, devclusters, devmachines",
			"clusterShape":      "ClusterClass based: one class per workspace, each Cluster naming it",
			"devClusterBackend": "inMemory",
			"endState":          "control plane initialized",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			core, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI,
				append(append(append([]string{}, coreReconcilerCoreCRDs...),
					coreReconcilerBootstrapCRDs...), coreReconcilerControlPlaneCRDs...)...)
			must(t, err)
			dev, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest, coreReconcilerDevCRDs...)
			must(t, err)
			return append(core, dev...)
		},
		crdTransform: keepStorageVersion,

		// The index the topology controllers list through, on the cache they
		// read through. See coremanager.FleetCacheIndexes.
		cacheIndexes: coremanager.FleetCacheIndexes,

		// The bootstrap and control plane providers write Secrets and
		// ConfigMaps. Without these claims they write nothing, and the sweep
		// would measure workspaces that never became active.
		permissionClaims: demo.PermissionClaims,

		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry) {
			t.Helper()

			coremanager.SetupProcessGlobals()

			fleetManager = mgr

			debugPort, minPort, maxPort := muxPorts(t)
			dev, err := coremanager.NewDevInfrastructure(ctx, "127.0.0.1",
				inmemoryserver.CustomPorts{MinPort: minPort, MaxPort: maxPort, DebugPort: debugPort})
			must(t, err)

			// One fleet for all four, which is what makes this a bound rather
			// than a deployment: four processes would build four of these.
			fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
				ShardConfig: shardCfg,

				// See the core sweep: two shapes in one test binary build two
				// ClusterCaches, and controller-runtime's controller-name
				// registry is process-global.
				SkipControllerNameValidation: true,
			})
			must(t, err)

			must(t, coremanager.SetupCoreControllers(ctx, mgr, fleet, dev))
			must(t, bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{}))
			must(t, controlplanemanager.SetupFleetControllers(ctx, mgr, fleet, controlplanemanager.Options{}))
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			// The blueprint once per workspace, then one Cluster per object.
			// This is the demo's own class and the demo's own Cluster: what
			// this shape measures is what an installation pays for a
			// ClusterClass based cluster, and building a different sort of
			// cluster here would measure something nobody deploys.
			for _, obj := range demo.Blueprint(demo.BackendInMemory) {
				if err := tn.directClient.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating %T %s in workspace %s: %v", obj, obj.GetName(), tn.name, err)
				}
			}
			for n := range objects {
				name := objectName(tn, n)
				// No workers: this shape's end state is an initialized control
				// plane, and a worker pool would add a MachineDeployment's
				// worth of reconciling to every point of the sweep without
				// moving that end state.
				cluster := demo.NewCluster(name, 1, 0, demo.DefaultKubernetesVersion)
				if err := tn.directClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		// Active means the control plane answers: the whole chain ran in this
		// workspace, across every provider, and what it wrote landed here.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				var kcp controlplanev1.KubeadmControlPlane
				key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ControlPlaneName(objectName(tn, n))}
				if err := tn.directClient.Get(ctx, key, &kcp); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading KubeadmControlPlane %s in workspace %s: %v", key.Name, tn.name, err)
				}
				if !ptr.Deref(kcp.Status.Initialization.ControlPlaneInitialized, false) {
					return false
				}
			}
			return true
		},

		// Winding down means deleting the Cluster and letting Cluster API's own
		// teardown run: control plane, then Machines, then the infrastructure
		// each one owns. The alternative — leaving the objects for the
		// APIBinding's deletion to remove — deadlocks; see sweepConfig's
		// deactivate for why.
		deactivate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)
				cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: demo.Namespace, Name: name}}
				if err := tn.directClient.Delete(ctx, cluster); err != nil && !apierrors.IsNotFound(err) {
					t.Fatalf("deleting Cluster %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		// Clear means the Cluster is gone and nothing it owned outlived it: a
		// Machine or a DevCluster still holding a finalizer would hold the
		// APIBinding's deletion too, and the sweep would time out on the
		// disengage rather than here, where the diagnostic says which object.
		deactivated: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				var cluster clusterv1.Cluster
				err := tn.directClient.Get(ctx, client.ObjectKey{Namespace: demo.Namespace, Name: name}, &cluster)
				if err == nil {
					return false
				}
				if !apierrors.IsNotFound(err) {
					t.Fatalf("reading Cluster %s in workspace %s: %v", name, tn.name, err)
				}
			}

			var machines clusterv1.MachineList
			if err := tn.directClient.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
				t.Fatalf("listing Machines in workspace %s: %v", tn.name, err)
			}
			if len(machines.Items) > 0 {
				return false
			}

			var devClusters infrav1.DevClusterList
			if err := tn.directClient.List(ctx, &devClusters, client.InNamespace(demo.Namespace)); err != nil {
				t.Fatalf("listing DevClusters in workspace %s: %v", tn.name, err)
			}
			return len(devClusters.Items) == 0
		},

		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				var cluster clusterv1.Cluster
				key := client.ObjectKey{Namespace: demo.Namespace, Name: name}
				if err := tn.directClient.Get(ctx, key, &cluster); err != nil {
					t.Logf("diagnose: reading Cluster %s in %s: %v", name, tn.name, err)
				} else {
					t.Logf("diagnose: Cluster %s in %s: initialization=%+v conditions=%s",
						name, tn.name, cluster.Status.Initialization, conditionSummary(cluster.Status.Conditions))
				}

				var kcp controlplanev1.KubeadmControlPlane
				cpKey := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ControlPlaneName(name)}
				if err := tn.directClient.Get(ctx, cpKey, &kcp); err != nil {
					t.Logf("diagnose: reading KubeadmControlPlane %s in %s: %v", cpKey.Name, tn.name, err)
					continue
				}
				t.Logf("diagnose: KubeadmControlPlane %s in %s: initialization=%+v conditions=%s",
					cpKey.Name, tn.name, kcp.Status.Initialization, conditionSummary(kcp.Status.Conditions))

				var machines clusterv1.MachineList
				if err := tn.directClient.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
					t.Logf("diagnose: listing Machines in %s: %v", tn.name, err)
					continue
				}
				for i := range machines.Items {
					t.Logf("diagnose: Machine %s in %s: phase=%s conditions=%s",
						machines.Items[i].Name, tn.name, machines.Items[i].Status.Phase,
						conditionSummary(machines.Items[i].Status.Conditions))
				}
			}
		},
	})
}
