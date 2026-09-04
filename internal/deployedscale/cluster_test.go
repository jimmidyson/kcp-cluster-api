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
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
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

// TestASetOfReplicasIsNotUpUntilAllOfThemAre.
//
// The shard runs three replicas in a comparable run, and taking the first one
// as "up" starts the measurement while the other two are still opening their
// caches: the baseline is a third of a control plane plus two processes paying
// a cost they are about to stop paying, and every slope measured from it is
// wrong in a direction nothing downstream can detect.
func TestASetOfReplicasIsNotUpUntilAllOfThemAre(t *testing.T) {
	cl := fakeClient(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: KcpName},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(3))},
		Status:     appsv1.DeploymentStatus{Replicas: 3, AvailableReplicas: 1},
	})
	err := WaitForDeployment(context.Background(), cl, "scale", KcpName,
		100*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("one replica of three was reported as the deployment being up")
	}
	if !strings.Contains(err.Error(), "1/3 available") {
		t.Errorf("the timeout does not say what it saw: %v", err)
	}
}

// TestEveryReplicaAvailableIsUp, so the wait is a wait rather than a refusal.
func TestEveryReplicaAvailableIsUp(t *testing.T) {
	cl := fakeClient(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "scale", Name: KcpName},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(3))},
		Status:     appsv1.DeploymentStatus{Replicas: 3, AvailableReplicas: 3},
	})
	if err := WaitForDeployment(context.Background(), cl, "scale", KcpName,
		time.Second, 10*time.Millisecond); err != nil {
		t.Errorf("a fully available deployment was not seen as available: %v", err)
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

// TestForwardSurvivesTheTunnelDying is the regression test for the run that
// reported a fleet failing to converge when what had actually failed was the
// harness's own tunnel:
//
//	50 of 50 workspaces short (listing control planes in 2wypd8khv58vy4t6:
//	dial tcp 127.0.0.1:41579: connect: connection refused)
//
// Connection refused means nothing was listening. The forwarder had exited and
// nobody was watching it — ForwardPorts ran in a goroutine whose error was read
// once, at startup. Every request after that failed, and the failure was
// attributed to the clusters.
func TestForwardSurvivesTheTunnelDying(t *testing.T) {
	var mu sync.Mutex
	var starts []int
	current := make(chan error, 1)

	fake := func(localPort int) (int, <-chan error, func(), error) {
		mu.Lock()
		defer mu.Unlock()
		starts = append(starts, localPort)
		ch := make(chan error, 1)
		current = ch
		bound := localPort
		if bound == 0 {
			bound = 34567 // what the first call would have been given
		}
		return bound, ch, func() {}, nil
	}

	fwd, err := forwardWith(t.Context(), fake)
	if err != nil {
		t.Fatalf("establishing the forward: %v", err)
	}
	defer fwd.Stop()

	before := fwd.Local
	mu.Lock()
	dead := current
	mu.Unlock()
	dead <- errors.New("error copying from remote stream to local connection: broken pipe")

	deadline := time.Now().Add(10 * time.Second)
	for fwd.Restarts() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the tunnel died and was never rebuilt, so every later request would be refused")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if fwd.Local != before {
		t.Errorf("the local address changed from %s to %s; every address already handed out is now wrong",
			before, fwd.Local)
	}

	mu.Lock()
	got := append([]int(nil), starts...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("the tunnel was started %d time(s), want at least 2", len(got))
	}
	if got[0] != 0 {
		t.Errorf("the first tunnel asked for port %d, not any free port", got[0])
	}
	if got[1] != 34567 {
		t.Errorf("the rebuilt tunnel asked for port %d rather than the one already published", got[1])
	}
}

// TestForwardStopsSupervising: Stop has to end the supervisor, or a finished
// run leaves a goroutine rebuilding a tunnel to a namespace being deleted.
func TestForwardStopsSupervising(t *testing.T) {
	started := make(chan int, 8)
	fake := func(localPort int) (int, <-chan error, func(), error) {
		started <- localPort
		bound := localPort
		if bound == 0 {
			bound = 34568
		}
		return bound, make(chan error, 1), func() {}, nil
	}

	fwd, err := forwardWith(t.Context(), fake)
	if err != nil {
		t.Fatalf("establishing the forward: %v", err)
	}
	fwd.Stop()
	fwd.Stop() // idempotent: cleanup runs it, and a caller may too.

	drain := len(started)
	for range drain {
		<-started
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(started); n != 0 {
		t.Errorf("the supervisor started %d more tunnel(s) after Stop", n)
	}
}

// TestServerTroubleNamesAnOOMKilledShard is the finding the 200x50 run should
// have reported instead of "50 of 50 workspaces short".
func TestServerTroubleNamesAnOOMKilledShard(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kcp-abc", Namespace: "scale",
			Labels: map[string]string{ComponentLabel: KcpName},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: KcpName, Ready: false, RestartCount: 3,
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				ExitCode: 137, Reason: "OOMKilled",
			}},
		}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pod).Build()

	got := ServerTrouble(t.Context(), cl, "scale")
	for _, want := range []string{"OOMKilled", "memory limit", "restarted 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not mention %q:\n%s", want, got)
		}
	}
}

// TestServerTroubleIsSilentWhenKcpIsHealthy: the gate has to be a signal. A
// wait that stopped early on a healthy server would turn a slow fleet into a
// failure.
func TestServerTroubleIsSilentWhenKcpIsHealthy(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kcp-abc", Namespace: "scale",
			Labels: map[string]string{ComponentLabel: KcpName},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: KcpName, Ready: true, RestartCount: 0,
		}}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(pod).Build()

	if got := ServerTrouble(t.Context(), cl, "scale"); got != "" {
		t.Errorf("a healthy kcp was reported as trouble: %s", got)
	}
}

// TestScrapeKcpAddressesTheShardItself pins two things a wrong URL would break
// silently: the metrics come from the bare server rather than from inside a
// workspace, and a refusal is reported rather than parsed into zeroes.
func TestScrapeKcpAddressesTheShardItself(t *testing.T) {
	var paths []string
	code := http.StatusOK
	body := "go_goroutines 1234\n" +
		"go_memstats_heap_alloc_bytes 5.5e+08\n" +
		"go_memstats_sys_bytes 1.2e+09\n" +
		"process_resident_memory_bytes 9.9e+08\n" +
		"process_cpu_seconds_total 12.5\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	// A workspace-scoped config, as the run's own kcp config is: /metrics is
	// served by the shard and does not exist under a workspace path.
	cfg := &rest.Config{Host: server.URL + "/clusters/root"}

	got, err := ScrapeKcp(t.Context(), cfg)
	if err != nil {
		t.Fatalf("scraping: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/metrics" {
		t.Errorf("scraped %v, want exactly [/metrics] — a workspace path would 404", paths)
	}
	if got.Goroutines != 1234 {
		t.Errorf("goroutines = %d, want 1234", got.Goroutines)
	}
	if got.ResidentBytes != 990000000 {
		t.Errorf("resident = %d, want 990000000", got.ResidentBytes)
	}

	code, body = http.StatusForbidden, "forbidden: user cannot get /metrics"
	if _, err := ScrapeKcp(t.Context(), cfg); err == nil {
		t.Error("a refused scrape returned no error, so a run would record an empty sample as a measurement")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error does not name the status: %v", err)
	}
}

// TestParseStorageObjectsCountsWhatTheShardHolds. The question a fleet size
// hides is what a fleet actually is: 50 clusters of 50 nodes is 2,500 Machines
// only in the sense that a Machine is what was asked for, and the shard also
// holds the infrastructure object, the bootstrap config and the rendered Secret
// for every one of them.
func TestParseStorageObjectsCountsWhatTheShardHolds(t *testing.T) {
	body := `# HELP apiserver_storage_objects Number of stored objects
# TYPE apiserver_storage_objects gauge
apiserver_storage_objects{resource="machines.cluster.x-k8s.io"} 2500
apiserver_storage_objects{resource="kubeadmconfigs.bootstrap.cluster.x-k8s.io"} 2500
apiserver_storage_objects{resource="secrets"} 2612
apiserver_storage_objects{resource="devmachines.infrastructure.cluster.x-k8s.io"} 2500
apiserver_storage_objects{resource="clusters.cluster.x-k8s.io"} 50
go_goroutines 1234
`
	counts, err := ParseStorageObjects(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if counts["machines.cluster.x-k8s.io"] != 2500 {
		t.Errorf("machines = %d, want 2500", counts["machines.cluster.x-k8s.io"])
	}
	if counts["secrets"] != 2612 {
		t.Errorf("secrets = %d, want 2612", counts["secrets"])
	}

	top := TopStorage(counts, 3)
	if !strings.Contains(top, "10162 objects in total") {
		t.Errorf("the total is missing or wrong, which is the number that explains the memory:\n%s", top)
	}
	if !strings.Contains(top, "secrets=2612") {
		t.Errorf("the largest resource is not first:\n%s", top)
	}
	if strings.Contains(top, "clusters.cluster.x-k8s.io") {
		t.Errorf("TopStorage(3) returned more than three resources:\n%s", top)
	}
}

// TestParseStorageObjectsSaysWhenTheMetricIsAbsent rather than reporting an
// empty shard.
func TestParseStorageObjectsSaysWhenTheMetricIsAbsent(t *testing.T) {
	if _, err := ParseStorageObjects(strings.NewReader("go_goroutines 1\n")); err == nil {
		t.Error("a body with no storage metric parsed as a shard holding nothing")
	}
}

// TestParseEtcdSampleSeparatesTheDatabaseFromTheCaches. kcp runs etcd in its
// own process, so a single container limit covers both and an OOM says nothing
// about which grew. These gauges do: a database far larger than the objects
// stored means writes that have not been compacted, not a fleet that is too big.
func TestParseEtcdSampleSeparatesTheDatabaseFromTheCaches(t *testing.T) {
	body := `# TYPE etcd_mvcc_db_total_size_in_bytes gauge
etcd_mvcc_db_total_size_in_bytes 2.147483648e+09
etcd_mvcc_db_total_size_in_use_in_bytes 4.02653184e+08
etcd_debugging_mvcc_keys_total 187432
etcd_server_has_leader 1
`
	got, err := ParseEtcdSample(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.DBTotalBytes != 2147483648 {
		t.Errorf("db total = %d", got.DBTotalBytes)
	}
	if got.DBInUseBytes != 402653184 {
		t.Errorf("db in use = %d", got.DBInUseBytes)
	}
	if got.Keys != 187432 {
		t.Errorf("keys = %d", got.Keys)
	}

	// The gap between allocated and in use is the whole point: 2 GiB held for
	// 384 MiB of live data is a compaction story, not a capacity one.
	if d := got.Describe(); !strings.Contains(d, "in use") || !strings.Contains(d, "superseded") {
		t.Errorf("the description does not distinguish live data from history:\n%s", d)
	}
}

func TestParseEtcdSampleRejectsSomethingElsesMetrics(t *testing.T) {
	if _, err := ParseEtcdSample(strings.NewReader("go_goroutines 12\n")); err == nil {
		t.Error("a non-etcd endpoint parsed as an empty database rather than as a mistake")
	}
}

// TestFetchProfileAddressesTheShardAndReportsRefusals. The path must be the
// bare server's: /debug/pprof is served by the shard, not inside a workspace,
// and a run's kcp config addresses a workspace.
func TestFetchProfileAddressesTheShardAndReportsRefusals(t *testing.T) {
	var paths []string
	code := http.StatusOK
	payload := "not really a profile, but not empty either"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	cfg := &rest.Config{Host: server.URL + "/clusters/root"}

	raw, err := FetchProfile(t.Context(), cfg, "heap")
	if err != nil {
		t.Fatalf("fetching: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/debug/pprof/heap" {
		t.Errorf("fetched %v, want [/debug/pprof/heap]", paths)
	}
	if string(raw) != payload {
		t.Errorf("the body was not returned intact")
	}

	// Profiling can be disabled, and the authorizer can refuse a non-resource
	// URL. Either must be reported, not written to disk as a profile.
	code, payload = http.StatusForbidden, "forbidden"
	if _, err := FetchProfile(t.Context(), cfg, "heap"); err == nil {
		t.Error("a refused profile fetch returned no error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error does not name the status: %v", err)
	}

	// An empty 200 is not a profile either: writing it would leave a file that
	// pprof cannot open and nobody can explain.
	code, payload = http.StatusOK, ""
	if _, err := FetchProfile(t.Context(), cfg, "heap"); err == nil {
		t.Error("an empty body was accepted as a profile")
	}
}

// TestCollectGarbageAsksTheShardToCollectBeforeItIsMeasured is the fix for the
// thing that stopped three runs from answering what a Machine costs.
//
// Live heap read from /metrics is whatever had been allocated and not yet
// collected at the instant of the scrape. Within one run that is stable enough
// to fit — the three runs at one, five and ten nodes per cluster fitted their
// own samples to 14.1%, 2.5% and 1.4%. Across runs it is not: the five-node
// run's slope came out at 35.3 MB per cluster and the ten-node run's at 13.6,
// so a fleet with half the Machines in it appeared to cost two and a half times
// as much. The heap-to-heapSys ratio at those two samples was 73% and 52%,
// which is the whole story: one was scraped near the top of a collection cycle
// and the other after one.
//
// So the shard is asked to collect first. `?gc=1` is net/http/pprof's own
// parameter for it — the handler calls runtime.GC() before writing the profile
// — and after it, live heap is the retained set rather than the retained set
// plus whatever has not been swept.
func TestCollectGarbageAsksTheShardToCollectBeforeItIsMeasured(t *testing.T) {
	var got []string
	code := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(code)
		_, _ = w.Write([]byte("a heap profile"))
	}))
	defer server.Close()

	cfg := &rest.Config{Host: server.URL + "/clusters/root"}
	if err := CollectGarbage(t.Context(), cfg); err != nil {
		t.Fatalf("forcing a collection: %v", err)
	}
	if len(got) != 1 || got[0] != "/debug/pprof/heap?gc=1" {
		t.Errorf("requested %v, want [/debug/pprof/heap?gc=1] — without gc=1 the handler "+
			"writes a profile and collects nothing, and the sample that follows is unchanged", got)
	}

	// A refusal is an error rather than a silent no-op: a run that thinks it
	// sampled a collected heap and did not would publish the same
	// incomparable figures while claiming they were comparable.
	code = http.StatusForbidden
	if err := CollectGarbage(t.Context(), cfg); err == nil {
		t.Error("a refused collection returned no error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Errorf("the error does not name the status: %v", err)
	}
}
