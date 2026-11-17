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

package reconcilers

import (
	"context"
	"time"

	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends"
	dockerbackend "sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends/docker"
	inmemorybackend "sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers/backends/inmemory"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	"sigs.k8s.io/cluster-api/util"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/finalizers"
	clog "sigs.k8s.io/cluster-api/util/log"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/paused"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// devMachineController is the part of the built controller the reconcile path
// uses.
//
// Narrowed from capicontrollerutil.Controller so the field can hold either the
// single-cluster controller or the fleet-wide one, which differ in their request
// type and so share no interface that mentions it.
type devMachineController interface {
	DeferNextReconcileForObject(obj metav1.Object, reconcileAfter time.Time)
}

// DevMachine reconciles a DevMachine object.
type DevMachine struct {
	client.Client
	controller devMachineController

	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string

	ContainerRuntime         container.Runtime
	ClusterCache             clustercache.ClusterCache
	InMemoryManager          inmemoryruntime.Manager
	APIServerMux             *inmemoryserver.WorkloadClustersMux
	DockerMachineTaskManager *dockerbackend.TaskManager
}

// SetupWithManager sets up the reconciler with the Manager.
func (r *DevMachine) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	if r.Client == nil || r.InMemoryManager == nil || r.APIServerMux == nil || r.ContainerRuntime == nil || r.ClusterCache == nil {
		return pkgerrors.New("Client, InMemoryManager and APIServerMux, ContainerRuntime and ClusterCache must not be nil")
	}

	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "devmachine")
	clusterToDevMachines, err := util.ClusterToTypedObjectsMapper(mgr.GetClient(), &infrav1.DevMachineList{}, mgr.GetScheme())
	if err != nil {
		return err
	}

	r.DockerMachineTaskManager = dockerbackend.NewTaskManager()
	c, err := capicontrollerutil.NewControllerManagedBy(mgr, predicateLog).
		For(&infrav1.DevMachine{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("DevMachine"))),
		).
		Watches(
			&infrav1.DevCluster{},
			handler.EnqueueRequestsFromMapFunc(r.DevClusterToDevMachines),
		).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToDevMachines),
			predicates.ClusterPausedTransitionsOrInfrastructureProvisioned(mgr.GetScheme(), predicateLog),
		).
		WatchesRawSource(r.ClusterCache.GetClusterSource("devmachine", clusterToDevMachines)).
		WatchesRawSource(r.DockerMachineTaskManager.GetSource()).
		Build(ctx, r)
	if err != nil {
		return pkgerrors.Wrap(err, "failed setting up with a controller manager")
	}

	r.controller = c
	return nil
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=devmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=devmachines/status;devmachines/finalizers,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machinesets;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;,verbs=get;list;watch;patch;
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile reconciles the passed in object.
func (r *DevMachine) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	ctx = container.RuntimeInto(ctx, r.ContainerRuntime)

	// Fetch the DevMachine instance.
	devMachine := &infrav1.DevMachine{}
	if err := r.Get(ctx, req.NamespacedName, devMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// AddOwners adds the owners of DevMachine as k/v pairs to the logger.
	// Specifically, it will add KubeadmControlPlane, MachineSet and MachineDeployment.
	ctx, log, err := clog.AddOwners(ctx, r.Client, devMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r.Client, devMachine.ObjectMeta)
	if err != nil {
		// An owner reference to a Machine that has already been deleted is not
		// the same as no owner reference at all, and only the second is handled
		// below: this lookup errors rather than returning nil, so a deleted
		// DevMachine whose Machine went first would retry forever. See
		// releaseDeletedDevMachine.
		if !devMachine.DeletionTimestamp.IsZero() && apierrors.IsNotFound(err) {
			return r.releaseDeletedDevMachine(ctx, devMachine, "its owner Machine")
		}
		return ctrl.Result{}, err
	}
	if machine == nil {
		// Note: If ownerRef was not set, there is nothing to delete. Remove finalizer so deletion can succeed.
		// Note: This should not be necessary anymore as we nowadays only set the finalizer after the ownerRef
		// is set, but keeping this as a safeguard.
		if !devMachine.DeletionTimestamp.IsZero() {
			return r.releaseDeletedDevMachine(ctx, devMachine, "an owner reference to a Machine")
		}

		log.Info("Waiting for Machine Controller to set OwnerRef on DevMachine")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("Machine", klog.KObj(machine))
	ctx = ctrl.LoggerInto(ctx, log)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		// A DevMachine being deleted whose Cluster has already gone has nothing
		// left to reconcile against: the Cluster is what names the backend
		// state this machine owns, and the DevCluster reconciler tears all of
		// that down with the Cluster. Without this the finalizer is never
		// removed - see releaseDeletedDevMachine.
		if !devMachine.DeletionTimestamp.IsZero() && (apierrors.IsNotFound(err) || pkgerrors.Is(err, util.ErrNoCluster)) {
			return r.releaseDeletedDevMachine(ctx, devMachine, "its Cluster")
		}
		log.Info("DevMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterNameLabel))
		return ctrl.Result{}, nil
	}

	log = log.WithValues("Cluster", klog.KObj(cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	// Add finalizer first if not set to avoid the race condition between init and delete.
	// Note: Only add finalizer after the Machine has an ownerRef to avoid unnecessary retries
	// because of conflicts in core CAPI ssa.RemoveManagedFieldsForLabelsAndAnnotations.
	if finalizerAdded, err := finalizers.EnsureFinalizer(ctx, r.Client, devMachine, infrav1.MachineFinalizer); err != nil || finalizerAdded {
		return ctrl.Result{}, err
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(devMachine, r)
	if err != nil {
		return ctrl.Result{}, err
	}

	if isPaused, requeue, err := paused.EnsurePausedCondition(ctx, r.Client, cluster, devMachine); err != nil || isPaused || requeue {
		return ctrl.Result{}, err
	}

	if !cluster.Spec.InfrastructureRef.IsDefined() {
		log.Info("Cluster infrastructureRef is not available yet")
		return ctrl.Result{}, nil
	}

	// Fetch the DevCluster.
	devCluster := &infrav1.DevCluster{}
	devClusterName := client.ObjectKey{
		Namespace: devMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Get(ctx, devClusterName, devCluster); err != nil {
		// As above: a deleted DevMachine whose DevCluster has already gone has
		// nothing left to clean up, and waiting for a DevCluster that is never
		// coming back is waiting forever.
		if !devMachine.DeletionTimestamp.IsZero() && apierrors.IsNotFound(err) {
			return r.releaseDeletedDevMachine(ctx, devMachine, "its DevCluster")
		}
		log.Info("DevCluster is not available yet")
		return ctrl.Result{}, nil
	}

	backendReconciler := r.backendReconcilerFactory(ctx, devMachine)

	// Always attempt to Patch the DevMachine object and status after each reconciliation.
	defer func() {
		if err := backendReconciler.PatchDevMachine(ctx, patchHelper, devMachine, util.IsControlPlaneMachine(machine)); err != nil {
			rerr = kerrors.NewAggregate([]error{rerr, err})
		}
	}()

	// Handle deleted machines
	if !devMachine.DeletionTimestamp.IsZero() {
		return backendReconciler.ReconcileDelete(ctx, cluster, devCluster, machine, devMachine)
	}

	// Handle non-deleted machines
	return backendReconciler.ReconcileNormal(ctx, cluster, devCluster, machine, devMachine)
}

// releaseDeletedDevMachine drops the finalizer from a DevMachine that is being
// deleted and whose backend state is already gone with the object named in
// missing.
//
// Reconciling a DevMachine needs its Machine, its Cluster and its DevCluster,
// and each of those is normally deleted *after* it. Normally: Cluster API's
// teardown is a sequence, and something that removes the objects in a different
// order - a kcp APIBinding being deleted takes every bound object at once -
// leaves this reconcile with nothing to work from. Before this, two of those
// three cases had no exit: the reconcile returned without requeueing, or
// errored forever, and the finalizer stayed. The DevMachine then held the
// Machine, which held the control plane, which held the Cluster, which held the
// APIBinding - a workspace that can never finish unbinding.
//
// It is a last resort, not the normal path. Backend state does not all belong
// to the cluster - the docker backend deletes only the load balancer with the
// DevCluster and leaves each machine's container to this reconcile - so
// releasing a DevMachine without running its backend delete can leak. The
// DevCluster reconciler is what keeps that from happening: it waits for its
// DevMachines before removing its own finalizer, so the ordinary case reaches
// ReconcileDelete with everything it needs.
//
// What is left here is the case where that guarantee is already broken: the
// Machine or the Cluster this DevMachine names has gone, and there is no way
// to reach the backend state at all, because it is keyed by the cluster.
// Holding the finalizer would not clean anything up either - it would only
// stop everything that owns this object from finishing.
func (r *DevMachine) releaseDeletedDevMachine(ctx context.Context, devMachine *infrav1.DevMachine, missing string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(devMachine, infrav1.MachineFinalizer) {
		return ctrl.Result{}, nil
	}

	ctrl.LoggerFrom(ctx).Info("Releasing a deleted DevMachine because "+missing+" is already gone", "DevMachine", klog.KObj(devMachine))

	devMachineWithoutFinalizer := devMachine.DeepCopy()
	controllerutil.RemoveFinalizer(devMachineWithoutFinalizer, infrav1.MachineFinalizer)
	if err := r.Client.Patch(ctx, devMachineWithoutFinalizer, client.MergeFrom(devMachine)); err != nil {
		return ctrl.Result{}, pkgerrors.Wrapf(err, "failed to patch DevMachine %s", klog.KObj(devMachine))
	}
	return ctrl.Result{}, nil
}

func (r *DevMachine) backendReconcilerFactory(_ context.Context, devMachine *infrav1.DevMachine) backends.DevMachineBackendReconciler {
	if devMachine.Spec.Backend.InMemory != nil {
		return &inmemorybackend.MachineBackendReconciler{
			Client:          r.Client,
			InMemoryManager: r.InMemoryManager,
			APIServerMux:    r.APIServerMux,
		}
	}
	return &dockerbackend.MachineBackendReconciler{
		Client:                      r.Client,
		ContainerRuntime:            r.ContainerRuntime,
		ClusterCache:                r.ClusterCache,
		TaskManager:                 r.DockerMachineTaskManager,
		DeferNextReconcileForObject: r.controller.DeferNextReconcileForObject,
	}
}

// DevClusterToDevMachines is a handler.ToRequestsFunc to be used to enqueue
// requests for reconciliation of DevMachines.
func (r *DevMachine) DevClusterToDevMachines(ctx context.Context, o client.Object) []ctrl.Request {
	result := []ctrl.Request{}
	c, ok := o.(*infrav1.DevCluster)
	if !ok {
		panic(fmt.Sprintf("Expected a DevCluster but got a %T", o))
	}

	cluster, err := util.GetOwnerCluster(ctx, r.Client, c.ObjectMeta)
	switch {
	case apierrors.IsNotFound(err) || cluster == nil:
		return result
	case err != nil:
		return result
	}

	labels := map[string]string{clusterv1.ClusterNameLabel: cluster.Name}
	machineList := &clusterv1.MachineList{}
	if err := r.List(ctx, machineList, client.InNamespace(c.Namespace), client.MatchingLabels(labels)); err != nil {
		return nil
	}
	for _, m := range machineList.Items {
		if m.Spec.InfrastructureRef.Name == "" {
			continue
		}
		name := client.ObjectKey{Namespace: m.Namespace, Name: m.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}

	return result
}
