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

// Package bootstrapmanager wires the kubeadm bootstrap provider as a
// controller that serves every workspace bound to the APIExport - the
// conversion plan's P1.
//
// # What is different about this provider
//
// It is made of Secrets. A KubeadmConfig produces a bootstrap data Secret, and
// for the first control plane machine it generates the cluster's certificate
// authorities as Secrets too. Every one of them lives in the workspace being
// reconciled, and the core provider's own experience says a virtual workspace
// does not serve core types - which is why internal/coremanager reads the
// ClusterCache's kubeconfig Secret through a separate, shard-scoped client.
//
// That is true of an APIExport that claims nothing, and it is the reason this
// provider could not simply be wired the way the others were. It stops being
// true with a permission claim: an export that claims `secrets` serves them
// through its virtual workspace to the workspaces that accepted the claim, so
// one cluster-aware client covers the whole reconciler. That is established by
// test/integration/claims rather than assumed, because the alternative -
// threading a second client through every Secret call in an upstream
// reconciler - is not a thing to discover halfway through.
//
// So a deployment of this provider has a prerequisite the others do not: the
// APIExport must claim `secrets` and `configmaps` (the init lock is a
// ConfigMap), and each workspace's APIBinding must accept those claims. See
// kcpfixtures.PublishAPIExport and BindExport.
//
// # What it caches
//
// Reads through the cluster-aware client are served by the provider's wildcard
// cache, so a Secret read here starts a wildcard informer over every
// workspace's Secrets. Upstream gives this reconciler a SecretCachingClient
// with a label-filtered cache for the same reason; doing the equivalent here is
// left until there is a measurement to size it against, and noted rather than
// silently accepted.
package bootstrapmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"sigs.k8s.io/cluster-api/bootstrap/kubeadm/reconcilers/kubeadmconfig"
)

// DefaultTokenTTL is how long a bootstrap token stays valid. It is upstream's
// own default (bootstrap/kubeadm/main.go's --bootstrap-token-ttl), restated
// here because this binary does not expose upstream's full flag surface.
const DefaultTokenTTL = 15 * time.Minute

// Options configures what SetupFleetControllers wires.
type Options struct {
	// TokenTTL is how long a bootstrap token is valid for. Zero means
	// DefaultTokenTTL.
	TokenTTL time.Duration
}

func (o Options) tokenTTL() time.Duration {
	if o.TokenTTL <= 0 {
		return DefaultTokenTTL
	}
	return o.TokenTTL
}

// SetupFleetControllers wires the KubeadmConfig reconciler onto an
// already-built Fleet, as one controller serving every engaged workspace.
//
// It MUST be called before mgr.Start, and exactly once. The Fleet is shared
// with whatever else the process wires: its ClusterCache is a controller of
// its own, and a second one in the same process is rejected rather than
// duplicated.
func SetupFleetControllers(ctx context.Context, mgr mcmanager.Manager, fleet *coremanager.Fleet, opts Options) error {
	if mgr == nil {
		return errors.New("a multi-cluster manager is required")
	}
	if fleet == nil {
		return errors.New("a fleet is required: it carries the clients, the ClusterCache and the wildcard registration this controller shares with every other provider in the process")
	}

	// Client and SecretCachingClient are the same value on purpose. Upstream
	// separates them so that Secret reads go through a cache filtered to the
	// ones this controller owns; here both resolve the workspace from the
	// context, and the filtered cache is the thing not yet built. Passing the
	// cluster-aware client for both is correct but not free - see the package
	// comment.
	if err := (&kubeadmconfig.Reconciler{
		Client:              fleet.Client,
		SecretCachingClient: fleet.Client,
		APIReader:           fleet.APIReader,
		ClusterCache:        fleet.ClusterCache,
		TokenTTL:            opts.tokenTTL(),
	}).SetupWithMulticlusterManager(ctx, mgr, fleet.Options, fleet.ClusterSource, fleet.BuilderOptions()...); err != nil {
		return fmt.Errorf("creating fleet-wide KubeadmConfig controller: %w", err)
	}

	return nil
}
