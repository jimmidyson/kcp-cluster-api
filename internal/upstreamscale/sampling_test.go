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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func pod(name string, opts ...func(*corev1.Pod)) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "capi-system"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, opt := range opts {
		opt(&p)
	}
	return p
}

func terminating(p *corev1.Pod) {
	p.DeletionTimestamp = &metav1.Time{}
	p.Finalizers = []string{"keep"}
}
func pending(p *corev1.Pod) { p.Status.Phase = corev1.PodPending }
func labelled(l map[string]string) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Labels = l }
}
func inNamespace(ns string) func(*corev1.Pod) {
	return func(p *corev1.Pod) { p.Namespace = ns }
}

// TestEveryRunningReplicaIsFound, in name order.
//
// Ordered for the reason the store's members are: a set read in whatever order
// the API server listed them gives replica #1 a different process on every
// sample, and the series that produces cannot be plotted against anything.
func TestEveryRunningReplicaIsFound(t *testing.T) {
	pods := []corev1.Pod{
		pod("kcp-7d9f-zzz"),
		pod("kcp-7d9f-aaa"),
		pod("kcp-7d9f-mmm"),
	}
	got := RunningPodsOf(pods)
	if len(got) != 3 {
		t.Fatalf("found %d replicas, want 3: one instance's figure is a third of a control plane", len(got))
	}
	if got[0].Name > got[1].Name || got[1].Name > got[2].Name {
		t.Errorf("replicas came back unordered: %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestAReplicaOnItsWayOutIsNotSampled. A terminating or not-yet-running pod
// serves no pprof, and counting it would report the set as larger than the
// processes actually paying for the fleet.
func TestAReplicaOnItsWayOutIsNotSampled(t *testing.T) {
	pods := []corev1.Pod{
		pod("kcp-7d9f-aaa"),
		pod("kcp-7d9f-bbb", terminating),
		pod("kcp-7d9f-ccc", pending),
	}
	got := RunningPodsOf(pods)
	if len(got) != 1 || got[0].Name != "kcp-7d9f-aaa" {
		t.Errorf("sampled %v, want only the running replica", names(got))
	}
}

// TestAPodOfAnotherDeploymentIsNotAReplica.
//
// A pod is named <deployment>-<replicaset hash>-<suffix>, so matching on the
// name looks sound right up until two deployments share a prefix — and
// clusterctl installs four managers built from the same stem. The replicas are
// found through the Deployment's own selector for that reason: a prefix test
// would sum two managers under one name, and the total would still look like a
// plausible controller.
func TestAPodOfAnotherDeploymentIsNotAReplica(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	mine := map[string]string{"control-plane": "controller-manager", "provider": "core"}
	theirs := map[string]string{"control-plane": "controller-manager", "provider": "extra"}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		deployment("capi-controller-manager", mine),
		deployment("capi-controller-manager-extra", theirs),
		ptrTo(pod("capi-controller-manager-6f8-aaa", labelled(mine))),
		ptrTo(pod("capi-controller-manager-extra-6f8-bbb", labelled(theirs))),
	).Build()

	got, err := ReplicasOf(context.Background(), cl, "capi-system", "capi-controller-manager")
	if err != nil {
		t.Fatalf("finding the replicas: %v", err)
	}
	if len(got) != 1 || got[0].Name != "capi-controller-manager-6f8-aaa" {
		t.Errorf("matched %v, want only this deployment's pod", names(got))
	}
}

// TestEveryReplicaOfADeploymentIsFound, whatever the deployment is called: the
// selector says which pods are its, and three of them are three processes.
func TestEveryReplicaOfADeploymentIsFound(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	shard := map[string]string{"app": "kcp"}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		deployment("kcp", shard),
		ptrTo(pod("kcp-7d9f-ccc", labelled(shard))),
		ptrTo(pod("kcp-7d9f-aaa", labelled(shard))),
		ptrTo(pod("kcp-7d9f-bbb", labelled(shard))),
	).Build()

	got, err := ReplicasOf(context.Background(), cl, "capi-system", "kcp")
	if err != nil {
		t.Fatalf("finding the replicas: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("found %v, want all three replicas", names(got))
	}
	if got[0].Name != "kcp-7d9f-aaa" {
		t.Errorf("replicas came back unordered: %v", names(got))
	}
}

func deployment(name string, selector map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "capi-system"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}},
	}
}

// TestTheControlPlaneIsFoundByLabel, so that the three API servers behind a VIP
// are read by name rather than through whichever one the load balancer picked.
func TestTheControlPlaneIsFoundByLabel(t *testing.T) {
	loc := KubeAPIServers()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		ptrTo(pod("kube-apiserver-cp-2", inNamespace(loc.Namespace), labelled(loc.Labels))),
		ptrTo(pod("kube-apiserver-cp-0", inNamespace(loc.Namespace), labelled(loc.Labels))),
		ptrTo(pod("kube-apiserver-cp-1", inNamespace(loc.Namespace), labelled(loc.Labels))),
		ptrTo(pod("etcd-cp-0", inNamespace(loc.Namespace), labelled(map[string]string{"component": "etcd"}))),
	).Build()

	got, err := ControlPlanePods(context.Background(), cl, loc)
	if err != nil {
		t.Fatalf("finding the control plane: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("found %v, want the three API servers", names(got))
	}
	if got[0].Name != "kube-apiserver-cp-0" {
		t.Errorf("instances came back unordered: %v", names(got))
	}
}

// TestAControlPlaneWithNoPodsSaysWhyRatherThanReadingZero.
//
// A managed control plane runs its API servers where no pod list shows them.
// That is a real cluster this measurement can still be pointed at, and the
// failure has to say so, because "0 instances" and "instances I cannot see"
// differ by the whole of the control plane's cost.
func TestAControlPlaneWithNoPodsSaysWhyRatherThanReadingZero(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).Build()

	_, err = ControlPlanePods(context.Background(), cl, KubeAPIServers())
	if err == nil {
		t.Fatal("an invisible control plane read as an empty one")
	}
	if !strings.Contains(err.Error(), "managed") {
		t.Errorf("the error does not say what this cluster might be: %v", err)
	}
}

func ptrTo(p corev1.Pod) *corev1.Pod { return &p }
