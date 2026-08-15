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

package verify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnavailableCapabilityIsItsOwnOutcome is the contract this package
// exists for (FR-012, SC-009). A step that cannot run because the environment
// lacks something must not be counted as a pass, must not vanish from the
// summary, and must be distinguishable from a genuine failure by exit status
// alone - without parsing logs.
func TestUnavailableCapabilityIsItsOwnOutcome(t *testing.T) {
	docker := Capability{
		Name:  "container runtime",
		Check: func() error { return errors.New("no docker socket") },
	}

	var summary strings.Builder
	results, code := Run(&summary, []Step{
		{Name: "test:unit", Run: func() error { return nil }},
		{Name: "test:integration", Needs: []Capability{docker}, Run: func() error { return nil }},
	})

	if code == ExitPass {
		t.Fatal("exit code is ExitPass; a step that could not run must never be reported as success")
	}
	if code == ExitFail {
		t.Fatal("exit code is ExitFail; 'could not run' must be distinguishable from a genuine failure by status alone")
	}
	if code != ExitCouldNotRun {
		t.Fatalf("exit code = %d, want ExitCouldNotRun (%d)", code, ExitCouldNotRun)
	}

	if got := outcomeFor(t, results, "test:integration"); got != OutcomeCouldNotRun {
		t.Errorf("test:integration outcome = %v, want OutcomeCouldNotRun", got)
	}
	if got := outcomeFor(t, results, "test:unit"); got != OutcomePass {
		t.Errorf("test:unit outcome = %v, want OutcomePass", got)
	}

	out := summary.String()
	if !strings.Contains(out, "test:integration") {
		t.Error("summary omits the step that did not run; silent omission is the failure mode this exists to prevent")
	}
	if !strings.Contains(out, "container runtime") {
		t.Errorf("summary does not name the missing capability; got:\n%s", out)
	}
}

// TestCapabilitiesCheckedBeforeStepRuns covers FR-013: an unmet capability is
// reported up front, not discovered part-way through the work it gates.
func TestCapabilitiesCheckedBeforeStepRuns(t *testing.T) {
	ran := false
	docker := Capability{
		Name:  "container runtime",
		Check: func() error { return errors.New("unavailable") },
	}

	var summary strings.Builder
	_, code := Run(&summary, []Step{
		{
			Name:  "test:integration",
			Needs: []Capability{docker},
			Run:   func() error { ran = true; return nil },
		},
	})

	if ran {
		t.Error("step body executed despite an unmet capability; the check must gate the step, not follow it")
	}
	if code != ExitCouldNotRun {
		t.Errorf("exit code = %d, want ExitCouldNotRun (%d)", code, ExitCouldNotRun)
	}
}

// TestGenuineFailureIsDistinct guards the other side of the same contract: a
// step that ran and failed must not be confused with one that never ran.
func TestGenuineFailureIsDistinct(t *testing.T) {
	var summary strings.Builder
	results, code := Run(&summary, []Step{
		{Name: "lint", Run: func() error { return errors.New("boom") }},
	})

	if code != ExitFail {
		t.Fatalf("exit code = %d, want ExitFail (%d)", code, ExitFail)
	}
	if got := outcomeFor(t, results, "lint"); got != OutcomeFail {
		t.Errorf("lint outcome = %v, want OutcomeFail", got)
	}
}

// TestAllPass is the ordinary case: every step runs and succeeds.
func TestAllPass(t *testing.T) {
	var summary strings.Builder
	_, code := Run(&summary, []Step{
		{Name: "build", Run: func() error { return nil }},
		{Name: "test:unit", Run: func() error { return nil }},
	})

	if code != ExitPass {
		t.Fatalf("exit code = %d, want ExitPass (%d)", code, ExitPass)
	}
}

// TestFailureOutranksCouldNotRun: if something genuinely broke, that is the
// headline, not the environment gap.
func TestFailureOutranksCouldNotRun(t *testing.T) {
	unavailable := Capability{Name: "container runtime", Check: func() error { return errors.New("nope") }}

	var summary strings.Builder
	_, code := Run(&summary, []Step{
		{Name: "test:integration", Needs: []Capability{unavailable}, Run: func() error { return nil }},
		{Name: "lint", Run: func() error { return errors.New("boom") }},
	})

	if code != ExitFail {
		t.Errorf("exit code = %d, want ExitFail (%d): a real failure must outrank a missing capability", code, ExitFail)
	}
}

func outcomeFor(t *testing.T, results []Result, step string) Outcome {
	t.Helper()
	for _, r := range results {
		if r.Step == step {
			return r.Outcome
		}
	}
	t.Fatalf("no result recorded for step %q; every step must appear in the results", step)
	return OutcomePass
}

// TestWriteReportSurvivesTheRunner covers why the report exists at all: task
// runners collapse every failure to one exit code (go-task uses 201), so the
// distinct statuses this package returns do not reach a caller invoking a
// named target. The report carries the outcome structurally instead.
func TestWriteReportSurvivesTheRunner(t *testing.T) {
	unavailable := Capability{Name: "container runtime", Check: func() error { return errors.New("no socket") }}

	var summary strings.Builder
	results, _ := Run(&summary, []Step{
		{Name: "test:unit", Run: func() error { return nil }},
		{Name: "test:integration", Needs: []Capability{unavailable}, Run: func() error { return nil }},
	})

	path := filepath.Join(t.TempDir(), "nested", "verify-result.json")
	if err := WriteReport(path, results); err != nil {
		t.Fatalf("WriteReport() error = %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading report: %v", err)
	}
	var rep Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}

	if rep.Status != "could-not-run" {
		t.Errorf("report status = %q, want %q", rep.Status, "could-not-run")
	}
	if rep.ExitCode != ExitCouldNotRun {
		t.Errorf("report exitCode = %d, want %d", rep.ExitCode, ExitCouldNotRun)
	}
	if len(rep.Steps) != 2 {
		t.Fatalf("report has %d steps, want 2: a step that did not run must still appear", len(rep.Steps))
	}

	var found bool
	for _, s := range rep.Steps {
		if s.Step == "test:integration" {
			found = true
			if s.MissingCapability != "container runtime" {
				t.Errorf("missingCapability = %q, want %q", s.MissingCapability, "container runtime")
			}
			if s.Reason == "" {
				t.Error("reason is empty; the report must say why the capability was unavailable")
			}
		}
	}
	if !found {
		t.Error("report omits test:integration")
	}
}
