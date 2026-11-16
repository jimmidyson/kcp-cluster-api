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

// Package controller provides utils for controller-runtime.
package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	pkgerrors "github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/cluster-api/feature"
	"sigs.k8s.io/cluster-api/util/cache"
)

const requeueDurationStaleCache = 100 * time.Millisecond

// RequestType is what a controller's queue items must satisfy for the wrapping
// machinery below — the reconcile cache, the rate limiter and the metrics — to
// work regardless of whether the controller serves one cluster or many.
//
// Both request types already satisfy it. reconcile.Request is comparable and
// promotes String() from its embedded NamespacedName;
// mcreconcile.Request is comparable and overrides String() to include the
// cluster, which is what keeps two clusters' identically-named objects apart in
// the reconcile cache and the rate limiter.
//
// That last point is the whole reason this is a type parameter rather than a
// second copy of the machinery: a duplicate would have to re-derive the cache
// key, and a duplicate that got it wrong would collide across clusters
// silently.
type RequestType interface {
	comparable
	fmt.Stringer
}

var atMostEvery10Seconds = newAtMostEvery(10 * time.Second)

type reconcilerWrapper[request RequestType] struct {
	name              string
	reconcileCache    cache.Cache[reconcileCacheEntry[request]]
	reconciler        reconcile.TypedReconciler[request]
	rateLimitInterval time.Duration
	queueRateLimiter  *typedItemExponentialFailureRateLimiter[request]
	consistencyStore  consistencyStore

	// namespacedName extracts the object identity from a request.
	//
	// A function rather than a constraint method, and passed in explicitly by
	// whichever builder constructs this: a type parameter has no fields, and
	// neither request type exposes its NamespacedName through a method. Wiring
	// it in keeps the dependency visible at the construction site instead of
	// hiding it behind an interface assertion.
	namespacedName func(request) types.NamespacedName
}

// Reconcile reconciles the passed in object.
func (r *reconcilerWrapper[request]) Reconcile(ctx context.Context, req request) (reconcile.Result, error) {
	if !feature.Gates.Enabled(feature.ReconcilerRateLimiting) {
		return r.reconciler.Reconcile(ctx, req)
	}

	reconcileStartTime := time.Now()

	// Check reconcileCache to ensure we won't run reconcile too frequently.
	if cacheEntry, ok := r.reconcileCache.Has(reconcileCacheEntry[request]{Request: req}.Key()); ok {
		if requeueAfter, requeue := cacheEntry.ShouldRequeue(reconcileStartTime); requeue {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	// Guarded rather than called blind. This is a required field on a struct
	// that is built as a literal, so omitting it is a compile-time silence and
	// a run-time nil dereference deep inside Reconcile — which is exactly how
	// it was first found. A named error says what is wrong at the first
	// reconcile instead.
	if r.namespacedName == nil {
		return ctrl.Result{}, pkgerrors.New("reconcilerWrapper.namespacedName must be set: it extracts the object identity the consistency store is keyed by")
	}

	consistencyErrs, err := r.consistencyStore.EnsureReady(ctx, r.namespacedName(req))
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(consistencyErrs) > 0 {
		log := ctrl.LoggerFrom(ctx)
		atMostEvery10Seconds.Do(func() {
			log.V(2).Info("Cache stale, reconciles are requeued until the cache is up-to-date")
		})
		for _, consistencyErr := range consistencyErrs {
			reconcileStaleCacheSkipsTotal.WithLabelValues(r.name, consistencyErr.GroupVersionKindType.Kind).Inc()
		}
		if log.V(5).Enabled() {
			var staleCaches []string
			for _, consistencyErr := range consistencyErrs {
				staleCaches = append(staleCaches, fmt.Sprintf("%s, writtenRV: %s, observedRV: %s", consistencyErr.GroupVersionKindType.Kind, consistencyErr.WroteRV, consistencyErr.ReadRV))
			}
			log.V(5).Info(fmt.Sprintf("Cache stale, requeueing after %s (%s)", requeueDurationStaleCache, strings.Join(staleCaches, ", ")))
		}
		return ctrl.Result{RequeueAfter: requeueDurationStaleCache}, nil
	}

	// Add entry to the reconcileCache so we won't run Reconcile more than once per second.
	// Under certain circumstances the ReconcileAfter time will be set to a later time via DeferNextReconcile /
	// DeferNextReconcileForObject, e.g. when we're waiting for Pods to terminate during node drain or
	// volumes to detach. This is done to ensure we're not spamming the workload cluster API server.
	r.reconcileCache.Add(reconcileCacheEntry[request]{Request: req, ReconcileAfter: reconcileStartTime.Add(r.rateLimitInterval)})

	// Update metrics after processing each item
	defer func() {
		reconcileTime.WithLabelValues(r.name).Observe(time.Since(reconcileStartTime).Seconds())
	}()

	result, err := r.reconciler.Reconcile(ctx, req)
	if err != nil {
		// Note: controller-runtime logs a warning if an error is returned in combination with
		// RequeueAfter / Requeue. Dropping RequeueAfter and Requeue here to avoid this warning
		// (while preserving Priority).
		result.RequeueAfter = 0
		result.Requeue = false //nolint:staticcheck // We have to handle Requeue until it is removed
	}
	switch {
	case err != nil:
		reconcileTotal.WithLabelValues(r.name, labelError).Inc()
	case result.RequeueAfter > 0:
		// It does not make sense to use a requeueAfter that is lower than the rate-limiting enforced via
		// the reconcileCache, so we use that as a minimum.
		// Note: Evaluating this here includes the reconcileCache entry set above, but also the entry that
		// might have been added during Reconcile via DeferNextReconcile / DeferNextReconcileForObject.
		// TODO: It would also be possible to extend r.queueRateLimiter to set a request-specific minimum requeueAfter.
		// This would allow us to also enforce a minimum requeueAfter for the err != nil and Requeue cases.
		minimumRequeueAfter := r.rateLimitInterval
		if cacheEntry, ok := r.reconcileCache.Has(reconcileCacheEntry[request]{Request: req}.Key()); ok {
			if requeueAfter, requeue := cacheEntry.ShouldRequeue(time.Now()); requeue {
				minimumRequeueAfter = requeueAfter
			}
		}
		result.RequeueAfter = max(result.RequeueAfter, minimumRequeueAfter)
		r.queueRateLimiter.ActualForget(req)
		reconcileTotal.WithLabelValues(r.name, labelRequeueAfter).Inc()
	case result.Requeue: //nolint: staticcheck // We have to handle Requeue until it is removed
		reconcileTotal.WithLabelValues(r.name, labelRequeue).Inc()
	default:
		r.queueRateLimiter.ActualForget(req)
		reconcileTotal.WithLabelValues(r.name, labelSuccess).Inc()
	}

	return result, err
}

type controllerWrapper[request RequestType] struct {
	controller.TypedController[request]
	reconcileCache   cache.Cache[reconcileCacheEntry[request]]
	consistencyStore consistencyStore

	// newRequest builds a queue item from an object identity. The multicluster
	// builder supplies one that attaches the cluster; the single-cluster
	// builder supplies one that does not.
	newRequest func(types.NamespacedName) request
}

// DeferNextReconcile takes a plain reconcile.Request, not the parameterised
// one, so that the public Controller interface is identical whether the
// controller serves one cluster or many — reconcilers call this from inside
// Reconcile, and changing its signature would change reconcile code.
//
// newRequest turns the identity into whatever the queue is keyed on. For a
// fleet-wide controller that request carries no cluster, so the deferral
// applies to the object in every cluster; see the note where newRequest is
// supplied.
func (c *controllerWrapper[request]) DeferNextReconcile(req reconcile.Request, reconcileAfter time.Time) {
	// Same reasoning as reconcilerWrapper.namespacedName: a required field on a
	// struct built as a literal. Dropping the deferral is wrong, so this is
	// loud rather than silent.
	if c.newRequest == nil {
		panic("controllerWrapper.newRequest must be set: it builds the queue item a deferral is recorded against")
	}
	c.reconcileCache.Add(reconcileCacheEntry[request]{
		Request:        c.newRequest(req.NamespacedName),
		ReconcileAfter: reconcileAfter,
	})
}

func (c *controllerWrapper[request]) DeferNextReconcileForObject(obj metav1.Object, reconcileAfter time.Time) {
	c.DeferNextReconcile(reconcile.Request{
		NamespacedName: types.NamespacedName{
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
		}}, reconcileAfter)
}

// reconcileCacheEntry is an Entry for the Cache that stores the
// earliest time after which the next Reconcile should be executed.
type reconcileCacheEntry[request RequestType] struct {
	Request        request
	ReconcileAfter time.Time
}

var _ cache.Entry = &reconcileCacheEntry[reconcile.Request]{}

// Key returns the cache key of a reconcileCacheEntry.
//
// String() rather than the bare NamespacedName, and that is load-bearing for
// multicluster use: mcreconcile.Request's String() prefixes the cluster, so two
// clusters' identically-named objects get distinct keys. A key derived from
// namespace and name alone would let one cluster's rate limiting suppress
// another's reconcile.
func (r reconcileCacheEntry[request]) Key() string {
	return r.Request.String()
}

// ShouldRequeue returns if the current Reconcile should be requeued.
func (r reconcileCacheEntry[request]) ShouldRequeue(now time.Time) (requeueAfter time.Duration, requeue bool) {
	if r.ReconcileAfter.IsZero() {
		return time.Duration(0), false
	}

	if r.ReconcileAfter.After(now) {
		return r.ReconcileAfter.Sub(now), true
	}

	return time.Duration(0), false
}

func (c *controllerWrapper[request]) DeferNextReconcileUntilCacheUpToDate(reconciledObject metav1.Object, writtenObjectGVKT GroupVersionKindType, writtenObjectResourceVersion string) {
	// Note: We are using GroupResource here because we want to avoid making bigger changes to the vendored consistencyStore util.
	// We could calculate GroupResource from a client.Object but we would have to handle error cases.
	c.consistencyStore.WroteAt(client.ObjectKey{Namespace: reconciledObject.GetNamespace(), Name: reconciledObject.GetName()},
		reconciledObject.GetUID(), writtenObjectGVKT, writtenObjectResourceVersion)
}

func (c *controllerWrapper[request]) ClearConsistencyStore(reconciledObject client.ObjectKey, reconciledObjectUID types.UID) {
	c.consistencyStore.Clear(reconciledObject, reconciledObjectUID)
}

// atMostEvery will never run the method more than once every specified duration.
type atMostEvery struct {
	delay    time.Duration
	lastCall time.Time
	mutex    sync.Mutex
}

// newAtMostEvery creates a new atMostEvery, that will run the method at most every given duration.
func newAtMostEvery(delay time.Duration) *atMostEvery {
	return &atMostEvery{
		delay: delay,
	}
}

// updateLastCall returns true if the lastCall time has been updated, false if it was too early.
func (s *atMostEvery) updateLastCall() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if time.Since(s.lastCall) < s.delay {
		return false
	}
	s.lastCall = time.Now()
	return true
}

// Do will run the method if enough time has passed, and return true.
// Otherwise, it does nothing and returns false.
func (s *atMostEvery) Do(fn func()) bool {
	if !s.updateLastCall() {
		return false
	}
	fn()
	return true
}
