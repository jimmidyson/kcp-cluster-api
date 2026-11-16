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

// Package multicluster holds the adapters a fleet-wide controller needs that
// multicluster-runtime does not provide.
//
// It is a leaf: it depends on controller-runtime and multicluster-runtime and on
// nothing in Cluster API, so both util/controller and controllers/external can
// use it without either importing the other.
package multicluster

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// LiftWithClusterInContext adapts a single-cluster event handler for a
// fleet-wide controller, so that both the requests it enqueues and the context
// it runs in carry the cluster the event came from.
//
// # Why mchandler.TypedLift alone is not enough
//
// TypedLift substitutes the workqueue: requests the handler adds come back out
// carrying the cluster. It does not touch the context. That is sufficient for a
// handler that only reads the incoming object — EnqueueRequestForOwner, for
// instance — and insufficient for the ones Cluster API actually uses.
//
// Both core reconcilers pass map functions built by util.ClusterToTypedObjectsMapper
// and its siblings. Those close over a client and List with the context they are
// given:
//
//	func(ctx context.Context, o client.Object) []ctrl.Request {
//	    ... c.List(ctx, objectList, listOpts...) ...
//	}
//
// Under a fleet-wide controller that client is scoped by the cluster in the
// context. With no cluster there, the List is unscoped: one cluster's Cluster
// event would fan out to every cluster's Machines, or to none. The requests
// would still be labelled with the right cluster by the lifted queue — which is
// what makes the bug quiet, because the queue looks correct while the objects
// enqueued into it were chosen from the wrong workspace.
//
// mccontext.ReconcilerWithClusterInContext does the equivalent for the reconcile
// path. Nothing in multicluster-runtime does it for the handler path, so this
// does.
func LiftWithClusterInContext(h handler.TypedEventHandler[client.Object, reconcile.Request]) mchandler.TypedEventHandlerFunc[client.Object, mcreconcile.Request] {
	return func(clusterName mcmulticluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
		return ForClusterWithClusterInContext(h, clusterName)
	}
}

// ForClusterWithClusterInContext is LiftWithClusterInContext for a watch whose
// cluster is already known, rather than one resolved as clusters engage.
//
// The Node watches the Machine reconciler adds against a workload cluster are
// the case that needs it: the watch is established from inside Reconcile, so the
// cluster it belongs to is whatever the reconcile was for. The events come from
// the workload cluster and the requests have to name the management cluster the
// Machine lives in, and the map function has to list there too.
func ForClusterWithClusterInContext(h handler.TypedEventHandler[client.Object, reconcile.Request], clusterName mcmulticluster.ClusterName) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return &handlerWithClusterInContext{
		h:           mchandler.TypedForCluster[client.Object](h, clusterName),
		clusterName: clusterName,
	}
}

// handlerWithClusterInContext puts the cluster in the context before delegating.
//
// It wraps the *lifted* handler rather than the original, so the queue
// substitution and the context both apply, and neither is reimplemented here.
type handlerWithClusterInContext struct {
	h           handler.TypedEventHandler[client.Object, mcreconcile.Request]
	clusterName mcmulticluster.ClusterName
}

func (e *handlerWithClusterInContext) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	e.h.Create(mccontext.WithCluster(ctx, e.clusterName), evt, q)
}

func (e *handlerWithClusterInContext) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	e.h.Update(mccontext.WithCluster(ctx, e.clusterName), evt, q)
}

func (e *handlerWithClusterInContext) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	e.h.Delete(mccontext.WithCluster(ctx, e.clusterName), evt, q)
}

func (e *handlerWithClusterInContext) Generic(ctx context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) {
	e.h.Generic(mccontext.WithCluster(ctx, e.clusterName), evt, q)
}
