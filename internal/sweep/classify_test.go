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
	"net/url"
	"testing"
)

// The URLs below are the shapes a kcp shard actually serves, not invented
// ones: a virtual-workspace path (what the APIExport provider and its
// wildcard cache talk to), a plain workspace path (what a client built from a
// workspace-scoped config talks to), and the discovery paths a RESTMapper
// walks. Classification has to tell them apart because the whole question
// this package exists to answer — does per-workspace cost multiply onto one
// shard — is a question about which of these grows with the workspace count.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		url    string
		want   Request
	}{
		{
			name:   "wildcard watch through the virtual workspace",
			method: http.MethodGet,
			url:    "https://kcp/services/apiexport/root/cluster-api/clusters/*/apis/cluster.x-k8s.io/v1beta2/clusters?allowWatchBookmarks=true&watch=true",
			want:   Request{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			// A current client-go informer starts with a watch that carries
			// its own initial state instead of a separate LIST. Classifying it
			// as an ordinary watch would leave a report showing zero LISTs
			// looking like a measurement error.
			name:   "streaming initial read",
			method: http.MethodGet,
			url:    "https://kcp/services/apiexport/root/cluster-api/clusters/*/apis/cluster.x-k8s.io/v1beta2/clusters?allowWatchBookmarks=true&sendInitialEvents=true&watch=true",
			want:   Request{Verb: VerbWatchList, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			name:   "wildcard list through the virtual workspace",
			method: http.MethodGet,
			url:    "https://kcp/services/apiexport/root/cluster-api/clusters/*/apis/cluster.x-k8s.io/v1beta2/clusters?limit=500&resourceVersion=0",
			want:   Request{Verb: VerbList, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			name:   "namespaced create scoped to one workspace",
			method: http.MethodPost,
			url:    "https://kcp/services/apiexport/root/cluster-api/clusters/2ab3c4/apis/cluster.x-k8s.io/v1beta2/namespaces/default/clusters",
			want:   Request{Verb: VerbCreate, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			name:   "get one object by name",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/apis/cluster.x-k8s.io/v1beta2/namespaces/default/clusters/example",
			want:   Request{Verb: VerbGet, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			name:   "status subresource is the resource it belongs to",
			method: http.MethodPatch,
			url:    "https://kcp/clusters/2ab3c4/apis/cluster.x-k8s.io/v1beta2/namespaces/default/clusters/example/status",
			want:   Request{Verb: VerbPatch, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/clusters"},
		},
		{
			name:   "core group has no group name",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/api/v1/namespaces/default/configmaps",
			want:   Request{Verb: VerbList, Cluster: "2ab3c4", Resource: "/configmaps"},
		},
		{
			name:   "delete",
			method: http.MethodDelete,
			url:    "https://kcp/clusters/2ab3c4/apis/apis.kcp.io/v1alpha1/apibindings/cluster-api",
			want:   Request{Verb: VerbDelete, Cluster: "2ab3c4", Resource: "apis.kcp.io/apibindings"},
		},
		{
			name:   "group discovery",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/apis",
			want:   Request{Verb: VerbDiscovery, Cluster: "2ab3c4"},
		},
		{
			name:   "legacy core discovery",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/api",
			want:   Request{Verb: VerbDiscovery, Cluster: "2ab3c4"},
		},
		{
			name:   "group-version discovery",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/apis/cluster.x-k8s.io/v1beta2",
			want:   Request{Verb: VerbDiscovery, Cluster: "2ab3c4", Resource: "cluster.x-k8s.io/"},
		},
		{
			name:   "aggregated discovery through the virtual workspace",
			method: http.MethodGet,
			url:    "https://kcp/services/apiexport/root/cluster-api/clusters/*/apis",
			want:   Request{Verb: VerbDiscovery, Cluster: WildcardCluster},
		},
		{
			name:   "openapi",
			method: http.MethodGet,
			url:    "https://kcp/clusters/2ab3c4/openapi/v3/apis/cluster.x-k8s.io/v1beta2",
			want:   Request{Verb: VerbDiscovery, Cluster: "2ab3c4"},
		},
		{
			name:   "a path with no cluster segment is still classified",
			method: http.MethodGet,
			url:    "https://kcp/healthz",
			want:   Request{Verb: VerbOther},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("parsing test URL: %v", err)
			}
			if got := Classify(tc.method, u); got != tc.want {
				t.Errorf("Classify(%s, %s) = %+v, want %+v", tc.method, tc.url, got, tc.want)
			}
		})
	}
}

// A watch that is re-established — which happens on kcp's own timetable, not
// this project's — must not look like a new watch stream, or a sweep long
// enough to cross a re-establishment would report per-workspace watch growth
// that is really just elapsed time. Distinct keys are what the sweep asserts
// on; totals are reported alongside them.
func TestCountsDistinguishStreamsFromRepeats(t *testing.T) {
	watch := Request{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "cluster.x-k8s.io/clusters"}
	other := Request{Verb: VerbWatch, Cluster: WildcardCluster, Resource: "apis.kcp.io/apibindings"}

	counts := Counts{watch: 4, other: 1}

	if got, want := counts.Streams(IsWatch), 2; got != want {
		t.Errorf("Streams(IsWatch) = %d, want %d: two distinct watches, however often each is re-established", got, want)
	}
	if got, want := counts.Total(IsWatch), 5; got != want {
		t.Errorf("Total(IsWatch) = %d, want %d", got, want)
	}
}

// The artifact this exists to prevent was real: a hundred-workspace sweep ran
// long enough for kcp to close the informers' watches, each of which re-opened
// as a plain watch where it had started as a streaming list — and the headline
// "watch streams" figure rose, describing elapsed time as though it were
// per-workspace growth.
func TestDistinctStreamsIgnoresHowAWatchWasOpened(t *testing.T) {
	clusters := "cluster.x-k8s.io/clusters"
	counts := Counts{
		{Verb: VerbWatchList, Cluster: WildcardCluster, Resource: clusters}:                     1,
		{Verb: VerbWatch, Cluster: WildcardCluster, Resource: clusters}:                         3,
		{Verb: VerbWatchList, Cluster: "root", Resource: "apis.kcp.io/apiexportendpointslices"}: 1,
	}

	if got, want := counts.DistinctStreams(IsWatch), 2; got != want {
		t.Errorf("DistinctStreams(IsWatch) = %d, want %d: one informer per cluster-and-resource, however it opened its stream", got, want)
	}
	if got, want := counts.Streams(IsWatch), 3; got != want {
		t.Errorf("Streams(IsWatch) = %d, want %d: Streams still counts requests, which is what makes it the wrong metric here", got, want)
	}
	if got, want := counts.DistinctStreams(And(IsWatch, IsWildcard)), 1; got != want {
		t.Errorf("DistinctStreams(wildcard watches) = %d, want %d", got, want)
	}
}

func TestCountsSub(t *testing.T) {
	a := Request{Verb: VerbList, Cluster: "a", Resource: "g/rs"}
	b := Request{Verb: VerbGet, Cluster: "b", Resource: "g/rs"}

	before := Counts{a: 3, b: 1}
	after := Counts{a: 5, b: 1}

	got := after.Sub(before)
	if len(got) != 1 || got[a] != 2 {
		t.Errorf("Sub() = %v, want exactly {%+v: 2}: unchanged keys drop out", got, a)
	}

	// Subtracting is not mutation: a sample is taken once and compared many
	// times, so an in-place implementation would corrupt every later delta.
	if after[a] != 5 || before[a] != 3 {
		t.Errorf("Sub() mutated its operands: before=%v after=%v", before, after)
	}
}
