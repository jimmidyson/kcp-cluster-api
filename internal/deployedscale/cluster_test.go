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
	"k8s.io/client-go/rest"
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

	if _, err := ClusterConfig("", ""); err == nil {
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

	cfg, err := ClusterConfig(path, "")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Host != "https://cluster.example:6443" {
		t.Errorf("host = %q", cfg.Host)
	}
}

// TestClusterConfigHonoursANamedContext is what keeps a run meant for a
// throwaway cluster out of whichever cluster happened to be current.
func TestClusterConfigHonoursANamedContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")
	body := `apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: https://local.example:6443
- name: production
  cluster:
    server: https://production.example:6443
contexts:
- name: local
  context:
    cluster: local
    user: u
- name: production
  context:
    cluster: production
    user: u
current-context: production
users:
- name: u
  user:
    token: t
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	cfg, err := ClusterConfig(path, "local")
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if cfg.Host != "https://local.example:6443" {
		t.Errorf("host = %q: the named context was ignored and the run would have deployed into the current one",
			cfg.Host)
	}

	if _, err := ClusterConfig(path, "not-a-context"); err == nil {
		t.Error("an unknown context was accepted")
	} else if !strings.Contains(err.Error(), "not-a-context") {
		t.Errorf("error %q does not name the context asked for", err)
	}
}

// TestFilterLogSkipsTheStartupBannerThatMentionsAPIBindings uses real lines from
// a kcp that failed exactly this way: the run below hung on system:apibindings,
// and the filter answered with kcp starting up correctly.
//
// Every noise line here matches a narrow pattern — the plugin banner contains
// "APIBinding", the controller name contains "apibinder", the sync line contains
// "initializ" — which is why keyword narrowing alone was not enough.
func TestFilterLogSkipsTheStartupBannerThatMentionsAPIBindings(t *testing.T) {
	log := strings.Join([]string{
		`I0901 14:06:11.000000 1 plugins.go:158] "Loaded admission plugin" plugin="APIBinding"`,
		`I0901 14:06:11.000001 1 plugins.go:158] "Enabled admission plugins" plugins="APIBinding,APIExport"`,
		`I0901 14:06:12.000000 1 shared_informer.go:313] "Waiting for sync" controller="kcp-apibinder-initializer"`,
		`I0901 14:06:12.000001 1 controller.go:100] "Starting controller" controller="kcp-apibinder-initializer"`,
		`I0901 14:06:13.000000 1 cacher.go:400] "Initializing cache" resource="apibindings.apis.kcp.io"`,
		`I0901 14:06:13.000001 1 apiextensions.go:159] "skipping APIBinding CRD because it came in via system CRDs"`,
		theOneLineThatMatters,
	}, "\n")

	got, narrow := FilterLog(log, InitializationLogPatterns, StartupFailurePatterns, 60)
	if !narrow {
		t.Fatal("nothing matched the narrow patterns, so the diagnosis fell back to startup noise")
	}
	if got != theOneLineThatMatters {
		t.Errorf("the filter returned\n%s\n\nrather than only\n%s", got, theOneLineThatMatters)
	}
}

const theOneLineThatMatters = `E0901 14:08:02.000000 1 workspace_controller.go:88] ` +
	`"failed to initialize" workspace="scale-0000" ` +
	`err="LogicalCluster 2gwkc88832wsignt|cluster had no createdBy recorded"`

// TestFilterLogFallsBackWhenTheServerNeverMentionedTheWorkspace is the other
// half: a server that says nothing about initialization did something else
// wrong, and then its startup errors are the best evidence there is.
func TestFilterLogFallsBackWhenTheServerNeverMentionedTheWorkspace(t *testing.T) {
	log := strings.Join([]string{
		`I0901 14:06:11.000000 1 server.go:1] serving securely on 0.0.0.0:6443`,
		`E0901 14:06:12.000000 1 run.go:74] "Unhandled Error" err="failed to list clusterroles"`,
	}, "\n")

	got, narrow := FilterLog(log, InitializationLogPatterns, StartupFailurePatterns, 60)
	if narrow {
		t.Error("a narrow pattern matched a log with nothing about initialization in it")
	}
	if !strings.Contains(got, "Unhandled Error") {
		t.Errorf("the fallback did not surface the startup error, got %q", got)
	}
}

// TestWorkspaceConfigDoesNotDoubleTheClusterPath is the regression test for a
// run that got all the way to binding an APIExport and then failed with
// "failed to get server groups: the server could not find the requested
// resource".
//
// The cause was kcpclient.SetCluster appending to a host that already ended in
// /clusters/root. Nothing in the error mentions the path or the workspace, so
// it reads as a workspace that is not ready — which is why it survived two
// rounds of diagnosis aimed at initialization.
func TestWorkspaceConfigDoesNotDoubleTheClusterPath(t *testing.T) {
	const want = "https://127.0.0.1:6443/clusters/2fj3k"

	for name, host := range map[string]string{
		"bare server":         "https://127.0.0.1:6443",
		"trailing slash":      "https://127.0.0.1:6443/",
		"root scoped":         "https://127.0.0.1:6443/clusters/root",
		"already a workspace": "https://127.0.0.1:6443/clusters/1abcd",
	} {
		t.Run(name, func(t *testing.T) {
			got := WorkspaceConfig(&rest.Config{Host: host}, "2fj3k")
			if got.Host != want {
				t.Errorf("from %q got host %q, want %q", host, got.Host, want)
			}
		})
	}
}

// TestWorkspaceConfigCopies checks the base is not mutated. SetCluster assigns
// to the config it is given, so a caller that forgets to copy first scopes
// every later client to the first workspace it built one for.
func TestWorkspaceConfigCopies(t *testing.T) {
	base := &rest.Config{Host: "https://127.0.0.1:6443", BearerToken: "t"}
	if got := WorkspaceConfig(base, "2fj3k"); got.BearerToken != "t" {
		t.Errorf("the copy lost the bearer token, got %q", got.BearerToken)
	}
	if base.Host != "https://127.0.0.1:6443" {
		t.Errorf("the base was mutated to %q", base.Host)
	}
}

// TestPodTroubleReportsARunningPodThatIsNeverReady covers the case that cost a
// whole ten-minute run and reported nothing: the manager was up and
// reconciling, its readiness probe answered 404, and the wait could only say
// "0/1 available" because the container was neither waiting nor terminated.
func TestPodTroubleReportsARunningPodThatIsNeverReady(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "core-manager-abc",
			Namespace: "scale",
			Labels:    map[string]string{ComponentLabel: "core-manager"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionFalse,
				Reason: "ContainersNotReady", Message: "containers with unready status: [core-manager]",
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "core-manager",
				Ready:        false,
				RestartCount: 0,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	event := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "core-manager-abc.1", Namespace: "scale"},
		InvolvedObject: corev1.ObjectReference{Name: "core-manager-abc", Namespace: "scale"},
		Type:           corev1.EventTypeWarning,
		Reason:         "Unhealthy",
		Message:        "Readiness probe failed: HTTP probe failed with statuscode: 404",
		LastTimestamp:  metav1.NewTime(time.Now()),
	}

	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pod, event).Build()

	detail, terminal := podTrouble(t.Context(), cl, "scale", "core-manager")
	if terminal {
		t.Error("a pod that is running but not ready was called terminal; it can still become ready")
	}
	for _, want := range []string{
		"running but not ready",
		"ContainersNotReady",
		"statuscode: 404",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail does not mention %q:\n%s", want, detail)
		}
	}
}

// TestPodTroubleStillPrefersTheWaitingReason checks the new case did not
// displace the old ones: a container waiting in a terminal state is still
// reported as terminal, so a wait gives up rather than sitting out its timeout.
func TestPodTroubleStillPrefersTheWaitingReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "core-manager-abc",
			Namespace: "scale",
			Labels:    map[string]string{ComponentLabel: "core-manager"},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "core-manager",
				Ready: false,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason: "ImagePullBackOff", Message: "back-off pulling image",
				}},
			}},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pod).Build()

	detail, terminal := podTrouble(t.Context(), cl, "scale", "core-manager")
	if !terminal {
		t.Error("ImagePullBackOff is not being treated as terminal any more")
	}
	if !strings.Contains(detail, "ImagePullBackOff") {
		t.Errorf("the detail does not name the waiting reason:\n%s", detail)
	}
}
