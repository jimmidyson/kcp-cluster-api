//go:build integration

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

package deployed_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
)

// TestKcpStartsAndInitializesWorkspacesWithTheDeployedFlags exercises the flag
// set the deployed run gives kcp, without a cluster.
//
// # Why this is worth a test of its own
//
// kcp's own controllers run inside kcp. A flag combination that stops one of
// them does not fail loudly — it produces workspaces that sit in Initializing
// with an initializer nobody removes, which reads as a problem with whatever
// created the workspace rather than with how the server was started. That is
// exactly how it presented: "waiting for workspace scale-0000 to become ready
// … Initializers still exist: [system:apibindings]", reported against the
// harness, caused by the server.
//
// It needs the kcp binary and nothing else, so it costs seconds and catches
// the class of failure that otherwise costs a cluster and ten minutes.
func TestKcpStartsAndInitializesWorkspacesWithTheDeployedFlags(t *testing.T) {
	if _, err := exec.LookPath("kcp"); err != nil {
		t.Skipf("could not run: no kcp binary on PATH (%v); `task tools:kcp` installs it", err)
	}

	ctx := t.Context()
	baseURL, creds := startDeployedKcp(t)
	scheme := deployedScheme(t)
	rootClient := rootClientFor(t, ctx, baseURL, creds, scheme)

	// The whole point: a workspace has to leave Initializing. kcp's own
	// controllers remove the initializers, so this fails when the server was
	// started in a way that stops them.
	logical, err := kcpfixtures.EnsureWorkspace(ctx, rootClient, "flagcheck", 2*time.Minute)
	if err != nil {
		t.Fatalf("a workspace never became ready under the flags the deployed run uses. "+
			"This is the server's doing rather than the harness's: %v", err)
	}
	t.Logf("workspace became ready as logical cluster %s", logical)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a port: %v", err)
	}
	defer l.Close()                     //nolint:errcheck // released deliberately so kcp can take it.
	return l.Addr().(*net.TCPAddr).Port //nolint:errcheck,forcetypeassert // a TCP listener has a TCP address.
}

func waitFor(ctx context.Context, timeout time.Duration, attempt func() error) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		if last = attempt(); last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimSpace(s), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// startDeployedKcp runs a kcp with exactly the flags the Deployment gives it,
// on this machine, and returns the URL it serves on and the credentials that
// reach it.
//
// No cluster and no container runtime: the flags, the certificate and the
// identity are the deployed ones, which is enough to catch the failures that
// come from those rather than from Kubernetes. Two whole-run failures were
// reproduced this way in seconds after costing ten minutes each in kind.
func startDeployedKcp(t *testing.T) (baseURL string, creds *deployedscale.Credentials) {
	t.Helper()
	requireBinary(t, "kcp")
	creds = deployedCredentials(t)
	return startKcp(t, creds, nil, false), creds
}

// requireBinary skips rather than fails: a binary is a capability of the
// machine, and a missing one is "could not run" rather than a defect in what
// is under test.
func requireBinary(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("could not run: no %s binary on PATH (%v); `task tools:%s` installs it", name, err, name)
	}
}

// deployedCredentials mints what the Deployment mounts: one serving
// certificate covering the Service names, and one token file.
//
// One set for every replica. That is the shape a Deployment has — one Secret,
// mounted into each pod — and it is what makes three processes one shard to a
// client rather than three servers with three identities.
func deployedCredentials(t *testing.T) *deployedscale.Credentials {
	t.Helper()
	creds, err := deployedscale.NewCredentials(
		deployedscale.ServiceNames(deployedscale.KcpName, "scale"), deployedscale.LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	return creds
}

// startKcp runs one kcp with exactly the flags the Deployment gives it, on this
// machine, and returns the URL it serves on.
//
// No cluster and no container runtime: the flags, the certificate and the
// identity are the deployed ones, which is enough to catch the failures that
// come from those rather than from Kubernetes. Two whole-run failures were
// reproduced this way in seconds after costing ten minutes each in kind.
//
// etcdServers empty leaves kcp on its embedded store, which is the local
// default. leaderElection is what makes several of these one shard rather than
// several: kcp's own controllers run inside it, and three copies of them
// racing on one store is not the shape a Deployment of three replicas has.
func startKcp(t *testing.T, creds *deployedscale.Credentials, etcdServers []string, leaderElection bool) string {
	t.Helper()

	dir := t.TempDir()
	port := freePort(t)
	// The name kcp is told to advertise itself as. In a cluster this is the
	// Service; here it is a loopback name the certificate also covers, which
	// is the point — what is under test is that kcp works when told to
	// advertise a name rather than detect one.
	baseURL := fmt.Sprintf("https://localhost:%d", port)

	credsDir := filepath.Join(dir, "credentials")
	if err := os.MkdirAll(credsDir, 0o700); err != nil {
		t.Fatalf("credentials dir: %v", err)
	}
	for name, body := range map[string][]byte{
		"tls.crt":    creds.ServingCertPEM,
		"tls.key":    creds.ServingKeyPEM,
		"tokens.csv": []byte(creds.TokenAuthCSV()),
	} {
		if err := os.WriteFile(filepath.Join(credsDir, name), body, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	// The same flags the Deployment passes, from the same function.
	args := deployedscale.KcpArgs(baseURL, filepath.Join(dir, "data"), credsDir, port, etcdServers)
	if leaderElection {
		args = append(args, deployedscale.LeaderElectionArgs()...)
	}
	t.Logf("kcp %s", strings.Join(args, " "))

	logPath := filepath.Join(dir, "kcp.log")
	logFile, err := os.Create(logPath) //nolint:gosec // a path this test made.
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	serverCtx, stop := context.WithCancel(t.Context())
	cmd := exec.CommandContext(serverCtx, "kcp", args...) //nolint:gosec // arguments this package builds.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		stop()
		t.Fatalf("starting kcp: %v", err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			if out, err := os.ReadFile(logPath); err == nil { //nolint:gosec // a path this test made.
				t.Logf("kcp log tail (%s):\n%s", baseURL, tail(string(out), 60))
			}
		}
	})

	return baseURL
}

// startEtcd runs one etcd member and returns its client URL.
//
// One member rather than three: what the replicas need from it here is a
// single store they all write to, and three members would measure etcd's
// quorum rather than kcp's leader election. The deployed run gets three, for
// the reason EtcdOptions gives.
func startEtcd(t *testing.T) string {
	t.Helper()
	requireBinary(t, "etcd")

	dir := t.TempDir()
	clientPort, peerPort := freePort(t), freePort(t)
	client := fmt.Sprintf("http://127.0.0.1:%d", clientPort)
	peer := fmt.Sprintf("http://127.0.0.1:%d", peerPort)

	logPath := filepath.Join(dir, "etcd.log")
	logFile, err := os.Create(logPath) //nolint:gosec // a path this test made.
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	ctx, stop := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, "etcd", //nolint:gosec // arguments this package builds.
		"--name=one",
		"--data-dir="+filepath.Join(dir, "data"),
		"--listen-client-urls="+client,
		"--advertise-client-urls="+client,
		"--listen-peer-urls="+peer,
		"--initial-advertise-peer-urls="+peer,
		"--initial-cluster=one="+peer,
		"--initial-cluster-state=new",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		stop()
		t.Fatalf("starting etcd: %v", err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			if out, err := os.ReadFile(logPath); err == nil { //nolint:gosec // a path this test made.
				t.Logf("etcd log tail:\n%s", tail(string(out), 30))
			}
		}
	})
	return client
}

// rootClientFor waits for kcp to answer and returns a client for its root
// workspace.
func rootClientFor(t *testing.T, ctx context.Context, baseURL string,
	creds *deployedscale.Credentials, scheme *k8sruntime.Scheme,
) client.Client {
	t.Helper()

	cfg := &rest.Config{
		Host:            baseURL + "/clusters/" + deployedscale.RootWorkspace,
		BearerToken:     creds.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: creds.CACertPEM},
	}
	var rootClient client.Client
	if err := waitFor(ctx, 3*time.Minute, func() error {
		cl, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			return err
		}
		var workspaces tenancyv1alpha1.WorkspaceList
		if err := cl.List(ctx, &workspaces); err != nil {
			return err
		}
		rootClient = cl
		return nil
	}); err != nil {
		t.Fatalf("kcp did not become usable at %s: %v", baseURL, err)
	}
	return rootClient
}

// clientForWorkspace builds a client scoped to one logical cluster on a kcp
// addressed by base URL.
func clientForWorkspace(baseURL string, creds *deployedscale.Credentials, logical string,
	scheme *k8sruntime.Scheme,
) (client.Client, error) {
	cfg := &rest.Config{
		Host:            baseURL,
		BearerToken:     creds.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: creds.CACertPEM},
	}
	return client.New(deployedscale.WorkspaceConfig(cfg, logical), client.Options{Scheme: scheme})
}

// TestAThreeReplicaShardServesOneStore is the check R4 of
// specs/20260904-090000-comparable-kcp-stock-scale rests on.
//
// # Why it is worth a test rather than a reading of the flags
//
// The comparison gives kcp the same control plane the stock side has: three
// processes serving one store, active/active. The stock side gets that from
// kubeadm and nobody has to think about it. On the kcp side it is a shape this
// project has never run — every deployed run so far has been one replica — and
// three things could each make it not work, none of them loudly:
//
//   - kcp's own controllers run inside kcp, so three copies of them race on one
//     store. --enable-leader-election is what stops that, and a flag that is
//     accepted is not the same as a flag that works.
//   - Each replica generates its PKI into its own root directory. If anything a
//     client depends on is generated per process rather than mounted from the
//     one Secret, two replicas are two servers and the third is a coin toss.
//   - A workspace is initialized by controllers on the leader and served by all
//     three. A replica that cannot serve what the leader created would show up
//     as an intermittent fleet rather than as a broken control plane.
//
// Failing any of those, the honest thing is to find out here in a minute
// rather than from figures taken against a control plane that was one process
// pretending to be three.
func TestAThreeReplicaShardServesOneStore(t *testing.T) {
	requireBinary(t, "kcp")

	ctx := t.Context()
	store := startEtcd(t)
	creds := deployedCredentials(t)
	scheme := deployedScheme(t)

	const replicas = 3
	urls := make([]string, 0, replicas)
	for range replicas {
		urls = append(urls, startKcp(t, creds, []string{store}, true))
	}

	// Every replica answers as the same server, with the same identity.
	clients := make([]client.Client, 0, len(urls))
	for _, url := range urls {
		clients = append(clients, rootClientFor(t, ctx, url, creds, scheme))
	}

	// Created through one of them, and initialized by whichever holds the
	// lease. A workspace that never leaves Initializing here is leader
	// election not working, which is the failure this exists to catch.
	logical, err := kcpfixtures.EnsureWorkspace(ctx, clients[0], "hacheck", 3*time.Minute)
	if err != nil {
		t.Fatalf("a workspace created against one replica of a three-replica shard never became ready. "+
			"Three copies of kcp's controllers on one store is what --enable-leader-election exists to "+
			"prevent, and this is what it looks like when that does not hold: %v", err)
	}
	t.Logf("workspace became ready as logical cluster %s", logical)

	// And served by the other two. The point of three replicas is that a
	// client reaching any of them sees one control plane.
	for i, cl := range clients[1:] {
		var ws tenancyv1alpha1.Workspace
		if err := cl.Get(ctx, client.ObjectKey{Name: "hacheck"}, &ws); err != nil {
			t.Fatalf("replica %d does not serve the workspace the shard created: %v", i+1, err)
		}
		if ws.Spec.Cluster != logical {
			t.Errorf("replica %d says the workspace is logical cluster %q, and the one that created it "+
				"says %q: these are two servers rather than one shard", i+1, ws.Spec.Cluster, logical)
		}
	}

	// Inside the workspace, through a third replica: what a fleet actually
	// does. Serving the Workspace object from :root is the easier half — the
	// logical cluster's own APIs are bound by the initializer and are what a
	// manager and a driver talk to.
	inside, err := clientForWorkspace(urls[len(urls)-1], creds, logical, scheme)
	if err != nil {
		t.Fatalf("building a client for %s through the last replica: %v", logical, err)
	}
	made := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "written-through-the-last-replica", Namespace: "default"},
		Data:       map[string]string{"replicas": fmt.Sprint(replicas)},
	}
	if err := inside.Create(ctx, made); err != nil {
		t.Fatalf("writing into %s through the last replica: %v", logical, err)
	}

	// Read back through the first, which is the whole claim: one store, three
	// front ends.
	readBack, err := clientForWorkspace(urls[0], creds, logical, scheme)
	if err != nil {
		t.Fatalf("building a client for %s through the first replica: %v", logical, err)
	}
	var got corev1.ConfigMap
	if err := readBack.Get(ctx, client.ObjectKey{Namespace: "default", Name: made.Name}, &got); err != nil {
		t.Fatalf("a write through one replica is not visible through another, so these are not one "+
			"control plane: %v", err)
	}
	if got.Data["replicas"] != fmt.Sprint(replicas) {
		t.Errorf("read back %v, want what was written", got.Data)
	}
}
