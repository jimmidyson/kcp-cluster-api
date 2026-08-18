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

// Command drift reports how far this project's Cluster API fork diverges
// from the upstream commit it is based on, and fails if that divergence is
// not recorded in DRIFT.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/drift"
)

func main() {
	record := flag.String("record", "DRIFT.md", "path to the drift record")
	fork := flag.String("fork", "https://github.com/jimmidyson/cluster-api", "fork repository to measure")
	ref := flag.String("ref", "", "fork ref carrying the patches; empty reads the version this module pins")
	upstream := flag.String("upstream", "https://github.com/kubernetes-sigs/cluster-api", "upstream repository")
	flag.Parse()

	if *ref == "" {
		v, err := pinnedForkVersion()
		if err != nil {
			fail("%v", err)
		}
		*ref = v
	}

	md, err := os.ReadFile(*record)
	if err != nil {
		fail("reading drift record: %v", err)
	}
	rec, err := drift.ParseRecord(string(md))
	if err != nil {
		fail("%v", err)
	}

	actual, err := divergingPaths(*upstream, rec.Base, *fork, *ref)
	if err != nil {
		fail("measuring divergence: %v", err)
	}

	res := drift.Compare(rec, actual)
	fmt.Printf("fork %s@%s vs upstream %s\n\n", *fork, *ref, rec.Base[:12])
	fmt.Print(res)

	if !res.OK() {
		os.Exit(1)
	}
}

// pinnedForkVersion reports the fork version this module actually depends on.
//
// # Why this is not a constant
//
// It was one, and it went stale the moment the pin moved: the check went on
// measuring the previous tag and reported every path added since as one the
// fork "no longer carries" — the exact opposite of the truth, in a tool whose
// only job is to say whether the record matches reality.
//
// A default that has to be edited in step with go.mod is a second pin, and two
// pins disagree eventually. Reading it from the module graph means the check
// measures what the project builds against, which is the only version the
// record is a record of.
func pinnedForkVersion() (string, error) {
	// The replace directive rather than the require: the require names
	// sigs.k8s.io/cluster-api at an upstream version, and what is actually
	// fetched is the fork the replace points at.
	out, err := exec.Command("go", "list", "-m", "-f", "{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}", "sigs.k8s.io/cluster-api").Output()
	if err != nil {
		return "", fmt.Errorf("reading the pinned fork version from the module graph: %w", err)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("the module graph reports no version for sigs.k8s.io/cluster-api")
	}
	return v, nil
}

// divergingPaths returns the files that differ between the upstream base
// commit and the fork ref, using a temporary bare repository so the check
// needs no pre-existing clone and cannot be fooled by local state.
func divergingPaths(upstream, base, fork, ref string) ([]string, error) {
	dir, err := os.MkdirTemp("", "drift-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	steps := [][]string{
		{"init", "--quiet", "--bare", dir},
		{"--git-dir", dir, "fetch", "--quiet", "--depth", "1", upstream, base},
		{"--git-dir", dir, "fetch", "--quiet", "--depth", "1", fork, ref},
	}
	for _, args := range steps {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}

	// Resolve the fork ref: a shallow tag fetch lands in FETCH_HEAD.
	forkSHA, err := exec.Command("git", "--git-dir", dir, "rev-parse", "FETCH_HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("resolving fork ref %s: %w", ref, err)
	}

	out, err := exec.Command("git", "--git-dir", dir, "diff", "--name-only",
		base, strings.TrimSpace(string(forkSHA))).Output()
	if err != nil {
		return nil, fmt.Errorf("diffing %s..%s: %w", base[:12], ref, err)
	}

	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			paths = append(paths, l)
		}
	}
	return paths, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
