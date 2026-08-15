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

// Package contractmetadata backs controllers/external.GetGKMetadataFunc
// (the fork's one deliberate, tracked exception to the upstream-is-read-only
// invariant - see AGENTS.md and ADR-0001's "Known gaps" section) with a
// static registry instead of a CustomResourceDefinition object lookup.
//
// Upstream resolves an externally-referenced object's current apiVersion by
// reading contract-version labels (e.g. "cluster.x-k8s.io/v1beta2: v1beta2")
// off that type's CustomResourceDefinition object. A KCP workspace consuming
// a type only via APIBinding has no such object - the CRD-shaped source of
// truth (the APIResourceSchema) lives in the exporting workspace instead,
// unreachable from a reconciler scoped to the consuming workspace. Since
// this fork's own tooling (kcp/internal/kcpfixtures) already reads the same
// generated CRD manifests to build APIResourceSchemas, this package reads
// the same files - plus the kustomize labels transformer that normally
// stamps the contract-version labels onto them at build time - to answer
// the same question without any object lookup at all.
package contractmetadata

import (
	"context"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Registry maps GroupKinds to the contract-version labels their
// CustomResourceDefinition would carry in a normal deployment.
type Registry struct {
	entries map[schema.GroupKind]map[string]string
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{entries: map[schema.GroupKind]map[string]string{}}
}

// Add registers gk with the given contract-version labels, e.g.
// {"cluster.x-k8s.io/v1beta2": "v1beta2"}.
func (r *Registry) Add(gk schema.GroupKind, labels map[string]string) {
	r.entries[gk] = labels
}

// GetGKMetadata implements controllers/external.GetGKMetadataFunc's
// signature: ctx and c are accepted (and ignored) only to match it, since
// this registry never makes a live call.
func (r *Registry) GetGKMetadata(_ context.Context, _ client.Reader, gk schema.GroupKind) (*metav1.PartialObjectMetadata, error) {
	labels, ok := r.entries[gk]
	if !ok {
		return nil, apierrors.NewNotFound(
			schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
			fmt.Sprintf("%s.%s", gk.Kind, gk.Group),
		)
	}
	md := &metav1.PartialObjectMetadata{}
	md.SetLabels(labels)
	return md, nil
}

// kustomizeLabels is the minimal shape this package reads out of a
// config/crd/kustomization.yaml's labels transformer - just enough to
// extract the pairs applied uniformly to every CRD that kustomization
// bundles (see e.g. test/infrastructure/docker/config/crd/kustomization.yaml).
type kustomizeLabels struct {
	Labels []struct {
		Pairs map[string]string `json:"pairs"`
	} `json:"labels"`
}

// LoadKustomizeLabels reads the label pairs a kustomize labels transformer
// applies, from a config/crd/kustomization.yaml file. Returns an empty map
// (not an error) if the file has no labels transformer.
func LoadKustomizeLabels(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is developer/test-supplied, not user input.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var parsed kustomizeLabels
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}

	merged := map[string]string{}
	for _, l := range parsed.Labels {
		for k, v := range l.Pairs {
			merged[k] = v
		}
	}
	return merged, nil
}
