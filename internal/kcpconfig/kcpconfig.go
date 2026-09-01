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

// Package kcpconfig addresses kcp workspaces from a rest.Config without
// depending on which workspace that config already points at.
//
// # The mistake this exists to stop
//
// kcp addresses a workspace by URL path, and the client libraries build that
// path by appending:
//
//	func SetCluster(cfg *rest.Config, clusterPath logicalcluster.Path) *rest.Config {
//		cfg.Host += clusterPath.RequestPath()
//	}
//
// which is right for a config addressing the bare server and wrong for one
// already addressing a workspace — it produces
//
//	https://kcp.svc:6443/clusters/root/clusters/2fj3k…
//
// kcp answers that with a plain 404, which client-go renders as "the server
// could not find the requested resource". Nothing in that names the path, the
// workspace or the doubling, so it reads as a missing object or an
// unsynchronised cache.
//
// Whether a given rest.Config is bare is not visible in its type, its name, or
// at the call site, and both kinds are legitimately in circulation: a
// kubeconfig handed to a deployed manager addresses a workspace, while an
// in-process fixture's base config does not. Code that assumes one of them
// works everywhere it is tested and fails everywhere else. This normalises
// instead of assuming.
package kcpconfig

import (
	"strings"

	"k8s.io/client-go/rest"
)

// BaseHost strips a trailing /clusters/<path> from a host, leaving the server.
// A host with no such suffix is returned unchanged, minus any trailing slash.
func BaseHost(host string) string {
	trimmed := strings.TrimSuffix(host, "/")
	if i := strings.LastIndex(trimmed, "/clusters/"); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// Base returns a copy of cfg addressing the bare server.
//
// Pass a config through this before handing it to anything that appends a
// cluster path of its own — kcp's client cache, kcpclient.SetCluster — so that
// a caller holding a workspace-scoped config gets the workspace it asked for
// rather than a 404.
func Base(cfg *rest.Config) *rest.Config {
	if cfg == nil {
		return nil
	}
	out := rest.CopyConfig(cfg)
	out.Host = BaseHost(out.Host)
	return out
}

// ForCluster returns a copy of cfg addressing one logical cluster, whatever
// cfg addressed before.
func ForCluster(cfg *rest.Config, cluster string) *rest.Config {
	out := rest.CopyConfig(cfg)
	out.Host = BaseHost(out.Host) + "/clusters/" + cluster
	return out
}
