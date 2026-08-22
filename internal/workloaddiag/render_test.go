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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/workloaddiag"
)

func report() workloaddiag.Report {
	return workloaddiag.Report{
		Workspace: "root:capi-docker-ready-2",
		Cluster:   "demo-00",
		Nodes: []workloaddiag.Node{{
			Name: "demo-00-cp-nl2fc",
			Conditions: []workloaddiag.Condition{
				{Type: "MemoryPressure", Status: "False", Reason: "KubeletHasSufficientMemory"},
				{
					Type:    "Ready",
					Status:  "False",
					Reason:  "KubeletNotReady",
					Message: "container runtime network not ready: cni plugin not initialized",
				},
			},
		}},
		DaemonSets: []workloaddiag.DaemonSet{
			{Namespace: "kube-system", Name: "kindnet", Desired: 1, Current: 1, Ready: 1, Available: 1},
		},
		Pods: []workloaddiag.Pod{{
			Namespace: "kube-system",
			Name:      "kindnet-2f9xq",
			Node:      "demo-00-cp-nl2fc",
			Phase:     "Running",
			Ready:     "1/1",
			Restarts:  3,
			Detail:    "CrashLoopBackOff: back-off restarting failed container",
			Logs: []workloaddiag.Log{
				{Container: "kindnet-cni", Previous: true, Content: "failed to get node: connection refused"},
				{Container: "kindnet-cni", Err: "reading the log: the server rejected our request"},
			},
		}},
		Probes: []workloaddiag.Probe{
			{Description: "ls -l /etc/cni/net.d on demo-00-cp-nl2fc", Output: "total 0"},
			{
				Description: "the last 200 kubelet log lines on demo-00-cp-nl2fc",
				Output:      "kubelet: Unable to update cni config",
				Err:         "exit status 1",
			},
		},
		Notes: []string{"listing Nodes: context deadline exceeded"},
	}
}

func TestRenderSaysWhatTheNodeIsWaitingOn(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	if err := workloaddiag.Render(&sb, []workloaddiag.Report{report()}); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	out := sb.String()

	for _, want := range []string{
		"root:capi-docker-ready-2",
		"demo-00-cp-nl2fc",
		"KubeletNotReady",
		"cni plugin not initialized",
		// The conditions that are not Ready still matter — a node under disk
		// pressure fails differently — but they do not deserve a row each.
		"MemoryPressure=False",
		"kindnet",
		"CrashLoopBackOff",
		// The log of the container that died, marked as the dead one's.
		"previous",
		"failed to get node: connection refused",
		// What the API server could not answer, asked of the node itself —
		// including what a probe managed to say before it failed, which the
		// error must not replace.
		"ls -l /etc/cni/net.d on demo-00-cp-nl2fc",
		"kubelet: Unable to update cni config",
		"error: exit status 1",
		// And what could not be read at all, rather than a silent gap.
		"listing Nodes: context deadline exceeded",
		"reading the log: the server rejected our request",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

func TestRenderWritesNothingForNothingCollected(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	if err := workloaddiag.Render(&sb, nil); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if sb.Len() != 0 {
		t.Errorf("rendering no reports wrote %q, want nothing", sb.String())
	}
}

func TestWriteKeepsTheReportWhereARunCanCollectIt(t *testing.T) {
	t.Parallel()

	// A directory that does not exist yet: the caller's is bin/, which a
	// checkout does not have until something builds.
	dir := filepath.Join(t.TempDir(), "artifacts")

	path, err := workloaddiag.Write(dir, "diagnostics-TestSomething", "Workload cluster diagnostics", []workloaddiag.Report{report()})
	if err != nil {
		t.Fatalf("writing the report: %v", err)
	}
	if want := filepath.Join(dir, "diagnostics-TestSomething.md"); path != want {
		t.Errorf("wrote %s, want %s", path, want)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the report back: %v", err)
	}
	if !strings.HasPrefix(string(written), "# Workload cluster diagnostics\n") {
		t.Errorf("the report does not open with its title:\n%s", written)
	}
	if !strings.Contains(string(written), "cni plugin not initialized") {
		t.Errorf("the report is missing its findings:\n%s", written)
	}
}
