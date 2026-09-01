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

	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
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
	dir := t.TempDir()

	port := freePort(t)
	// The name kcp is told to advertise itself as. In a cluster this is the
	// Service; here it is a loopback name the certificate also covers, which
	// is the point — what is under test is that kcp works when told to
	// advertise a name rather than detect one.
	baseURL := fmt.Sprintf("https://localhost:%d", port)

	creds, err := deployedscale.NewCredentials(
		deployedscale.ServiceNames(deployedscale.KcpName, "scale"), deployedscale.LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

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
	args := deployedscale.KcpArgs(baseURL, filepath.Join(dir, "data"), credsDir, port)
	t.Logf("kcp %s", strings.Join(args, " "))

	logPath := filepath.Join(dir, "kcp.log")
	logFile, err := os.Create(logPath) //nolint:gosec // a path this test made.
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	defer logFile.Close() //nolint:errcheck // a test's log file.

	serverCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(serverCtx, "kcp", args...) //nolint:gosec // arguments this package builds.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting kcp: %v", err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			if out, err := os.ReadFile(logPath); err == nil { //nolint:gosec // a path this test made.
				t.Logf("kcp log tail:\n%s", tail(string(out), 60))
			}
		}
	})

	cfg := &rest.Config{
		Host:            baseURL + "/clusters/" + deployedscale.RootWorkspace,
		BearerToken:     creds.Token,
		TLSClientConfig: rest.TLSClientConfig{CAData: creds.CACertPEM},
	}
	scheme := deployedScheme(t)
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
