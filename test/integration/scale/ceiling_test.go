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

// Package scale_test measures what this environment can host, before anything
// is designed against it.
//
// This is the scale feature's R10, and it is deliberately the first thing that
// runs. A sweep designed against a workspace count the environment cannot reach
// produces no departure point, and a capacity figure with no departure point behind it is a guess
// wearing a measurement's clothes. So the ceiling is established first, and the
// sweep is designed against the number this reports rather than against a
// number somebody hoped for.
//
// It is a measurement, not an assertion. It fails only when it cannot measure —
// which, per FR-022, is reported as "could not run" rather than as a pass.
package scale_test

import (
	"flag"
	"fmt"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	exportName  = "cluster-api-scale"
	bindingName = "cluster-api-scale"
)

// coreCRDs is one type, deliberately. The ceiling being measured is the cost of
// creating and binding a workspace, not the cost of the API surface bound into
// it; adding types would measure something else and make the number harder to
// attribute.
var coreCRDs = []string{"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml"}

var (
	targetWorkspaces = flag.Int("ceiling-target", 32,
		"How many workspaces to attempt. The default is modest so the measurement completes in a normal test run; "+
			"raise it to find where this environment actually stops.")
	ceilingBudget = flag.Duration("ceiling-budget", 10*time.Minute,
		"Wall-clock budget. Reaching it stops the measurement and reports the count achieved, which is a result, not a failure.")
)

// TestWorkspaceCeiling reports how many workspaces this environment can create
// and bind, and what each costs.
func TestWorkspaceCeiling(t *testing.T) {
	ctx := t.Context()

	crdPaths, err := kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, coreCRDs...)
	if err != nil {
		t.Fatalf("resolving CRD manifests: %v", err)
	}

	_, server := kcpenvtest.EnvironmentAndServer(t, "")
	baseCfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		apiextensionsv1.AddToScheme,
		apisv1alpha1.AddToScheme,
		clusterv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("building scheme: %v", err)
		}
	}

	rootCfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), logicalcluster.NewPath("root"))
	rootClient, err := client.New(rootCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("root client: %v", err)
	}

	if err := kcpfixtures.PublishAPIExport(ctx, rootClient, kcpfixtures.PublishAPIExportOptions{
		ExportName:   exportName,
		SchemaPrefix: "v1",
		CRDPaths:     crdPaths,
		CRDTransform: keepOneVersion,
	}); err != nil {
		t.Fatalf("publishing APIExport: %v", err)
	}
	// The endpoint slice is deliberately *not* waited on here. Its virtual
	// workspace URLs appear only once something has bound, so waiting before
	// the loop deadlocks against work the loop has not done yet — which is how
	// the first run of this test failed.
	deadline := time.Now().Add(*ceilingBudget)
	creates := make([]time.Duration, 0, *targetWorkspaces)
	binds := make([]time.Duration, 0, *targetWorkspaces)

	var stoppedBy string
	for i := range *targetWorkspaces {
		if time.Now().After(deadline) {
			stoppedBy = fmt.Sprintf("wall-clock budget %s", *ceilingBudget)
			break
		}

		startCreate := time.Now()
		wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		creates = append(creates, time.Since(startCreate))

		cfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
		wsClient, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			stoppedBy = fmt.Sprintf("client for workspace %d: %v", i, err)
			break
		}

		startBind := time.Now()
		if err := kcpfixtures.BindExport(ctx, wsClient, kcpfixtures.BindExportOptions{
			BindingName:  bindingName,
			ExportPath:   "root",
			ExportName:   exportName,
			ReadyTimeout: 90 * time.Second,
		}); err != nil {
			stoppedBy = fmt.Sprintf("binding workspace %d: %v", i, err)
			break
		}
		binds = append(binds, time.Since(startBind))
	}

	reached := len(binds)

	// A ceiling of zero is not a measurement of zero capacity — it means the
	// measurement itself did not run. FR-022 requires that be distinguishable
	// from a result, so it is the one failing outcome here.
	if reached == 0 {
		t.Fatalf("could not run: no workspace could be created and bound (stopped by: %s)", stoppedBy)
	}

	if stoppedBy == "" {
		stoppedBy = fmt.Sprintf("reached the requested target of %d", *targetWorkspaces)
	}

	// Now that something is bound, the export should be serving. This confirms
	// the workspaces counted above are genuinely consumable rather than merely
	// created, which is the difference between a ceiling and a tally.
	sliceReady := "yes"
	if err := kcpfixtures.WaitForAPIExportEndpointSlice(ctx, rootClient, exportName, 60*time.Second); err != nil {
		sliceReady = fmt.Sprintf("no (%v)", err)
	}

	t.Logf("workspace ceiling in this environment")
	t.Logf("  export serving:    %s", sliceReady)
	t.Logf("  bound workspaces:  %d", reached)
	t.Logf("  stopped by:        %s", stoppedBy)
	t.Logf("  create per ws:     p50=%s p99=%s", percentile(creates, 0.50), percentile(creates, 0.99))
	t.Logf("  bind per ws:       p50=%s p99=%s", percentile(binds, 0.50), percentile(binds, 0.99))
	t.Logf("  total per ws:      p50=%s", percentile(creates, 0.50)+percentile(binds, 0.50))

	// Onboarding cost is expected to grow with fleet size — that is the
	// quadratic this feature exists to find. Reporting first against last is
	// the cheapest possible signal of it, and it is only a signal: the sweep,
	// not this, is what establishes a departure point.
	if reached >= 4 {
		firstQuarter := mean(binds[:reached/4])
		lastQuarter := mean(binds[reached-reached/4:])
		t.Logf("  bind cost drift:   first quarter %s -> last quarter %s (ratio %.2fx)",
			firstQuarter, lastQuarter, float64(lastQuarter)/float64(firstQuarter))
	}

	if reached < *targetWorkspaces {
		t.Logf("NOTE: the requested target of %d was not reached. Any sweep designed above %d workspaces "+
			"cannot run here, and a capacity figure derived from it would be an extrapolation.",
			*targetWorkspaces, reached)
	}
}

// keepOneVersion trims the published CRD to its v1beta2 version alone.
//
// Not a convenience: kcp rejects a multi-version schema outright unless a
// conversion strategy is given, and a conversion strategy means a webhook
// server, which this measurement has no business standing up. The first run of
// this test failed on exactly that, which is a live confirmation of the G4
// spike's finding that conversion is mandatory rather than optional for Cluster
// API's two served versions.
//
// The trim does not distort what is being measured. This counts the cost of
// creating and binding a workspace; the number of versions in the bound schema
// is not part of it.
func keepOneVersion(crd *apiextensionsv1.CustomResourceDefinition) {
	if crd.Spec.Group != clusterv1.GroupVersion.Group {
		return
	}
	kept := crd.Spec.Versions[:0]
	for _, v := range crd.Spec.Versions {
		if v.Name == clusterv1.GroupVersion.Version {
			kept = append(kept, v)
		}
	}
	crd.Spec.Versions = kept
}

func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func mean(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total / time.Duration(len(ds))
}
