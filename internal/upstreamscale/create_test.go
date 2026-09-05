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
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// warmingWebhook is verbatim what stopped a stock climb at its second rung.
//
// Note what it does not have: a Reason. controller-runtime's Errored builds a
// response carrying a code and a message and nothing else, and the API server
// turns that into a status with the same shape — so apierrors.IsInternalError
// is false for it, because that asks for the reason. Anything checking only the
// reason lets this through as a permanent failure.
func warmingWebhook() error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure,
		Code:   500,
		Message: `admission webhook "default.cluster.cluster.x-k8s.io" denied the request: ` +
			`Internal error occurred: Cluster c0120 can't be defaulted. ClusterClass demo ` +
			`can not be retrieved: Timeout: failed waiting for *v1beta2.ClusterClass Informer to sync`,
	}}
}

// TestAWebhookWhoseCacheIsWarmingIsNotACeiling.
//
// A climb held 500 clusters, defragmented, and abandoned the 1000 rung two
// seconds in on this error. Reported as a ceiling it would say a management
// cluster cannot hold a thousand clusters, which is not what happened and is
// the kind of number that gets quoted.
func TestAWebhookWhoseCacheIsWarmingIsNotACeiling(t *testing.T) {
	if !Transient(warmingWebhook()) {
		t.Error("a webhook reporting its own cache is not ready was taken as a permanent refusal")
	}
}

// TestARefusalAboutTheObjectIsNotRetried. Invalid, Forbidden and NotFound are
// answers: asking again returns the same one, and a run that keeps asking has
// swapped a clear failure for a slow one.
func TestARefusalAboutTheObjectIsNotRetried(t *testing.T) {
	gr := schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "clusters"}
	for _, err := range []error{
		apierrors.NewInvalid(schema.GroupKind{Kind: "Cluster"}, "c0120", nil),
		apierrors.NewForbidden(gr, "c0120", errors.New("nope")),
		apierrors.NewNotFound(gr, "c0120"),
		apierrors.NewConflict(gr, "c0120", errors.New("nope")),
	} {
		if Transient(err) {
			t.Errorf("%v was treated as something that would pass on its own", err)
		}
	}
}

// TestTheOtherWaysAServerSaysNotNow, each of which a climbing run meets: 429
// from priority and fairness, 503 from a control plane still starting, and a
// dead connection to a webhook service whose endpoints have gone.
func TestTheOtherWaysAServerSaysNotNow(t *testing.T) {
	gr := schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "clusters"}
	for _, err := range []error{
		apierrors.NewTooManyRequests("busy", 1),
		apierrors.NewServiceUnavailable("starting"),
		apierrors.NewServerTimeout(gr, "create", 1),
		apierrors.NewTimeoutError("timed out", 1),
	} {
		if !Transient(err) {
			t.Errorf("%v was treated as a permanent refusal", err)
		}
	}
	if Transient(nil) {
		t.Error("no error at all was treated as a transient one")
	}
}

// TestARetryGivesUpAndSaysHowLongItTried, because a server that is never ready
// must still stop the climb — with the right reason, not with silence.
func TestARetryGivesUpAndSaysHowLongItTried(t *testing.T) {
	attempts := 0
	err := retry{Budget: 20 * time.Millisecond, First: time.Millisecond, Max: 2 * time.Millisecond}.
		do(t.Context(), "creating cluster demo/c0120", func() error {
			attempts++
			return warmingWebhook()
		})
	if err == nil {
		t.Fatal("a server that never became ready let the climb continue")
	}
	if attempts < 2 {
		t.Errorf("gave up after %d attempt(s), so nothing was retried", attempts)
	}
	if !strings.Contains(err.Error(), "creating cluster demo/c0120") {
		t.Errorf("the failure does not say what was being created: %v", err)
	}
	if !strings.Contains(err.Error(), "attempts over") {
		t.Errorf("the failure does not say how long it was retried for: %v", err)
	}
	if !strings.Contains(err.Error(), "Informer to sync") {
		t.Errorf("the server's own last words were lost: %v", err)
	}
}

// TestARetryStopsAsSoonAsItWorks, and does not count an object that already
// exists as a failure — a half-built rung is re-applied on the way back up.
func TestARetryStopsAsSoonAsItWorks(t *testing.T) {
	attempts := 0
	err := retry{First: time.Millisecond, Max: time.Millisecond}.
		do(t.Context(), "creating cluster demo/c0120", func() error {
			attempts++
			if attempts < 3 {
				return warmingWebhook()
			}
			return nil
		})
	if err != nil {
		t.Fatalf("a rejection that passed still failed the rung: %v", err)
	}
	if attempts != 3 {
		t.Errorf("%d attempts, want it to stop at the one that worked", attempts)
	}

	gr := schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "clusters"}
	if err := (retry{}).do(t.Context(), "recreate", func() error {
		return apierrors.NewAlreadyExists(gr, "c0120")
	}); err != nil {
		t.Errorf("an object that already existed was a failure: %v", err)
	}
}

// TestAnInterruptedRetrySaysWhatItWasWaitingOn, so a run cancelled during a
// rollout does not report only "context canceled".
func TestAnInterruptedRetrySaysWhatItWasWaitingOn(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := retry{First: time.Hour}.do(ctx, "creating cluster demo/c0120", warmingWebhook)
	if err == nil {
		t.Fatal("a cancelled retry succeeded")
	}
	if !strings.Contains(err.Error(), "Informer to sync") {
		t.Errorf("the rejection it was riding out was lost: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the cancellation is not recognisable: %v", err)
	}
}
