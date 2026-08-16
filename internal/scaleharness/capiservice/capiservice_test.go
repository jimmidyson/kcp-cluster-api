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

package capiservice

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func newClient(t *testing.T) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clusterv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme)
}

func TestPopulateCreatesTheRequestedObjects(t *testing.T) {
	svc := Service{Prefix: "run"}
	c := newClient(t).Build()

	if err := svc.Populate(t.Context(), c, 5); err != nil {
		t.Fatalf("Populate: %v", err)
	}

	list := &clusterv1.ClusterList{}
	if err := c.List(t.Context(), list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 5 {
		t.Errorf("created %d Clusters, want 5", len(list.Items))
	}
}

func TestPopulateOfNothingIsNotAnError(t *testing.T) {
	// The idle-heavy profile holds no objects, and it is the profile that
	// bounds how many workspaces a shard can hold — so zero is a normal input
	// here, not a degenerate one.
	if err := (Service{Prefix: "run"}).Populate(t.Context(), newClient(t).Build(), 0); err != nil {
		t.Errorf("Populate(0): %v", err)
	}
}

func TestTouchProducesADistinctUpdate(t *testing.T) {
	svc := Service{Prefix: "run"}
	c := newClient(t).Build()
	ctx := t.Context()

	if err := svc.Populate(ctx, c, 1); err != nil {
		t.Fatalf("Populate: %v", err)
	}

	before := &clusterv1.ClusterList{}
	if err := c.List(ctx, before); err != nil {
		t.Fatalf("List: %v", err)
	}
	firstRV := before.Items[0].ResourceVersion

	if err := svc.Touch(ctx, c); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	after := &clusterv1.ClusterList{}
	if err := c.List(ctx, after); err != nil {
		t.Fatalf("List: %v", err)
	}
	if after.Items[0].ResourceVersion == firstRV {
		t.Error("Touch did not change the object: an event the apiserver discards is not load")
	}
}

func TestTouchWithoutObjectsFailsLoudly(t *testing.T) {
	// Silently succeeding here would let a misconfigured profile — an event
	// rate with no objects to mutate — report a measurement of zero load as
	// though it were a measurement of the declared load.
	if err := (Service{Prefix: "run"}).Touch(t.Context(), newClient(t).Build()); err == nil {
		t.Error("Touch on an empty workspace succeeded; it must not report load it did not generate")
	}
}

func TestWatchedTypesCoverTheWiredSet(t *testing.T) {
	got := (Service{}).WatchedTypes()
	if len(got) < 2 {
		t.Fatalf("WatchedTypes = %v; the listener term is driven by this, so it must reflect what is wired", got)
	}
	for _, want := range []string{"Cluster", "Machine"} {
		found := false
		for _, g := range got {
			if len(g) >= len(want) && g[len(g)-len(want):] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("WatchedTypes %v does not include %s, which the core reconcilers watch", got, want)
		}
	}
}
