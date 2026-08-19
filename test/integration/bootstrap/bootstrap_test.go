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

// Package bootstrap_test is the conversion plan's P1 and P2 exit criterion:
// the kubeadm bootstrap and control plane providers, unmodified, bringing up a
// control plane in every workspace bound to their exports.
//
// It is a separate package from test/integration/demo because it asserts
// something that package deliberately does not: what the bootstrap provider
// writes is Secrets, in the workspace being reconciled, and Secrets are the
// resource this project could not reach through the virtual workspace until
// the export claimed them. A test that only checked "the machine says it has
// bootstrap data" would pass with both workspaces sharing one Secret.
package bootstrap_test

import (
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	workspaces = 2

	// One control plane machine and one worker, per workspace: the smallest
	// shape that exercises both providers that create Machines.
	controlPlaneMachines = 1
	workerMachines       = 1
	machinesPerWorkspace = controlPlaneMachines + workerMachines
)

func TestBootstrapDataIsProducedInEveryWorkspace(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig:           server.BaseConfig(t),
		WorkspacePrefix:      "capi-bootstrap",
		Workspaces:           workspaces,
		ControlPlaneMachines: controlPlaneMachines,
		WorkerMachines:       workerMachines,
		Backend:              demo.BackendInMemory,
		RunManager:           true,
		// Ten minutes for a run that takes about two on an unloaded machine.
		//
		// Ten minutes for a run that takes about ninety seconds when it works.
		//
		// The budget was five while demo.Run stopped at provisioned, and
		// waiting for ready is a longer wait by construction. But this number
		// is headroom, not a diagnosis: CI has twice shown a run reaching
		// "1 of 2 clusters ready" and staying there, in the same job where
		// another package did the identical work in 87 seconds. That is a
		// stall, not slowness, and it is recorded in docs/conversion-plan.md
		// rather than fixed by this number. Raise nothing further here - if
		// this budget is hit again, the stall is what needs looking at.
		Timeout:      10 * time.Minute,
		PollInterval: 2 * time.Second,
		Log:          ctrl.Log.WithName("demo"),
	})
	if err != nil {
		var sb strings.Builder
		_ = demo.RenderTable(&sb, result.Statuses)
		_ = demo.RenderControlPlaneTable(&sb, result.ControlPlanes)
		_ = demo.RenderMachineTable(&sb, result.Machines)
		t.Fatalf("run failed: %v\n%s", err, sb.String())
	}

	if got := len(result.ControlPlanes); got != workspaces {
		t.Fatalf("run produced %d control planes, want %d", got, workspaces)
	}
	for _, cp := range result.ControlPlanes {
		if !cp.Initialized {
			t.Errorf("control plane %s in %s is not initialized: %s", cp.ControlPlane, cp.Workspace, cp.Detail)
		}
	}

	// The Machines are the control plane provider's, not the demo's: it names
	// and creates them, which is why they are counted rather than looked up.
	if got, want := len(result.Machines), workspaces*machinesPerWorkspace; got != want {
		t.Fatalf("run produced %d machines, want %d", got, want)
	}
	for _, machine := range result.Machines {
		if !machine.Bootstrapped {
			t.Errorf("machine %s in %s has no bootstrap data: %s", machine.Machine, machine.Workspace, machine.Detail)
		}
	}

	assertSecretsAreWorkspaceLocal(t, result)
}

// assertSecretsAreWorkspaceLocal checks the part that matters for tenancy: the
// bootstrap data and the cluster's certificate authority are Secrets, one set
// per workspace, holding different bytes.
//
// Identical names across workspaces make this meaningful. Every workspace's
// machine is called demo-00-cp-0 and its data Secret the same, so a provider
// writing one workspace's data into another - or all of them reading one
// Secret - shows up as equal contents rather than as a missing object.
func assertSecretsAreWorkspaceLocal(t *testing.T, result demo.Result) {
	t.Helper()
	ctx := t.Context()

	seenBootstrapData := map[string]string{} // data digest -> workspace
	seenCA := map[string]string{}
	seenKubeconfig := map[string]string{}

	for _, ws := range result.Workspaces {
		machines := &clusterv1.MachineList{}
		if err := ws.Client.List(ctx, machines, client.InNamespace(demo.Namespace)); err != nil {
			t.Fatalf("listing Machines in %s: %v", ws.Path, err)
		}
		if len(machines.Items) != machinesPerWorkspace {
			t.Fatalf("workspace %s holds %d Machines, want its own %d", ws.Path, len(machines.Items), machinesPerWorkspace)
		}
		// The control plane machine specifically: its KubeadmConfig carries
		// the data the first machine of a cluster boots from, and a list does
		// not promise an order.
		var machine *clusterv1.Machine
		for i := range machines.Items {
			if _, isControlPlane := machines.Items[i].Labels[clusterv1.MachineControlPlaneLabel]; isControlPlane {
				machine = &machines.Items[i]
				break
			}
		}
		if machine == nil {
			t.Fatalf("workspace %s holds no control plane Machine", ws.Path)
		}

		config := &bootstrapv1.KubeadmConfig{}
		key := client.ObjectKey{Namespace: machine.Namespace, Name: machine.Spec.Bootstrap.ConfigRef.Name}
		if err := ws.Client.Get(ctx, key, config); err != nil {
			t.Fatalf("reading KubeadmConfig %s in %s: %v", key.Name, ws.Path, err)
		}
		if config.Status.DataSecretName == "" {
			t.Fatalf("KubeadmConfig %s in %s has no data secret", key.Name, ws.Path)
		}

		data := &corev1.Secret{}
		dataKey := client.ObjectKey{Namespace: demo.Namespace, Name: config.Status.DataSecretName}
		if err := ws.Client.Get(ctx, dataKey, data); err != nil {
			t.Fatalf("reading the bootstrap data Secret %s in %s: %v", dataKey.Name, ws.Path, err)
		}
		value := string(data.Data["value"])
		if value == "" {
			t.Errorf("bootstrap data Secret %s in %s is empty", dataKey.Name, ws.Path)
		}
		if other, ok := seenBootstrapData[value]; ok {
			t.Errorf("workspaces %s and %s hold identical bootstrap data: one workspace's machine was configured with another's", other, ws.Path)
		}
		seenBootstrapData[value] = ws.Path

		// The certificate authority the control plane provider generated for
		// this cluster. Two workspaces sharing one CA would be the same class
		// of fault as sharing bootstrap data, and a worse one.
		ca := &corev1.Secret{}
		caKey := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ClusterName(0) + "-ca"}
		if err := ws.Client.Get(ctx, caKey, ca); err != nil {
			t.Fatalf("reading the cluster CA Secret %s in %s: %v", caKey.Name, ws.Path, err)
		}
		// The kubeconfig the control plane provider wrote for this cluster.
		// It names that cluster's API server and embeds that cluster's CA, so
		// two workspaces holding the same bytes would mean one of them is
		// being handed the other's cluster.
		kubeconfig := &corev1.Secret{}
		kubeconfigKey := client.ObjectKey{Namespace: demo.Namespace, Name: demo.KubeconfigSecretName(demo.ClusterName(0))}
		if err := ws.Client.Get(ctx, kubeconfigKey, kubeconfig); err != nil {
			t.Fatalf("reading the workload cluster kubeconfig %s in %s: %v", kubeconfigKey.Name, ws.Path, err)
		}
		if len(kubeconfig.Data["value"]) == 0 {
			t.Errorf("kubeconfig Secret %s in %s is empty", kubeconfigKey.Name, ws.Path)
		}
		if other, ok := seenKubeconfig[string(kubeconfig.Data["value"])]; ok {
			t.Errorf("workspaces %s and %s hold the same workload cluster kubeconfig", other, ws.Path)
		}
		seenKubeconfig[string(kubeconfig.Data["value"])] = ws.Path

		caValue := string(ca.Data["tls.crt"])
		if caValue == "" {
			t.Errorf("cluster CA Secret %s in %s has no certificate", caKey.Name, ws.Path)
		}
		if other, ok := seenCA[caValue]; ok {
			t.Errorf("workspaces %s and %s hold the same cluster certificate authority", other, ws.Path)
		}
		seenCA[caValue] = ws.Path
	}
}
