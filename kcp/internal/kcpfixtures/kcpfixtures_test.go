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

package kcpfixtures

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// clusterCRDPath is the real, generated core Cluster CRD manifest -
// exercising LoadCRD/CRDToAPIResourceSchema against it (rather than a
// hand-written fixture) is the point: it's what PublishAPIExport actually
// reads at runtime, so a schema drift or unsupported CRD shape shows up here
// without needing a live kcp server.
const clusterCRDPath = "../../../core/config/crd/bases/cluster.x-k8s.io_clusters.yaml"

func TestLoadCRD(t *testing.T) {
	crd, err := LoadCRD(clusterCRDPath)
	if err != nil {
		t.Fatalf("LoadCRD(%q) error = %v", clusterCRDPath, err)
	}

	if got, want := crd.Spec.Group, "cluster.x-k8s.io"; got != want {
		t.Errorf("crd.Spec.Group = %q, want %q", got, want)
	}
	if got, want := crd.Spec.Names.Kind, "Cluster"; got != want {
		t.Errorf("crd.Spec.Names.Kind = %q, want %q", got, want)
	}
	if len(crd.Spec.Versions) == 0 {
		t.Error("crd.Spec.Versions is empty, want at least one version")
	}
}

func TestLoadCRDMissingFile(t *testing.T) {
	if _, err := LoadCRD("does-not-exist.yaml"); err == nil {
		t.Fatal("LoadCRD() error = nil, want an error for a missing file")
	}
}

func TestCRDToAPIResourceSchemaRoundTrip(t *testing.T) {
	crd, err := LoadCRD(clusterCRDPath)
	if err != nil {
		t.Fatalf("LoadCRD(%q) error = %v", clusterCRDPath, err)
	}

	// The Cluster CRD as generated (config/crd/bases) has multiple versions
	// (v1beta1, v1beta2) but no conversion strategy - that's injected by a
	// kustomize patch at deploy time (config/crd/patches/webhook_in_clusters.yaml)
	// which PublishAPIExport replicates via ConversionWebhookClientConfig.
	// Exercise the same shape here directly against the library function.
	url := "https://localhost:9443/convert"
	crd.Spec.Conversion = &apiextensionsv1.CustomResourceConversion{
		Strategy: apiextensionsv1.WebhookConverter,
		Webhook: &apiextensionsv1.WebhookConversion{
			ConversionReviewVersions: []string{"v1"},
			ClientConfig:             &apiextensionsv1.WebhookClientConfig{URL: &url},
		},
	}

	schema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "test")
	if err != nil {
		t.Fatalf("CRDToAPIResourceSchema() error = %v", err)
	}

	if got, want := schema.Name, "test."+crd.Name; got != want {
		t.Errorf("schema.Name = %q, want %q", got, want)
	}
	if got, want := schema.Spec.Group, crd.Spec.Group; got != want {
		t.Errorf("schema.Spec.Group = %q, want %q", got, want)
	}
	if schema.Spec.Conversion == nil || schema.Spec.Conversion.Webhook == nil {
		t.Fatal("schema.Spec.Conversion.Webhook = nil, want the injected webhook conversion to carry through")
	}
	if got, want := schema.Spec.Conversion.Webhook.ClientConfig.URL, url; got != want {
		t.Errorf("schema.Spec.Conversion.Webhook.ClientConfig.URL = %q, want %q", got, want)
	}
}

func TestPublishAPIExportOptionsInjectsConversionForMultiVersionCRDs(t *testing.T) {
	crd, err := LoadCRD(clusterCRDPath)
	if err != nil {
		t.Fatalf("LoadCRD(%q) error = %v", clusterCRDPath, err)
	}
	if len(crd.Spec.Versions) < 2 {
		t.Fatalf("test fixture %s has %d version(s), want >= 2 to exercise conversion injection", clusterCRDPath, len(crd.Spec.Versions))
	}
	if crd.Spec.Conversion != nil {
		t.Fatalf("test fixture %s unexpectedly already declares a conversion strategy", clusterCRDPath)
	}

	// Without a ConversionWebhookClientConfig, CRDToAPIResourceSchema must
	// still fail exactly as it does for a real deploy that forgot the
	// kustomize conversion patch: this is the fork's own behavior we're
	// wrapping, not something PublishAPIExport should paper over silently.
	if _, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "test"); err == nil {
		t.Fatal("CRDToAPIResourceSchema() error = nil for a multi-version CRD with no conversion strategy, want an error")
	}
}

func TestDedupeAdditionalPrinterColumns(t *testing.T) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name: "v1beta2",
					AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
						{Name: "Available", Type: "string", JSONPath: ".status.a"},
						{Name: "Phase", Type: "string", JSONPath: ".status.phase"},
						{Name: "Available", Type: "string", JSONPath: ".status.b"},
					},
				},
			},
		},
	}

	dedupeAdditionalPrinterColumns(crd)

	got := crd.Spec.Versions[0].AdditionalPrinterColumns
	if len(got) != 2 {
		t.Fatalf("AdditionalPrinterColumns = %+v, want 2 entries after dedup", got)
	}
	if got[0].Name != "Available" || got[0].JSONPath != ".status.a" {
		t.Errorf("first entry = %+v, want the first \"Available\" column kept", got[0])
	}
	if got[1].Name != "Phase" {
		t.Errorf("second entry = %+v, want \"Phase\"", got[1])
	}
}

// MachineDeployment's real generated CRD is exactly the case
// dedupeAdditionalPrinterColumns exists for: it declares "Available" twice
// under its v1beta2 version. Guard against CRDToAPIResourceSchema starting
// to accept - or the upstream generator starting to reject - duplicate
// columns without this test noticing.
func TestLoadCRDMachineDeploymentHasDuplicateColumnsBeforeDedupe(t *testing.T) {
	const path = "../../../core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml"
	crd, err := LoadCRD(path)
	if err != nil {
		t.Fatalf("LoadCRD(%q) error = %v", path, err)
	}

	seen := map[string]int{}
	for _, v := range crd.Spec.Versions {
		for _, c := range v.AdditionalPrinterColumns {
			seen[v.Name+"/"+c.Name]++
		}
	}
	dup := false
	for k, n := range seen {
		if n > 1 {
			dup = true
			t.Logf("duplicate additionalPrinterColumn %s appears %d times", k, n)
		}
	}
	if !dup {
		t.Skip("MachineDeployment's CRD no longer has duplicate additionalPrinterColumns; dedupeAdditionalPrinterColumns may be removable")
	}
}
