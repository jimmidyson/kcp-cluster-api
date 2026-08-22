/*
Copyright 2026 The Kubernetes Authors.

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

package workloaddiag_test

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgofake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jimmidyson/kcp-cluster-api/internal/workloaddiag"
)

// node builds a Node carrying one Ready condition and one that is not, which
// is the shape the renderer has to reduce.
func node(name string, ready corev1.ConditionStatus, reason, message string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse, Reason: "KubeletHasSufficientMemory"},
				{Type: corev1.NodeReady, Status: ready, Reason: reason, Message: message},
			},
		},
	}
}

func kindnetDaemonSet(desired, ready int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kindnet"},
		Status: appsv1.DaemonSetStatus{
			DesiredNumberScheduled: desired,
			CurrentNumberScheduled: desired,
			NumberReady:            ready,
			NumberAvailable:        ready,
		},
	}
}

func kindnetPod(name string, ready bool, restarts int32, waiting string) *corev1.Pod {
	status := corev1.ContainerStatus{Name: "kindnet-cni", Ready: ready, RestartCount: restarts}
	if waiting != "" {
		status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting, Message: "back-off restarting"}}
	} else {
		status.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      name,
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "kindnet"},
			},
		},
		Spec:   corev1.PodSpec{NodeName: "demo-00-cp-abcde", Containers: []corev1.Container{{Name: "kindnet-cni"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{status}},
	}
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	return s
}

func TestCollectReportsNodeConditionsDaemonSetsAndPods(t *testing.T) {
	t.Parallel()

	objects := []runtime.Object{
		node("demo-00-cp-abcde", corev1.ConditionFalse, "KubeletNotReady",
			"container runtime network not ready: cni plugin not initialized"),
		kindnetDaemonSet(1, 1),
		kindnetPod("kindnet-xyz", true, 2, ""),
	}
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithRuntimeObjects(objects...).Build()

	got := workloaddiag.Collect(context.Background(), cl, clientgofake.NewSimpleClientset(objects...).CoreV1(),
		workloaddiag.Options{Workspace: "root:capi-docker-ready-2", Cluster: "demo-00", LogFrom: []string{"kindnet"}})

	if len(got.Nodes) != 1 {
		t.Fatalf("collected %d nodes, want 1", len(got.Nodes))
	}
	ready := got.Nodes[0].Ready()
	if ready.Status != string(corev1.ConditionFalse) || ready.Reason != "KubeletNotReady" {
		t.Errorf("Ready condition is %+v, want False/KubeletNotReady", ready)
	}
	if !strings.Contains(ready.Message, "cni plugin not initialized") {
		t.Errorf("Ready message is %q, want the kubelet's own text", ready.Message)
	}

	if len(got.DaemonSets) != 1 || got.DaemonSets[0].Ready != 1 || got.DaemonSets[0].Desired != 1 {
		t.Errorf("collected DaemonSets %+v, want kindnet 1/1", got.DaemonSets)
	}

	if len(got.Pods) != 1 {
		t.Fatalf("collected %d pods, want 1", len(got.Pods))
	}
	pod := got.Pods[0]
	if pod.Ready != "1/1" || pod.Restarts != 2 {
		t.Errorf("pod reports %s ready and %d restarts, want 1/1 and 2", pod.Ready, pod.Restarts)
	}
	if len(pod.Logs) == 0 {
		t.Fatal("a pod of a DaemonSet named in LogFrom was collected without logs")
	}
	// A pod that has restarted is worth reading twice: what the container says
	// now, and what the one that died said.
	var previous bool
	for _, l := range pod.Logs {
		if l.Previous {
			previous = true
		}
	}
	if !previous {
		t.Error("a pod with restarts was collected without the previous container's logs")
	}
}

func TestCollectReadsLogsFromEveryPodThatIsNotReady(t *testing.T) {
	t.Parallel()

	// Owned by nothing in LogFrom, so only its not-readiness makes it
	// interesting - which is the case that matters when the CNI is fine and
	// something else is wedged.
	pod := kindnetPod("coredns-abc", false, 0, "ContainerCreating")
	pod.Name = "coredns-abc"
	pod.OwnerReferences = nil

	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithRuntimeObjects(pod).Build()
	got := workloaddiag.Collect(context.Background(), cl, clientgofake.NewSimpleClientset(pod).CoreV1(),
		workloaddiag.Options{LogFrom: []string{"kindnet"}})

	if len(got.Pods) != 1 {
		t.Fatalf("collected %d pods, want 1", len(got.Pods))
	}
	if got.Pods[0].Ready != "0/1" {
		t.Errorf("pod reports %s ready, want 0/1", got.Pods[0].Ready)
	}
	if !strings.Contains(got.Pods[0].Detail, "ContainerCreating") {
		t.Errorf("pod detail is %q, want the waiting reason", got.Pods[0].Detail)
	}
	if len(got.Pods[0].Logs) == 0 {
		t.Error("a pod that is not ready was collected without logs")
	}
}

func TestCollectRecordsWhatItCouldNotRead(t *testing.T) {
	t.Parallel()

	// An empty scheme cannot list anything, which is the same shape as an API
	// server that has stopped answering: the report says so rather than the
	// caller getting nothing.
	cl := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()

	got := workloaddiag.Collect(context.Background(), cl, clientgofake.NewSimpleClientset().CoreV1(),
		workloaddiag.Options{Workspace: "root:capi-docker-ready-1"})

	if len(got.Notes) == 0 {
		t.Fatal("a collection that read nothing recorded no notes")
	}
	if got.Workspace != "root:capi-docker-ready-1" {
		t.Errorf("report is for workspace %q, want the one asked for", got.Workspace)
	}
}

// TestCollectReadsRealLogBodies guards the shape of the log request rather
// than its content: a GetLogs call built wrongly fails at the request, and
// every other assertion here would still pass with an error in its place.
func TestCollectReadsRealLogBodies(t *testing.T) {
	t.Parallel()

	pod := kindnetPod("kindnet-xyz", true, 0, "")
	cl := fake.NewClientBuilder().WithScheme(scheme(t)).WithRuntimeObjects(pod).Build()

	got := workloaddiag.Collect(context.Background(), cl, clientgofake.NewSimpleClientset(pod).CoreV1(),
		workloaddiag.Options{LogFrom: []string{"kindnet"}})

	if len(got.Pods) != 1 || len(got.Pods[0].Logs) != 1 {
		t.Fatalf("collected %d pods with logs %+v", len(got.Pods), got.Pods)
	}
	log := got.Pods[0].Logs[0]
	if log.Err != "" {
		t.Fatalf("reading a log failed: %s", log.Err)
	}
	if log.Content == "" {
		t.Error("the log was read as empty")
	}
}
