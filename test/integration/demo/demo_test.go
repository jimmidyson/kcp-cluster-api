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

// Package demo_test runs the demo against a real kcp server and asserts the
// three things it exists to show: every workspace's cluster is provisioned by
// one manager, no workspace's objects are another's, and no tenant can read
// another tenant's workspaces or what is in them.
//
// This is the conversion plan's P8 in the shape P8 was missing - many
// workspaces and a full infrastructure reconcile at the same time, with
// tenancy asserted rather than assumed. TestEveryBoundWorkspaceIsWired
// exercises many workspaces without reconciling anything to completion, and
// TestCoreManagerClusterToMachine reconciles one workspace's cluster without
// there being a second.
package demo_test

import (
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// workspaces is deliberately more than one and no more than it has to be.
// What the demo demonstrates is that a second workspace needs no second
// anything; a third would cost a minute of test time to demonstrate it again.
const workspaces = 2

// users is one per workspace, so that every workspace has an owner and every
// owner has something to be isolated from. Two is what makes "the other one"
// mean anything; a third would pay another minute of test time to say it
// again.
var users = []string{"alice", "bob"}

func TestDemoBringsEveryWorkspaceToAReadyCluster(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig: server.BaseConfig(t),
		// The privileged credential, and it is not interchangeable with the
		// one above. kcp scopes an impersonated user to the logical cluster
		// the request addresses unless the impersonator is in system:masters,
		// and a scoped tenant is refused in the workspace holding the
		// APIExports - which is where the right to enable a provider is
		// checked. Impersonated from an ordinary admin, alice is refused with
		// "no permission to bind to export ...", which is a fact about
		// impersonation rather than about anything this test asserts. See
		// demo.ConfigForUser.
		ImpersonationConfig: server.RootShardSystemMasterBaseConfig(t),
		Workspaces:          workspaces,
		// One tenant per workspace, each granted their own and nothing else.
		// The run reports what kcp let each of them read of the others, which
		// is asserted below.
		Users: users,
		// The in-memory backend, so this needs no container runtime and pulls
		// no images. The docker backend is the same reconcilers over a real
		// container runtime, and is exercised by
		// test/integration/dockerbackend.
		Backend: demo.BackendInMemory,
		// What `task demo` asks for by default, and the reason this test is
		// the one that catches the whole chain: a cluster only reaches ready
		// if the bootstrap provider writes the data secret, the control plane
		// provider brings the machine up, the ClusterCache connects to the
		// workload cluster and the Machine reconciler finds its Node. Stopping
		// at provisioned infrastructure asserted none of that, and every bug
		// this wiring has had lived in it.
		ControlPlaneMachines: demo.DefaultControlPlaneMachines,
		WorkerMachines:       demo.DefaultWorkerMachines,
		RunManager:           true,
		// Ten minutes for a run that takes about ninety seconds when it works.
		//
		// The budget was five while demo.Run stopped at provisioned, and
		// waiting for ready is a longer wait by construction. But this number
		// is headroom, not a diagnosis: CI has twice shown a run reaching
		// "1 of 2 clusters ready" and staying there, in the same job where
		// another package did the identical work in 87 seconds. That is a
		// stall, not slowness: the in-memory mux was handing the second
		// workspace a port something else held, forever. That is fixed in the
		// fork; see docs/conversion-plan.md. The budget stays because a loaded
		// runner is still slow, but it is no longer covering for anything -
		// so if it is hit again, that is a new fault, not this one.
		Timeout:      10 * time.Minute,
		PollInterval: 2 * time.Second,
		Log:          ctrl.Log.WithName("demo"),
	})
	if err != nil {
		// The tables, not just the error. A run that times out says how many
		// clusters got there; only the tables say which condition each one is
		// waiting on, and without them a stalled workspace is indistinguishable
		// from a slow one - which is exactly the confusion this suite has
		// already cost once.
		var sb strings.Builder
		_ = demo.RenderTable(&sb, result.Statuses)
		_ = demo.RenderControlPlaneTable(&sb, result.ControlPlanes)
		_ = demo.RenderMachineTable(&sb, result.Machines)
		t.Fatalf("demo run failed: %v\n%s", err, sb.String())
	}

	if got := len(result.Workspaces); got != workspaces {
		t.Fatalf("demo created %d workspaces, want %d", got, workspaces)
	}
	if !result.Ready() {
		var sb strings.Builder
		_ = demo.RenderTable(&sb, result.Statuses)
		_ = demo.RenderControlPlaneTable(&sb, result.ControlPlanes)
		_ = demo.RenderMachineTable(&sb, result.Machines)
		t.Fatalf("not every cluster was ready:\n%s", sb.String())
	}

	assertWorkspacesAreIsolated(t, result)
	assertUsersAreIsolated(t, result)
}

// assertUsersAreIsolated is the tenancy question asked of kcp rather than of
// the objects: not "does each workspace hold only its own cluster" but "can
// the person who owns one workspace see anything of another's".
//
// Both halves are asserted, because a run where every check was refused would
// satisfy "no user read another's" while demonstrating nothing at all - it is
// what a broken grant looks like, and it is the failure mode a check that only
// counted denials would report as a pass.
func assertUsersAreIsolated(t *testing.T, result demo.Result) {
	t.Helper()

	if len(result.Users) != len(users) {
		t.Fatalf("run created %d users, want %d", len(result.Users), len(users))
	}
	if len(result.Access) == 0 {
		t.Fatal("the run reported no access checks, so it asserted nothing about tenancy")
	}

	var allowed, denied int
	for _, check := range result.Access {
		if check.Owner == check.User && check.Allowed {
			allowed++
		}
		if check.Owner != check.User && !check.Allowed {
			denied++
		}
	}
	if allowed == 0 {
		t.Errorf("no user could read their own workspaces:\n%s", accessTable(result))
	}
	if denied == 0 {
		t.Errorf("no user was refused another's workspaces:\n%s", accessTable(result))
	}
	if !result.Isolated() {
		t.Errorf("the users are not isolated from each other:\n%s", accessTable(result))
	}

	// Every workspace has exactly one owner and the owners differ, which is
	// what makes the checks above about two tenants rather than one.
	owners := map[string]string{}
	for _, ws := range result.Workspaces {
		if ws.Owner == "" {
			t.Errorf("workspace %s has no owner", ws.Path)
			continue
		}
		if other, ok := owners[ws.Owner]; ok {
			t.Errorf("workspaces %s and %s share the owner %s, so neither is another tenant's", other, ws.Path, ws.Owner)
		}
		owners[ws.Owner] = ws.Path
	}
}

func accessTable(result demo.Result) string {
	var sb strings.Builder
	_ = demo.RenderAccessTable(&sb, result.Access)
	return sb.String()
}

// assertWorkspacesAreIsolated is the property a multi-workspace demo exists to
// show, and the one the rest of the suite never asserted: every workspace
// holds exactly its own objects, and the status written into each was written
// for that workspace.
//
// Identical names across workspaces are what makes this meaningful. A leak
// between two workspaces holding a "demo-00" each cannot hide behind a name
// that happens not to collide.
func assertWorkspacesAreIsolated(t *testing.T, result demo.Result) {
	t.Helper()
	ctx := t.Context()

	seen := map[string]string{} // Cluster UID -> workspace that holds it
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

		cluster := clusters.Items[0]
		uid := string(cluster.UID)
		if other, ok := seen[uid]; ok {
			t.Fatalf("workspaces %s and %s report the same Cluster (uid %s)", other, ws.Path, uid)
		}
		seen[uid] = ws.Path

		// The DevCluster the Cluster reconciler claimed has to be this
		// workspace's own. An owner reference naming another workspace's
		// Cluster would be a cross-workspace write that every "is it
		// provisioned" assertion would still pass.
		devCluster := &infrav1.DevCluster{}
		key := client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}
		if err := ws.Client.Get(ctx, key, devCluster); err != nil {
			t.Fatalf("reading DevCluster %s in %s: %v", key.Name, ws.Path, err)
		}
		assertOwnedBy(t, ws.Path, devCluster.OwnerReferences, uid)
	}

	if len(seen) != len(result.Workspaces) {
		t.Fatalf("%d workspaces reported %d distinct Clusters", len(result.Workspaces), len(seen))
	}
}

func assertOwnedBy(t *testing.T, workspace string, owners []metav1.OwnerReference, clusterUID string) {
	t.Helper()
	for _, owner := range owners {
		if owner.Kind != "Cluster" {
			continue
		}
		if string(owner.UID) != clusterUID {
			t.Errorf("DevCluster in %s is owned by Cluster uid %s, but that workspace's Cluster is uid %s",
				workspace, owner.UID, clusterUID)
		}
		return
	}
	t.Errorf("DevCluster in %s has no Cluster owner reference (%v), so nothing claimed it", workspace, owners)
}
