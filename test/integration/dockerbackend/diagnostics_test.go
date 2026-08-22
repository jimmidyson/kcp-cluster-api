//go:build integration

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

package dockerbackend_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/workloaddiag"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
)

const (
	// cniDaemonSetName is kind's own CNI, whose logs are collected whether or
	// not it reports itself healthy — the failure this file exists for had it
	// reporting its pods ready while the Node it runs on never left "cni
	// plugin not initialized", so its own account is the evidence.
	//
	// A node image shipping a different CNI still appears in the DaemonSet
	// table, and its pods' logs are collected as soon as one of them is not
	// ready; only the healthy-looking case is specific to this name.
	cniDaemonSetName = "kindnet"

	// diagnosticsTimeout bounds the dump of one workspace. It runs after a
	// test has already failed, against a cluster that may be why, so it must
	// not be able to turn a failure into a hang — and one unreachable cluster
	// must not spend the budget the next one needs, which is why it is per
	// workspace rather than for the lot.
	diagnosticsTimeout = 2 * time.Minute

	// diagnosticsTailLines is how much kubelet log is worth carrying: enough
	// to cover the last few minutes of a node that never came up.
	diagnosticsTailLines = 200
)

// logf is t.Logf with a wall-clock stamp.
//
// `go test` buffers a package's output and prints it when the test fails, so
// every line of the CI failure this file was written for carried the same
// timestamp: the moment the run gave up. "The CNI reported itself installed"
// could not be placed against "the Node never came up", and the interval
// between them is the whole question. The stamp goes on the line, or it does
// not exist.
func logf(t *testing.T, format string, args ...any) {
	t.Helper()
	t.Logf("%s "+format, append([]any{time.Now().UTC().Format("15:04:05.000")}, args...)...)
}

// dumpWorkloadDiagnostics reports what every workload cluster says about
// itself, and writes it where a CI run can collect it.
//
// It is called from the failure path rather than deferred, because it is only
// worth its cost when something has gone wrong — and it has to run before the
// fixture's cleanup, which cancels the context and tears the containers down
// with everything unread.
func dumpWorkloadDiagnostics(ctx context.Context, t *testing.T, workspaces []demo.Workspace) {
	t.Helper()

	if len(workspaces) == 0 {
		logf(t, "no workspaces to diagnose")
		return
	}

	// Detached from the test's context: the run may have failed *because* that
	// context was cancelled, and a diagnosis that needs the failure not to
	// have happened is no diagnosis.
	ctx = context.WithoutCancel(ctx)

	reports := make([]workloaddiag.Report, 0, len(workspaces))
	for _, ws := range workspaces {
		reports = append(reports, collectWithin(ctx, ws, diagnosticsTimeout))
	}

	var sb strings.Builder
	if err := workloaddiag.Render(&sb, reports); err != nil {
		logf(t, "rendering the workload diagnostics: %v", err)
		return
	}
	logf(t, "\n%s", sb.String())

	path, err := workloaddiag.Write(diagnosticsDir(t), "diagnostics-"+t.Name(),
		"Workload cluster diagnostics for "+t.Name(), reports)
	if err != nil {
		logf(t, "the workload diagnostics were not kept: %v", err)
		return
	}
	logf(t, "workload diagnostics written to %s", path)
}

func collectWithin(ctx context.Context, ws demo.Workspace, timeout time.Duration) workloaddiag.Report {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return collect(ctx, ws)
}

// collect reads one workspace's workload cluster: what its API server says,
// and then what the node itself says, which is not the same account.
func collect(ctx context.Context, ws demo.Workspace) workloaddiag.Report {
	report, err := collectFromAPI(ctx, ws)
	if err != nil {
		report = workloaddiag.Report{
			Workspace: ws.Path,
			Cluster:   demo.ClusterName(0),
			Notes:     []string{err.Error()},
		}
	}

	// The probes go through the container runtime rather than the workload API
	// server, so they run either way: a cluster whose API server cannot be
	// reached is exactly one worth looking at from the outside.
	report.Probes = probeNode(ctx, ws)
	return report
}

func collectFromAPI(ctx context.Context, ws demo.Workspace) (workloaddiag.Report, error) {
	cfg, err := workloadRESTConfig(ctx, ws)
	if err != nil {
		return workloaddiag.Report{}, fmt.Errorf("reaching the workload cluster: %w", err)
	}
	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		return workloaddiag.Report{}, fmt.Errorf("building a workload client: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return workloaddiag.Report{}, fmt.Errorf("building a workload clientset: %w", err)
	}

	return workloaddiag.Collect(ctx, cl, clientset.CoreV1(), workloaddiag.Options{
		Workspace: ws.Path,
		Cluster:   demo.ClusterName(0),
		LogFrom:   []string{cniDaemonSetName},
	}), nil
}

// probeNode asks the control plane container the two questions its API server
// cannot answer: whether a CNI configuration was ever written, and what
// kubelet made of it.
func probeNode(ctx context.Context, ws demo.Workspace) []workloaddiag.Probe {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ClusterName(0)}
	if err := ws.Client.Get(ctx, key, cluster); err != nil {
		return []workloaddiag.Probe{{Description: "reading the Cluster", Err: err.Error()}}
	}

	runtime, err := container.NewDockerClient()
	if err != nil {
		return []workloaddiag.Probe{{Description: "connecting to the container runtime", Err: err.Error()}}
	}
	ctx = container.RuntimeInto(ctx, runtime)

	name, err := controlPlaneContainer(ctx, runtime, cluster)
	if err != nil {
		return []workloaddiag.Probe{{Description: "finding the control plane container", Err: err.Error()}}
	}

	probes := []struct {
		description string
		command     []string
	}{
		{
			// The decisive one. kubelet reports "cni plugin not initialized"
			// for exactly as long as this directory holds no valid
			// configuration, so its contents separate "the CNI never wrote
			// one" from "it did, and kubelet did not act on it".
			description: "the CNI configuration on " + name,
			command:     []string{"sh", "-c", "ls -la /etc/cni/net.d; echo; cat /etc/cni/net.d/* 2>/dev/null"},
		},
		{
			description: fmt.Sprintf("the last %d kubelet log lines on %s", diagnosticsTailLines, name),
			command:     []string{"journalctl", "-u", "kubelet", "--no-pager", "-n", fmt.Sprint(diagnosticsTailLines)},
		},
	}

	collected := make([]workloaddiag.Probe, 0, len(probes))
	for _, p := range probes {
		var out, errOut bytes.Buffer
		probe := workloaddiag.Probe{Description: p.description}
		err := runtime.ExecContainer(ctx, name,
			&container.ExecContainerInput{OutputBuffer: &out, ErrorBuffer: &errOut},
			p.command[0], p.command[1:]...)
		probe.Output = out.String()
		if err != nil {
			probe.Err = fmt.Sprintf("%v (%s)", err, strings.TrimSpace(errOut.String()))
		}
		collected = append(collected, probe)
	}
	return collected
}

// diagnosticsDir is where the report is written: alongside the kcp server's
// own artifacts when a run says where those go, and bin/ otherwise, which is
// where this repository already puts output meant to outlive a run.
//
// ARTIFACT_DIR is not this package's invention — the kcp test fixture reads it
// to place its audit log and server output — so a run that collects one
// collects the other, from one setting.
//
// A test's working directory is its own source directory, so the repository
// root is three levels up from test/integration/dockerbackend.
func diagnosticsDir(t *testing.T) string {
	t.Helper()

	if dir, ok := os.LookupEnv("ARTIFACT_DIR"); ok && dir != "" {
		return dir
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "bin"))
	if err != nil {
		// Not fatal: a report in the working directory beats no report.
		logf(t, "resolving the diagnostics directory: %v", err)
		return "bin"
	}
	return dir
}
