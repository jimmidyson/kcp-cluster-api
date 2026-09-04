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

package upstreamscale

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
)

func kcpScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	for _, add := range []func(*runtime.Scheme) error{
		apisv1alpha2.AddToScheme,
		tenancyv1alpha1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("registering kcp types: %v", err)
		}
	}
	return s
}

func exportObject(name string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// TestAnUnpublishedExportIsCaughtBeforeAnythingIsCreated.
//
// An export that was never published fails every binding in every workspace,
// and without this the first thing to say so is a rung into a climb, with a
// message about one binding rather than about the installation.
func TestAnUnpublishedExportIsCaughtBeforeAnythingIsCreated(t *testing.T) {
	providers := capiexports.All()
	s := kcpScheme(t)

	// Every export but the first.
	objects := make([]client.Object, 0, len(providers))
	for _, p := range providers[1:] {
		objects = append(objects, exportObject(p.Export))
	}
	tenancy := &WorkspaceTenancy{
		Root:      fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build(),
		Base:      &rest.Config{Host: "https://kcp.example:6443"},
		Scheme:    s,
		Providers: providers,
	}

	err := tenancy.Preflight(context.Background())
	if err == nil {
		t.Fatal("a shard missing an export was reported as ready")
	}
	if !strings.Contains(err.Error(), providers[0].Export) {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// TestAPublishedShardPassesPreflight, so that the check is a check rather than
// a refusal.
func TestAPublishedShardPassesPreflight(t *testing.T) {
	providers := capiexports.All()
	s := kcpScheme(t)

	objects := make([]client.Object, 0, len(providers))
	for _, p := range providers {
		objects = append(objects, exportObject(p.Export))
	}
	tenancy := &WorkspaceTenancy{
		Root:      fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build(),
		Base:      &rest.Config{Host: "https://kcp.example:6443"},
		Scheme:    s,
		Providers: providers,
	}

	if err := tenancy.Preflight(context.Background()); err != nil {
		t.Errorf("a shard with every export published was refused: %v", err)
	}
}

// TestAShardWithNoClientIsNotAShard. The kcp side is given its clients by the
// driver, and a nil one would fail at the first workspace with a panic rather
// than a sentence.
func TestAShardWithNoClientIsNotAShard(t *testing.T) {
	if err := (&WorkspaceTenancy{}).Preflight(context.Background()); err == nil {
		t.Error("a tenancy with no shard behind it passed preflight")
	}
}

// TestAWorkspaceIsGoneOnlyWhenItIsGone.
//
// Deleting a workspace is not instant, and one still going is one the next run
// measures as its baseline — which is exactly the failure the stock side's
// teardown was rewritten to stop.
func TestAWorkspaceIsGoneOnlyWhenItIsGone(t *testing.T) {
	s := kcpScheme(t)
	ws := &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "capi-scale-0000"}}
	tenancy := &WorkspaceTenancy{
		Root:      fake.NewClientBuilder().WithScheme(s).WithObjects(ws).Build(),
		Base:      &rest.Config{Host: "https://kcp.example:6443"},
		Scheme:    s,
		Providers: capiexports.All(),
	}
	ctx := context.Background()

	if gone, err := tenancy.Gone(ctx, "capi-scale-0000"); err != nil || gone {
		t.Errorf("a workspace that is still there reads as gone (%v, %v)", gone, err)
	}
	if err := tenancy.Remove(ctx, "capi-scale-0000"); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if gone, err := tenancy.Gone(ctx, "capi-scale-0000"); err != nil || !gone {
		t.Errorf("a deleted workspace does not read as gone (%v, %v)", gone, err)
	}
	// And removing one that was never there is not an error: a teardown after a
	// half-built rung asks for workspaces that may not exist.
	if err := tenancy.Remove(ctx, "capi-scale-9999"); err != nil {
		t.Errorf("removing a workspace that is not there: %v", err)
	}
}
