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

// Package bootstrap_test is the conversion plan's P1 exit criterion: the
// kubeadm bootstrap provider, unmodified, producing bootstrap data for a
// control plane machine in every workspace bound to the export, from one
// manager.
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
)

const workspaces = 2

func TestBootstrapDataIsProducedInEveryWorkspace(t *testing.T) {
	ctrl.SetLogger(testr.New(t))
	ctx := t.Context()

	_, server := kcpenvtest.EnvironmentAndServer(t, "")

	result, err := demo.Run(ctx, demo.Options{
		BaseConfig:           server.BaseConfig(t),
		WorkspacePrefix:      "capi-bootstrap",
		Workspaces:           workspaces,
		ControlPlaneMachines: 1,
		Backend:              demo.BackendInMemory,
		RunManager:           true,
		Timeout:              5 * time.Minute,
		PollInterval:         2 * time.Second,
		Log:                  ctrl.Log.WithName("demo"),
	})
	if err != nil {
		var sb strings.Builder
		_ = demo.RenderTable(&sb, result.Statuses)
		_ = demo.RenderMachineTable(&sb, result.Machines)
		t.Fatalf("run failed: %v\n%s", err, sb.String())
	}

	if got := len(result.Machines); got != workspaces {
		t.Fatalf("run produced %d machines, want %d", got, workspaces)
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

	for _, ws := range result.Workspaces {
		machine := demo.MachineName(demo.ClusterName(0), 0)

		config := &bootstrapv1.KubeadmConfig{}
		key := client.ObjectKey{Namespace: demo.Namespace, Name: machine}
		if err := ws.Client.Get(ctx, key, config); err != nil {
			t.Fatalf("reading KubeadmConfig %s in %s: %v", machine, ws.Path, err)
		}
		if config.Status.DataSecretName == "" {
			t.Fatalf("KubeadmConfig %s in %s has no data secret", machine, ws.Path)
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

		// The certificate authority the bootstrap provider generated for this
		// cluster, because there is no control plane provider to do it. Two
		// workspaces sharing one CA would be the same class of fault as
		// sharing bootstrap data, and a worse one.
		ca := &corev1.Secret{}
		caKey := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ClusterName(0) + "-ca"}
		if err := ws.Client.Get(ctx, caKey, ca); err != nil {
			t.Fatalf("reading the cluster CA Secret %s in %s: %v", caKey.Name, ws.Path, err)
		}
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
