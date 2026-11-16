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

// Command drift reports how far a vendored upstream tree diverges from the
// upstream commit it is based on, and fails if that divergence is not
// recorded in the tree's drift record.
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
	tree := flag.String("tree", "forks/cluster-api", "vendored upstream tree to measure")
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

	dir, err := os.MkdirTemp("", "drift-upstream-")
	if err != nil {
		fail("%v", err)
	}
	defer os.RemoveAll(dir)
	if err := checkoutUpstream(*upstream, rec.Base, dir); err != nil {
		fail("fetching upstream at the record's base: %v", err)
	}

	actual, err := drift.DivergingPaths(dir, *tree)
	if err != nil {
		fail("measuring divergence: %v", err)
	}
	actual, err = drift.WithoutIgnored(*tree, actual)
	if err != nil {
		fail("measuring divergence: %v", err)
	}

	res := drift.Compare(rec, actual)
	fmt.Printf("%s vs upstream %s@%s\n\n", *tree, *upstream, rec.Base[:12])
	fmt.Print(res)

	if !res.OK() {
		os.Exit(1)
	}
}

// checkoutUpstream puts the upstream tree at the base commit into dir, as a
// shallow checkout of exactly that commit. The base is the one thing the
// record pins, so it is the one thing fetched; nothing on the local machine
// is consulted, and a stale clone cannot change the answer.
//
// The vendored tree needs no fetch at all: it is the directory being measured,
// as it is on disk. That is what retired the pinned-version lookup this
// command used to start with - there is no pin, and a tree does not have a
// version to be stale about.
func checkoutUpstream(upstream, base, dir string) error {
	steps := [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "fetch", "--quiet", "--depth", "1", upstream, base},
		{"-C", dir, "checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	// The checkout's own metadata is not upstream content and must not be
	// compared as if it were.
	return os.RemoveAll(dir + "/.git")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
