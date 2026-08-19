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

// Package kcpfixtures builds the KCP-native objects (APIResourceSchema,
// APIExport, APIExportEndpointSlice, APIBinding) that publish a set of
// generated upstream CRDs into a KCP workspace and bind another workspace to
// them. It is shared by the Phase 1 manual dev-loop (kcp/hack/apiexport-bootstrap)
// and the Phase 1 integration test, so the two stay in sync per D3's APIExport
// scope decision in ADR-0001 instead of hand-rolling this twice.
//
// It intentionally does not do horizontal-sharding topology (Partition/
// PartitionSet, see D6) - that's out of scope until a real kcp install runs
// multiple shards.
package kcpfixtures

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// LoadCRD reads and decodes a single CustomResourceDefinition manifest, as
// produced by controller-gen under config/crd/bases.
func LoadCRD(path string) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is developer/test-supplied, not user input.
	if err != nil {
		return nil, fmt.Errorf("reading CRD manifest %s: %w", path, err)
	}

	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(crd); err != nil {
		return nil, fmt.Errorf("decoding CRD manifest %s: %w", path, err)
	}
	return crd, nil
}

// dedupeAdditionalPrinterColumns drops any additionalPrinterColumns entry
// (per version) whose name repeats an earlier one, keeping the first
// occurrence. Some of this fork's own controller-gen-generated CRD manifests
// carry duplicate-named columns (e.g. MachineDeployment's v1beta2 version
// declares "Available" twice) that vanilla Kubernetes CRD admission
// tolerates, but kcp's APIResourceSchema validation rejects outright
// ("Duplicate value"). This is a real difference between the two, not a
// CRDToAPIResourceSchema bug, so it's worked around here rather than
// upstream (which stays untouched per AGENTS.md).
func dedupeAdditionalPrinterColumns(crd *apiextensionsv1.CustomResourceDefinition) {
	for i := range crd.Spec.Versions {
		cols := crd.Spec.Versions[i].AdditionalPrinterColumns
		seen := make(map[string]bool, len(cols))
		deduped := cols[:0]
		for _, c := range cols {
			if seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			deduped = append(deduped, c)
		}
		crd.Spec.Versions[i].AdditionalPrinterColumns = deduped
	}
}

// PublishAPIExportOptions configures PublishAPIExport.
type PublishAPIExportOptions struct {
	// ExportName is the name of the APIExport (and, by convention, of the
	// APIExportEndpointSlice created for it).
	ExportName string

	// SchemaPrefix is passed to apisv1alpha1.CRDToAPIResourceSchema; the
	// resulting APIResourceSchema is named "<prefix>.<crd.Name>".
	SchemaPrefix string

	// CRDPaths are the CRD manifests to publish, converted to
	// APIResourceSchemas.
	CRDPaths []string

	// ConversionWebhookClientConfig, if set, is injected as a Webhook
	// conversion strategy into any loaded CRD that has more than one
	// version (and doesn't already declare a conversion strategy) before
	// converting it to an APIResourceSchema - mirroring what this fork's
	// config/crd/patches/webhook_in_*.yaml kustomize patches do for a real
	// deployment. apisv1alpha1.CRDToAPIResourceSchema requires the
	// ClientConfig to use URL, not Service (kcp doesn't resolve in-cluster
	// Service references for this).
	ConversionWebhookClientConfig *apiextensionsv1.WebhookClientConfig

	// CRDTransform, if set, is called on each loaded CRD before conversion
	// to an APIResourceSchema (and before the ConversionWebhookClientConfig
	// injection above). Use it to work around CRDs whose conversion isn't
	// viable under kcp's APIBinding model - e.g. dropping a non-storage
	// version so no conversion strategy is required at all - by trimming
	// crd.Spec.Versions down. See ADR-0001's "Known gaps" section.
	CRDTransform func(crd *apiextensionsv1.CustomResourceDefinition)
}

// PublishAPIExport converts each of opts.CRDPaths to an APIResourceSchema,
// creates an APIExport referencing them, and creates an APIExportEndpointSlice
// for that export - all in the workspace cl is scoped to. All creates are
// idempotent (AlreadyExists is treated as success) so this is safe to call
// repeatedly against a long-lived dev-loop workspace.
//
// It deliberately does not wait for the APIExportEndpointSlice to be
// populated: kcp's apiexportendpointsliceurls controller only fills in
// status.endpoints once at least one APIBinding is consuming the export (see
// updateEndpoints in kcp's apiexportendpointsliceurls_reconcile.go - a slice
// with zero consumers is explicitly left/reset empty). Call
// WaitForAPIExportEndpointSlice after binding at least one workspace to this
// export via BindExport.
func PublishAPIExport(ctx context.Context, cl client.Client, opts PublishAPIExportOptions) error {
	schemaNames := make([]string, 0, len(opts.CRDPaths))
	for _, path := range opts.CRDPaths {
		crd, err := LoadCRD(path)
		if err != nil {
			return err
		}
		dedupeAdditionalPrinterColumns(crd)

		if opts.CRDTransform != nil {
			opts.CRDTransform(crd)
		}

		if crd.Spec.Conversion == nil && len(crd.Spec.Versions) > 1 && opts.ConversionWebhookClientConfig != nil {
			crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
				Strategy: apiextensionsv1.WebhookConverter,
				Webhook: &apiextensionsv1.WebhookConversion{
					ConversionReviewVersions: []string{"v1"},
					ClientConfig:             opts.ConversionWebhookClientConfig,
				},
			}
		}

		schema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, opts.SchemaPrefix)
		if err != nil {
			return fmt.Errorf("converting CRD %s to APIResourceSchema: %w", crd.Name, err)
		}

		if err := cl.Create(ctx, schema); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating APIResourceSchema %s: %w", schema.Name, err)
		}
		schemaNames = append(schemaNames, schema.Name)
	}

	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: opts.ExportName},
		Spec:       apisv1alpha1.APIExportSpec{LatestResourceSchemas: schemaNames},
	}
	if err := cl.Create(ctx, export); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating APIExport %s: %w", opts.ExportName, err)
	}

	slice := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: opts.ExportName},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{Name: opts.ExportName},
		},
	}
	if err := cl.Create(ctx, slice); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating APIExportEndpointSlice %s: %w", opts.ExportName, err)
	}

	return nil
}

// WaitForAPIExportEndpointSlice waits for the named APIExportEndpointSlice
// (in the workspace cl is scoped to) to be populated with at least one shard
// endpoint. Call this only after at least one workspace has bound to the
// export (see PublishAPIExport's doc comment for why). Defaults to a 30s
// timeout.
func WaitForAPIExportEndpointSlice(ctx context.Context, cl client.Client, name string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	key := client.ObjectKey{Name: name}
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, timeout, true, func(ctx context.Context) (bool, error) {
		got := &apisv1alpha1.APIExportEndpointSlice{}
		if err := cl.Get(ctx, key, got); err != nil {
			return false, nil //nolint:nilerr // transient; keep polling until timeout.
		}
		return len(got.Status.APIExportEndpoints) > 0, nil
	})
}

// BindExportOptions configures BindExport.
type BindExportOptions struct {
	// BindingName is the name of the APIBinding created in the consuming
	// workspace.
	BindingName string

	// ExportPath is the (human-readable) workspace path containing the
	// APIExport, e.g. "root". Empty means the same workspace cl is scoped
	// to.
	ExportPath string

	// ExportName is the APIExport's name.
	ExportName string

	// ReadyTimeout bounds how long BindExport waits for the APIBinding to
	// reach phase Bound. Defaults to 30s.
	ReadyTimeout time.Duration
}

// BindExport creates an APIBinding for opts.ExportName in the workspace cl is
// scoped to, and waits for it to become Bound. Idempotent: an
// already-existing APIBinding is left as-is and only waited on.
func BindExport(ctx context.Context, cl client.Client, opts BindExportOptions) error {
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 30 * time.Second
	}

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: opts.BindingName},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: opts.ExportPath,
					Name: opts.ExportName,
				},
			},
		},
	}
	if err := cl.Create(ctx, binding); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating APIBinding %s: %w", opts.BindingName, err)
	}

	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, opts.ReadyTimeout, true, func(ctx context.Context) (bool, error) {
		got := &apisv1alpha1.APIBinding{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(binding), got); err != nil {
			return false, nil //nolint:nilerr // transient; keep polling until timeout.
		}
		return got.Status.Phase == apisv1alpha1.APIBindingPhaseBound, nil
	})
}

// KeepStorageVersion trims a CRD to the single version kcp will store, for
// use as a PublishAPIExportOptions.CRDTransform.
//
// A multi-version CRD needs a conversion strategy before kcp accepts it as an
// APIResourceSchema, and a conversion strategy means a webhook server. A
// caller that serves no webhooks - because webhook wiring is single-workspace
// by construction until the conversion plan's G4 lands - publishes one version
// instead.
func KeepStorageVersion(crd *apiextensionsv1.CustomResourceDefinition) {
	kept := crd.Spec.Versions[:0]
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			kept = append(kept, v)
		}
	}
	crd.Spec.Versions = kept
}
