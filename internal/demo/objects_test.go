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

package demo

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"text/template"

	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

func TestBackendValidate(t *testing.T) {
	for _, tc := range []struct {
		backend Backend
		wantErr bool
	}{
		{BackendInMemory, false},
		{BackendDocker, false},
		{Backend(""), true},
		{Backend("kind"), true},
	} {
		if err := tc.backend.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("Backend(%q).Validate() = %v, wantErr %v", tc.backend, err, tc.wantErr)
		}
	}
}

func TestTemplatesCarryTheChosenBackend(t *testing.T) {
	inMemory := NewDevClusterTemplate(BackendInMemory)
	if got := inMemory.Spec.Template.Spec.Backend; got.InMemory == nil || got.Docker != nil {
		t.Errorf("in-memory DevClusterTemplate backend = %+v, want only InMemory set", got)
	}
	docker := NewDevClusterTemplate(BackendDocker)
	if got := docker.Spec.Template.Spec.Backend; got.Docker == nil || got.InMemory != nil {
		t.Errorf("docker DevClusterTemplate backend = %+v, want only Docker set", got)
	}

	inMemoryMachine := NewDevMachineTemplate(ControlPlaneMachineTemplateName, BackendInMemory)
	if got := inMemoryMachine.Spec.Template.Spec.Backend; got.InMemory == nil || got.Docker != nil {
		t.Errorf("in-memory DevMachineTemplate backend = %+v, want only InMemory set", got)
	}
	dockerMachine := NewDevMachineTemplate(ControlPlaneMachineTemplateName, BackendDocker)
	if got := dockerMachine.Spec.Template.Spec.Backend; got.Docker == nil || got.InMemory != nil {
		t.Errorf("docker DevMachineTemplate backend = %+v, want only Docker set", got)
	}
}

// TestClusterNamesAClassAndNothingElse is what makes these ClusterClass based
// clusters rather than hand-built ones with a topology bolted on.
//
// A Cluster that set its own infrastructureRef or controlPlaneRef would have
// the topology controller fighting it: those two fields are what that
// controller writes, from the class.
func TestClusterNamesAClassAndNothingElse(t *testing.T) {
	cluster := NewCluster(ClusterName(0), 1, 1, DefaultKubernetesVersion)

	if !cluster.Spec.Topology.IsDefined() {
		t.Fatal("Cluster has no topology, so no topology controller will ever act on it")
	}
	if got := cluster.Spec.Topology.ClassRef.Name; got != ClassName {
		t.Errorf("topology.classRef.name = %q, want %q", got, ClassName)
	}
	if got := cluster.Spec.Topology.Version; got != DefaultKubernetesVersion {
		t.Errorf("topology.version = %q, want %q", got, DefaultKubernetesVersion)
	}
	if cluster.Spec.InfrastructureRef.IsDefined() {
		t.Errorf("Cluster sets infrastructureRef %+v, which is the topology controller's to write", cluster.Spec.InfrastructureRef)
	}
	if cluster.Spec.ControlPlaneRef.IsDefined() {
		t.Errorf("Cluster sets controlPlaneRef %+v, which is the topology controller's to write", cluster.Spec.ControlPlaneRef)
	}
}

// The shape asked for is the shape written down: replicas reach the topology,
// and a run that asks for no workers declares none rather than declaring zero.
func TestClusterTopologyCarriesTheRequestedShape(t *testing.T) {
	full := NewCluster(ClusterName(0), 3, 2, DefaultKubernetesVersion)
	if got := full.Spec.Topology.ControlPlane.Replicas; got == nil || *got != 3 {
		t.Errorf("topology.controlPlane.replicas = %v, want 3", got)
	}
	if len(full.Spec.Topology.Workers.MachineDeployments) != 1 {
		t.Fatalf("topology has %d machine deployments, want 1", len(full.Spec.Topology.Workers.MachineDeployments))
	}
	md := full.Spec.Topology.Workers.MachineDeployments[0]
	if md.Class != WorkerClass {
		t.Errorf("machineDeployment.class = %q, want %q", md.Class, WorkerClass)
	}
	if md.Name != WorkerTopologyName {
		t.Errorf("machineDeployment.name = %q, want %q", md.Name, WorkerTopologyName)
	}
	if got := md.Replicas; got == nil || *got != 2 {
		t.Errorf("machineDeployment.replicas = %v, want 2", got)
	}

	none := NewCluster(ClusterName(0), 1, 0, DefaultKubernetesVersion)
	if len(none.Spec.Topology.Workers.MachineDeployments) != 0 {
		t.Errorf("a cluster asking for no workers declares %d machine deployments", len(none.Spec.Topology.Workers.MachineDeployments))
	}

	// Zero control plane machines is still a stated zero, not an absent field.
	// A class always names a control plane template, so the cluster gets a
	// control plane object either way - and left unstated its replica count
	// would be waiting for a webhook this project does not serve.
	noControlPlane := NewCluster(ClusterName(0), 0, 0, DefaultKubernetesVersion)
	if got := noControlPlane.Spec.Topology.ControlPlane.Replicas; got == nil || *got != 0 {
		t.Errorf("topology.controlPlane.replicas = %v for a cluster asking for no control plane machines, want a stated 0", got)
	}
}

// TestClusterClassRefersOnlyToTemplatesTheBlueprintCreates is the invariant a
// name typo breaks, and it breaks it silently: the ClusterClass reconciler
// reports the class not ready, the topology controller waits for a class that
// will never be ready, and every Cluster naming it sits with no infrastructure
// and no control plane while nothing in either object says why.
func TestClusterClassRefersOnlyToTemplatesTheBlueprintCreates(t *testing.T) {
	created := map[string]bool{}
	for _, obj := range Blueprint(BackendInMemory) {
		created[key(obj)] = true
	}

	class := NewClusterClass(BackendInMemory)
	refs := map[string]clusterv1.ClusterClassTemplateReference{
		"infrastructure":            class.Spec.Infrastructure.TemplateRef,
		"controlPlane":              class.Spec.ControlPlane.TemplateRef,
		"controlPlane.machineInfra": class.Spec.ControlPlane.MachineInfrastructure.TemplateRef,
		"workers[0].bootstrap":      class.Spec.Workers.MachineDeployments[0].Bootstrap.TemplateRef,
		"workers[0].infrastructure": class.Spec.Workers.MachineDeployments[0].Infrastructure.TemplateRef,
	}
	for field, ref := range refs {
		if ref.Name == "" || ref.Kind == "" || ref.APIVersion == "" {
			t.Errorf("%s is not fully specified: %+v", field, ref)
			continue
		}
		if !created[fmt.Sprintf("%s/%s", ref.Kind, ref.Name)] {
			t.Errorf("%s refers to %s/%s, which the blueprint does not create", field, ref.Kind, ref.Name)
		}
	}
}

// TestBlueprintCreatesTheClassLast is ordering the demo depends on for its
// output rather than for correctness: a class created before its templates is
// simply not ready until they exist.
func TestBlueprintCreatesTheClassLast(t *testing.T) {
	objs := Blueprint(BackendInMemory)
	if len(objs) == 0 {
		t.Fatal("the blueprint is empty")
	}
	if _, ok := objs[len(objs)-1].(*clusterv1.ClusterClass); !ok {
		t.Errorf("the last object of the blueprint is %T, want the ClusterClass", objs[len(objs)-1])
	}
	for _, obj := range objs[:len(objs)-1] {
		if _, ok := obj.(*clusterv1.ClusterClass); ok {
			t.Error("the blueprint holds more than one ClusterClass")
		}
	}
}

// TestWorkerRolloutStrategyIsSpelledOut covers what the absent MachineDeployment
// webhook would otherwise have defaulted.
//
// The topology controller copies the class's strategy onto the MachineDeployment
// it creates, so an unset strategy here is an unset strategy there, and the
// MachineDeployment reconciler fails every reconcile with "unexpected deployment
// strategy type: ".
func TestWorkerRolloutStrategyIsSpelledOut(t *testing.T) {
	strategy := NewClusterClass(BackendInMemory).Spec.Workers.MachineDeployments[0].Rollout.Strategy
	if strategy.Type != clusterv1.RollingUpdateMachineDeploymentStrategyType {
		t.Errorf("worker rollout strategy type = %q, want %q", strategy.Type, clusterv1.RollingUpdateMachineDeploymentStrategyType)
	}
	if strategy.RollingUpdate.MaxSurge == nil || strategy.RollingUpdate.MaxUnavailable == nil {
		t.Errorf("worker rolling update is not fully specified: %+v", strategy.RollingUpdate)
	}
}

// TestNamingTemplatesMatchTheNameHelpers keeps the two statements of a name in
// step. The class is what actually names the objects; the helpers are what the
// demo's status table, the integration tests and the walkthrough look them up
// by, and a class that stopped agreeing with them would leave every lookup
// finding nothing.
func TestNamingTemplatesMatchTheNameHelpers(t *testing.T) {
	class := NewClusterClass(BackendInMemory)
	const cluster = "demo-00"

	for _, tc := range []struct {
		field    string
		template string
		want     string
	}{
		{"infrastructure", class.Spec.Infrastructure.Naming.Template, InfraClusterName(cluster)},
		{"controlPlane", class.Spec.ControlPlane.Naming.Template, ControlPlaneName(cluster)},
		{"workers[0]", class.Spec.Workers.MachineDeployments[0].Naming.Template, WorkerDeploymentName(cluster)},
	} {
		got := renderName(tc.template, cluster, WorkerTopologyName)
		if got != tc.want {
			t.Errorf("%s naming template %q renders %q, but the name helper says %q",
				tc.field, tc.template, got, tc.want)
		}
	}
}

// TestDockerTemplateIsFullySpecified is the demo's stated contract, enforced
// rather than asserted in prose.
//
// The demo serves no webhooks, so nothing defaults what it creates. That was
// written down in the design doc and was not true of the docker backend: the
// control plane port came from the admission webhook, the demo left it zero,
// and the docker backend sets only the host. The resulting endpoint fails
// APIEndpoint.IsValid, and the control plane provider returns early on every
// reconcile without ever creating a Machine - while the DevCluster reports
// itself provisioned, so nothing else looks wrong.
func TestDockerTemplateIsFullySpecified(t *testing.T) {
	for _, backend := range []Backend{BackendInMemory, BackendDocker} {
		t.Run(string(backend), func(t *testing.T) {
			spec := NewDevClusterTemplate(backend).Spec.Template.Spec

			switch backend {
			case BackendDocker:
				// The docker backend fills in the host from the load balancer
				// it creates and takes the port as given, so the port has to
				// be here or the endpoint is never valid.
				if spec.ControlPlaneEndpoint.Port == 0 {
					t.Error("a docker-backed DevCluster has no control plane port, so its endpoint can never become valid and no control plane machine is ever created")
				}
			case BackendInMemory:
				// The in-memory backend assigns the port of the listener it
				// starts, so setting one here would be overwritten and would
				// suggest the demo had a say in it.
				if spec.ControlPlaneEndpoint.Port != 0 {
					t.Errorf("an in-memory DevCluster specifies port %d, but the backend assigns the listener's own port",
						spec.ControlPlaneEndpoint.Port)
				}
			}

			if spec.Backend.Docker == nil && spec.Backend.InMemory == nil {
				t.Error("neither backend is set, which the webhook would have defaulted")
			}
		})
	}
}

// Every workspace holds the same cluster names, which is what makes a
// cross-workspace leak visible in a demo rather than plausible.
func TestClusterNameIsWorkspaceIndependent(t *testing.T) {
	if got, want := ClusterName(0), "demo-00"; got != want {
		t.Errorf("ClusterName(0) = %q, want %q", got, want)
	}
	if ClusterName(1) == ClusterName(0) {
		t.Error("ClusterName is not unique within a workspace")
	}
}

// key names an object the way a ClusterClass template reference does: by kind
// and name. The kind is taken from the Go type, which is what the scheme
// registers it under.
func key(obj client.Object) string {
	t := reflect.TypeOf(obj)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return fmt.Sprintf("%s/%s", t.Name(), obj.GetName())
}

// renderName renders a ClusterClass naming template the way the topology
// controller does, with the arguments it supplies. Only the two this demo's
// templates use are bound: a template naming `.random` would render empty here
// and is exactly what these tests exist to keep out, because a random name is
// one nothing else in this project can look up.
func renderName(tmpl, cluster, topologyName string) string {
	t, err := template.New("name").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return fmt.Sprintf("<unparseable: %v>", err)
	}
	var out strings.Builder
	if err := t.Execute(&out, map[string]any{
		"cluster":           map[string]any{"name": cluster},
		"machineDeployment": map[string]any{"topologyName": topologyName},
	}); err != nil {
		return fmt.Sprintf("<unrenderable: %v>", err)
	}
	return out.String()
}
