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

package upstreamscale

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// ForwardedShard reads each shard replica through a port-forward of its own.
//
// # Why not the pod proxy, which is how the stock side is read
//
// The shard serves its metrics on its own secure port, to an authenticated
// caller. The API server's pod proxy strips the caller's credentials before
// forwarding, so through it every request arrives anonymous and is refused —
// and the run falls back to one arbitrary instance, which is a third of a
// three-replica control plane.
//
// A forward per replica keeps the credentials, so each replica is read as
// itself. The tunnel is the driver's own path and not the fleet's: the managers
// reach the shard through its Service. A tunnel that flaps is worth reporting
// (Forward.Restarts) and is not a finding about the fleet.
type ForwardedShard struct {
	// Host is the hosting cluster's config, which is what opens a forward.
	Host *rest.Config
	// Shard carries the credentials and the CA that reach a shard. Its host is
	// replaced by each forward's local address, which the serving certificate
	// covers because it is minted for the loopback addresses as well as for the
	// Service names.
	Shard *rest.Config
	// Namespace and Port are where the replicas are and what they serve on.
	Namespace string
	Port      int

	mu       sync.Mutex
	forwards map[string]*deployedscale.Forward
}

var _ ControlPlaneScraper = (*ForwardedShard)(nil)

// Metrics reads one replica's exposition.
func (f *ForwardedShard) Metrics(ctx context.Context, pod string) ([]byte, error) {
	return f.get(ctx, pod, "/metrics")
}

// ForceCollection asks one replica to collect before its heap is read.
//
// Best effort by contract — see ControlPlaneScraper — and the profile it
// returns is discarded: it is the collection that is wanted, so that the heap
// in the metrics that follow is the retained set rather than a point on the
// collector's sawtooth.
func (f *ForwardedShard) ForceCollection(ctx context.Context, pod string) error {
	_, err := f.get(ctx, pod, "/debug/pprof/heap?gc=1")
	return err
}

// Close tears down every forward this opened.
func (f *ForwardedShard) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, forward := range f.forwards {
		forward.Stop()
	}
	f.forwards = nil
}

// Restarts is how many times a tunnel had to be rebuilt, summed. A run that
// fought its instrument all the way through should say so on its face.
func (f *ForwardedShard) Restarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, forward := range f.forwards {
		total += forward.Restarts()
	}
	return total
}

func (f *ForwardedShard) get(ctx context.Context, pod, path string) ([]byte, error) {
	forward, err := f.forward(ctx, pod)
	if err != nil {
		return nil, err
	}

	cfg := rest.CopyConfig(f.Shard)
	cfg.Host = "https://" + forward.Local
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("building a client for %s: %w", pod, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Host+path, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", pod, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w", path, pod, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a response body.
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048)) //nolint:errcheck // best effort.
		return nil, fmt.Errorf("reading %s from %s: HTTP %d: %s",
			path, pod, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// forward is this replica's tunnel, opened once and kept.
//
// Kept because opening one is a round trip and a scale run takes a sample every
// rung and every soak interval; one per replica for the life of the run is the
// same shape the deployed harness has used since it was written.
func (f *ForwardedShard) forward(ctx context.Context, pod string) (*deployedscale.Forward, error) {
	f.mu.Lock()
	if existing, ok := f.forwards[pod]; ok {
		f.mu.Unlock()
		return existing, nil
	}
	f.mu.Unlock()

	// Opened outside the lock: a forward takes a round trip to establish, and
	// holding the lock across it would serialise the first sample of every
	// replica behind the slowest of them.
	opened, err := deployedscale.PortForward(ctx, f.Host, f.Namespace, pod, f.Port)
	if err != nil {
		return nil, fmt.Errorf("forwarding a port to %s: %w", pod, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.forwards[pod]; ok {
		// Another sample opened one while this was being established. Keep the
		// first and drop this, rather than leaking a tunnel nothing will close.
		opened.Stop()
		return existing, nil
	}
	if f.forwards == nil {
		f.forwards = map[string]*deployedscale.Forward{}
	}
	f.forwards[pod] = opened
	return opened, nil
}
