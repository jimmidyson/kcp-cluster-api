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
	"testing"

	. "github.com/onsi/gomega"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

// namedCache is a stand-in for a fleet-spanning cache. The registry never calls
// anything on one — it only hands it to the registration function — so an
// embedded nil interface is enough, and is honest about that.
type namedCache struct {
	crcache.Cache
	name string
}

// TestWildcardRegistryDoesNotCareWhichArrivesFirst is the property the registry
// exists for.
//
// Controllers are wired before the manager starts; the provider builds its
// caches after. Neither side can wait for the other, so the registry has to
// produce the same registrations regardless of order.
func TestWildcardRegistryDoesNotCareWhichArrivesFirst(t *testing.T) {
	for _, tt := range []struct {
		name       string
		cacheFirst bool
	}{
		{"cache first", true},
		{"controller first", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			r := &WildcardRegistry{}
			var got []string
			register := func(c crcache.Cache) error {
				got = append(got, c.(*namedCache).name)
				return nil
			}

			if tt.cacheFirst {
				g.Expect(r.AddCache("shard-a", &namedCache{name: "shard-a"})).To(Succeed())
				g.Expect(r.add("cluster", register)).To(Succeed())
			} else {
				g.Expect(r.add("cluster", register)).To(Succeed())
				g.Expect(r.AddCache("shard-a", &namedCache{name: "shard-a"})).To(Succeed())
			}

			g.Expect(got).To(Equal([]string{"shard-a"}))
		})
	}
}

// TestWildcardRegistryRegistersEveryControllerOnEveryCache is what makes a fleet
// spanning shards a fleet.
//
// A watch registered on one shard's cache sees that shard and no other, and the
// symptom is a workspace nothing reconciles — indistinguishable from a
// reconciler that decided to do nothing. So every declared watch has to reach
// every cache, whenever each of them turns up.
func TestWildcardRegistryRegistersEveryControllerOnEveryCache(t *testing.T) {
	g := NewWithT(t)

	r := &WildcardRegistry{}
	seen := map[string][]string{}
	watcher := func(controller string) func(crcache.Cache) error {
		return func(c crcache.Cache) error {
			seen[controller] = append(seen[controller], c.(*namedCache).name)
			return nil
		}
	}

	g.Expect(r.AddCache("shard-a", &namedCache{name: "shard-a"})).To(Succeed())
	g.Expect(r.add("cluster", watcher("cluster"))).To(Succeed())
	g.Expect(r.AddCache("shard-b", &namedCache{name: "shard-b"})).To(Succeed())
	g.Expect(r.add("machine", watcher("machine"))).To(Succeed())
	g.Expect(r.AddCache("shard-c", &namedCache{name: "shard-c"})).To(Succeed())

	// Both controllers on all three shards, whichever side of each arrival they
	// were declared on.
	g.Expect(seen["cluster"]).To(Equal([]string{"shard-a", "shard-b", "shard-c"}))
	g.Expect(seen["machine"]).To(Equal([]string{"shard-a", "shard-b", "shard-c"}))
	g.Expect(r.Caches()).To(Equal([]string{"shard-a", "shard-b", "shard-c"}))
}

// TestWildcardRegistryIgnoresARepeatedCache covers how callers actually learn
// about caches.
//
// A provider builds one cache per endpoint but constructs a cluster from it for
// every logical cluster it serves, so the same cache is offered once per
// workspace. Registering the watches again each time would add a duplicate
// source per tenant — the per-workspace cost this whole design exists to
// remove.
func TestWildcardRegistryIgnoresARepeatedCache(t *testing.T) {
	g := NewWithT(t)

	r := &WildcardRegistry{}
	calls := 0
	g.Expect(r.add("cluster", func(crcache.Cache) error { calls++; return nil })).To(Succeed())

	cache := &namedCache{name: "shard-a"}
	for range 5 {
		g.Expect(r.AddCache("shard-a", cache)).To(Succeed())
	}

	g.Expect(calls).To(Equal(1))
	g.Expect(r.Caches()).To(Equal([]string{"shard-a"}))
}

// TestWildcardRegistryReportsWhichControllerAndCacheFailed keeps a registration
// failure attributable.
//
// It is reported through a return value rather than swallowed because the
// alternative is a controller silently watching fewer shards than the fleet
// has, which is the fault this registry was written to prevent.
func TestWildcardRegistryReportsWhichControllerAndCacheFailed(t *testing.T) {
	g := NewWithT(t)

	r := &WildcardRegistry{}
	boom := errors.New("informer refused")
	g.Expect(r.add("cluster", func(crcache.Cache) error { return boom })).To(Succeed())

	err := r.AddCache("shard-a", &namedCache{name: "shard-a"})
	g.Expect(err).To(HaveOccurred())
	g.Expect(errors.Is(err, boom)).To(BeTrue())
	g.Expect(err.Error()).To(ContainSubstring("cluster"))
	g.Expect(err.Error()).To(ContainSubstring("shard-a"))
}

func TestWildcardRegistryRejectsUnusableInput(t *testing.T) {
	g := NewWithT(t)

	r := &WildcardRegistry{}
	g.Expect(r.AddCache("", &namedCache{})).ToNot(Succeed())
	g.Expect(r.AddCache("shard-a", nil)).ToNot(Succeed())

	var nilRegistry *WildcardRegistry
	g.Expect(nilRegistry.AddCache("shard-a", &namedCache{})).ToNot(Succeed())
	g.Expect(nilRegistry.Caches()).To(BeEmpty())
}
