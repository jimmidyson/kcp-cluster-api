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
	"sync"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// reconcileLog records which objects each workspace's controller has
// reconciled. It is how this sweep knows a workspace is active rather than
// merely bound: until a workspace's own controller has reconciled the objects
// written into that workspace, a sample would describe a process that has not
// yet started doing the work being measured.
type reconcileLog struct {
	mu   sync.Mutex
	seen map[multicluster.ClusterName]map[string]struct{}
}

func newReconcileLog() *reconcileLog {
	return &reconcileLog{seen: map[multicluster.ClusterName]map[string]struct{}{}}
}

func (l *reconcileLog) record(workspace multicluster.ClusterName, name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[workspace] == nil {
		l.seen[workspace] = map[string]struct{}{}
	}
	l.seen[workspace][name] = struct{}{}
}

func (l *reconcileLog) count(workspace multicluster.ClusterName) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.seen[workspace])
}

// TestActiveWorkspaceSweep measures the floor: one controller, one watched
// type, a reconciler that does nothing but record that it ran.
//
// This is the shape to sweep wide. It isolates what the wiring and the shared
// cache cost per workspace from what any particular reconciler set costs, and
// it is cheap enough to run at a hundred workspaces — which is where a curve
// that bends becomes visible and four points would not have shown it.
// TestCoreReconcilerWorkspaceSweep measures the other end: everything
// cmd/core-manager actually wires.
func TestActiveWorkspaceSweep(t *testing.T) {
	scheme := k8sruntime.NewScheme()
	must(t, clientgoscheme.AddToScheme(scheme))
	must(t, apiextensionsv1.AddToScheme(scheme))
	must(t, apisv1alpha1.AddToScheme(scheme))
	must(t, apisv1alpha2.AddToScheme(scheme))
	must(t, clusterv1.AddToScheme(scheme))

	reconciled := newReconcileLog()

	runSweep(t, sweepConfig{
		title:      "Active workspace sweep (one controller, one type)",
		reportName: "sweep-report",
		exportName: "cluster-api-sweep",

		workspacesEnv:     "SWEEP_WORKSPACES",
		objectsEnv:        "SWEEP_OBJECTS",
		defaultWorkspaces: 4,
		defaultObjects:    5,

		// The one shape that still wires a controller per workspace, and the
		// only one that may retain anything on departure.
		wiresPerWorkspaceControllers: true,

		watchedTypes:  1,
		eventHandlers: 1,
		facts: map[string]string{
			"shape":           "one controller watching cluster.x-k8s.io/clusters",
			"reconciledTypes": "cluster.x-k8s.io/clusters",
		},

		scheme: scheme,
		crds: func(t *testing.T) []string {
			t.Helper()
			paths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI,
				"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml")
			must(t, err)
			return paths
		},
		crdTransform: keepStorageVersion,

		newSetup: func(t *testing.T, _ context.Context) providerwiring.SetupFunc {
			return func(_ context.Context, workspace multicluster.ClusterName, wsMgr manager.Manager) error {
				// A real controller, wired the way a provider binary wires
				// one: its watch, its workqueue, its rate limiter and its
				// goroutines are what the per-workspace cost in the report is
				// made of.
				//
				// The name is per workspace because controller-runtime records
				// controller names in a process-global set that is never
				// emptied, and SkipNameValidation is set because that set is
				// not emptied on disengagement either — a re-engaged workspace
				// would otherwise fail.
				return builder.ControllerManagedBy(wsMgr).
					For(&clusterv1.Cluster{}).
					Named(fmt.Sprintf("cluster-%s", workspace)).
					WithOptions(controller.Options{SkipNameValidation: ptr.To(true)}).
					Complete(reconcile.Func(func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
						reconciled.record(workspace, req.Name)
						return reconcile.Result{}, nil
					}))
			}
		},

		activate: func(t *testing.T, ctx context.Context, tn *tenant, objects int) {
			t.Helper()
			for n := range objects {
				err := tn.directClient.Create(ctx, &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sweep-%02d", n), Namespace: "default"},
					// Every field of ClusterSpec is optional; paused makes the
					// object valid without asking anything of an
					// infrastructure provider, which this shape does not wire.
					Spec: clusterv1.ClusterSpec{Paused: ptr.To(true)},
				})
				if err != nil && !apierrors.IsAlreadyExists(err) {
					t.Fatalf("creating Cluster %d in workspace %s: %v", n, tn.name, err)
				}
			}
		},
		active: func(_ *testing.T, _ context.Context, tn *tenant, objects int) bool {
			return reconciled.count(tn.name) == objects
		},
	})
}
