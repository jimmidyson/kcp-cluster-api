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

package providerwiring

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kcp-dev/logicalcluster/v3"
	mcpcache "github.com/kcp-dev/multicluster-provider/pkg/cache"
	mcrecorder "github.com/kcp-dev/multicluster-provider/pkg/events/recorder"
	mcpprovider "github.com/kcp-dev/multicluster-provider/pkg/provider"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// NewAPIExportProvider builds the kcp APIExport provider and hands every
// fleet-spanning cache it creates to a WildcardRegistry.
//
// # Why not apiexport.New
//
// Because the caches have to be reachable, and through that constructor they
// are not. The provider builds one per virtual-workspace endpoint, inside a
// watchedEndpoint, and exposes only an aggregate Lister — enough to read
// through, not enough to register a watch on. The seam that does reach them is
// provider.Options.NewCluster, which is handed each cache as it constructs a
// scoped cluster from it, and apiexport.New does not forward that option. So
// this assembles the same provider from the base package with the option set.
//
// Everything else is apiexport.New's own defaults, kept identical deliberately:
// the endpoint slice type, the URL extractor, the APIBinding it watches to
// discover logical clusters, and the readiness filters. This is the same
// provider with a hook, not a different one.
//
// # Why the caches have to be reachable
//
// A controller's watches must be registered on the cache its reconcilers read
// through. They were not: watches went on the local manager's cache and reads
// through the provider's, two informers over one endpoint with independent lag.
// A reconcile woken by one could read a version older than the event that woke
// it, take the wrong branch and return without requeueing — and never be woken
// again, because the event was spent and the other cache emits none of its own
// when it catches up. That is measured, not suspected:
// evidence/fleet-two-caches.md.
//
// # Shards
//
// One cache per endpoint, and an APIExportEndpointSlice names one per shard. A
// watch registered on one of them sees that shard and no other, which looks
// exactly like a workspace nothing reconciles. The registry replays every
// declared watch onto each cache as it appears, so a fleet spanning shards gets
// every watch on every shard.
//
// Not verified against a real multi-shard installation: the test fixture runs a
// single shard, so the multi-cache path is exercised by unit tests and by the
// ordering it shares with the single-shard case, and nothing more.
func NewAPIExportProvider(cfg *rest.Config, endpointSliceName string, scheme *runtime.Scheme, registry *capicontrollerutil.WildcardRegistry, opts ...ProviderOption) (*mcpprovider.Provider, error) {
	if registry == nil {
		return nil, fmt.Errorf("a WildcardRegistry is required: it is how the fleet-wide watches reach the caches this provider builds")
	}

	logger := log.Log.WithName("kcp-apiexport-cluster-provider")
	caches := &shardCaches{registry: registry}
	for _, opt := range opts {
		opt(caches)
	}

	return mcpprovider.NewProvider(cfg, endpointSliceName, mcpprovider.Options{
		Scheme:                       scheme,
		EndpointSliceObject:          &apisv1alpha1.APIExportEndpointSlice{},
		ExtractURLsFromEndpointSlice: mcpprovider.DefaultExtractURLsFromEndpointSlice,
		ObjectToWatch:                &apisv1alpha1.APIBinding{},
		Log:                          &logger,
		AddFilter:                    mcpprovider.ConditionReadyFunc("Ready"),
		UpdateFilter:                 mcpprovider.ConditionReadyFunc("Ready"),
		NewCluster:                   caches.newCluster,
	})
}

// shardCaches turns the per-cluster NewCluster callback into a per-endpoint
// cache registration.
//
// The callback fires once per logical cluster, so the same cache arrives once
// per workspace; the registry deduplicates by name. The name is the endpoint's
// host, which is what one cache corresponds to.
type shardCaches struct {
	registry *capicontrollerutil.WildcardRegistry

	// indexes are registered on each shard's cache before its watches are,
	// and indexCtx is the context they are registered with. See
	// WithCacheIndexes.
	indexes  []CacheIndex
	indexCtx context.Context

	// indexed records the shards whose indexes are already registered.
	// controller-runtime rejects a second indexer on the same field, and this
	// callback fires once per workspace rather than once per shard.
	indexed map[string]struct{}

	// mu guards nothing the registry does not already guard, and exists for the
	// error: a registration that failed once will fail the same way for every
	// subsequent workspace on that shard, and reporting it per workspace would
	// bury it.
	mu       sync.Mutex
	reported map[string]struct{}
}

func (s *shardCaches) newCluster(cfg *rest.Config, clusterName logicalcluster.Name, wildcard mcpcache.WildcardCache, scheme *runtime.Scheme, recorder mcrecorder.EventRecorderGetter) (*mcpcache.ScopedCluster, error) {
	// Before the cluster is built, not after: the scoped cluster this returns is
	// what the manager engages, and a workspace that engaged before its shard's
	// watches were registered would be served by controllers that cannot see it.
	//
	// A registration failure fails the engagement rather than being logged,
	// because a half-wired shard is worse than a workspace that visibly did not
	// join — the first is silent.
	// Indexes before watches, and for the same reason watches come before the
	// engagement: a field index has to exist before the informer it belongs to
	// starts, and registering the shard's watches is what starts them.
	// controller-runtime does not fall back to a scan for a selector it has no
	// index for - it fails the List - so a reconciler that lists by one against
	// an unindexed cache never gets an answer.
	if err := s.addIndexes(cfg.Host, wildcard); err != nil {
		return nil, err
	}

	if err := s.registry.AddCache(cfg.Host, wildcard); err != nil {
		s.mu.Lock()
		if s.reported == nil {
			s.reported = map[string]struct{}{}
		}
		_, seen := s.reported[cfg.Host]
		s.reported[cfg.Host] = struct{}{}
		s.mu.Unlock()
		if !seen {
			return nil, fmt.Errorf("registering fleet-wide watches on the cache for %s: %w", cfg.Host, err)
		}
		return nil, fmt.Errorf("shard %s is not serving fleet-wide watches, so %s cannot be engaged", cfg.Host, clusterName)
	}

	return mcpcache.NewScopedCluster(cfg, clusterName, wildcard, scheme, recorder)
}

// CacheIndex is a field index to register on every shard's fleet-spanning
// cache.
//
// It is the same triple client.FieldIndexer takes, held as data because the
// cache it goes on does not exist when a manager is wired: the provider builds
// one per endpoint as it sees one, which is after every controller has been
// declared. Cluster API registers its own indexes against a manager
// (index.AddDefaultIndexes), and that manager is the local one here - whose
// cache no reconciler reads through.
type CacheIndex struct {
	// Object is the type being indexed.
	Object client.Object
	// Field is the field selector the index answers, e.g.
	// index.ClusterClassRefPath.
	Field string
	// Extract returns the values an object is indexed under.
	Extract client.IndexerFunc
}

// ProviderOption configures the APIExport provider.
type ProviderOption func(*shardCaches)

// WithCacheIndexes registers field indexes on each shard's fleet-spanning cache
// before that shard's watches are registered and before any workspace on it is
// engaged.
//
// The context is the one the indexes are registered with, and outlives the
// registration: controller-runtime uses it to start the informer being indexed.
func WithCacheIndexes(ctx context.Context, indexes ...CacheIndex) ProviderOption {
	return func(s *shardCaches) {
		s.indexCtx = ctx
		s.indexes = append(s.indexes, indexes...)
	}
}

// addIndexes registers the declared indexes on one shard's cache, once.
func (s *shardCaches) addIndexes(host string, wildcard mcpcache.WildcardCache) error {
	if len(s.indexes) == 0 {
		return nil
	}

	s.mu.Lock()
	if s.indexed == nil {
		s.indexed = map[string]struct{}{}
	}
	_, done := s.indexed[host]
	s.indexed[host] = struct{}{}
	s.mu.Unlock()
	if done {
		return nil
	}

	ctx := s.indexCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, idx := range s.indexes {
		if err := wildcard.IndexField(ctx, idx.Object, idx.Field, idx.Extract); err != nil {
			return fmt.Errorf("indexing %T by %s on the cache for %s: %w", idx.Object, idx.Field, host, err)
		}
	}
	return nil
}
