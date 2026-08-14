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

package contractmetadata

import (
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestRegistryGetGKMetadata(t *testing.T) {
	gk := schema.GroupKind{Group: "infrastructure.cluster.x-k8s.io", Kind: "DevCluster"}
	labels := map[string]string{"cluster.x-k8s.io/v1beta2": "v1beta2"}

	reg := New()
	reg.Add(gk, labels)

	md, err := reg.GetGKMetadata(t.Context(), nil, gk)
	if err != nil {
		t.Fatalf("GetGKMetadata() error = %v", err)
	}
	if got := md.GetLabels()["cluster.x-k8s.io/v1beta2"]; got != "v1beta2" {
		t.Errorf("label cluster.x-k8s.io/v1beta2 = %q, want %q", got, "v1beta2")
	}
}

func TestRegistryGetGKMetadataUnregisteredIsNotFound(t *testing.T) {
	reg := New()

	_, err := reg.GetGKMetadata(t.Context(), nil, schema.GroupKind{Group: "unknown.example.com", Kind: "Widget"})
	if err == nil {
		t.Fatal("GetGKMetadata() error = nil, want a NotFound error for an unregistered GroupKind")
	}
	if !apierrors.IsNotFound(err) {
		t.Errorf("GetGKMetadata() error = %v, want an apierrors.IsNotFound error (controllers/external.GetObjectFromContractVersionedRef branches on this)", err)
	}
}

func TestLoadKustomizeLabels(t *testing.T) {
	const path = "../../../test/infrastructure/docker/config/crd/kustomization.yaml"

	labels, err := LoadKustomizeLabels(path)
	if err != nil {
		t.Fatalf("LoadKustomizeLabels(%q) error = %v", path, err)
	}

	want := map[string]string{
		"cluster.x-k8s.io/v1beta1": "v1beta1",
		"cluster.x-k8s.io/v1beta2": "v1beta2",
	}
	for k, v := range want {
		if got := labels[k]; got != v {
			t.Errorf("labels[%q] = %q, want %q (full: %v)", k, got, v, labels)
		}
	}
}

func TestLoadKustomizeLabelsMissingFile(t *testing.T) {
	if _, err := LoadKustomizeLabels("does-not-exist.yaml"); err == nil {
		t.Fatal("LoadKustomizeLabels() error = nil, want an error for a missing file")
	}
}
