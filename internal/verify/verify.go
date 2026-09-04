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

// Package verify runs the project's verification steps and reports three
// outcomes rather than two.
//
// Pass and fail are not enough. A step that cannot run because the
// environment lacks a capability - no container runtime, no reachable image
// source - is neither: reporting it as a pass hides missing coverage behind a
// green tick, and reporting it as a failure trains people to ignore red. It
// gets its own outcome and its own exit status, detectable by automation
// without reading logs.
//
// This project has already shipped a test that asserted reconciliation "got
// past" a failure rather than reaching its goal. This package exists so that
// class of quiet degradation is structurally hard rather than merely
// discouraged.
package verify

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Exit statuses. They are part of the contract in
// specs/.../contracts/task-surface.md: callers distinguish outcomes by status
// alone, so these values must stay stable.
const (
	// ExitPass means every step in scope ran and succeeded.
	ExitPass = 0
	// ExitFail means a step ran and failed.
	ExitFail = 1
	// ExitCouldNotRun means no step failed, but at least one could not run
	// because the environment lacked a required capability.
	ExitCouldNotRun = 2
)

// Outcome is the result of a single step.
type Outcome int

const (
	// OutcomePass - the step ran and succeeded.
	OutcomePass Outcome = iota
	// OutcomeFail - the step ran and failed.
	OutcomeFail
	// OutcomeCouldNotRun - the step was not attempted: a capability it
	// depends on is unavailable.
	OutcomeCouldNotRun
)

func (o Outcome) String() string {
	switch o {
	case OutcomePass:
		return "pass"
	case OutcomeFail:
		return "FAIL"
	case OutcomeCouldNotRun:
		return "could not run"
	default:
		return "unknown"
	}
}

// Capability is something the environment must provide for a step to run,
// such as a container runtime or a reachable image registry.
type Capability struct {
	// Name is what appears in the summary when the capability is missing.
	// Write it for someone who has to fix it: "container runtime", not
	// "docker.sock".
	Name string
	// Check reports whether the capability is available. A non-nil error
	// means unavailable, and its text is shown as the reason.
	Check func() error
}

// Step is one unit of verification.
type Step struct {
	// Name matches the named operation a contributor can invoke directly,
	// so a failure here is reproducible by running that name.
	Name string
	// Needs are checked, in order, before Run is called. If any is
	// unavailable, Run is not called at all.
	Needs []Capability
	// Run performs the step.
	Run func() error
}

// Result is what happened to one step. Every step yields exactly one Result,
// including steps that never ran: a step missing from the results is
// indistinguishable from a step that quietly passed.
type Result struct {
	Step    string
	Outcome Outcome
	// Err is the failure, for OutcomeFail.
	Err error
	// MissingCapability is the capability that was unavailable, for
	// OutcomeCouldNotRun.
	MissingCapability string
	// Reason is why that capability was unavailable.
	Reason error
}

// Run executes steps in order and writes a summary to w.
//
// Every step is attempted unless a capability it needs is unavailable; a
// failing step does not stop the ones after it, so one run reports everything
// that is wrong rather than only the first thing.
//
// The returned status is ExitFail if any step failed, otherwise
// ExitCouldNotRun if any step could not run, otherwise ExitPass. A genuine
// failure outranks a missing capability: if something is broken, that is the
// headline.
func Run(w io.Writer, steps []Step) ([]Result, int) {
	results := make([]Result, 0, len(steps))

	for _, s := range steps {
		if cap, err := missingCapability(s); err != nil {
			results = append(results, Result{
				Step:              s.Name,
				Outcome:           OutcomeCouldNotRun,
				MissingCapability: cap,
				Reason:            err,
			})
			continue
		}

		// Tell the step which capabilities were confirmed for it. Child
		// processes inherit this, so a test can refuse to skip over a
		// capability the harness has just asserted is present.
		if err := os.Setenv(EnvCapabilitiesAsserted, assertedNames(s)); err != nil {
			results = append(results, Result{Step: s.Name, Outcome: OutcomeFail, Err: err})
			continue
		}

		err := s.Run()
		// Unset either way: the next step asserts its own capabilities, and
		// the only way this fails is a name no longer being a valid variable.
		_ = os.Unsetenv(EnvCapabilitiesAsserted)

		if err != nil {
			results = append(results, Result{Step: s.Name, Outcome: OutcomeFail, Err: err})
			continue
		}
		results = append(results, Result{Step: s.Name, Outcome: OutcomePass})
	}

	writeSummary(w, results)
	return results, statusFor(results)
}

// missingCapability checks every capability a step needs, before the step
// runs. Reporting up front is the point (FR-013): discovering a missing
// container runtime part-way through an integration suite wastes the run and
// buries the reason in output.
func missingCapability(s Step) (string, error) {
	for _, c := range s.Needs {
		if c.Check == nil {
			continue
		}
		if err := c.Check(); err != nil {
			return c.Name, err
		}
	}
	return "", nil
}

func statusFor(results []Result) int {
	status := ExitPass
	for _, r := range results {
		switch r.Outcome {
		case OutcomeFail:
			return ExitFail
		case OutcomeCouldNotRun:
			status = ExitCouldNotRun
		case OutcomePass:
		}
	}
	return status
}

// reportStep is the machine-readable form of a Result.
type reportStep struct {
	Step              string `json:"step"`
	Outcome           string `json:"outcome"`
	Error             string `json:"error,omitempty"`
	MissingCapability string `json:"missingCapability,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

// Report is the machine-readable verification result.
type Report struct {
	// Status is "pass", "fail" or "could-not-run".
	Status string `json:"status"`
	// ExitCode is the status this run would exit with when invoked directly.
	ExitCode int          `json:"exitCode"`
	Steps    []reportStep `json:"steps"`
}

// WriteReport writes the machine-readable result to path.
//
// This exists because exit status alone is not survivable. go-task collapses
// every failing task to a single exit code (201) regardless of what the
// command returned, so a caller invoking `task verify` cannot tell
// "could not run" from "failed" by status - the distinction FR-012 requires.
// The status codes remain correct when the harness is invoked directly; this
// file is what makes the outcome detectable by automation either way, without
// parsing logs.
func WriteReport(path string, results []Result) error {
	steps := make([]reportStep, 0, len(results))
	for _, r := range results {
		s := reportStep{Step: r.Step, Outcome: r.Outcome.String(), MissingCapability: r.MissingCapability}
		if r.Err != nil {
			s.Error = r.Err.Error()
		}
		if r.Reason != nil {
			s.Reason = r.Reason.Error()
		}
		steps = append(steps, s)
	}

	code := statusFor(results)
	rep := Report{Status: statusName(code), ExitCode: code, Steps: steps}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating report directory: %w", err)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding report: %w", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}

func statusName(code int) string {
	switch code {
	case ExitPass:
		return "pass"
	case ExitFail:
		return "fail"
	case ExitCouldNotRun:
		return "could-not-run"
	default:
		return "unknown"
	}
}

func writeSummary(w io.Writer, results []Result) {
	fmt.Fprintln(w, "\nverification summary")
	fmt.Fprintln(w, "--------------------")
	for _, r := range results {
		switch r.Outcome {
		case OutcomeCouldNotRun:
			fmt.Fprintf(w, "  %-20s %s: %s unavailable (%v)\n", r.Step, r.Outcome, r.MissingCapability, r.Reason)
		case OutcomeFail:
			fmt.Fprintf(w, "  %-20s %s: %v\n", r.Step, r.Outcome, r.Err)
		case OutcomePass:
			fmt.Fprintf(w, "  %-20s %s\n", r.Step, r.Outcome)
		}
	}

	switch statusFor(results) {
	case ExitPass:
		fmt.Fprintln(w, "\nall steps passed")
	case ExitFail:
		fmt.Fprintln(w, "\nFAILED: at least one step ran and failed")
	case ExitCouldNotRun:
		fmt.Fprintln(w, "\nINCOMPLETE: no step failed, but at least one could not run.")
		fmt.Fprintln(w, "This is not a pass. Re-run where the missing capabilities are available.")
	}
}
