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

package external

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/internal/contract"
)

// GKMetadataGetter resolves the metadata carrying contract-version labels for
// a GroupKind.
type GKMetadataGetter = contract.GKMetadataGetter

// SetGKMetadataGetter overrides how contract-version metadata is resolved for
// a GroupKind. Passing nil restores the default behaviour, which reads the
// CustomResourceDefinition object.
//
// Resolving an externally-referenced object's current apiVersion — for
// spec.infrastructureRef, spec.bootstrap.configRef, spec.controlPlaneRef and
// the rest — requires reading contract-version labels off the referenced
// type's CustomResourceDefinition. Every such lookup funnels through a single
// point, so this one override covers GetObjectFromContractVersionedRef and the
// contract version and apiVersion helpers alike.
//
// It exists for environments where a CustomResourceDefinition object is not
// the source of truth: where a type is served through an aggregation or
// binding mechanism rather than by hosting its CRD, no such object exists to
// read, and reconciliation of every contract-versioned reference fails.
//
// This is the public counterpart of the SetAPIVersionGetter escape hatch in
// core/webhooks/conversion, which solves the same problem for the conversion
// webhook's call path.
//
// It is process-global and not safe for concurrent use with reconciliation.
// Call it once during setup, before controllers start.
func SetGKMetadataGetter(f GKMetadataGetter) {
	contract.SetGKMetadataGetter(f)
}

// GetAPIVersion returns the apiVersion currently served for a GroupKind,
// according to the contract-version metadata resolved for it.
//
// It is the public entry point for callers outside this module that need the
// same resolution the contract-versioned reference helpers perform internally,
// and it honours any getter installed via SetGKMetadataGetter.
func GetAPIVersion(ctx context.Context, c client.Reader, gk schema.GroupKind) (string, error) {
	return contract.GetAPIVersion(ctx, c, gk)
}

// GetGKMetadata returns the metadata carrying contract-version labels for a
// GroupKind, honouring any getter installed via SetGKMetadataGetter.
func GetGKMetadata(ctx context.Context, c client.Reader, gk schema.GroupKind) (*metav1.PartialObjectMetadata, error) {
	return contract.GetGKMetadata(ctx, c, gk)
}
