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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// VirtualWorkspaceConfig returns a rest.Config addressed at an APIExport's
// virtual workspace.
//
// # Why a fleet-wide manager needs this
//
// multicluster-runtime's manager has a *local* manager as well as the clusters
// its provider engages, and the local one is not decoration: its scheme and its
// RESTMapper answer every question that has no cluster to resolve from. A
// cluster-aware client's RESTMapper is one such question, and
// util.ClusterToTypedObjectsMapper asks it — at setup time, before any workspace
// has engaged — to decide whether the type it maps to is namespaced.
//
// Point the local manager at the workspace holding the APIExport and that lookup
// fails, because the exporting workspace does not itself bind what it exports:
//
//	failed to get restmapping: no matches for kind "MachineList" in group "cluster.x-k8s.io"
//
// It is not a missing CRD. It is that the local manager was addressing a cluster
// that is not a member of the fleet, and so describes a different API surface
// from every cluster the controllers actually serve.
//
// The virtual workspace is the endpoint that does describe it: it is what the
// provider builds each engaged cluster from, so its discovery is by construction
// the surface the fleet has in common.
//
// # Why taking the first endpoint is enough
//
// A slice can name several — one per shard — and this takes the first. That is
// safe precisely because the local manager is not a source of events: the
// fleet-wide watches are registered on the provider's per-shard caches, through
// NewAPIExportProvider and the wildcard registry, not on the local manager's.
// What is left for the local manager to answer are scheme, RESTMapper and
// discovery questions, and every shard serving the same APIExport describes the
// same API surface. Choosing among them for locality is a scheduling question
// this does not answer.
//
// # Why it polls
//
// The endpoint slice has no URLs until something has bound the APIExport, so a
// process starting alongside its first tenant would otherwise fail on a
// condition that resolves itself in seconds.
func VirtualWorkspaceConfig(ctx context.Context, cl client.Client, exportName string, base *rest.Config, timeout time.Duration) (*rest.Config, error) {
	if cl == nil || base == nil {
		return nil, fmt.Errorf("a client and a base config are required")
	}
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	var url string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		slice := &apisv1alpha1.APIExportEndpointSlice{}
		if err := cl.Get(ctx, client.ObjectKey{Name: exportName}, slice); err != nil {
			return false, nil //nolint:nilerr // transient; keep polling until timeout.
		}
		if len(slice.Status.APIExportEndpoints) == 0 {
			return false, nil
		}
		// The first endpoint, deliberately. A slice can name several — one per
		// shard — and they are alternatives rather than a set to merge: each
		// serves the same API surface, which is all this config is used to
		// describe. Choosing among them for locality is a scheduling question
		// this does not answer.
		url = slice.Status.APIExportEndpoints[0].URL
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("APIExport %q has no virtual workspace endpoint after %s: %w", exportName, timeout, err)
	}

	cfg := rest.CopyConfig(base)
	// The wildcard path, which is what the endpoint URL alone is not. A virtual
	// workspace URL addresses no logical cluster on its own, and discovery
	// against it fails ("failed to get server groups: unknown"); /clusters/*
	// addresses every logical cluster the export serves, which is the surface a
	// fleet-wide controller needs described. It is the same path
	// multicluster-provider's WildcardCache builds for the same reason.
	cfg.Host = strings.TrimSuffix(url, "/") + "/clusters/*"
	return cfg, nil
}
