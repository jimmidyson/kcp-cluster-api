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
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type fakeManagerGetter struct {
	// failuresBeforeSuccess is how many times GetManager should return an
	// error before it starts succeeding.
	failuresBeforeSuccess int32
	calls                 atomic.Int32
	result                manager.Manager
	permanentErr          error
}

func (f *fakeManagerGetter) GetManager(_ context.Context, _ multicluster.ClusterName) (manager.Manager, error) {
	n := f.calls.Add(1)
	if f.permanentErr != nil {
		return nil, f.permanentErr
	}
	if n <= f.failuresBeforeSuccess {
		return nil, errors.New("cluster not yet engaged")
	}
	return f.result, nil
}

func TestWaitForManagerSucceedsAfterRetries(t *testing.T) {
	want := fakeManager{}
	fg := &fakeManagerGetter{failuresBeforeSuccess: 3, result: want}

	got, err := WaitForManager(t.Context(), fg, multicluster.ClusterName("test-cluster"), time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("WaitForManager() error = %v, want nil", err)
	}
	if got != want {
		t.Fatalf("WaitForManager() = %v, want %v", got, want)
	}
	if calls := fg.calls.Load(); calls != 4 {
		t.Fatalf("GetManager called %d times, want 4 (3 failures + 1 success)", calls)
	}
}

func TestWaitForManagerTimesOut(t *testing.T) {
	fg := &fakeManagerGetter{permanentErr: errors.New("never engaged")}

	_, err := WaitForManager(t.Context(), fg, multicluster.ClusterName("test-cluster"), time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForManager() error = nil, want a timeout error")
	}
}

func TestWaitForManagerRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	fg := &fakeManagerGetter{permanentErr: errors.New("never engaged")}

	cancel()
	_, err := WaitForManager(ctx, fg, multicluster.ClusterName("test-cluster"), time.Millisecond, time.Minute)
	if err == nil {
		t.Fatal("WaitForManager() error = nil, want an error from the already-cancelled context")
	}
}

// fakeManager is a zero-cost, comparable stand-in for manager.Manager: the
// tests above only need to assert that waitForManager returns whatever
// GetManager gave it back, not that it behaves like a real manager.
type fakeManager struct {
	manager.Manager
}
