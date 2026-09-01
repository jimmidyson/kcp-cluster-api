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

package deployedscale

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTeardownAndWaitReturnsOnceTheNamespaceIsGone(t *testing.T) {
	cl := fakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "scale"}})
	if err := TeardownAndWait(context.Background(), cl, "scale", time.Second, 10*time.Millisecond); err != nil {
		t.Errorf("teardown: %v", err)
	}
	// Tearing down twice, or after an interrupted setup, must not fail.
	if err := TeardownAndWait(context.Background(), cl, "scale", time.Second, 10*time.Millisecond); err != nil {
		t.Errorf("tearing down an absent namespace failed: %v", err)
	}
}

// TestATerminatingNamespaceIsNotSilentlyAcceptedIsThePoint: the next spread
// deploys into this namespace, and one still terminating would be scheduled
// alongside the previous run's kcp and managers. The second measurement would
// then be of a cluster doing twice the work, with nothing in the report saying
// so — which is why the timeout says exactly that.
func TestATerminatingNamespaceIsReportedNotIgnored(t *testing.T) {
	// A namespace with a finalizer never goes away on a fake client, which is
	// what a terminating one looks like to a caller.
	stuck := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:       "scale",
		Finalizers: []string{"kubernetes.io/test"},
	}}
	cl := fakeClient(stuck)

	err := TeardownAndWait(context.Background(), cl, "scale", 100*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("a namespace that never went away was reported as torn down")
	}
	if !strings.Contains(err.Error(), "still terminating") {
		t.Errorf("error %q does not say the namespace is still going", err)
	}
	if !strings.Contains(err.Error(), "measure the previous one") {
		t.Errorf("error %q does not say why that matters", err)
	}
}
