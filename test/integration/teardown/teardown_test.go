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

// Package teardown_test asks what happens to a workspace that stops using
// Cluster API while it still holds clusters.
package teardown_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

const (
	// One workspace with a running control plane: the state a tenant is
	// actually in when they decide to stop.
	workspaces           = 1
	controlPlaneMachines = 1

	unbindTimeout = 3 * time.Minute
	pollInterval  = 2 * time.Second
)

// TestUnbindingAWorkspaceThatStillHoldsClusters is the regression test for a
// deadlock, and the check on a claim this project makes to operators.
//
// Deleting an APIBinding makes kcp delete every object of every bound type at
// once. Cluster API's teardown is a sequence — a Cluster deletes its control
// plane, which deletes its Machines, which delete the DevMachines underneath
// them — and removed all together it used not to finish. The DevMachine
// reconciler needs its Machine, its Cluster and its DevCluster, and when any of
// them had already gone it returned without requeueing or errored forever. Its
// finalizer stayed; that held the Machine, which held the control plane, which
// held the Cluster, which held the APIBinding. The workspace never disengaged
// and the binding never finished deleting.
//
// The fork's DevMachine reconciler now releases a deleted DevMachine whose
// prerequisites are already gone (releaseDeletedDevMachine, carried in
// DRIFT.md). This test is what says so from the outside: bring a real control
// plane up in a workspace, delete every APIBinding without touching the
// cluster, and require the bindings to actually go.
//
// It deliberately does not tidy up first. The sweeps delete their clusters
// before unbinding, because that is what a tenant winding a workspace down
// would do and it keeps a measurement clean; this is the other case, the one
// where a tenant simply stops.
func TestUnbindingAWorkspaceThatStillHoldsClusters(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig:           server.BaseConfig(t),
		WorkspacePrefix:      "capi-teardown",
		Workspaces:           workspaces,
		ControlPlaneMachines: controlPlaneMachines,
		Backend:              demo.BackendInMemory,
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
		PollInterval: pollInterval,
		Log:          ctrl.Log.WithName("demo"),
	})
	if err != nil {
		var sb strings.Builder
		_ = demo.RenderTable(&sb, result.Statuses)
		_ = demo.RenderControlPlaneTable(&sb, result.ControlPlanes)
		t.Fatalf("bringing the cluster up failed: %v\n%s", err, sb.String())
	}
	if !result.Ready() {
		t.Fatalf("the run did not reach a ready control plane, so what follows would not be testing a teardown")
	}

	for _, ws := range result.Workspaces {
		var bindings apisv1alpha1.APIBindingList
		if err := ws.Client.List(ctx, &bindings); err != nil {
			t.Fatalf("listing APIBindings in %s: %v", ws.Path, err)
		}
		if len(bindings.Items) == 0 {
			t.Fatalf("workspace %s has no APIBindings to delete", ws.Path)
		}

		for i := range bindings.Items {
			name := bindings.Items[i].Name
			if err := ws.Client.Delete(ctx, &apisv1alpha1.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
				t.Fatalf("deleting APIBinding %s in %s: %v", name, ws.Path, err)
			}
		}
		t.Logf("deleted %d APIBinding(s) in %s with its cluster still running", len(bindings.Items), ws.Path)
	}

	// The bindings going is the whole assertion. kcp holds an APIBinding open
	// until every object of every type it bound is gone, so a binding that
	// finishes deleting is proof that the Cluster, its control plane, its
	// Machines and its DevMachines all finished too.
	deadline := time.Now().Add(unbindTimeout)
	for {
		remaining := 0
		for _, ws := range result.Workspaces {
			var bindings apisv1alpha1.APIBindingList
			if err := ws.Client.List(ctx, &bindings); err != nil {
				// The workspace's bound APIs going away can make a list fail
				// transiently while kcp reworks what it serves there.
				t.Logf("listing APIBindings in %s: %v", ws.Path, err)
				remaining++
				continue
			}
			remaining += len(bindings.Items)
		}
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			for _, ws := range result.Workspaces {
				describeStuckTeardown(t, ctx, ws)
			}
			t.Fatalf("%d APIBinding(s) had not finished deleting after %s: a workspace that unbinds while it holds clusters "+
				"is stuck, and nothing in it can be cleaned up", remaining, unbindTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// describeStuckTeardown says which object in the chain is still holding on,
// because "the binding did not go" names the symptom and never the cause.
func describeStuckTeardown(t *testing.T, ctx context.Context, ws demo.Workspace) {
	t.Helper()

	var clusters clusterv1.ClusterList
	if err := ws.Client.List(ctx, &clusters); err == nil {
		for i := range clusters.Items {
			c := &clusters.Items[i]
			t.Logf("stuck: Cluster %s in %s: deleting=%v finalizers=%v", c.Name, ws.Path, !c.DeletionTimestamp.IsZero(), c.Finalizers)
		}
	}

	var machines clusterv1.MachineList
	if err := ws.Client.List(ctx, &machines); err == nil {
		for i := range machines.Items {
			m := &machines.Items[i]
			t.Logf("stuck: Machine %s in %s: phase=%s deleting=%v finalizers=%v",
				m.Name, ws.Path, m.Status.Phase, !m.DeletionTimestamp.IsZero(), m.Finalizers)
		}
	}

	var devMachines infrav1.DevMachineList
	if err := ws.Client.List(ctx, &devMachines); err == nil {
		for i := range devMachines.Items {
			d := &devMachines.Items[i]
			t.Logf("stuck: DevMachine %s in %s: deleting=%v finalizers=%v",
				d.Name, ws.Path, !d.DeletionTimestamp.IsZero(), d.Finalizers)
		}
	}

	var devClusters infrav1.DevClusterList
	if err := ws.Client.List(ctx, &devClusters); err == nil {
		for i := range devClusters.Items {
			d := &devClusters.Items[i]
			t.Logf("stuck: DevCluster %s in %s: deleting=%v finalizers=%v",
				d.Name, ws.Path, !d.DeletionTimestamp.IsZero(), d.Finalizers)
		}
	}

	var bindings apisv1alpha1.APIBindingList
	if err := ws.Client.List(ctx, &bindings); err == nil {
		for i := range bindings.Items {
			b := &bindings.Items[i]
			t.Logf("stuck: APIBinding %s in %s: deleting=%v finalizers=%v phase=%s",
				b.Name, ws.Path, !b.DeletionTimestamp.IsZero(), b.Finalizers, b.Status.Phase)
		}
	}
}
