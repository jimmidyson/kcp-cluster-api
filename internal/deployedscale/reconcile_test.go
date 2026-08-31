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

package deployedscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReconcileWithinAndOutsideTolerance(t *testing.T) {
	// The same program doing the same work, sampled two ways: close enough is
	// agreement, and a large divergence is a finding about one instrument.
	close := Reconcile("goroutinesPerWorkspace", ComponentCore, "evidence/sweep-report-core.json", 47.0, 45.0, DefaultTolerance)
	if !close.WithinTolerance {
		t.Errorf("47 against 45 was treated as a disagreement (ratio %.2f)", close.Ratio)
	}

	far := Reconcile("goroutinesPerWorkspace", ComponentCore, "evidence/sweep-report-core.json", 94.0, 45.0, DefaultTolerance)
	if far.WithinTolerance {
		t.Error("a two-fold divergence was treated as noise")
	}
	if far.Ratio < 2.0 || far.Ratio > 2.1 {
		t.Errorf("ratio = %v, want about 2.09", far.Ratio)
	}
}

// TestNoReferenceIsNotAgreement: a zero in-process figure means the reference
// is missing, and calling that "within tolerance" would let a run with nothing
// to compare against report itself as reconciled.
func TestNoReferenceIsNotAgreement(t *testing.T) {
	got := Reconcile("goroutinesPerWorkspace", ComponentCore, "nowhere", 47.0, 0, DefaultTolerance)
	if got.WithinTolerance {
		t.Error("a missing reference was reported as agreement")
	}
	if got.Ratio != 0 {
		t.Errorf("ratio = %v with no reference", got.Ratio)
	}
}

func writeSweep(t *testing.T, facts string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sweep-report-core.json")
	body := `{"title":"Active workspace sweep","facts":` + facts + `,"samples":[]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	return path
}

func TestLoadSweepReference(t *testing.T) {
	path := writeSweep(t, `{"deploymentName":"core-manager","goroutinesPerWorkspace":"47.0","heapBytesPerWorkspace":"2496000"}`)

	ref, err := LoadSweepReference(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if ref.DeploymentName != ComponentCore {
		t.Errorf("deployment = %q", ref.DeploymentName)
	}
	if ref.GoroutinesPerWorkspace != 47.0 {
		t.Errorf("goroutines = %v", ref.GoroutinesPerWorkspace)
	}
	if ref.HeapBytesPerWorkspace != 2_496_000 {
		t.Errorf("heap = %v", ref.HeapBytesPerWorkspace)
	}
}

// TestANotMeasuredFactIsNotZero is the one that matters most. The in-process
// instrument writes prose into these facts when a fit did not resolve, and
// coercing that to zero would produce a confident reconciliation against a
// reference that does not exist.
func TestANotMeasuredFactIsNotZero(t *testing.T) {
	path := writeSweep(t, `{"deploymentName":"core-manager","goroutinesPerWorkspace":"not measured: one checkpoint is a point, not a slope"}`)

	_, err := LoadSweepReference(path)
	if err == nil {
		t.Fatal("a not-measured reference loaded")
	}
	if !strings.Contains(err.Error(), "nothing to reconcile against") {
		t.Errorf("error %q does not say why", err)
	}
}

// Heap is optional: the in-process instrument legitimately reports it as not
// measured, and that must not take the goroutine comparison down with it.
func TestAMissingHeapFactStillYieldsAGoroutineReference(t *testing.T) {
	path := writeSweep(t, `{"deploymentName":"core-manager","goroutinesPerWorkspace":"47.0","heapBytesPerWorkspace":"not measured"}`)

	ref, err := LoadSweepReference(path)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if ref.GoroutinesPerWorkspace != 47.0 {
		t.Errorf("goroutines = %v", ref.GoroutinesPerWorkspace)
	}
	if ref.HeapBytesPerWorkspace != 0 {
		t.Errorf("heap = %v, want zero for absent", ref.HeapBytesPerWorkspace)
	}
}

func TestLoadSweepReferenceRejectsRubbish(t *testing.T) {
	if _, err := LoadSweepReference(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing file loaded")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := LoadSweepReference(path); err == nil {
		t.Error("malformed json loaded")
	}

	if _, err := LoadSweepReference(writeSweep(t, `{"deploymentName":"core-manager"}`)); err == nil {
		t.Error("a report with no per-workspace figure loaded")
	}
}

// TestAgainstTheCommittedSweep is the reconciliation this repository will
// actually make: the deployed core-manager against the in-process core
// deployment sweep. Reading the committed artefact keeps the comparison
// re-derivable rather than quoted.
func TestAgainstTheCommittedSweep(t *testing.T) {
	const committed = "../../specs/20260820-152056-clusterclass-based-clusters/evidence/sweep-report-core.json"
	if _, err := os.Stat(committed); err != nil {
		t.Skipf("the committed sweep is not present: %v", err)
	}

	ref, err := LoadSweepReference(committed)
	if err != nil {
		t.Fatalf("the committed core sweep does not load as a reference: %v", err)
	}
	if ref.DeploymentName != ComponentCore {
		t.Errorf("the committed sweep says it measured %q, not %s: a deployed core-manager reconciled "+
			"against another deployment's sweep would be a confident wrong answer", ref.DeploymentName, ComponentCore)
	}
	if ref.GoroutinesPerWorkspace <= 0 {
		t.Errorf("the committed sweep has no usable per-workspace goroutine figure: %v", ref.GoroutinesPerWorkspace)
	}
	t.Logf("in-process reference: %s costs %.1f goroutines per workspace",
		ref.DeploymentName, ref.GoroutinesPerWorkspace)
}
