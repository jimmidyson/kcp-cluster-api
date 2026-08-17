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
	"sync"

	"k8s.io/client-go/rest"
)

// Counter tallies every request a client makes, classified by [Classify].
//
// It sits in the transport rather than in a client wrapper deliberately.
// Everything a sweep needs to see — the wildcard cache's watches, a
// per-workspace RESTMapper's discovery, a reconciler's writes — goes through
// the transport, and most of it is made by machinery inside
// controller-runtime, multicluster-runtime and the kcp provider that a test
// has no other handle on.
//
// The zero value is not usable; call [NewCounter].
type Counter struct {
	mu     sync.Mutex
	counts Counts
}

// NewCounter returns a Counter that has seen nothing.
func NewCounter() *Counter {
	return &Counter{counts: Counts{}}
}

// WrapConfig returns a copy of cfg whose traffic this Counter observes.
//
// The copy matters: kcp's provider copies the config it is given for each
// virtual-workspace endpoint and each engaged workspace
// (multicluster-provider pkg/provider and pkg/cache), so a config instrumented
// once is instrumented for everything derived from it afterwards — but the
// caller's own config, used for test fixtures, stays uncounted and does not
// pollute the measurement.
func (c *Counter) WrapConfig(cfg *rest.Config) *rest.Config {
	out := rest.CopyConfig(cfg)
	existing := out.WrapTransport
	out.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if existing != nil {
			rt = existing(rt)
		}
		return c.wrap(rt)
	}
	return out
}

// Snapshot returns an independent copy of the counts so far.
func (c *Counter) Snapshot() Counts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts.Clone()
}

func (c *Counter) wrap(rt http.RoundTripper) http.RoundTripper {
	return &countingTransport{counter: c, base: rt}
}

func (c *Counter) observe(req Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[req]++
}

type countingTransport struct {
	counter *Counter
	base    http.RoundTripper
}

// RoundTrip counts the request as it is sent, not as it completes.
//
// For a watch that is the only workable choice — the response body of a watch
// is open for as long as the stream lives, so counting on completion would
// count exactly the streams that have already stopped costing anything.
func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.counter.observe(Classify(req.Method, req.URL))
	return t.base.RoundTrip(req)
}
