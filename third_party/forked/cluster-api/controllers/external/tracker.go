/*
Copyright 2020 The Kubernetes Authors.

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

package external

import (
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/cluster-api/util/predicates"
)

// MultiClusterWatcher registers a watch across every cluster a controller
// serves, including clusters that engage after registration.
//
// controller-runtime's Watch cannot express that: it binds a source to one
// cache. A watch added at runtime — which is what this tracker exists for — must
// reach clusters that were not engaged when it was added.
//
// It takes a type and a handler rather than a source because *how* that reaches
// every cluster is the controller's business: registering per cluster and
// registering once against a fleet-spanning cache are both correct, they differ
// by orders of magnitude in cost, and a tracker that named one would have to be
// changed to use the other.
type MultiClusterWatcher interface {
	WatchAllClusters(obj client.Object, h handler.TypedEventHandler[client.Object, reconcile.Request], predicates ...predicate.Predicate) error
}

// ObjectTracker is a helper struct to deal when watching external unstructured objects.
type ObjectTracker struct {
	m sync.Map

	Controller      controller.Controller
	Cache           cache.Cache
	Scheme          *runtime.Scheme
	PredicateLogger *logr.Logger

	// MultiClusterController, when set, replaces Controller and Cache: watches
	// are registered fleet-wide instead of against one cluster's cache, and the
	// requests they enqueue carry the cluster the event came from.
	//
	// # Why this is a field on the existing tracker rather than a second type
	//
	// It was a second type — a MulticlusterObjectTracker in its own file, on the
	// same reasoning applied to the builder: genericise behavioural machinery so
	// the two paths cannot drift, duplicate declarative registration so nothing
	// existing has to be modified. Registration looked like the duplicable half.
	//
	// It is not, because the reconcilers hold the tracker as a *concrete struct
	// field* and reach into it:
	//
	//	r.externalTracker.Watch(log, obj, ..., predicates.ResourceIsChanged(..., *r.externalTracker.PredicateLogger))
	//
	// A second type means that field becomes an interface, and an interface
	// cannot carry PredicateLogger — so every reconciler's reconcile path would
	// have to change to call an accessor instead. That is precisely the code
	// ADR-0003's narrowed premise exists to leave alone. A field here costs one
	// branch in one function and leaves both reconcile paths byte-identical.
	MultiClusterController MultiClusterWatcher
}

// Watch uses the controller to issue a Watch only if the object hasn't been seen before.
func (o *ObjectTracker) Watch(log logr.Logger, obj client.Object, handler handler.EventHandler, p ...predicate.Predicate) error {
	if o.Scheme == nil || o.PredicateLogger == nil {
		return pkgerrors.New("both of Scheme and PredicateLogger must be set for object tracker")
	}
	if o.MultiClusterController == nil && (o.Controller == nil || o.Cache == nil) {
		return pkgerrors.New("either MultiClusterController, or both of Controller and Cache, must be set for object tracker")
	}

	gvk := obj.GetObjectKind().GroupVersionKind()
	key := gvk.GroupKind().String()
	if _, loaded := o.m.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}

	log.Info(fmt.Sprintf("Adding watch on external object %q", gvk.String()))
	preds := append(p, predicates.ResourceNotPaused(o.Scheme, *o.PredicateLogger))

	var err error
	if o.MultiClusterController != nil {
		err = o.MultiClusterController.WatchAllClusters(obj.DeepCopyObject().(client.Object), handler, preds...)
	} else {
		err = o.Controller.Watch(source.Kind(
			o.Cache,
			obj.DeepCopyObject().(client.Object),
			handler,
			preds...,
		))
	}
	if err != nil {
		o.m.Delete(key)
		return pkgerrors.Wrapf(err, "failed to add watch on external object %q", gvk.String())
	}
	return nil
}
