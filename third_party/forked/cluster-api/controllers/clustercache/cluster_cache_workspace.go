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

package clustercache

import (
	"context"
	"os"
	"time"

	"github.com/go-logr/logr"

	pkgerrors "github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// accessorKey identifies a workload Cluster within the logical cluster that
// holds it.
//
// # Why the accessors are keyed on more than namespace and name
//
// A ClusterCache serving many logical clusters — kcp workspaces, or any other
// multicluster-runtime provider's clusters — will be asked about two different
// workload Clusters that share a namespace and a name. Keyed on the ObjectKey
// alone they share an accessor, which means one tenant's kubeconfig opens the
// other tenant's workload cluster. That is a cross-tenant fault rather than a
// wrong answer, and it is silent.
//
// # Why the workspace can be empty
//
// A ClusterCache built by SetupWithManager serves one logical cluster and is
// handed contexts that name none. Those all key on the same empty workspace,
// which is correct: they are all the same logical cluster. The zero value is the
// single-cluster case rather than an error case, which is what lets one
// implementation serve both without a mode flag.
type accessorKey struct {
	workspace multicluster.ClusterName
	cluster   client.ObjectKey
}

// keyFor derives the accessor key for a call.
//
// Every method of the ClusterCache interface takes a context except
// GetClusterSource, so this is the whole of what makes the cache workspace-aware
// on the read path.
func keyFor(ctx context.Context, cluster client.ObjectKey) accessorKey {
	workspace, _ := mccontext.ClusterFrom(ctx)
	return accessorKey{workspace: workspace, cluster: cluster}
}

// SetupWithMulticlusterManager creates a ClusterCache that serves every cluster
// the manager's provider offers, with one controller rather than one per
// cluster.
//
// It is SetupWithManager with two substitutions, and no third: the builder, so
// the queue is keyed on a request that carries the logical cluster, and the
// client, so reads resolve to the cluster the reconcile is for. The reconciler
// is the same value, running the same code.
//
// Options.SecretClient must resolve the logical cluster from the context too —
// it reads each Cluster's kubeconfig Secret, and reading the wrong workspace's
// is how a workload cluster gets handed to the wrong tenant.
func SetupWithMulticlusterManager(ctx context.Context, mgr mcmanager.Manager, cl client.Client, options Options, controllerOptions controller.TypedOptions[mcreconcile.Request], opts ...capicontrollerutil.MulticlusterOption) (MulticlusterClusterCache, error) {
	log := ctrl.LoggerFrom(ctx).WithValues("controller", "clustercache")

	if cl == nil {
		return nil, pkgerrors.New("a cluster-aware client is required: a fleet-wide ClusterCache cannot read Clusters through the local manager's client")
	}
	if err := validateAndDefaultOptions(&options); err != nil {
		return nil, err
	}

	localMgr := mgr.GetLocalManager()
	cacheCtx, cacheCtxCancel := context.WithCancelCause(context.Background())

	cc := &clusterCache{
		client:                cl,
		clusterAccessorConfig: buildClusterAccessorConfig(localMgr.GetScheme(), options, controllerPodMetadata(log)),
		clusterAccessors:      make(map[accessorKey]*clusterAccessor),
		cacheCtx:              cacheCtx,
		cacheCtxCancel:        cacheCtxCancel,
	}

	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "clustercache")
	err := capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, predicateLog).
		Apply(opts...).
		Named("clustercache").
		For(&clusterv1.Cluster{}).
		WithOptions(controllerOptions).
		WithEventFilter(predicates.ResourceHasFilterLabel(localMgr.GetScheme(), log, options.WatchFilterValue)).
		Complete(ctx, cc)
	if err != nil {
		return nil, pkgerrors.WithMessage(err, "failed setting up ClusterCache with a multicluster manager")
	}

	return cc, nil
}

// GetMulticlusterClusterSource is GetClusterSource for a controller that serves
// every cluster.
//
// It satisfies MulticlusterClusterSourceFunc, which is what the fleet-wide setup
// functions take.
//
// The requests it enqueues carry the logical cluster whose Cluster produced the
// event, and the map function is called with a context that names it — the
// second matters as much as the first, because the map functions Cluster API
// passes here list through a context-scoped client.
func (cc *clusterCache) GetMulticlusterClusterSource(controllerName string, mapFunc func(ctx context.Context, cluster client.Object) []ctrl.Request, opts ...GetClusterSourceOption) source.TypedSource[mcreconcile.Request] {
	cc.clusterSourcesLock.Lock()
	defer cc.clusterSourcesLock.Unlock()

	getClusterSourceOptions := &GetClusterSourceOptions{}
	getClusterSourceOptions.ApplyOptions(opts)

	src := &multiclusterClusterSource{
		controllerName: controllerName,
		mapFunc:        mapFunc,
		events:         make(chan multiclusterClusterEvent),
	}

	cs := clusterSource{
		controllerName: controllerName,
		// Blocking, like the single-cluster send it mirrors: the reconcile that
		// produced the event waits for the source to take it, which is what
		// keeps the cache from running ahead of a controller that is behind.
		send: func(ctx context.Context, workspace multicluster.ClusterName, cluster *clusterv1.Cluster) {
			select {
			case src.events <- multiclusterClusterEvent{workspace: workspace, cluster: cluster}:
			case <-ctx.Done():
			}
		},
		sendEventAfterProbeFailureDurations: getClusterSourceOptions.watchForProbeFailures,
		lastEventSentTimeByCluster:          map[accessorKey]time.Time{},
	}
	cc.clusterSources = append(cc.clusterSources, cs)

	return src
}

// multiclusterClusterEvent is a Cluster event together with the logical cluster
// it came from, which the Cluster object itself does not carry.
type multiclusterClusterEvent struct {
	workspace multicluster.ClusterName
	cluster   *clusterv1.Cluster
}

// multiclusterClusterSource delivers those events to a fleet-wide controller.
//
// # Why not source.Channel
//
// source.Channel carries client.Object and enqueues through a handler, and
// neither has anywhere to put the logical cluster: the object does not name it
// and the handler is chosen before any cluster is known. Lifting the handler is
// how the *watch* path solves this, and it works there because a lifted handler
// is built per engaged cluster. A raw source is registered once for all of them,
// so the cluster has to travel with the event instead.
type multiclusterClusterSource struct {
	controllerName string
	mapFunc        func(ctx context.Context, cluster client.Object) []ctrl.Request
	events         chan multiclusterClusterEvent
}

var _ source.TypedSource[mcreconcile.Request] = &multiclusterClusterSource{}

// Start implements source.TypedSource. It returns immediately, like
// source.Channel, and does its work in a goroutine that ends with ctx.
func (s *multiclusterClusterSource) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-s.events:
				// The workspace goes in the context as well as on the requests.
				// The map function lists through a client scoped by it, so
				// without this the requests would be labelled correctly and
				// built from the wrong cluster's objects.
				mapCtx := mccontext.WithCluster(ctx, evt.workspace)
				for _, req := range s.mapFunc(mapCtx, evt.cluster) {
					q.Add(mcreconcile.Request{Request: req, ClusterName: evt.workspace})
				}
			}
		}
	}()
	return nil
}

// controllerPodMetadata reads the Pod this controller runs in, if the downward
// API supplied it, so the ClusterCache can shortcut to its own cluster.
//
// Factored out of SetupWithManager so both setups read it the same way rather
// than one growing a second copy.
func controllerPodMetadata(log logr.Logger) *metav1.ObjectMeta {
	podNamespace := os.Getenv("POD_NAMESPACE")
	podName := os.Getenv("POD_NAME")
	podUID := os.Getenv("POD_UID")
	if podNamespace == "" || podName == "" || podUID == "" {
		log.Info("Couldn't find controller Pod metadata, the ClusterCache will always access the cluster it is running on using the regular apiserver endpoint")
		return nil
	}

	log.Info("Found controller Pod metadata, the ClusterCache will try to access the cluster it is running on directly if possible")
	return &metav1.ObjectMeta{
		Namespace: podNamespace,
		Name:      podName,
		UID:       types.UID(podUID),
	}
}

// MulticlusterClusterCache is a ClusterCache that also serves fleet-wide
// Cluster-event sources.
//
// Separate from ClusterCache rather than folded into it: a ClusterCache built by
// SetupWithManager cannot serve one — its consumers are keyed on a request that
// carries no cluster — so putting the method on the base interface would promise
// something one of the two constructions cannot keep.
type MulticlusterClusterCache interface {
	ClusterCache

	// GetMulticlusterClusterSource returns a Source of Cluster events whose
	// requests carry the logical cluster the Cluster lives in.
	GetMulticlusterClusterSource(controllerName string, mapFunc func(ctx context.Context, cluster client.Object) []ctrl.Request, opts ...GetClusterSourceOption) source.TypedSource[mcreconcile.Request]
}

// MulticlusterClusterSourceFunc is the shape of GetMulticlusterClusterSource.
//
// Cluster API's fleet-wide setup functions take one of these rather than reading
// a source off their ClusterCache field, because GetClusterSource takes no
// context and so cannot resolve a cluster. The signature is deliberately
// GetClusterSource's with only the request type changed, so a setup function
// passes the same map function and the same probe-failure options on both paths.
type MulticlusterClusterSourceFunc func(
	controllerName string,
	mapFunc func(ctx context.Context, cluster client.Object) []ctrl.Request,
	opts ...GetClusterSourceOption,
) source.TypedSource[mcreconcile.Request]
