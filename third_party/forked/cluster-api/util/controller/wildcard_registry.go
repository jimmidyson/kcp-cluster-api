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

package controller

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

// WildcardRegistry joins fleet-wide controllers to the caches their watches are
// registered on, without either side having to exist first.
//
// # Why the indirection
//
// Controllers are wired before the manager starts, because multicluster-runtime
// hands each engagement to the components registered at that moment and never
// replays earlier ones. The caches are built later, by the provider, when it
// first sees an endpoint to serve. So at the moment a controller declares what
// it watches there is nothing yet to register the watch against.
//
// A registry inverts that. A controller records what it wants to watch and
// hands over a function that will register it; a cache arriving later is offered
// to every controller already registered, and a controller registering later is
// offered every cache already known. Neither has to be second.
//
// # Why there can be more than one cache
//
// A provider's cache spans the logical clusters behind one endpoint, and a fleet
// can span several — under kcp, one virtual workspace URL per shard. A watch
// registered on one of them sees that shard and no other, which is a fault that
// looks exactly like a workspace nothing reconciles. Registering every declared
// watch on every cache is what makes the fleet the fleet rather than whichever
// shard happened to be first.
//
// The caches are disjoint: a logical cluster lives on one shard, so no object
// arrives twice, and the demultiplexing handler keys each request on the
// object's own cluster rather than on which cache delivered it.
//
// # What it does not do
//
// Remove a cache. controller-runtime has no way to unregister a source from a
// running controller, so a shard that goes away leaves its sources behind on a
// stopped informer, where they receive nothing. That residue is bounded by the
// number of shards a fleet has ever had — a handful of long-lived endpoints —
// rather than by tenants, which is the distinction that makes it acceptable
// rather than a leak.
type WildcardRegistry struct {
	mu sync.Mutex

	// caches are keyed by the caller's name for them, which is also what
	// deduplicates: a provider builds one cache per endpoint but constructs a
	// cluster from it for every logical cluster it serves, so the same cache is
	// offered many times.
	//
	// Names rather than the cache values themselves because comparing interface
	// values is only safe when what they hold is comparable, and nothing here
	// gets to choose that.
	caches map[string]crcache.Cache
	order  []string

	registrants []wildcardRegistrant
}

type wildcardRegistrant struct {
	controller string
	register   func(crcache.Cache) error
}

// AddCache offers a fleet-spanning cache to every controller registered so far,
// and to every controller that registers later.
//
// Idempotent by name: calling it again with a name already known is a no-op, so
// a caller that learns about a cache once per engaged cluster does not have to
// deduplicate.
//
// It does not block. Registering a source on a controller that is already
// running starts the source and returns; the informer syncs in the background,
// and a handler added to a running informer is given the current store as
// creates, so nothing that arrived before the registration is missed.
func (r *WildcardRegistry) AddCache(name string, c crcache.Cache) error {
	if r == nil {
		return errors.New("a WildcardRegistry is required")
	}
	if name == "" {
		return errors.New("a cache needs a name: it is what deduplicates repeated offers of the same one")
	}
	if c == nil {
		return fmt.Errorf("cache %q is nil", name)
	}

	r.mu.Lock()
	if r.caches == nil {
		r.caches = map[string]crcache.Cache{}
	}
	if _, known := r.caches[name]; known {
		r.mu.Unlock()
		return nil
	}
	r.caches[name] = c
	r.order = append(r.order, name)
	registrants := slices.Clone(r.registrants)
	r.mu.Unlock()

	var errs []error
	for _, reg := range registrants {
		if err := reg.register(c); err != nil {
			errs = append(errs, fmt.Errorf("registering %s's fleet-wide watches on cache %q: %w", reg.controller, name, err))
		}
	}
	return errors.Join(errs...)
}

// Caches reports how many fleet-spanning caches are known, for callers that
// report on the shape of the fleet.
func (r *WildcardRegistry) Caches() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

// add records a controller's watch registration and applies it to every cache
// already known.
func (r *WildcardRegistry) add(controller string, register func(crcache.Cache) error) error {
	r.mu.Lock()
	r.registrants = append(r.registrants, wildcardRegistrant{controller: controller, register: register})
	known := make([]crcache.Cache, 0, len(r.order))
	for _, name := range r.order {
		known = append(known, r.caches[name])
	}
	names := slices.Clone(r.order)
	r.mu.Unlock()

	var errs []error
	for i, c := range known {
		if err := register(c); err != nil {
			errs = append(errs, fmt.Errorf("registering %s's fleet-wide watches on cache %q: %w", controller, names[i], err))
		}
	}
	return errors.Join(errs...)
}
