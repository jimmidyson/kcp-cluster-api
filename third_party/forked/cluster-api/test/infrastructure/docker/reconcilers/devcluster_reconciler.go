/*
Copyright 2024 The Kubernetes Authors.

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

// Package reconcilers implements controller functionality.
package reconcilers

import (
	"context"
	"time"

	pkgerrors "github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends"
	dockerbackend "sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends/docker"
	inmemorybackend "sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends/inmemory"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/finalizers"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// DevCluster reconciles a DevCluster object.
type DevCluster struct {
	client.Client

	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string

	ContainerRuntime container.Runtime
	InMemoryManager  inmemoryruntime.Manager
	APIServerMux     *inmemoryserver.WorkloadClustersMux
}

// SetupWithManager sets up the reconciler with the Manager.
func (r *DevCluster) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	if r.Client == nil || r.InMemoryManager == nil || r.APIServerMux == nil || r.ContainerRuntime == nil {
		return pkgerrors.New("Client, InMemoryManager and APIServerMux, ContainerRuntime must not be nil")
	}

	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "devcluster")
	err := capicontrollerutil.NewControllerManagedBy(mgr, predicateLog).
		For(&infrav1.DevCluster{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind("DevCluster"), mgr.GetClient(), &infrav1.DevCluster{})),
			predicates.ClusterPausedTransitions(mgr.GetScheme(), predicateLog),
		).Complete(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a controller manager")
	}
	return nil
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=devclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=devclusters/status;devclusters/finalizers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch;update;patch

// Reconcile reconciles the passed in object.
func (r *DevCluster) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(ctx)
	ctx = container.RuntimeInto(ctx, r.ContainerRuntime)

	// Fetch the DevCluster instance
	devCluster := &infrav1.DevCluster{}
	if err := r.Get(ctx, req.NamespacedName, devCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Return early if the DevCluster is externally managed.
	if annotations.IsExternallyManaged(devCluster) {
		log.V(4).Info("DevCluster is externally managed, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, devCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		// Note: If ownerRef was not set, there is nothing to delete. Remove finalizer so deletion can succeed.
		if !devCluster.DeletionTimestamp.IsZero() {
			if controllerutil.ContainsFinalizer(devCluster, infrav1.ClusterFinalizer) {
				devClusterWithoutFinalizer := devCluster.DeepCopy()
				controllerutil.RemoveFinalizer(devClusterWithoutFinalizer, infrav1.ClusterFinalizer)
				if err := r.Client.Patch(ctx, devClusterWithoutFinalizer, client.MergeFrom(devCluster)); err != nil {
					return ctrl.Result{}, pkgerrors.Wrapf(err, "failed to patch DevCluster %s", klog.KObj(devCluster))
				}
			}
			return ctrl.Result{}, nil
		}

		log.Info("Waiting for Cluster Controller to set OwnerRef on DevCluster")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("Cluster", klog.KObj(cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	// Add finalizer first if not set to avoid the race condition between init and delete.
	if finalizerAdded, err := finalizers.EnsureFinalizer(ctx, r.Client, devCluster, infrav1.ClusterFinalizer); err != nil || finalizerAdded {
		return ctrl.Result{}, err
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(devCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	if isPaused, requeue, err := paused.EnsurePausedCondition(ctx, r.Client, cluster, devCluster); err != nil || isPaused || requeue {
		return ctrl.Result{}, err
	}

	backendReconciler := r.backendReconcilerFactory(ctx, devCluster)

	// Always attempt to Patch the DevCluster object and status after each reconciliation.
	defer func() {
		if err := backendReconciler.PatchDevCluster(ctx, patchHelper, devCluster); err != nil {
			rerr = kerrors.NewAggregate([]error{rerr, err})
		}
	}()

	// Handle deleted clusters
	if !devCluster.DeletionTimestamp.IsZero() {
		// A DevCluster outlives the DevMachines that belong to it, because they
		// cannot clean themselves up without it: the docker backend deletes
		// only the load balancer here and leaves each machine's container to
		// that machine's own reconcile, and the in-memory backend names its
		// per-cluster state after the Cluster. Deleted first, the machines have
		// nothing to delete their containers with.
		//
		// Cluster API's own teardown never gets this wrong — it deletes
		// Machines before the infrastructure cluster — but nothing here depends
		// on it: deleting a kcp APIBinding removes every bound object at once,
		// and a person can always delete a DevCluster by hand. Waiting makes the
		// order a property of this controller rather than of its callers.
		remaining, err := r.devMachinesFor(ctx, devCluster.Namespace, cluster.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if remaining > 0 {
			log.Info("Waiting for DevMachines to be deleted before deleting the DevCluster", "DevMachines", remaining)
			return ctrl.Result{RequeueAfter: devMachineDeletionRequeue}, nil
		}
		return backendReconciler.ReconcileDelete(ctx, cluster, devCluster)
	}

	// Handle non-deleted clusters
	return backendReconciler.ReconcileNormal(ctx, cluster, devCluster)
}

// devMachineDeletionRequeue is how often a deleting DevCluster re-checks
// whether its machines have gone. The DevCluster controller does not watch
// DevMachines - it has never needed to - so this is a poll rather than an
// event, and it is short because it only runs while a cluster is being deleted.
const devMachineDeletionRequeue = 5 * time.Second

// devMachinesFor counts the DevMachines that still belong to a cluster.
func (r *DevCluster) devMachinesFor(ctx context.Context, namespace, clusterName string) (int, error) {
	devMachines := &infrav1.DevMachineList{}
	if err := r.Client.List(ctx, devMachines,
		client.InNamespace(namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: clusterName},
	); err != nil {
		return 0, pkgerrors.Wrapf(err, "failed to list DevMachines for Cluster %s", klog.KRef(namespace, clusterName))
	}
	return len(devMachines.Items), nil
}

func (r *DevCluster) backendReconcilerFactory(_ context.Context, devCluster *infrav1.DevCluster) backends.DevClusterBackendReconciler {
	if devCluster.Spec.Backend.InMemory != nil {
		return &inmemorybackend.ClusterBackendReconciler{
			Client:          r.Client,
			InMemoryManager: r.InMemoryManager,
			APIServerMux:    r.APIServerMux,
		}
	}
	return &dockerbackend.ClusterBackEndReconciler{
		Client:           r.Client,
		ContainerRuntime: r.ContainerRuntime,
	}
}
