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
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
)

// TestManagerAnswersTheReadinessProbeTheDeploymentDeclares runs the real
// core-manager binary with the real Deployment's flags and asks its readiness
// endpoint the question the kubelet asks.
//
// # Why
//
// A deployed run failed with
//
//	core-manager did not come up: core-manager was not available within 10m0s:
//	0/1 available
//
// while that same container's log showed it reconciling Machines throughout.
// A manager doing its job in a pod that never goes Ready is a readiness
// question, not a controller one, and nothing in the Deployment's own
// description says whether /readyz on the health port actually answers — the
// probe target and the flag that opens that port are written in two different
// files and were never checked against each other.
//
// This needs kcp and the built binary, and no cluster: it takes the flags from
// deployedscale, so a probe path or port that drifts from what the binary
// serves fails here in seconds rather than in a ten-minute wait.
func TestManagerAnswersTheReadinessProbeTheDeploymentDeclares(t *testing.T) {
	binary := managerBinary(t, "core-manager")

	ctx := t.Context()
	baseURL, creds := startDeployedKcp(t)
	scheme := deployedScheme(t)
	rootClient := rootClientFor(t, ctx, baseURL, creds, scheme)

	// The manager resolves its virtual workspace through a real
	// APIExportEndpointSlice, and a slice carries no endpoints until something
	// binds the export — so the export has to be published and bound before
	// the manager is started, exactly as the deployed run does it.
	providers := capiexports.All()
	discovery, err := capiexports.Publish(ctx, rootClient, providers, 2*time.Minute)
	if err != nil {
		t.Fatalf("publishing the provider exports: %v", err)
	}

	logical, err := kcpfixtures.EnsureWorkspace(ctx, rootClient, "readiness", 2*time.Minute)
	if err != nil {
		t.Fatalf("creating the workspace: %v", err)
	}
	wsClient, err := clientForWorkspace(baseURL, creds, logical, scheme)
	if err != nil {
		t.Fatalf("client for the workspace: %v", err)
	}
	for _, provider := range providers {
		if err := kcpfixtures.BindExport(ctx, wsClient, kcpfixtures.BindExportOptions{
			BindingName:      provider.Export,
			ExportPath:       deployedscale.RootWorkspace,
			ExportName:       provider.Export,
			PermissionClaims: provider.Claims(discovery.Identities(), discovery),
			ReadyTimeout:     2 * time.Minute,
		}); err != nil {
			t.Fatalf("binding %s: %v", provider.Export, err)
		}
	}

	kubeconfig, err := creds.Kubeconfig(baseURL + "/clusters/" + deployedscale.RootWorkspace)
	if err != nil {
		t.Fatalf("building a kubeconfig: %v", err)
	}
	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, kubeconfig, 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}

	health, metrics := freePort(t), freePort(t)
	// The flags the Deployment passes, with only the ports moved off the
	// fixed ones so a developer's machine can run two of these at once.
	args := []string{
		"--endpoint-slice-name=" + capiexports.CoreExport,
		fmt.Sprintf("--health-addr=:%d", health),
		fmt.Sprintf("--metrics-bind-address=:%d", metrics),
	}
	t.Logf("%s %s", binary, strings.Join(args, " "))

	logPath := filepath.Join(t.TempDir(), "manager.log")
	logFile, err := os.Create(logPath) //nolint:gosec // a path this test made.
	if err != nil {
		t.Fatalf("log file: %v", err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	managerCtx, stop := context.WithCancel(ctx)
	cmd := exec.CommandContext(managerCtx, binary, args...) //nolint:gosec // a binary this test built.
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		stop()
		t.Fatalf("starting the manager: %v", err)
	}
	t.Cleanup(func() {
		stop()
		_ = cmd.Wait()
		if t.Failed() {
			if out, err := os.ReadFile(logPath); err == nil { //nolint:gosec // a path this test made.
				t.Logf("%s log tail:\n%s", filepath.Base(binary), tail(string(out), 60))
			}
		}
	})

	// The probe's own terms: the path and scheme the Deployment declares, and
	// a window matching its InitialDelaySeconds and FailureThreshold rather
	// than one this test invented.
	probe := fmt.Sprintf("http://127.0.0.1:%d/readyz", health)
	deadline := time.Now().Add(5 * time.Minute)
	var last string
	for {
		resp, err := http.Get(probe) //nolint:gosec,noctx // a loopback port this test opened.
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)) //nolint:errcheck // best effort.
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Logf("readiness answered %d after %s", resp.StatusCode, time.Until(deadline))
				return
			}
			last = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		} else {
			last = err.Error()
		}

		if time.Now().After(deadline) {
			t.Fatalf("the readiness endpoint the Deployment probes never answered OK. "+
				"A manager whose /readyz does not answer is a pod that never goes Ready, "+
				"however much work its log shows it doing. Last attempt at %s: %s", probe, last)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cancelled waiting for readiness: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// managerBinary builds one cmd/ binary and returns its path, so the test
// exercises what the image runs rather than a library call that resembles it.
func managerBinary(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	build := exec.CommandContext(t.Context(), "go", "build", "-o", path, "./cmd/"+name)
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", name, err, out)
	}
	return path
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root: no go.mod in any parent")
		}
		dir = parent
	}
}
