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

package capiworkspaces

import (
	"context"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// binding is an APIBinding that has bound, accepting claims of which some
// number have been applied.
func binding(name string, accepted, applied int) *apisv1alpha2.APIBinding {
	b := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     apisv1alpha2.APIBindingStatus{Phase: apisv1alpha2.APIBindingPhaseBound},
	}
	for range accepted {
		b.Spec.PermissionClaims = append(b.Spec.PermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
			State: apisv1alpha2.ClaimAccepted,
		})
	}
	for range applied {
		b.Status.AppliedPermissionClaims = append(b.Status.AppliedPermissionClaims, apisv1alpha2.ScopedPermissionClaim{})
	}
	return b
}

func TestNudgeOnlyTouchesBindingsThatAreStuck(t *testing.T) {
	ctx := context.Background()
	stuck := binding("stuck", 6, 2)
	// Nothing to wait for.
	settled := binding("settled", 6, 6)
	// No claims at all.
	plain := binding("plain", 0, 0)
	// Still being worked on: poking this one would fight the controller
	// rather than restart it.
	binding3 := binding("binding", 6, 0)
	binding3.Status.Phase = apisv1alpha2.APIBindingPhaseBinding

	cl := workspaceClient(t, stuck, settled, plain, binding3)

	nudged, err := NudgeUnappliedClaims(ctx, cl)
	if err != nil {
		t.Fatalf("NudgeUnappliedClaims() = %v", err)
	}
	if len(nudged) != 1 || nudged[0] != "stuck" {
		t.Fatalf("NudgeUnappliedClaims() poked %v, want only the stuck binding", nudged)
	}

	got := &apisv1alpha2.APIBinding{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "stuck"}, got); err != nil {
		t.Fatalf("reading the binding: %v", err)
	}
	if got.Annotations[ClaimRetryAnnotation] != "1" {
		t.Errorf("the retry annotation is %q, want a counter starting at 1", got.Annotations[ClaimRetryAnnotation])
	}
}

// A workspace that is stuck for some other reason must not turn into a write
// every few seconds for as long as it exists.
func TestNudgeGivesUp(t *testing.T) {
	ctx := context.Background()
	stuck := binding("stuck", 6, 2)
	cl := workspaceClient(t, stuck)

	for range MaxClaimRetries {
		if _, err := NudgeUnappliedClaims(ctx, cl); err != nil {
			t.Fatalf("NudgeUnappliedClaims() = %v", err)
		}
	}

	got := &apisv1alpha2.APIBinding{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "stuck"}, got); err != nil {
		t.Fatalf("reading the binding: %v", err)
	}
	if got.Annotations[ClaimRetryAnnotation] != strconv.Itoa(MaxClaimRetries) {
		t.Fatalf("the retry counter is %q after %d tries", got.Annotations[ClaimRetryAnnotation], MaxClaimRetries)
	}

	nudged, err := NudgeUnappliedClaims(ctx, cl)
	if err != nil {
		t.Fatalf("NudgeUnappliedClaims() = %v", err)
	}
	if len(nudged) != 0 {
		t.Errorf("NudgeUnappliedClaims() poked %v after giving up", nudged)
	}
}
