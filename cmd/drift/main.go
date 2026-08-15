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
	ref := flag.String("ref", "v1.15.0-kcp.1", "fork ref carrying the patches")
	upstream := flag.String("upstream", "https://github.com/kubernetes-sigs/cluster-api", "upstream repository")
	flag.Parse()

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
