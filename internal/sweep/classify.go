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

// Package sweep measures what one core-manager process costs as the number of
// workspaces it serves grows.
//
// docs/conversion-plan.md's "Scalability" section makes three claims about
// that cost, and Constitution Principle V requires a design claim about a
// dependency to be verified rather than assumed. Two of the three are claims
// about traffic — watches and startup LISTs are O(types), not O(types ×
// workspaces), and no cache or transport is duplicated per workspace — and one
// is a claim about process state: each workspace still gets its own
// controller, workqueue and goroutines. None of them can be settled by reading
// source alone, because what they are really about is the shape of a curve.
//
// This package provides the instruments for measuring that curve: a
// classifier that says what a request to a kcp shard costs it, a
// [Counter] that installs on a rest.Config and tallies requests by that
// classification, [Sample] for process state, and a [Report] that renders the
// sweep as a table and as JSON.
//
// It measures; it asserts nothing. What the numbers have to satisfy belongs
// in the tests that take them — internal/providerwiring for the wiring's own
// per-workspace cost, and test/integration/sweep for the real thing against a
// real kcp server.
package sweep

import (
	"net/http"
	"net/url"
	"strings"
)

// WildcardCluster is the cluster segment kcp uses for a request that spans
// every logical cluster behind a virtual workspace: /clusters/*/... . A watch
// against it is one stream serving all workspaces, which is the mechanism the
// conversion plan's O(types) claim rests on, so telling it apart from a
// per-workspace request is the whole point of classification.
const WildcardCluster = "*"

// Verb is what a request does, in the terms that decide what it costs the
// shard serving it. It is derived from the HTTP method and the path shape
// rather than taken from a header, because that is all a RoundTripper sees.
type Verb string

const (
	// VerbWatch is a long-running stream. The expensive one: it holds a
	// connection open on the shard for as long as the client wants it.
	VerbWatch Verb = "watch"
	// VerbWatchList is a watch that carries its own initial state
	// (sendInitialEvents), which is how a current client-go informer starts:
	// the LIST that used to precede a watch is now part of it. It is counted
	// apart from VerbWatch so that a report showing no LISTs at all is
	// self-explanatory rather than suspicious — the initial read happened, in
	// the stream.
	VerbWatchList Verb = "watch-list"
	// VerbList is a collection read — the startup cost an informer pays
	// before it can watch.
	VerbList Verb = "list"
	// VerbGet reads one object.
	VerbGet Verb = "get"
	// VerbCreate, VerbUpdate, VerbPatch and VerbDelete are writes.
	VerbCreate Verb = "create"
	VerbUpdate Verb = "update"
	VerbPatch  Verb = "patch"
	VerbDelete Verb = "delete"
	// VerbDiscovery is a request for the API surface itself rather than for
	// any object in it: /api, /apis, /apis/<group>/<version>, /openapi/... .
	// It is called out separately because a RESTMapper built per workspace
	// pays it per workspace, which is exactly the kind of hidden per-workspace
	// cost a sweep exists to find.
	VerbDiscovery Verb = "discovery"
	// VerbOther is anything else a client sends: health probes, and whatever
	// this classifier has not been taught. It is reported rather than dropped,
	// so that traffic growing here is visible instead of invisible.
	VerbOther Verb = "other"
)

// Request is one API call, reduced to the three things that decide whether it
// multiplies with the workspace count: what it does, which logical cluster it
// addresses, and which resource it is about.
//
// It is deliberately comparable, so a map keyed by it counts distinct streams
// as well as total calls. A watch re-established on the shard's timetable
// produces the same key twice; a second workspace's watch produces a new one.
type Request struct {
	// Verb is what the call does.
	Verb Verb
	// Cluster is the logical cluster from the path: [WildcardCluster] for a
	// wildcard request, the logical cluster name for a scoped one, empty for a
	// path with no cluster segment at all.
	Cluster string
	// Resource is "<group>/<resource>", with an empty group for the core
	// group, e.g. "cluster.x-k8s.io/clusters" or "/configmaps". Discovery of a
	// group-version keeps the group and leaves the resource empty; discovery
	// with no group at all leaves the whole field empty.
	Resource string
}

// Classify reduces one outgoing request to a [Request].
//
// The paths it parses are kcp's, not plain Kubernetes': every object path is
// prefixed by /clusters/<logical cluster>, and a virtual workspace puts its
// own prefix (/services/apiexport/<cluster>/<export>) in front of that. The
// parser finds the cluster segment and treats what follows as an ordinary
// Kubernetes API path, which makes it indifferent to which virtual workspace
// the request went through.
func Classify(method string, u *url.URL) Request {
	segments := splitPath(u.Path)

	req := Request{}
	rest := segments
	if i := indexOf(segments, "clusters"); i >= 0 && i+1 < len(segments) {
		req.Cluster = segments[i+1]
		rest = segments[i+2:]
	}

	if len(rest) == 0 {
		req.Verb = VerbDiscovery
		return req
	}

	var group string
	var tail []string
	switch rest[0] {
	case "api":
		// Core group: /api/<version>/... — the version is skipped rather than
		// recorded. Two versions of one resource are the same watch stream on
		// the shard, and this project's question is about how many streams
		// there are.
		if len(rest) == 1 {
			req.Verb = VerbDiscovery
			return req
		}
		tail = rest[2:]
	case "apis":
		// Named group: /apis/<group>/<version>/...
		if len(rest) < 3 {
			req.Verb = VerbDiscovery
			if len(rest) == 2 {
				req.Resource = rest[1] + "/"
			}
			return req
		}
		group, tail = rest[1], rest[3:]
	default:
		// Not an object path: /openapi/..., /version, /healthz, /readyz.
		req.Verb = VerbDiscovery
		if rest[0] != "openapi" && rest[0] != "version" {
			req.Verb = VerbOther
		}
		return req
	}

	// Everything left is <resource>[/<name>[/<subresource>]], optionally
	// preceded by namespaces/<namespace>.
	if len(tail) >= 2 && tail[0] == "namespaces" && len(tail) > 2 {
		tail = tail[2:]
	}
	if len(tail) == 0 {
		// A group-version with nothing after it is discovery of that
		// group-version, not a read of anything in it.
		req.Verb = VerbDiscovery
		req.Resource = group + "/"
		return req
	}

	req.Resource = group + "/" + tail[0]
	named := len(tail) > 1

	switch method {
	case http.MethodPost:
		req.Verb = VerbCreate
	case http.MethodPut:
		req.Verb = VerbUpdate
	case http.MethodPatch:
		req.Verb = VerbPatch
	case http.MethodDelete:
		req.Verb = VerbDelete
	default:
		query := u.Query()
		switch {
		case query.Get("watch") == "true" && query.Get("sendInitialEvents") == "true":
			req.Verb = VerbWatchList
		case query.Get("watch") == "true":
			req.Verb = VerbWatch
		case named:
			req.Verb = VerbGet
		default:
			req.Verb = VerbList
		}
	}
	return req
}

func splitPath(path string) []string {
	var out []string
	for segment := range strings.SplitSeq(strings.Trim(path, "/"), "/") {
		if segment != "" {
			out = append(out, segment)
		}
	}
	return out
}

func indexOf(segments []string, want string) int {
	for i, segment := range segments {
		if segment == want {
			return i
		}
	}
	return -1
}

// Counts is how many times each distinct request was made.
//
// The two numbers it yields answer different questions. The number of
// distinct keys ([Counts.Streams]) is how many separate things the process
// asked the shard for, which is the quantity the O(types) claim is about;
// the sum ([Counts.Total]) includes every retry and every re-established
// watch, which is the quantity a shard operator's connection count reflects.
type Counts map[Request]int

// Predicate selects the requests a total is over.
type Predicate func(Request) bool

// IsWatch selects long-running streams, whether or not they carry their own
// initial state. Both hold a connection open on the shard, which is what the
// O(types) claim is about.
func IsWatch(r Request) bool { return r.Verb == VerbWatch || r.Verb == VerbWatchList }

// IsList selects collection reads.
func IsList(r Request) bool { return r.Verb == VerbList }

// IsDiscovery selects requests for the API surface itself.
func IsDiscovery(r Request) bool { return r.Verb == VerbDiscovery }

// IsWildcard selects requests that address every workspace at once.
func IsWildcard(r Request) bool { return r.Cluster == WildcardCluster }

// IsWorkspaceScoped selects requests addressed to one named logical cluster.
func IsWorkspaceScoped(r Request) bool {
	return r.Cluster != "" && r.Cluster != WildcardCluster
}

// InClusters selects requests addressed to one of the named logical clusters.
//
// It is how a sweep asks the question it actually cares about — is there a
// per-tenant watch? — rather than the question that is easy to ask. A process
// serving many workspaces legitimately holds workspace-scoped requests that
// are nothing to do with tenants: the APIExportEndpointSlice it discovers
// endpoints from lives in the workspace that owns the APIExport, and there is
// one of those however many tenants bind.
func InClusters(names ...string) Predicate {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return func(r Request) bool {
		_, ok := set[r.Cluster]
		return ok
	}
}

// Any selects everything.
func Any(Request) bool { return true }

// And composes predicates.
func And(predicates ...Predicate) Predicate {
	return func(r Request) bool {
		for _, p := range predicates {
			if !p(r) {
				return false
			}
		}
		return true
	}
}

// Streams counts the distinct requests matching p — one per thing asked for,
// however many times it was asked.
//
// Distinct means distinct [Request], verb included. For counting watches, use
// [Counts.DistinctStreams] instead: a stream and its own re-establishment are
// two different Requests but one thing being watched.
func (c Counts) Streams(p Predicate) int {
	n := 0
	for req := range c {
		if p(req) {
			n++
		}
	}
	return n
}

// DistinctStreams counts what is being watched, rather than how it was asked
// for: distinct cluster-and-resource pairs among the requests matching p.
//
// This is the number the O(types) claim is about, and it is not the same as
// [Counts.Streams]. An informer opens its first watch with sendInitialEvents
// (classified [VerbWatchList]) and, when the shard closes that stream some
// minutes later, re-opens it from a resource version as a plain
// [VerbWatch] — two Requests, one informer, one connection at a time. Counting
// Requests would make a sweep that ran long enough to cross a re-establishment
// report watch growth that is really elapsed time, which is exactly the
// artifact this project's own hundred-workspace run first surfaced.
func (c Counts) DistinctStreams(p Predicate) int {
	type stream struct{ cluster, resource string }

	seen := map[stream]struct{}{}
	for req := range c {
		if p(req) {
			seen[stream{cluster: req.Cluster, resource: req.Resource}] = struct{}{}
		}
	}
	return len(seen)
}

// Total counts every call matching p, re-establishments and retries included.
func (c Counts) Total(p Predicate) int {
	n := 0
	for req, count := range c {
		if p(req) {
			n += count
		}
	}
	return n
}

// Sub returns what happened between other and c. Keys whose counts did not
// change are dropped, so a delta shows only what the step being measured
// caused. Neither operand is modified.
func (c Counts) Sub(other Counts) Counts {
	delta := Counts{}
	for req, count := range c {
		if diff := count - other[req]; diff != 0 {
			delta[req] = diff
		}
	}
	return delta
}

// Clone returns an independent copy, so that a sample taken now is not
// changed by traffic that happens next.
func (c Counts) Clone() Counts {
	out := make(Counts, len(c))
	for req, count := range c {
		out[req] = count
	}
	return out
}
