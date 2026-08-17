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

package sweep

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"k8s.io/client-go/rest"
)

// stubTransport answers every request with 200 and records nothing: what is
// under test is the counting wrapper, not the round trip.
type stubTransport struct{}

func (stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
}

func TestCounterCountsByClassification(t *testing.T) {
	counter := NewCounter()
	rt := counter.wrap(stubTransport{})

	get := func(url string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
	}

	get("https://kcp/services/apiexport/root/cluster-api/clusters/*/apis/cluster.x-k8s.io/v1beta2/clusters?watch=true")
	get("https://kcp/services/apiexport/root/cluster-api/clusters/*/apis/cluster.x-k8s.io/v1beta2/clusters?watch=true")
	get("https://kcp/clusters/2ab3c4/apis")

	counts := counter.Snapshot()
	watch := Request{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"}
	if counts[watch] != 2 {
		t.Errorf("counted %d of %+v, want 2", counts[watch], watch)
	}
	if got, want := counts.Streams(IsWatch), 1; got != want {
		t.Errorf("Streams(IsWatch) = %d, want %d: the same stream re-established is not a second stream", got, want)
	}
	if got, want := counts.Total(IsDiscovery), 1; got != want {
		t.Errorf("Total(IsDiscovery) = %d, want %d", got, want)
	}

	// A snapshot is a value, not a view. A sweep compares a snapshot taken
	// before a step with one taken after, so traffic arriving in between must
	// not appear in the earlier one.
	before := counter.Snapshot()
	get("https://kcp/clusters/2ab3c4/apis")
	if got, want := before.Total(IsDiscovery), 1; got != want {
		t.Errorf("an earlier snapshot changed: Total(IsDiscovery) = %d, want %d", got, want)
	}
	if got, want := counter.Snapshot().Total(IsDiscovery), 2; got != want {
		t.Errorf("Total(IsDiscovery) = %d, want %d", got, want)
	}
}

func TestCounterIsSafeUnderConcurrency(t *testing.T) {
	counter := NewCounter()
	rt := counter.wrap(stubTransport{})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				req := httptest.NewRequest(http.MethodGet, "https://kcp/clusters/2ab3c4/apis", nil)
				if _, err := rt.RoundTrip(req); err != nil {
					t.Errorf("RoundTrip: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got, want := counter.Snapshot().Total(Any), 200; got != want {
		t.Errorf("Total(Any) = %d, want %d", got, want)
	}
}

// The config a sweep instruments is the one the provider, the manager and the
// test's own direct clients are all built from. Wrapping it must hand back a
// new config: an in-place wrap would count the test's own fixture traffic as
// the manager's, and would double-count if called twice.
func TestWrapConfigDoesNotMutateItsInput(t *testing.T) {
	counter := NewCounter()
	original := &rest.Config{Host: "https://kcp"}

	wrapped := counter.WrapConfig(original)

	if original.WrapTransport != nil {
		t.Error("WrapConfig installed itself on the config it was given")
	}
	if wrapped.WrapTransport == nil {
		t.Fatal("WrapConfig returned a config with no transport wrapper")
	}
	if wrapped == original {
		t.Error("WrapConfig returned the same config it was given")
	}

	rt := wrapped.WrapTransport(stubTransport{})
	req := httptest.NewRequest(http.MethodGet, "https://kcp/clusters/2ab3c4/apis", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if got, want := counter.Snapshot().Total(IsDiscovery), 1; got != want {
		t.Errorf("Total(IsDiscovery) = %d, want %d: the wrapped config's traffic is not being counted", got, want)
	}
}

// An existing wrapper must survive. rest.Config carries at most one
// WrapTransport, and client-go's own machinery (and this project's fixtures)
// may already have installed one; silently dropping it would change the
// behaviour of the thing being measured.
func TestWrapConfigKeepsAnExistingWrapper(t *testing.T) {
	counter := NewCounter()
	inner := false
	original := &rest.Config{
		Host: "https://kcp",
		WrapTransport: func(rt http.RoundTripper) http.RoundTripper {
			inner = true
			return rt
		},
	}

	wrapped := counter.WrapConfig(original)
	rt := wrapped.WrapTransport(stubTransport{})
	req := httptest.NewRequest(http.MethodGet, "https://kcp/clusters/2ab3c4/apis", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	if !inner {
		t.Error("the config's existing transport wrapper was dropped")
	}
	if got, want := counter.Snapshot().Total(IsDiscovery), 1; got != want {
		t.Errorf("Total(IsDiscovery) = %d, want %d", got, want)
	}
}
