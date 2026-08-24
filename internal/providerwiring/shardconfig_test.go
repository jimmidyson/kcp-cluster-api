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
	"testing"

	"k8s.io/client-go/rest"
)

// A manager is configured with one kubeconfig and needs its server two ways:
// scoped to the workspace its APIExportEndpointSlice lives in, and unscoped,
// because everything that reaches into a *tenant* workspace - the ClusterCache
// reading a kubeconfig Secret, the event sink - scopes the config itself by
// appending to the path.
//
// Handing those the workspace-scoped config produces
// /clusters/root/clusters/<workspace>, which kcp answers with 404 and
// controller-runtime reports as "the server could not find the requested
// resource (get secrets ...)" - a message about a Secret, for a fault that has
// nothing to do with Secrets.
func TestShardConfigStripsTheWorkspaceFromTheHost(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ host, want string }{
		"a workspace-scoped config": {
			host: "https://kcp.kcp-demo.svc.cluster.local:6443/clusters/root",
			want: "https://kcp.kcp-demo.svc.cluster.local:6443",
		},
		"a deeper workspace": {
			host: "https://localhost:6443/clusters/root:providers:capi",
			want: "https://localhost:6443",
		},
		"the wildcard a virtual workspace uses": {
			host: "https://localhost:6443/services/apiexport/root/cluster-api-core/clusters/*",
			want: "https://localhost:6443/services/apiexport/root/cluster-api-core",
		},
		"a trailing slash": {
			host: "https://localhost:6443/clusters/root/",
			want: "https://localhost:6443",
		},
		"an already unscoped config": {
			host: "https://localhost:6443",
			want: "https://localhost:6443",
		},
		"a path that merely contains the word": {
			host: "https://localhost:6443/clusters",
			want: "https://localhost:6443/clusters",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := ShardConfig(&rest.Config{Host: tc.host})
			if got.Host != tc.want {
				t.Errorf("ShardConfig(%q).Host = %q, want %q", tc.host, got.Host, tc.want)
			}
		})
	}
}

// The original is what the caller still reads its endpoint slice with, so it
// has to survive being asked for the shard.
func TestShardConfigDoesNotModifyItsArgument(t *testing.T) {
	t.Parallel()

	const host = "https://localhost:6443/clusters/root"
	cfg := &rest.Config{Host: host, BearerToken: "unchanged"}

	shard := ShardConfig(cfg)
	if cfg.Host != host {
		t.Errorf("the caller's config was modified: Host is now %q", cfg.Host)
	}
	if shard.BearerToken != "unchanged" {
		t.Error("the credentials did not survive: the shard is the same server, reached as the same user")
	}
}
