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

package coremanager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
)

// TestWorkspaceSecretReaderAddressesTheWorkspaceOnce is the regression test for
// a deployed run in which no Machine ever became Ready.
//
// The kubeconfig a deployed manager mounts addresses root, because that is the
// workspace it looks for its APIExportEndpointSlice in. kcp's client cache
// builds a per-workspace client by appending to the host it is given, so every
// kubeconfig Secret was fetched from
//
//	/clusters/root/clusters/<workspace>/api/v1/namespaces/default/secrets/…
//
// kcp answers a path like that with a bare 404, which surfaces as
//
//	error getting kubeconfig secret: the server could not find the requested
//	resource (get secrets t0000-c000-kubeconfig)
//
// reading as a Secret that has not been created yet rather than a URL that
// cannot exist. Cluster API's clustercache then never connected to a workload
// cluster, no Machine got a node reference, and the run timed out with
// "0 of 2 machines Ready" — three layers away from the cause.
//
// In-process runs never saw it: their base config carries no workspace, so the
// append is correct there and only there.
func TestWorkspaceSecretReaderAddressesTheWorkspaceOnce(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"c0-kubeconfig","namespace":"default"}}`))
	}))
	defer server.Close()

	// A root-scoped config, exactly as the deployed kubeconfig is.
	reader, err := NewWorkspaceSecretReader(&rest.Config{Host: server.URL + "/clusters/root"})
	if err != nil {
		t.Fatalf("building the reader: %v", err)
	}

	ctx := mccontext.WithCluster(t.Context(), "2fj3k")
	var secret corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: "default", Name: "c0-kubeconfig"}, &secret); err != nil {
		t.Fatalf("reading the Secret: %v", err)
	}

	if len(paths) == 0 {
		t.Fatal("no request reached the server")
	}
	got := paths[len(paths)-1]
	if strings.Count(got, "/clusters/") != 1 {
		t.Errorf("the request addressed a workspace %d times, want once:\n%s",
			strings.Count(got, "/clusters/"), got)
	}
	if !strings.HasPrefix(got, "/clusters/2fj3k/") {
		t.Errorf("the request did not address the workspace in context:\n%s", got)
	}
}
