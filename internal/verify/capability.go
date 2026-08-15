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
	"fmt"
	"os"
	"strings"
)

// EnvCapabilitiesAsserted names the environment variable Run sets, for the
// duration of a step, to the capabilities it has just confirmed are available.
//
// It exists so a test can tell the difference between "nobody checked" and
// "the harness checked and said this environment has what you need". Those
// call for opposite behaviour: the first makes skipping reasonable, the second
// makes skipping a defect.
const EnvCapabilitiesAsserted = "KCP_CAPI_CAPABILITIES_ASSERTED"

// CapabilityContainerRuntime is the name used in summaries, reports and
// assertions. It is compared as a string across process boundaries, so it is a
// constant rather than a literal repeated in each place.
const CapabilityContainerRuntime = "container runtime"

// ContainerRuntimeAvailable reports whether a container runtime is reachable.
//
// This is the single definition. It was previously written twice - once in the
// harness (socket present, or DOCKER_HOST set) and once as a skip guard inside
// the integration test (socket present only) - and the two disagreed. On a
// machine with a remote or rootless daemon, reached through DOCKER_HOST with
// no socket at the default path, the harness declared the capability present,
// ran the step, and the test skipped itself. The step reported pass. That is
// the failure this whole package exists to prevent, one level further down, so
// there is now one function and both callers use it.
func ContainerRuntimeAvailable() error {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return nil
	}
	const sock = "/var/run/docker.sock"
	if _, err := os.Stat(sock); err != nil {
		return fmt.Errorf("%s is not present and DOCKER_HOST is unset", sock)
	}
	return nil
}

// ContainerRuntime is the capability an integration step depends on.
func ContainerRuntime() Capability {
	return Capability{Name: CapabilityContainerRuntime, Check: ContainerRuntimeAvailable}
}

// CapabilityAsserted reports whether the harness confirmed the named
// capability before starting the current step.
//
// A test that cannot proceed should consult this before skipping: if the
// answer is true, the environment was supposed to be sufficient, and skipping
// would report missing coverage as a pass.
func CapabilityAsserted(name string) bool {
	for _, c := range strings.Split(os.Getenv(EnvCapabilitiesAsserted), ",") {
		if strings.TrimSpace(c) == name {
			return true
		}
	}
	return false
}

// assertedNames is the value EnvCapabilitiesAsserted takes while a step runs.
func assertedNames(s Step) string {
	names := make([]string, 0, len(s.Needs))
	for _, c := range s.Needs {
		names = append(names, c.Name)
	}
	return strings.Join(names, ",")
}
