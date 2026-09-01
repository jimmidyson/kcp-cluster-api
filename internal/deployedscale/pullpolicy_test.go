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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestEveryContainerStatesItsPullPolicy is the defect this file exists for.
//
// Kubernetes derives the policy from the tag, and `:latest` gets Always. The
// local path loads images onto the nodes without a registry existing at all,
// so Always sends the kubelet looking for a registry called kind.local and the
// run dies in ImagePullBackOff — which is exactly what happened.
func TestEveryContainerStatesItsPullPolicy(t *testing.T) {
	o := testOptions()
	for name, spec := range podSpecs(testObjects(t, o)) {
		for _, c := range spec.Containers {
			if c.ImagePullPolicy == "" {
				t.Errorf("%s/%s states no pull policy, so Kubernetes derives one from the tag — and a :latest "+
					"tag gets Always, which cannot work for an image loaded straight onto the nodes", name, c.Name)
			}
			if c.ImagePullPolicy != DefaultImagePullPolicy {
				t.Errorf("%s/%s has policy %q, want %q", name, c.Name, c.ImagePullPolicy, DefaultImagePullPolicy)
			}
		}
	}
}

// A run against a real registry with a moving tag needs Always, so the default
// has to be overridable rather than baked in.
func TestPullPolicyIsOverridable(t *testing.T) {
	o := testOptions()
	o.ImagePullPolicy = corev1.PullAlways

	for name, spec := range podSpecs(testObjects(t, o)) {
		for _, c := range spec.Containers {
			if c.ImagePullPolicy != corev1.PullAlways {
				t.Errorf("%s/%s has policy %q, want Always", name, c.Name, c.ImagePullPolicy)
			}
		}
	}
}

// TestAnImagePullFailureIsReportedNotWaitedOut is the other half. The run that
// found this waited ten minutes and then said "0/1 available", which is the one
// thing that does not say what to fix.
func TestAnImagePullFailureIsReportedNotWaitedOut(t *testing.T) {
	cl := fakeClient(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
			Status:     appsv1.DeploymentStatus{Replicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "core-1", Labels: labels(ComponentCore)},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: ComponentCore,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ImagePullBackOff",
					Message: `Back-off pulling image "kind.local/core-manager:latest"`,
				}},
			}}},
		},
	)

	start := time.Now()
	err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, 30*time.Second, 10*time.Millisecond)
	if err == nil {
		t.Fatal("a deployment stuck in ImagePullBackOff was reported as available")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the wait sat through a backoff the kubelet had already given up on")
	}
	for _, want := range []string{"ImagePullBackOff", "kind.local/core-manager:latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q, so it does not say what to fix", err, want)
		}
	}
}

func TestACrashLoopIsAlsoReported(t *testing.T) {
	cl := fakeClient(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
			Status:     appsv1.DeploymentStatus{Replicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "core-1", Labels: labels(ComponentCore)},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  ComponentCore,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 1, Reason: "Error", Message: "unable to resolve the APIExport's virtual workspace\n",
				}},
			}}},
		},
	)

	err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, 30*time.Second, 10*time.Millisecond)
	if err == nil {
		t.Fatal("a crash looping deployment was reported as available")
	}
	if !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Errorf("error %q does not name the crash loop", err)
	}
}

// A container that is merely still starting is waited for, not given up on.
func TestATransientWaitingStateIsNotTerminal(t *testing.T) {
	cl := fakeClient(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
			Status:     appsv1.DeploymentStatus{Replicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "core-1", Labels: labels(ComponentCore)},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  ComponentCore,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
			}}},
		},
	)

	err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, 200*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if strings.Contains(err.Error(), "will not come up") {
		t.Errorf("a container that was still starting was treated as terminal: %v", err)
	}
	// The timeout still carries what it saw.
	if !strings.Contains(err.Error(), "ContainerCreating") {
		t.Errorf("the timeout does not say what the container was doing: %v", err)
	}
}
