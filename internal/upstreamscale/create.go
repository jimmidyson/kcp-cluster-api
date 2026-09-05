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
	"fmt"
	"net"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Transient reports whether an API error is one that passes on its own.
//
// # The rung this exists for
//
// A stock climb held 500 clusters, defragmented, and then abandoned the 1000
// rung two seconds in on the twentieth Cluster:
//
//	admission webhook "default.cluster.cluster.x-k8s.io" denied the request:
//	Internal error occurred: Cluster c0120 can't be defaulted. ClusterClass demo
//	can not be retrieved: Timeout: failed waiting for *v1beta2.ClusterClass
//	Informer to sync
//
// That is Cluster API's own defaulting webhook reporting that its cache was not
// ready yet — the manager could not list ClusterClasses inside the informer's
// sync timeout. It says nothing about whether the management cluster can hold a
// thousand clusters, which is the only question the rung was asked. A ceiling
// recorded from it is a false one, and a false ceiling is worse than no run:
// it is a number someone will quote.
//
// # Why retrying is not papering over the failure
//
// The distinction is between a rejection about this object and a rejection
// about the server's own readiness. AlreadyExists, Invalid and Forbidden are
// answers: retrying them returns the same answer forever. A 500 from a webhook
// whose cache is warming, a 503 from an API server that is starting, a 429 from
// priority and fairness, a dropped connection — these are the server saying
// "not now", and a client that does not hear that is measuring its own
// impatience.
//
// The retry is bounded and the failure says how long it went on for, so a
// server that is never ready still stops the climb — it just stops it with the
// right reason attached.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case apierrors.IsInternalError(err),
		apierrors.IsServiceUnavailable(err),
		apierrors.IsTooManyRequests(err),
		apierrors.IsServerTimeout(err),
		apierrors.IsTimeout(err),
		apierrors.IsUnexpectedServerError(err):
		return true
	}

	// A webhook that errors is wrapped in a status whose reason is empty and
	// whose code is the webhook's — the case above misses it, because
	// IsInternalError asks for the reason rather than the code. This is
	// exactly the ClusterClass informer rejection.
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		switch status.Status().Code {
		case 429, 500, 502, 503, 504:
			return true
		}
	}

	// Nothing reached the server at all: a rolling manager's webhook service
	// with no endpoints behind it, or a control plane mid-restart.
	var netErr net.Error
	return errors.As(err, &netErr)
}

// retry is a bounded backoff. Zero values mean the defaults, so a caller that
// does not care about pacing passes retry{}.
type retry struct {
	// Budget is how long transient rejections are tolerated for before the
	// climb gives up. Long enough for a manager to finish a rollout and warm
	// its caches; short enough that a server which will never be ready does
	// not hold a run open for the rest of the day.
	Budget time.Duration
	// First and Max bound the delay between attempts.
	First time.Duration
	Max   time.Duration
}

func (r retry) withDefaults() retry {
	if r.Budget <= 0 {
		r.Budget = 2 * time.Minute
	}
	if r.First <= 0 {
		r.First = time.Second
	}
	if r.Max <= 0 {
		r.Max = 15 * time.Second
	}
	return r
}

// do calls fn until it succeeds, fails for a reason retrying cannot change, or
// the budget runs out.
func (r retry) do(ctx context.Context, what string, fn func() error) error {
	r = r.withDefaults()
	started := time.Now()
	delay := r.First

	for attempt := 1; ; attempt++ {
		err := fn()
		switch {
		case err == nil, apierrors.IsAlreadyExists(err):
			return nil
		case !Transient(err):
			return err
		}

		if waited := time.Since(started); waited >= r.Budget {
			return fmt.Errorf("%s was still being refused for a reason that should have "+
				"passed, after %d attempts over %s: %w",
				what, attempt, waited.Round(time.Second), err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w (last rejection: %w)", what, ctx.Err(), err)
		case <-time.After(delay):
		}
		if delay < r.Max {
			delay = min(delay*2, r.Max)
		}
	}
}

// createRetrying creates one object, riding out the rejections that are about
// the server rather than about the object.
func createRetrying(ctx context.Context, cl client.Writer, obj client.Object, what string) error {
	return retry{}.do(ctx, what, func() error { return cl.Create(ctx, obj) })
}
