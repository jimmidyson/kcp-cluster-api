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
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/feature"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// What one workspace's worth of the kubeadm bootstrap deployment watches.
//
// Three types — KubeadmConfig, Machine, Cluster — one handler registration
// each, all from the one KubeadmConfig controller.
const (
	bootstrapReconcilerWatchedTypes  = 3
	bootstrapReconcilerEventHandlers = 3
)

// TestBootstrapDeploymentWorkspaceSweep measures what the kubeadm bootstrap
// provider's deployment pays per workspace: the KubeadmConfig controller
// cmd/kubeadm-bootstrap-manager wires, and only that.
//
// The workload is one control plane machine's worth of bootstrap: a Cluster
// whose infrastructure is provisioned, a control plane Machine, and the
// KubeadmConfig it points at. That is this provider's expensive path — it
// generates the cluster's certificates and writes the bootstrap data secret —
// rather than its cheapest.
//
// As in the dev infrastructure sweep, the objects the other deployments would
// have written are written here instead: core creates the Machine and the
// infrastructure provider sets the Cluster provisioned. See that sweep for
// what a synthetic driver does and does not change about the numbers.
func TestBootstrapDeploymentWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))
	must(t, bootstrapv1.AddToScheme(scheme))

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (the kubeadm bootstrap provider's deployment)",
		reportName: "sweep-report-bootstrap",
		exportName: "cluster-api-sweep-bootstrap",

		workspacesEnv:     "SWEEP_BOOTSTRAP_WORKSPACES",
		objectsEnv:        "SWEEP_BOOTSTRAP_OBJECTS",
		defaultWorkspaces: 3,
		defaultObjects:    1,

		watchedTypes:  bootstrapReconcilerWatchedTypes,
		eventHandlers: bootstrapReconcilerEventHandlers,
		facts: map[string]string{
			"deploymentName":  "kubeadm-bootstrap-manager",
			"deployment":      "kubeadm-bootstrap-manager, one of four provider deployments",
			"shape":           "bootstrapmanager.SetupFleetControllers: KubeadmConfig — one controller for the whole shard",
			"reconciledTypes": "bootstrap.cluster.x-k8s.io/kubeadmconfigs",
			"endState":        "bootstrap data secret written",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			paths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI,
				append(append([]string{}, coreReconcilerCoreCRDs...), coreReconcilerBootstrapCRDs...)...)
			must(t, err)
			return paths
		},
		crdTransform: keepStorageVersion,

		// This provider's output is a Secret, and its init lock is a ConfigMap.
		// Without the claims it writes nothing and the sweep would measure
		// workspaces that never became active.
		permissionClaims: demo.PermissionClaims,

		newSetup: func(*testing.T, context.Context) providerwiring.SetupFunc {
			return func(context.Context, multicluster.ClusterName, manager.Manager) error { return nil }
		},

		newFleetSetup: func(t *testing.T, ctx context.Context, mgr mcmanager.Manager, shardCfg *rest.Config, registry *capicontrollerutil.WildcardRegistry) {
			t.Helper()

			must(t, feature.MutableGates.Set("MachinePool=false"))
			coremanager.SetupProcessGlobals()

			fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
				ShardConfig:                  shardCfg,
				SkipControllerNameValidation: true,
			})
			must(t, err)

			must(t, bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{}))
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				name := objectName(tn, n)

				// A Cluster the infrastructure provider has already finished
				// with: an endpoint in the spec and provisioned in the status,
				// which is the state this provider waits for.
				cluster := &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: demo.Namespace},
					Spec: clusterv1.ClusterSpec{
						Paused:               ptr.To(false),
						ControlPlaneEndpoint: clusterv1.APIEndpoint{Host: "127.0.0.1", Port: 6443},
					},
				}
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

				// The first control plane Machine. The control plane label is
				// what sends this down the init path, where the certificates
				// are generated.
				machine := &clusterv1.Machine{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name + "-cp-0",
						Namespace: demo.Namespace,
						Labels: map[string]string{
							clusterv1.ClusterNameLabel:         name,
							clusterv1.MachineControlPlaneLabel: "",
						},
					},
					Spec: clusterv1.MachineSpec{
						ClusterName: name,
						Version:     demo.DefaultKubernetesVersion,
						// Required by the schema, and never resolved: this
						// provider reads the Machine for its cluster, its
						// version and its role, and nothing here follows the
						// reference to an infrastructure provider that is not
						// running.
						InfrastructureRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: infrav1.GroupVersion.Group,
							Kind:     "DevMachine",
							Name:     name,
						},
						Bootstrap: clusterv1.Bootstrap{
							ConfigRef: clusterv1.ContractVersionedObjectReference{
								APIGroup: bootstrapv1.GroupVersion.Group,
								Kind:     "KubeadmConfig",
								Name:     name,
							},
						},
					},
				}
				if err := tn.directClient.Create(ctx, machine); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Machine %s in workspace %s: %v", machine.Name, tn.name, err)
				}
				if err := tn.directClient.Get(ctx, client.ObjectKeyFromObject(machine), machine); err != nil {
					t.Fatalf("reading back Machine %s in workspace %s: %v", machine.Name, tn.name, err)
				}

				// The config itself, owned by the Machine — the owner is how
				// this provider finds the cluster, the version and whether the
				// machine is a control plane one.
				config := &bootstrapv1.KubeadmConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: demo.Namespace,
						Labels:    map[string]string{clusterv1.ClusterNameLabel: name},
						OwnerReferences: []metav1.OwnerReference{{
							APIVersion: clusterv1.GroupVersion.String(),
							Kind:       "Machine",
							Name:       machine.Name,
							UID:        machine.UID,
							Controller: ptr.To(true),
						}},
					},
					// Format rather than nothing: KubeadmConfigSpec's fields
					// are all omitzero, and a spec that serialises to nothing
					// is rejected as "spec: Required value".
					Spec: bootstrapv1.KubeadmConfigSpec{Format: bootstrapv1.CloudConfig},
				}
				if err := tn.directClient.Create(ctx, config); err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating KubeadmConfig %s in workspace %s: %v", name, tn.name, err)
				}
			}
		},

		// Active means this deployment generated the bootstrap data for this
		// workspace's machine and the reference landed back here.
		active: func(t *testing.T, ctx context.Context, tn *tenant, objects int) bool {
			t.Helper()
			for n := range objects {
				var config bootstrapv1.KubeadmConfig
				key := client.ObjectKey{Namespace: demo.Namespace, Name: objectName(tn, n)}
				if err := tn.directClient.Get(ctx, key, &config); err != nil {
					if apierrors.IsNotFound(err) {
						return false
					}
					t.Fatalf("reading KubeadmConfig %s in workspace %s: %v", key.Name, tn.name, err)
				}
				if config.Status.DataSecretName == "" {
					return false
				}
			}
			return true
		},

		diagnose: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				key := client.ObjectKey{Namespace: demo.Namespace, Name: objectName(tn, n)}

				var config bootstrapv1.KubeadmConfig
				if err := tn.directClient.Get(ctx, key, &config); err != nil {
					t.Logf("diagnose: reading KubeadmConfig %s in %s: %v", key.Name, tn.name, err)
					continue
				}
				t.Logf("diagnose: KubeadmConfig %s in %s: owners=%d dataSecret=%q initialization=%+v conditions=%s",
					key.Name, tn.name, len(config.OwnerReferences), config.Status.DataSecretName,
					config.Status.Initialization, conditionSummary(config.Status.Conditions))
			}
		},
	})
}
