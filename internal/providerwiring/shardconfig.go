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
	"regexp"

	"k8s.io/client-go/rest"
)

// clusterPath matches the /clusters/<path> segment kcp addresses a logical
// cluster with, at the end of a URL and nowhere else. The path is a workspace
// path, a logical cluster name, or the wildcard - anything but a slash.
var clusterPath = regexp.MustCompile(`/clusters/[^/]+/?$`)

// ShardConfig returns cfg addressed at the shard rather than at one workspace.
//
// A manager is configured with one kubeconfig and needs its server two ways.
// Its APIExportEndpointSlice lives in the workspace the exports were published
// in, so reading it needs a config scoped to that workspace - which is what
// --kubeconfig is documented to point at. Everything that reaches into a
// *tenant* workspace scopes the config itself, by appending
// /clusters/<workspace> to the host: the ClusterCache reading a cluster's
// kubeconfig Secret, and the event sink. Handed the scoped config those
// produce /clusters/root/clusters/<workspace>, which kcp answers with a 404.
//
// That 404 is worth describing, because it does not look like this. It arrives
// as "error creating REST config: error getting kubeconfig secret: the server
// could not find the requested resource" - a message about a Secret, from a
// controller that is doing nothing wrong, for a URL nobody looks at. The
// cluster then never initializes, and every provider blames the one before it.
//
// So the shard is derived rather than configured: it is the same server and
// the same credentials, without the workspace. A config that already addresses
// no workspace is returned unchanged.
func ShardConfig(cfg *rest.Config) *rest.Config {
	if cfg == nil {
		return nil
	}
	out := rest.CopyConfig(cfg)
	out.Host = clusterPath.ReplaceAllString(out.Host, "")
	return out
}
