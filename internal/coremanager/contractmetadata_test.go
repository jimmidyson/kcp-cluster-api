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

package coremanager

import (
	"errors"
	"maps"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/controllers/external"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/contractmetadata"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
)

// TestDevInfraContractLabelsMatchKustomization guards against
// devInfraContractLabels (hardcoded, since a built binary doesn't carry the
// source tree) silently drifting from the real kustomize labels transformer
// it's meant to mirror.
func TestDevInfraContractLabelsMatchKustomization(t *testing.T) {
	const rel = "infrastructure/docker/config/crd/kustomization.yaml"

	path, err := kcpfixtures.ManifestPath(kcpfixtures.ModuleClusterAPITest, rel)
	if err != nil {
		t.Fatalf("resolving %s: %v", rel, err)
	}

	want, err := contractmetadata.LoadKustomizeLabels(path)
	if err != nil {
		t.Fatalf("LoadKustomizeLabels(%q) error = %v", path, err)
	}

	if !maps.Equal(devInfraContractLabels, want) {
		t.Errorf("devInfraContractLabels = %v, want %v (matching %s)", devInfraContractLabels, want, path)
	}
}

func TestSetupProcessGlobalsRegistersDevTypes(t *testing.T) {
	SetupProcessGlobals()

	for _, kind := range []string{"DevCluster", "DevMachine", "DevClusterTemplate", "DevMachineTemplate"} {
		gk := schema.GroupKind{Group: infrav1.GroupVersion.Group, Kind: kind}
		md, err := external.GetGKMetadata(t.Context(), nil, gk)
		if err != nil {
			t.Fatalf("GetGKMetadata(%v) error = %v", gk, err)
		}
		if got := md.GetLabels()["cluster.x-k8s.io/v1beta2"]; got != "v1beta2" {
			t.Errorf("GetGKMetadata(%v) labels[cluster.x-k8s.io/v1beta2] = %q, want %q", gk, got, "v1beta2")
		}
	}
}

// TestProcessGlobalsResolveWithoutAWorkspaceClient covers FR-009. The
// conversion resolver installed by SetupProcessGlobals passes noReader, so
// what this asserts is what that resolver does: contract metadata resolves
// with no client at all, and therefore with nobody's client. A resolver that
// closed over one workspace's client would answer every other workspace's
// lookups with it, and nothing about that would be an error.
func TestProcessGlobalsResolveWithoutAWorkspaceClient(t *testing.T) {
	SetupProcessGlobals()

	gk := schema.GroupKind{Group: infrav1.GroupVersion.Group, Kind: "DevCluster"}
	got, err := external.GetAPIVersion(t.Context(), noReader{}, gk)
	if err != nil {
		t.Fatalf("GetAPIVersion(%v) error = %v", gk, err)
	}
	if want := infrav1.GroupVersion.String(); got != want {
		t.Errorf("GetAPIVersion(%v) = %q, want %q", gk, got, want)
	}
}

// TestNoReaderRefusesToRead guards the fallback. If the static registry is
// ever removed or stops covering a type, resolution reaches the reader — and
// what it finds there should be a sentence explaining the situation, not a nil
// dereference.
func TestNoReaderRefusesToRead(t *testing.T) {
	err := noReader{}.Get(t.Context(), client.ObjectKey{Name: "anything"}, &apiextensionsv1.CustomResourceDefinition{})
	if !errors.Is(err, errNoProcessWideClient) {
		t.Errorf("noReader.Get() = %v, want %v", err, errNoProcessWideClient)
	}
	if err := (noReader{}).List(t.Context(), &apiextensionsv1.CustomResourceDefinitionList{}); !errors.Is(err, errNoProcessWideClient) {
		t.Errorf("noReader.List() = %v, want %v", err, errNoProcessWideClient)
	}
}
