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
	"maps"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

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

func TestSetupContractMetadataRegistersDevTypes(t *testing.T) {
	SetupContractMetadata()

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
