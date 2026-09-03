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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func teardownClient(t *testing.T, funcs interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).WithInterceptorFuncs(funcs).Build()
}

func namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func fleetCluster(namespace, name string, finalizers ...string) *clusterv1.Cluster {
	return &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, Finalizers: finalizers,
	}}
}

// TestTeardownDeletesClustersBeforeNamespaces. Deleting a namespace stamps
// every object in it at once, and stock Cluster API cannot finish from there:
// the DevCluster releases its finalizer immediately and every DevMachine then
// waits forever for a DevCluster that is never coming back, which holds the
// Machines, which holds the Clusters, which holds the namespace. So the
// Clusters go first, in Cluster API's own order, and the namespace only once
// none of them remain.
func TestTeardownDeletesClustersBeforeNamespaces(t *testing.T) {
	var order []string
	record := interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			switch o := obj.(type) {
			case *corev1.Namespace:
				order = append(order, "namespace/"+o.Name)
			case *clusterv1.Cluster:
				order = append(order, "cluster/"+o.Namespace+"/"+o.Name)
			}
			return cl.Delete(ctx, obj, opts...)
		},
	}
	cl := teardownClient(t, record,
		namespace("capi-scale-0000"), namespace("capi-scale-0001"),
		fleetCluster("capi-scale-0000", "c0000"), fleetCluster("capi-scale-0000", "c0001"),
		fleetCluster("capi-scale-0001", "c0002"),
	)

	// The same namespace twice: a run re-plans the whole fleet every rung, so
	// the driver's list of what it created repeats itself.
	err := Teardown(context.Background(), cl, []string{"capi-scale-0000", "capi-scale-0001", "capi-scale-0000"},
		time.Second, time.Millisecond, nil)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}

	firstNamespace := -1
	lastCluster := -1
	for i, step := range order {
		if strings.HasPrefix(step, "namespace/") && firstNamespace < 0 {
			firstNamespace = i
		}
		if strings.HasPrefix(step, "cluster/") {
			lastCluster = i
		}
	}
	if lastCluster < 0 || firstNamespace < 0 || lastCluster > firstNamespace {
		t.Fatalf("a namespace was deleted before its Clusters were: %v", order)
	}
	if got := strings.Count(strings.Join(order, " "), "namespace/"); got != 2 {
		t.Errorf("namespace deletes = %d, want one per distinct namespace: %v", got, order)
	}

	var clusters clusterv1.ClusterList
	if err := cl.List(context.Background(), &clusters); err != nil {
		t.Fatal(err)
	}
	if len(clusters.Items) != 0 {
		t.Errorf("%d Clusters remain after teardown", len(clusters.Items))
	}
	for _, name := range []string{"capi-scale-0000", "capi-scale-0001"} {
		err := cl.Get(context.Background(), client.ObjectKey{Name: name}, &corev1.Namespace{})
		if !apierrors.IsNotFound(err) {
			t.Errorf("namespace %s remains after teardown (err=%v)", name, err)
		}
	}
}

// TestTeardownWaitsForClustersAndSaysWhatRemains. A Cluster that will not go
// keeps its namespace: deleting the namespace anyway is what breaks stock
// Cluster API's deletion, so a teardown that runs out of patience reports what
// it is still waiting for — by name, with Cluster API's own account of why —
// rather than stamping everything and leaving a namespace stuck Terminating.
func TestTeardownWaitsForClustersAndSaysWhatRemains(t *testing.T) {
	stuck := fleetCluster("capi-scale-0000", "c0001", "cluster.cluster.x-k8s.io")
	stuck.Status.Conditions = []metav1.Condition{{
		Type: clusterv1.ClusterDeletingCondition, Status: metav1.ConditionTrue,
		Message: "Waiting for KubeadmControlPlane to be deleted",
	}}
	cl := teardownClient(t, interceptor.Funcs{},
		namespace("capi-scale-0000"), fleetCluster("capi-scale-0000", "c0000"), stuck)

	err := Teardown(context.Background(), cl, []string{"capi-scale-0000"}, 30*time.Millisecond, time.Millisecond, nil)
	if err == nil {
		t.Fatal("a teardown with a Cluster still present reported success")
	}
	for _, want := range []string{"capi-scale-0000/c0001", "Waiting for KubeadmControlPlane"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say what remains: want %q in %q", want, err)
		}
	}
	if strings.Contains(err.Error(), "c0000") {
		t.Errorf("a Cluster that was deleted is reported as remaining: %q", err)
	}

	if err := cl.Get(context.Background(), client.ObjectKey{Name: "capi-scale-0000"}, &corev1.Namespace{}); err != nil {
		t.Errorf("the namespace was deleted over a Cluster still in it: %v", err)
	}
}

// TestTeardownToleratesAPreviousRunsAbsence. Safe against a cluster where a
// previous run died, or one where the operator already cleaned up: a namespace
// that is not there is not an error.
func TestTeardownToleratesAPreviousRunsAbsence(t *testing.T) {
	cl := teardownClient(t, interceptor.Funcs{})
	if err := Teardown(context.Background(), cl, []string{"capi-scale-0000"}, time.Second, time.Millisecond, nil); err != nil {
		t.Fatalf("teardown of a namespace that does not exist: %v", err)
	}
	if err := Teardown(context.Background(), cl, nil, time.Second, time.Millisecond, nil); err != nil {
		t.Fatalf("teardown of nothing: %v", err)
	}
}
