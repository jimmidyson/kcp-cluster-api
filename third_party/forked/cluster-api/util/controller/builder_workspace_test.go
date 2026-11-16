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
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// TestMulticlusterBuilderForKeepsItsPredicatesInWildcardMode covers the one
// thing that can silently go missing between the two modes.
//
// In wildcard mode the multicluster builder is never built: buildWildcard
// registers each declared watch against the shared cache itself. A predicate
// handed to For therefore has to survive as data on the declaration, and if it
// does not, nothing fails - the controller is simply woken by every object of
// its type in the whole shard instead of the subset it asked for, which is a
// cost rather than an error and so is invisible.
//
// The topology controllers are the reason it matters: each of them filters its
// primary watch down to the objects a topology owns, and unfiltered that is one
// queue item per Cluster, MachineDeployment and MachineSet in the fleet.
func TestMulticlusterBuilderForKeepsItsPredicatesInWildcardMode(t *testing.T) {
	g := NewWithT(t)

	// A predicate that refuses everything, so that "the predicate that was
	// declared is the predicate that is registered" is answerable rather than
	// merely countable.
	refuseAll := predicate.NewPredicateFuncs(func(client.Object) bool { return false })

	blder := &MulticlusterBuilder{registry: &WildcardRegistry{}}
	blder.For(&corev1.ConfigMap{}, refuseAll)

	g.Expect(blder.wildcardWatches).To(HaveLen(1))
	w := blder.wildcardWatches[0]
	g.Expect(w.object).To(BeAssignableToTypeOf(&corev1.ConfigMap{}))
	g.Expect(w.predicates).To(HaveLen(1), "the For predicate was dropped on the way to the shared cache")
	g.Expect(w.predicates[0].Create(event.CreateEvent{Object: &corev1.ConfigMap{}})).To(BeFalse(),
		"the registered predicate is not the one that was declared")
}

// TestMulticlusterBuilderForWithoutPredicates is the case every reconciler
// wired before the topology ones is in: no predicates, and none invented.
func TestMulticlusterBuilderForWithoutPredicates(t *testing.T) {
	g := NewWithT(t)

	blder := &MulticlusterBuilder{registry: &WildcardRegistry{}}
	blder.For(&corev1.ConfigMap{})

	g.Expect(blder.wildcardWatches).To(HaveLen(1))
	g.Expect(blder.wildcardWatches[0].predicates).To(BeEmpty())
}
