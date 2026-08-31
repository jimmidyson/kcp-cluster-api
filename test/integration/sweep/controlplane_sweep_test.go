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

	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/fleetfixture"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// What one workspace's worth of the kubeadm control plane deployment watches.
//
// Three types — KubeadmControlPlane, Machine, Cluster — one handler
// registration each, all from the one KubeadmControlPlane controller.
const (
	controlPlaneReconcilerWatchedTypes  = 3
	controlPlaneReconcilerEventHandlers = 3
)

// TestControlPlaneDeploymentWorkspaceSweep measures what the kubeadm control
// plane provider's deployment pays per workspace: the KubeadmControlPlane
// controller cmd/kubeadm-control-plane-manager wires, and only that.
//
// The workload is a one-replica control plane on a Cluster whose
// infrastructure is provisioned. What this provider then does per workspace is
// its expensive path: generate the cluster's certificate authorities and the
// admin kubeconfig, then stamp out the Machine, the infrastructure machine and
// the bootstrap config for the first replica.
//
// It stops there, and that is not a shortfall in the measurement. A control
// plane only becomes initialized once core, the bootstrap provider and an
// infrastructure provider have each done their part, and none of them is
// running here — this shape is deliberately one deployment on its own. See
// TestFleetWorkspaceSweep for the end state, and the dev infrastructure sweep
// for what a synthetic driver does and does not change about the numbers.
func TestControlPlaneDeploymentWorkspaceSweep(t *testing.T) {
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
		title:      "Active workspace sweep (the kubeadm control plane provider's deployment)",
		reportName: "sweep-report-controlplane",
		exportName: "cluster-api-sweep-controlplane",

		workspacesEnv:     "SWEEP_CONTROLPLANE_WORKSPACES",
		objectsEnv:        "SWEEP_CONTROLPLANE_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  controlPlaneReconcilerWatchedTypes,
		eventHandlers: controlPlaneReconcilerEventHandlers,
		facts: map[string]string{
			"deploymentName":  "kubeadm-control-plane-manager",
			"deployment":      "kubeadm-control-plane-manager, one of four provider deployments",
			"shape":           "controlplanemanager.SetupFleetControllers: KubeadmControlPlane — one controller for the whole shard",
			"reconciledTypes": "controlplane.cluster.x-k8s.io/kubeadmcontrolplanes",
			"endState":        "certificates written and the first replica's Machine created",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			// Everything it creates as well as everything it watches: this
			// provider writes Machines, KubeadmConfigs and infrastructure
			// machines from a template, and a type it cannot resolve fails the
			// reconcile.
			core, err := fleetfixture.CoreModulePaths(fleetfixture.CoreCRDs, fleetfixture.BootstrapCRDs, fleetfixture.ControlPlaneCRDs)
			must(t, err)
			dev, err := fleetfixture.DevModulePaths(fleetfixture.DevCRDs)
			must(t, err)
			return append(core, dev...)
		},
		crdTransform: keepStorageVersion,

		// Certificate authorities and the admin kubeconfig are Secrets.
		permissionClaims: demo.PermissionClaims,

		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry) {
			t.Helper()

			coremanager.SetupProcessGlobals()

			fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
				ShardConfig:                  shardCfg,
				SkipControllerNameValidation: true,
			})
			must(t, err)

			must(t, controlplanemanager.SetupFleetControllers(ctx, mgr, fleet, controlplanemanager.Options{}))
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				// The template the control plane stamps its machines from.
				if err := tn.directClient.Create(ctx, demo.NewDevMachineTemplate(demo.ControlPlaneMachineTemplateName, demo.BackendInMemory)); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating DevMachineTemplate %s in workspace %s: %v", demo.ControlPlaneMachineTemplateName, tn.name, err)
				}

				kcp := newKubeadmControlPlane(name, 1, demo.DefaultKubernetesVersion)
				if err := tn.directClient.Create(ctx, kcp); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating KubeadmControlPlane %s in workspace %s: %v", kcp.Name, tn.name, err)
				}

				// A Cluster the infrastructure provider has finished with,
				// pointing at that control plane. Both the endpoint and the
				// provisioned status are what core and the infrastructure
				// provider would have written.
				cluster := clusterWithControlPlane(name)
				cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "127.0.0.1", Port: 6443}
				if err := tn.directClient.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %s in workspace %s: %v", name, tn.name, err)
				}
				if err := tn.directClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
					t.Fatalf("reading back Cluster %s in workspace %s: %v", name, tn.name, err)
				}
				cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
				if err := tn.directClient.Status().Update(ctx, cluster); err != nil {
					t.Fatalf("marking Cluster %s provisioned in workspace %s: %v", name, tn.name, err)
				}

				// The owner reference core writes, which is what this provider
				// waits for before it acts.
				if err := tn.directClient.Get(ctx, client.ObjectKeyFromObject(kcp), kcp); err != nil {
					t.Fatalf("reading back KubeadmControlPlane %s in workspace %s: %v", kcp.Name, tn.name, err)
				}
				kcp.OwnerReferences = []metav1.OwnerReference{{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       cluster.Name,
					UID:        cluster.UID,
					Controller: ptr.To(true),
				}}
				if err := tn.directClient.Update(ctx, kcp); err != nil {
					t.Fatalf("setting the owner of KubeadmControlPlane %s in workspace %s: %v", kcp.Name, tn.name, err)
				}
			}
		},

		// Active means this deployment did its per-workspace work here: the
		// Machine for the first replica exists, which it only creates after
		// the certificates and the kubeconfig are written.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			var machines clusterv1.MachineList
			if err := tn.directClient.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
				t.Fatalf("listing Machines in workspace %s: %v", tn.name, err)
			}
			return len(machines.Items) >= objects
		},

		// The KubeadmControlPlane keeps a finalizer and owns Machines, so it
		// goes before the APIBinding — see sweepConfig.deactivate.
		deactivate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)
				kcp := &controlplanev1.KubeadmControlPlane{
					ObjectMeta: metav1.ObjectMeta{Namespace: demo.Namespace, Name: demo.ControlPlaneName(name)},
				}
				if err := tn.directClient.Delete(ctx, kcp); err != nil && !apierrors.IsNotFound(err) {
					t.Fatalf("deleting KubeadmControlPlane %s in workspace %s: %v", kcp.Name, tn.name, err)
				}
			}
		},

		deactivated: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			var kcps controlplanev1.KubeadmControlPlaneList
			if err := tn.directClient.List(ctx, &kcps, client.InNamespace(demo.Namespace)); err != nil {
				t.Fatalf("listing KubeadmControlPlanes in workspace %s: %v", tn.name, err)
			}
			return len(kcps.Items) == 0
		},

		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				var kcp controlplanev1.KubeadmControlPlane
				key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ControlPlaneName(name)}
				if err := tn.directClient.Get(ctx, key, &kcp); err != nil {
					t.Logf("diagnose: reading KubeadmControlPlane %s in %s: %v", key.Name, tn.name, err)
				} else {
					t.Logf("diagnose: KubeadmControlPlane %s in %s: owners=%d replicas=%d initialization=%+v conditions=%s",
						key.Name, tn.name, len(kcp.OwnerReferences), ptr.Deref(kcp.Status.Replicas, 0),
						kcp.Status.Initialization, conditionSummary(kcp.Status.Conditions))
				}

				var machines clusterv1.MachineList
				if err := tn.directClient.List(ctx, &machines, client.InNamespace(demo.Namespace)); err != nil {
					t.Logf("diagnose: listing Machines in %s: %v", tn.name, err)
					continue
				}
				t.Logf("diagnose: %d Machine(s) in %s", len(machines.Items), tn.name)
			}
		},
	})
}
