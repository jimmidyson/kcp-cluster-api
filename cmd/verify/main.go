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

// Command verify runs the project's verification steps and reports pass,
// fail, or could-not-run.
//
// Each step invokes a named task target rather than reimplementing it, so
// running `task lint` by hand does exactly what verification does. This
// command owns only the capability checks and the outcome reporting.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
)

func main() {
	fast := flag.Bool("fast", false, "run only the steps that need no container runtime or external service")
	report := flag.String("report", filepath.Join("bin", "verify-result.json"),
		"where to write the machine-readable result; the exit status alone does not survive a task runner")
	flag.Parse()

	steps := []verify.Step{
		{Name: "build", Run: taskTarget("build")},
		{Name: "lint", Run: taskTarget("lint")},
		{Name: "test:unit", Run: taskTarget("test:unit")},
	}

	if !*fast {
		// Split by capability rather than run as one step. Most of what the
		// integration suite proves - that every bound workspace is engaged,
		// that each one's writes land in itself, that unbinding stops its work
		// - needs a real kcp server and nothing else. Bundling it behind a
		// container runtime would report all of it as "could not run" wherever
		// Docker is absent, which is missing coverage reported as an
		// environment problem.
		steps = append(steps,
			verify.Step{
				Name: "test:integration:kcp",
				Run:  taskTarget("test:integration:kcp"),
			},
			verify.Step{
				Name:  "test:integration:docker",
				Needs: []verify.Capability{verify.ContainerRuntime()},
				Run:   taskTarget("test:integration:docker"),
			},
		)
	}

	results, code := verify.Run(os.Stdout, steps)

	if err := verify.WriteReport(*report, results); err != nil {
		fmt.Fprintf(os.Stderr, "writing verification report: %v\n", err)
		os.Exit(verify.ExitFail)
	}
	fmt.Fprintf(os.Stdout, "machine-readable result: %s\n", *report)

	os.Exit(code)
}

// taskTarget shells out to the named task target. Composition, not
// reimplementation: whatever `task <name>` does is what verification does.
func taskTarget(name string) func() error {
	return func() error {
		cmd := exec.Command("task", name)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}
