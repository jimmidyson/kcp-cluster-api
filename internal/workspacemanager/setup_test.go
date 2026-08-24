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

package workspacemanager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
)

// A workspace manager is a Deployment, and a Deployment starts when Kubernetes
// starts it - which is not after somebody has published the WorkspaceType it
// initializes. It waits for the type rather than exiting, so that applying a
// whole installation at once converges instead of crash-looping through it.
func TestWaitForWorkspaceTypeWaitsForOneThatArrivesLate(t *testing.T) {
	t.Parallel()

	scheme, err := Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(100 * time.Millisecond)
		if err := cl.Create(context.Background(), &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: string(capiworkspaces.WorkspaceTypeName)},
		}); err != nil {
			t.Errorf("creating the WorkspaceType: %v", err)
		}
	}()
	defer wg.Wait()

	workspaceType, err := waitForWorkspaceType(t.Context(), cl, "root", 10*time.Second)
	if err != nil {
		t.Fatalf("waiting for the WorkspaceType: %v", err)
	}
	if got := workspaceType.Name; got != string(capiworkspaces.WorkspaceTypeName) {
		t.Errorf("got WorkspaceType %q, want %q", got, capiworkspaces.WorkspaceTypeName)
	}
}

// The wait is bounded, and what it reports when it runs out has to name the
// thing that is missing and where it was looked for: a manager that says only
// "timed out" sends its reader to the wrong workspace.
func TestWaitForWorkspaceTypeGivesUpAndSaysWhat(t *testing.T) {
	t.Parallel()

	scheme, err := Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err = waitForWorkspaceType(t.Context(), cl, "root:providers", 50*time.Millisecond)
	if err == nil {
		t.Fatal("waiting for a WorkspaceType that never arrives returned no error")
	}
	for _, want := range []string{string(capiworkspaces.WorkspaceTypeName), "root:providers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An error that is not "not found yet" is a mistake to report at once: a
// client that cannot read the type at all will not start reading it in a
// minute, and waiting out the timeout buries the reason.
func TestWaitForWorkspaceTypeReportsARealFailureImmediately(t *testing.T) {
	t.Parallel()

	scheme, err := Scheme()
	if err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	cl := &failingClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		err:    &forbidden{},
	}

	start := time.Now()
	if _, err := waitForWorkspaceType(t.Context(), cl, "root", time.Minute); err == nil {
		t.Fatal("a forbidden read returned no error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("a forbidden read took %s to report: it should not be retried to the timeout", elapsed)
	}
}

type failingClient struct {
	client.Client
	err error
}

func (c *failingClient) Get(_ context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	return c.err
}

type forbidden struct{}

func (f *forbidden) Error() string { return "workspacetypes.tenancy.kcp.io is forbidden" }

func (f *forbidden) Status() metav1.Status {
	return metav1.Status{Reason: metav1.StatusReasonForbidden, Code: 403}
}
