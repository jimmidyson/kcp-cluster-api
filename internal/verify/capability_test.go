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
	"os"
	"strings"
	"testing"
)

// TestRunTellsTheStepWhatWasAsserted is what stops a skipped test from being
// reported as a pass. The harness checks for a container runtime and then
// starts the step; if the step's own guard disagrees and skips, `go test`
// still prints "ok" and the step passes. The step can only refuse to skip if
// it knows the capability was asserted, so Run has to tell it.
func TestRunTellsTheStepWhatWasAsserted(t *testing.T) {
	var asserted string
	var seenInStepWithNoNeeds string

	_, code := Run(&strings.Builder{}, []Step{
		{
			Name: "test:unit",
			Run: func() error {
				seenInStepWithNoNeeds = os.Getenv(EnvCapabilitiesAsserted)
				return nil
			},
		},
		{
			Name:  "test:integration",
			Needs: []Capability{{Name: CapabilityContainerRuntime, Check: func() error { return nil }}},
			Run: func() error {
				asserted = os.Getenv(EnvCapabilitiesAsserted)
				return nil
			},
		},
	})

	if code != ExitPass {
		t.Fatalf("expected pass, got exit code %d", code)
	}
	if asserted != CapabilityContainerRuntime {
		t.Errorf("step should see the capability that was checked for it: got %q, want %q",
			asserted, CapabilityContainerRuntime)
	}
	if seenInStepWithNoNeeds != "" {
		t.Errorf("a step needing nothing must assert nothing, got %q", seenInStepWithNoNeeds)
	}
	if v, ok := os.LookupEnv(EnvCapabilitiesAsserted); ok {
		t.Errorf("the variable must not outlive the run, still set to %q", v)
	}
}

// TestUncheckedCapabilityIsNotAsserted guards the direction that matters for
// correctness: a step whose capability was never checked must not be told it
// was. Getting this backwards would turn every legitimate skip on a developer
// machine into a hard failure.
func TestUncheckedCapabilityIsNotAsserted(t *testing.T) {
	t.Setenv(EnvCapabilitiesAsserted, "")
	if CapabilityAsserted(CapabilityContainerRuntime) {
		t.Error("nothing was asserted, but CapabilityAsserted said otherwise")
	}

	t.Setenv(EnvCapabilitiesAsserted, "some other capability")
	if CapabilityAsserted(CapabilityContainerRuntime) {
		t.Error("a different capability was asserted, but CapabilityAsserted matched")
	}

	t.Setenv(EnvCapabilitiesAsserted, "some other capability,"+CapabilityContainerRuntime)
	if !CapabilityAsserted(CapabilityContainerRuntime) {
		t.Error("the capability was asserted among others, but CapabilityAsserted missed it")
	}
}

// TestSkippedStepAssertsNothing: when the capability is unavailable the step
// never runs, so nothing may be left claiming it was asserted.
func TestSkippedStepAssertsNothing(t *testing.T) {
	ran := false
	_, code := Run(&strings.Builder{}, []Step{
		{
			Name:  "test:integration",
			Needs: []Capability{{Name: CapabilityContainerRuntime, Check: func() error { return errNoRuntime }}},
			Run:   func() error { ran = true; return nil },
		},
	})

	if ran {
		t.Error("the step ran despite its capability being unavailable")
	}
	if code != ExitCouldNotRun {
		t.Errorf("expected could-not-run (%d), got %d", ExitCouldNotRun, code)
	}
	if v, ok := os.LookupEnv(EnvCapabilitiesAsserted); ok {
		t.Errorf("nothing was asserted, but the variable is set to %q", v)
	}
}

// TestContainerRuntimeHonoursDockerHost is the disagreement that made this
// file necessary. The integration test used to guard on the socket alone,
// while the harness accepted DOCKER_HOST too - so with a remote daemon the
// harness ran the step and the test skipped itself, and the step passed.
// One definition, and it accepts both.
func TestContainerRuntimeHonoursDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://192.0.2.1:2376")
	if err := ContainerRuntimeAvailable(); err != nil {
		t.Errorf("DOCKER_HOST is set, so a runtime is reachable: %v", err)
	}

	if ContainerRuntime().Name != CapabilityContainerRuntime {
		t.Errorf("capability name must match the constant tests compare against, got %q",
			ContainerRuntime().Name)
	}
}

var errNoRuntime = &capabilityError{"no container runtime"}

type capabilityError struct{ msg string }

func (e *capabilityError) Error() string { return e.msg }
