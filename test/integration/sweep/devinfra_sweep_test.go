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

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/fleetfixture"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// What one workspace's worth of the dev infrastructure deployment watches.
//
// Four types — DevCluster, DevMachine, Cluster, Machine — and six handler
// registrations across them: the DevCluster controller registers two
// (DevCluster, Cluster) and the DevMachine controller four (DevMachine,
// Machine, DevCluster, Cluster).
const (
	devReconcilerWatchedTypes  = 4
	devReconcilerEventHandlers = 6
)

// TestDevInfrastructureDeploymentWorkspaceSweep measures what the dev
// infrastructure provider's deployment pays per workspace: the DevCluster and
// DevMachine controllers cmd/dev-infrastructure-manager wires, and only those.
//
// # A deployment measured alone reconciles a workload nobody else wrote
//
// This provider cannot act on its own. A DevCluster is reconciled only once
// the core provider has stamped an owner reference onto it, and core is a
// different deployment which is not running here — so the sweep writes that
// owner reference itself, standing in for the deployment it is isolating this
// one from.
//
// That is the price of a per-deployment figure, and it is worth being exact
// about what it costs. The terms that scale — goroutines, watch streams,
// discovery requests per workspace — are exactly what a deployment pays,
// because they come from the engagement and the wiring rather than from who
// wrote the objects. The request count is representative rather than exact: a
// real installation's core provider would write the same owner reference, and
// this process is not billed for it.
func TestDevInfrastructureDeploymentWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))
	must(t, infrav1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (the dev infrastructure provider's deployment)",
		reportName: "sweep-report-dev",
		exportName: "cluster-api-sweep-dev",

		workspacesEnv:     "SWEEP_DEV_WORKSPACES",
		objectsEnv:        "SWEEP_DEV_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  devReconcilerWatchedTypes,
		eventHandlers: devReconcilerEventHandlers,
		facts: map[string]string{
			"deploymentName":    "dev-infrastructure-manager",
			"deployment":        "dev-infrastructure-manager, one of four provider deployments",
			"shape":             "coremanager.SetupDevInfrastructureControllers: DevCluster, DevMachine — one controller each for the whole shard",
			"reconciledTypes":   "infrastructure.cluster.x-k8s.io/devclusters",
			"devClusterBackend": "inMemory",
			"endState":          "infrastructure provisioned",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			// Core's types as well as its own: both reconcilers watch Cluster
			// and the DevMachine one watches Machine, and a controller whose
			// source cannot sync never starts.
			core, err := fleetfixture.CoreModulePaths(fleetfixture.CoreCRDs)
			must(t, err)
			dev, err := fleetfixture.DevModulePaths(fleetfixture.DevCRDs)
			must(t, err)
			return append(core, dev...)
		},
		crdTransform: keepStorageVersion,

		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry) {
			t.Helper()

			coremanager.SetupProcessGlobals()

			dev, err := coremanager.NewDevInfrastructure(ctx, "127.0.0.1", muxPorts(t))
			must(t, err)

			fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
				ShardConfig:                  shardCfg,
				SkipControllerNameValidation: true,
			})
			must(t, err)

			// This deployment's wiring, and no core reconcilers: the whole
			// point of the shape is that a DevCluster here is acted on by one
			// provider's controllers.
			must(t, coremanager.SetupDevInfrastructureControllers(ctx, mgr, fleet, dev))
		},

		// A Cluster and the DevCluster it points at, with the owner reference
		// core would have written already in place.
		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				cluster := bareCluster(name)
				if err := tn.directClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %s in workspace %s: %v", name, tn.name, err)
				}
				// Read back for the UID: an owner reference without one is
				// rejected, and the whole point of writing it here is that it
				// is the reference core would have written.
				if err := tn.directClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
					t.Fatalf("reading back Cluster %s in workspace %s: %v", name, tn.name, err)
				}

				devCluster := newDevCluster(name)
				devCluster.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
					Controller: ptr.To(true),
				}}
				if err := tn.directClient.Create(ctx, devCluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating DevCluster %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		// Active means this deployment did its work in this workspace and the
		// result landed here: the in-memory workload cluster is up and the
		// DevCluster says so.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				var devCluster infrav1.DevCluster
				key := client.ObjectKey{Namespace: demo.Namespace, Name: objectName(tn, n)}
				if err := tn.directClient.Get(ctx, key, &devCluster); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading DevCluster %s in workspace %s: %v", key.Name, tn.name, err)
				}
				if !ptr.Deref(devCluster.Status.Initialization.Provisioned, false) {
					return false
				}
			}
			return true
		},

		// The DevCluster keeps a finalizer, so it has to go before the
		// APIBinding does — see sweepConfig.deactivate.
		deactivate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)
				devCluster := &infrav1.DevCluster{ObjectMeta: metav1.ObjectMeta{Namespace: demo.Namespace, Name: name}}
				if err := tn.directClient.Delete(ctx, devCluster); err != nil && !apierrors.IsNotFound(err) {
					t.Fatalf("deleting DevCluster %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		deactivated: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			var devClusters infrav1.DevClusterList
			if err := tn.directClient.List(ctx, &devClusters, client.InNamespace(demo.Namespace)); err != nil {
				t.Fatalf("listing DevClusters in workspace %s: %v", tn.name, err)
			}
			return len(devClusters.Items) == 0
		},

		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				key := client.ObjectKey{Namespace: demo.Namespace, Name: objectName(tn, n)}

				var devCluster infrav1.DevCluster
				if err := tn.directClient.Get(ctx, key, &devCluster); err != nil {
					t.Logf("diagnose: reading DevCluster %s in %s: %v", key.Name, tn.name, err)
					continue
				}
				t.Logf("diagnose: DevCluster %s in %s: owners=%d finalizers=%v endpoint=%+v initialization=%+v conditions=%s",
					key.Name, tn.name, len(devCluster.OwnerReferences), devCluster.Finalizers,
					devCluster.Spec.ControlPlaneEndpoint, devCluster.Status.Initialization,
					conditionSummary(devCluster.Status.Conditions))
			}
		},
	})
}

// muxPorts picks ports for one shape's in-memory workload cluster mux.
//
// Two shapes in one test binary stand up two muxes, and the upstream defaults
// are fixed numbers — so without this the second shape to run fails to bind
// rather than measuring anything. Binding :0 and reading the port back is the
// same trick the demo uses, and it is racy only against something that grabs
// the port in the microseconds after the probe closes it.
func muxPorts(t *testing.T) inmemoryserver.CustomPorts {
	t.Helper()
	ports, err := fleetfixture.MuxPorts(0)
	must(t, err)
	return ports
}
