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

package providerwiring_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
)

func sliceScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return scheme
}

func servedSlice(name, url string) *apisv1alpha1.APIExportEndpointSlice {
	return &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: apisv1alpha1.APIExportEndpointSliceStatus{
			APIExportEndpoints: []apisv1alpha1.APIExportEndpoint{{URL: url}},
		},
	}
}

func TestVirtualWorkspaceConfigResolvesAServedSlice(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(sliceScheme(t)).
		WithObjects(servedSlice("cluster-api-core", "https://kcp.example/services/apiexport/root/cluster-api-core")).
		Build()

	cfg, err := providerwiring.VirtualWorkspaceConfig(context.Background(), cl, "cluster-api-core",
		&rest.Config{Host: "https://kcp.example"}, time.Second)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if !strings.HasSuffix(cfg.Host, "/clusters/*") {
		t.Errorf("host = %q, want the wildcard path", cfg.Host)
	}
}

// TestADeploymentWaitsRatherThanFailing is the behaviour this file exists for.
//
// A zero timeout is what every manager deployment passes. The endpoint slice
// stays empty until a tenant binds the export, which can be days after the
// provider is installed — so bounding the wait made a fleet with no tenants
// yet look like a broken one, and the process crash looped until somebody
// onboarded.
func TestADeploymentWaitsRatherThanFailing(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(sliceScheme(t)).Build()

	// A deadline stands in for the process being asked to stop; nothing else
	// ends an unbounded wait.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := providerwiring.VirtualWorkspaceConfig(ctx, cl, "cluster-api-core", &rest.Config{Host: "https://kcp.example"}, 0)
	if err == nil {
		t.Fatal("resolving succeeded with no endpoint slice at all")
	}

	// It waited for the context rather than giving up on its own schedule.
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("gave up after %s, before the context ended: a deployment would crash loop", elapsed)
	}
	if strings.Contains(err.Error(), "after 1m0s") {
		t.Errorf("error %q reports a bound this call did not have", err)
	}
	// And it says what it was waiting for, which is the difference between a
	// process that is stuck and one that has nothing to do yet.
	for _, want := range []string{"no workspace had bound the export", "stopped while waiting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A caller that must fail rather than hang still can — the tests in this
// repository depend on it.
func TestAnExplicitTimeoutStillBounds(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(sliceScheme(t)).Build()

	start := time.Now()
	_, err := providerwiring.VirtualWorkspaceConfig(context.Background(), cl, "cluster-api-core",
		&rest.Config{Host: "https://kcp.example"}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("a bounded wait did not time out")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the bound was not honoured")
	}
	if !strings.Contains(err.Error(), "after 200ms") {
		t.Errorf("error %q does not name the bound it was given", err)
	}
}

// The wait ends as soon as the slice is served, so a tenant binding does not
// wait out a poll cycle plus a backoff.
func TestTheWaitEndsWhenATenantBinds(t *testing.T) {
	scheme := sliceScheme(t)
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&apisv1alpha1.APIExportEndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "cluster-api-core"}}).
		Build()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := providerwiring.VirtualWorkspaceConfig(ctx, cl, "cluster-api-core", &rest.Config{Host: "https://kcp.example"}, 0)
		done <- err
	}()

	// The binding arrives late, as a tenant does.
	time.Sleep(200 * time.Millisecond)
	var slice apisv1alpha1.APIExportEndpointSlice
	if err := cl.Get(ctx, client.ObjectKey{Name: "cluster-api-core"}, &slice); err != nil {
		t.Fatalf("reading the slice: %v", err)
	}
	slice.Status.APIExportEndpoints = []apisv1alpha1.APIExportEndpoint{{URL: "https://kcp.example/services/apiexport/root/cluster-api-core"}}
	if err := cl.Update(ctx, &slice); err != nil {
		t.Fatalf("serving the slice: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the wait did not resolve once the slice was served: %v", err)
		}
	case <-ctx.Done():
		t.Error("the wait did not notice the slice being served")
	}
}
