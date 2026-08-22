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
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
	"github.com/jimmidyson/kcp-cluster-api/internal/workloaddiag"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Two workspaces, one control plane machine each, and no workers.
//
// Two because the whole claim is about workspaces not interfering, and one is
// not a fleet. No workers because each machine here is a real container on a
// runner with four cores: the worker adds nothing this test asserts that the
// control plane machine does not, and it would double what the slowest part
// of the suite costs.
const (
	readyWorkspaces           = 2
	readyControlPlaneMachines = 1

	// Generous, and deliberately so. Two container-backed control planes come
	// up concurrently on a shared runner, after pulling images that are not
	// cached there. A tight bound would turn a slow runner into a red build,
	// which teaches nobody anything; a real failure still fails, just later.
	readyTimeout = 20 * time.Minute
)

// TestTwoWorkspacesReachReadyOnTheDockerBackend is the conversion plan's P8 on
// a real container runtime.
//
// # Why this exists when test/integration/demo asserts the same shape
//
// That test reconciles two workspaces to ready concurrently and asserts they
// stay each other's business, which is the property. It does it on the dev
// provider's *in-memory* backend: the workload cluster is a process, its API
// server is a fake, and its Node objects are written by the backend rather
// than joined by a kubelet.
//
// Everything this project does sits above that line, so the in-memory backend
// is the right default and the sweeps depend on it. But P8 asked for the proof
// on a real runtime, and the difference is not cosmetic - a container-backed
// control plane takes minutes rather than milliseconds, its API server is real
// enough to reject things, and its kubeconfig points at a load balancer that
// has to exist. A wiring bug that only shows up against a real API server
// would pass every other suite in this repository.
//
// # What it asserts
//
// That both workspaces reach ready, and that each holds exactly its own
// Cluster. Ready is the whole chain: infrastructure provisioned, bootstrap
// data written, the control plane initialized and available, the ClusterCache
// connected to the workload cluster, and the Machine's Node found and healthy.
func TestTwoWorkspacesReachReadyOnTheDockerBackend(t *testing.T) {
	// Same guard as the suite's other test, for the same reason: skipping is
	// reasonable without a runtime and is a defect when the harness has just
	// asserted there is one.
	if err := verify.ContainerRuntimeAvailable(); err != nil {
		if verify.CapabilityAsserted(verify.CapabilityContainerRuntime) {
			t.Fatalf("verification asserted a container runtime is available, but this test cannot reach one: %v", err)
		}
		t.Skipf("no container runtime in this environment: %v", err)
	}
	ensureKindDockerNetwork(t)

	// Timestamped: a buffered test log prints every line at the moment the run
	// gave up, and what this suite needs from a failure is the order things
	// happened in. See logf.
	ctrl.SetLogger(testr.NewWithOptions(t, testr.Options{LogTimestamp: true}))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig:           server.BaseConfig(t),
		WorkspacePrefix:      "capi-docker-ready",
		Workspaces:           readyWorkspaces,
		ControlPlaneMachines: readyControlPlaneMachines,
		Backend:              demo.BackendDocker,
		RunManager:           true,
		// A kubeadm Node stays NotReady until a CNI is applied, and nothing in
		// Cluster API applies one. It has to happen while the clusters are
		// coming up rather than before or after: the kubeconfig it needs does
		// not exist until the control plane is up, and waiting for ready first
		// would wait for the thing this unblocks.
		WhileProvisioning: installCNIWhileProvisioning(t),
		Timeout:           readyTimeout,
		PollInterval:      5 * time.Second,
		// The status tables, deduplicated. Unset they go to io.Discard, which
		// is how a twenty minute wait came to leave no record of when either
		// cluster reached which state - the one thing a timeout needs.
		Out: workloaddiag.NewChangeWriter(func(line string) { logf(t, "%s", line) }),
		Log: ctrl.Log.WithName("demo"),
	})
	if err != nil {
		// Before the fatal, because the fixture's cleanup runs after it and
		// takes the containers - and every account of why they never came up -
		// with it.
		dumpWorkloadDiagnostics(ctx, t, result.Workspaces)
		t.Fatalf("bringing two container-backed clusters up failed: %v\n%s", err, tables(result))
	}
	if !result.Ready() {
		dumpWorkloadDiagnostics(ctx, t, result.Workspaces)
		t.Fatalf("not every cluster reached ready:\n%s", tables(result))
	}

	// The isolation half of P8. Identical names in both workspaces are what
	// makes it meaningful: a leak cannot hide behind names that happen not to
	// collide.
	seen := map[string]string{} // Cluster UID -> the workspace holding it
	for _, ws := range result.Workspaces {
		clusters := &clusterv1.ClusterList{}
		if err := ws.Client.List(ctx, clusters); err != nil {
			t.Fatalf("listing Clusters in %s: %v", ws.Path, err)
		}
		if len(clusters.Items) != 1 {
			names := make([]string, 0, len(clusters.Items))
			for _, c := range clusters.Items {
				names = append(names, c.Namespace+"/"+c.Name)
			}
			t.Fatalf("workspace %s sees %d Clusters (%v), want only its own", ws.Path, len(clusters.Items), names)
		}

		uid := string(clusters.Items[0].UID)
		if other, ok := seen[uid]; ok {
			t.Fatalf("workspaces %s and %s report the same Cluster (uid %s)", other, ws.Path, uid)
		}
		seen[uid] = ws.Path
	}
	if len(seen) != readyWorkspaces {
		t.Fatalf("%d workspaces reported %d distinct Clusters", readyWorkspaces, len(seen))
	}

	// Each workspace's control plane has its own kubeconfig Secret, which is
	// what the ClusterCache connected through to reach the workload cluster.
	// Asserted because "ready" would be reachable with one of them missing
	// only if something were reading another workspace's.
	for _, ws := range result.Workspaces {
		key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.KubeconfigSecretName(demo.ClusterName(0))}
		secret := &corev1.Secret{}
		if err := ws.Client.Get(ctx, key, secret); err != nil {
			t.Errorf("reading the workload cluster kubeconfig %s in %s: %v", key.Name, ws.Path, err)
		}
	}
}

func tables(result demo.Result) string {
	var sb strings.Builder
	_ = demo.RenderTable(&sb, result.Statuses)
	_ = demo.RenderControlPlaneTable(&sb, result.ControlPlanes)
	_ = demo.RenderMachineTable(&sb, result.Machines)
	return sb.String()
}
