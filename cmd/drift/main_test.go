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

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckoutUpstreamFetchesExactlyTheBase uses a local repository as the
// upstream, which git fetches by URL exactly as it would a remote one, so the
// test exercises the real fetch path without the network.
func TestCheckoutUpstreamFetchesExactlyTheBase(t *testing.T) {
	upstream := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", upstream}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(upstream, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "base.txt")
	git("commit", "--quiet", "-m", "base")
	base := git("rev-parse", "HEAD")

	// A later commit the checkout must not include: the record names a base,
	// and measuring against anything newer would hide drift that upstream
	// happened to adopt since.
	if err := os.WriteFile(filepath.Join(upstream, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "later.txt")
	git("commit", "--quiet", "-m", "later")

	dir := t.TempDir()
	if err := checkoutUpstream(upstream, base, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "base.txt")); err != nil {
		t.Errorf("base commit's file is missing from the checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "later.txt")); err == nil {
		t.Error("the checkout includes a commit after the base")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("the checkout's .git directory was left in place and would be measured as divergence")
	}
}

func TestCheckoutUpstreamFailsOnAnUnknownBase(t *testing.T) {
	upstream := t.TempDir()
	if out, err := exec.Command("git", "-C", upstream, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := checkoutUpstream(upstream, "0123456789abcdef0123456789abcdef01234567", t.TempDir()); err == nil {
		t.Fatal("a base commit upstream does not have must be an error, not an empty tree")
	}
}
