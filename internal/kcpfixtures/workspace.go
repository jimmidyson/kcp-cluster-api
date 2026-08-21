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

package kcpfixtures

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// DefaultWorkspaceType is the type new workspaces are created with when the
// caller names none: kcp's own "universal" type, which carries no initializers
// of its own.
//
// The workspaces that hold clusters are not created with it. They use the
// Cluster API WorkspaceType (internal/capiworkspaces), which binds Cluster
// API's APIExport and writes the workspace's roles before it is ready. This
// one is for the workspaces around them - an org, a tenant's home - which hold
// no Cluster API objects and need none of that.
var DefaultWorkspaceType = tenancyv1alpha1.WorkspaceTypeReference{
	Name: tenancyv1alpha1.WorkspaceTypeName("universal"),
	Path: "root",
}

// NewWorkspace builds the Workspace object EnsureWorkspace creates. It is
// separate so the shape can be asserted without a server.
func NewWorkspace(name string, workspaceType tenancyv1alpha1.WorkspaceTypeReference) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: workspaceType.DeepCopy(),
		},
	}
}

// EnsureWorkspace creates a workspace named name under the workspace cl is
// scoped to, waits for it to become Ready, and returns its internal logical
// cluster name - the identifier the multicluster provider engages workspaces
// by, which is not the human-readable path.
//
// Idempotent: an existing workspace is left alone and only waited on, so a
// dev loop can be re-run against a long-lived kcp server.
//
// This is kcp's own e2e workspace fixture (github.com/kcp-dev/sdk/testing)
// reduced to what a non-test caller can use: that one takes a *testing.T,
// registers cleanup on it, and fails the test rather than returning an
// error, so a demo or an operator tool cannot call it at all.
func EnsureWorkspace(ctx context.Context, cl client.Client, name string, timeout time.Duration) (string, error) {
	return EnsureWorkspaceOfType(ctx, cl, name, DefaultWorkspaceType, timeout)
}

// EnsureWorkspaceOfType is EnsureWorkspace with the WorkspaceType named.
//
// A type carrying an initializer changes what "ready" means here rather than
// only what the workspace contains: kcp holds the workspace out of Ready until
// every initializer has removed itself, so this waits for the controller
// behind the type as well as for kcp. A run that creates a Cluster API
// workspace without running the workspace manager therefore times out here,
// which is the right place to find out.
func EnsureWorkspaceOfType(ctx context.Context, cl client.Client, name string, workspaceType tenancyv1alpha1.WorkspaceTypeReference, timeout time.Duration) (string, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	// The create is retried rather than attempted once. It can fail for
	// reasons that pass on their own: kcp's admission controller rejects a
	// workspace whose WorkspaceType its cache has not seen yet, and a client
	// built moments after the server started can hold a discovery document
	// with no tenancy API in it at all. kcp's own e2e fixture retries the
	// create for the first of those reasons; the second is what a demo run
	// against a server it started itself hits.
	ws := NewWorkspace(name, workspaceType)
	var lastCreateErr error
	createErr := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		err := cl.Create(ctx, ws.DeepCopy())
		if err == nil || apierrors.IsAlreadyExists(err) {
			return true, nil
		}
		lastCreateErr = err
		return false, nil
	})
	if createErr != nil {
		return "", fmt.Errorf("creating workspace %s: %w (last attempt: %v)", name, createErr, lastCreateErr)
	}

	var clusterName string
	// What the workspace last said about itself, so that a timeout can report
	// why rather than only that. A workspace held out of Ready is almost
	// always waiting on an initializer, and which one is the whole diagnosis -
	// "waiting for workspace X to become ready: context deadline exceeded" is
	// a sentence that has never helped anybody.
	var lastPhase corev1alpha1.LogicalClusterPhaseType
	var lastReason string
	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		got := &tenancyv1alpha1.Workspace{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name}, got); err != nil {
			return false, nil //nolint:nilerr // transient; keep polling until timeout.
		}
		lastPhase, lastReason = got.Status.Phase, conditionSummary(got)
		if got.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
			return false, nil
		}
		// Ready without a cluster name would leave the caller with nothing to
		// engage, so both are the condition rather than just the phase.
		clusterName = got.Spec.Cluster
		return clusterName != "", nil
	})
	if err != nil {
		return "", fmt.Errorf("waiting for workspace %s to become ready: %w (phase %q; %s)",
			name, err, lastPhase, lastReason)
	}
	return clusterName, nil
}

// conditionSummary renders the conditions that are not True, which is the set
// that explains a workspace that is not ready.
func conditionSummary(ws *tenancyv1alpha1.Workspace) string {
	var unmet []string
	for _, c := range ws.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			continue
		}
		unmet = append(unmet, fmt.Sprintf("%s=%s (%s: %s)", c.Type, c.Status, c.Reason, c.Message))
	}
	if len(unmet) == 0 {
		return "no unmet conditions reported"
	}
	return strings.Join(unmet, "; ")
}
