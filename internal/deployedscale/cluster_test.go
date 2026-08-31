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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fakeClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objects...).Build()
}

func TestApplyCreatesThenUpdates(t *testing.T) {
	ctx := context.Background()
	cl := fakeClient()

	o := testOptions()
	creds, err := NewCredentials(ServiceNames(KcpName, o.Namespace), LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	objects, err := o.Objects(creds)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}

	if err := Apply(ctx, cl, objects); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var d appsv1.Deployment
	if err := cl.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: ComponentCore}, &d); err != nil {
		t.Fatalf("the core deployment was not created: %v", err)
	}

	// A rerun with a new image must land, or the run silently measures the
	// previous build.
	o.Images[ComponentCore] = "example.test/core-manager:second"
	objects, err = o.Objects(creds)
	if err != nil {
		t.Fatalf("objects: %v", err)
	}
	if err := Apply(ctx, cl, objects); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: o.Namespace, Name: ComponentCore}, &d); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := d.Spec.Template.Spec.Containers[0].Image; got != "example.test/core-manager:second" {
		t.Errorf("image = %q; a rerun measured the previous build", got)
	}
}

func TestWaitForDeploymentReturnsWhenAvailable(t *testing.T) {
	cl := fakeClient(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
		Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1},
	})
	if err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, time.Second, 10*time.Millisecond); err != nil {
		t.Errorf("an available deployment was not seen as available: %v", err)
	}
}

func TestWaitForDeploymentTimesOutWithWhatItSaw(t *testing.T) {
	cl := fakeClient(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
		Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 0},
	})
	err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, 100*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("an unavailable deployment was reported as available")
	}
	if !strings.Contains(err.Error(), "0/1 available") {
		t.Errorf("the timeout does not say what it saw: %v", err)
	}
}

// TestUnschedulableIsReportedImmediately. Anti-affinity on a cluster with too
// few nodes produces a pod that will never be placed, and waiting the whole
// timeout for it hides the one sentence that explains the run.
func TestUnschedulableIsReportedImmediately(t *testing.T) {
	cl := fakeClient(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: ComponentCore},
			Status:     appsv1.DeploymentStatus{Replicas: 1},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "core-1", Labels: labels(ComponentCore)},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  corev1.PodReasonUnschedulable,
				Message: "0/1 nodes are available: 1 node(s) didn't satisfy anti-affinity rules",
			}}},
		},
	)

	start := time.Now()
	err := WaitForDeployment(context.Background(), cl, "scale", ComponentCore, 30*time.Second, 10*time.Millisecond)
	if err == nil {
		t.Fatal("an unschedulable deployment was reported as available")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the harness waited out the timeout instead of reporting the scheduling failure")
	}
	if !strings.Contains(err.Error(), "anti-affinity") {
		t.Errorf("the error does not carry the scheduler's reason: %v", err)
	}
}

func TestTeardownDeletesTheNamespaceAndToleratesAbsence(t *testing.T) {
	ctx := context.Background()
	cl := fakeClient(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "scale"}})

	if err := Teardown(ctx, cl, "scale"); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	// A run torn down twice, or torn down after an interrupted setup, must not
	// fail on the second attempt.
	if err := Teardown(ctx, cl, "scale"); err != nil {
		t.Errorf("tearing down an absent namespace failed: %v", err)
	}
}

func TestComponentPodsSelectsByComponent(t *testing.T) {
	cl := fakeClient(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "core-1", Labels: labels(ComponentCore)}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: "boot-1", Labels: labels(ComponentBootstrap)}},
	)
	pods, err := ComponentPods(context.Background(), cl, "scale", ComponentCore)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "core-1" {
		t.Errorf("selected %v", pods)
	}
}

// TestClusterConfigWithNoKubeconfigIsAnError is the "could not run" path
// (FR-005): a run with no cluster must say so rather than fail obscurely once
// it has started creating things.
func TestClusterConfigWithNoKubeconfigIsAnError(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "does-not-exist"))
	t.Setenv("HOME", t.TempDir())

	if _, err := ClusterConfig(""); err == nil {
		t.Skip("this environment has an in-cluster or default config, so there is no absence to test")
	} else if !strings.Contains(err.Error(), "no cluster to run against") {
		t.Errorf("error %q does not say a cluster is missing", err)
	}
}

func TestClusterConfigHonoursAnExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	body := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://cluster.example:6443
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: t
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	cfg, err := ClusterConfig(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Host != "https://cluster.example:6443" {
		t.Errorf("host = %q", cfg.Host)
	}
}
