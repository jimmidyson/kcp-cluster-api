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

package drift

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DivergingPaths reports every path, relative to the trees' roots, that
// differs between an upstream checkout and a vendored tree: modified, added
// and deleted files alike.
//
// It compares what is on disk, not what git tracks, and deliberately so. The
// vendored tree is a snapshot of upstream with the recorded patches applied,
// and the question the record answers is "what is different from upstream";
// a file that is present, whatever an ignore rule in either tree says about
// it, is different if upstream does not have it. In CI the disk is the commit,
// so the two readings agree; locally this one reflects an edit before it is
// staged, which is when a contributor wants to know.
//
// Renames are reported as a deletion and an addition rather than detected,
// because the record lists paths and a rename touches two of them.
func DivergingPaths(upstreamDir, treeDir string) ([]string, error) {
	upstreamDir, err := filepath.Abs(upstreamDir)
	if err != nil {
		return nil, err
	}
	treeDir, err = filepath.Abs(treeDir)
	if err != nil {
		return nil, err
	}
	// git diff --no-index treats a missing directory as empty, which would
	// read as "everything was deleted" or, for a missing upstream, as "every
	// file is an addition". Neither is a measurement; both are a wrong path.
	for _, d := range []string{upstreamDir, treeDir} {
		if st, err := os.Stat(d); err != nil {
			return nil, err
		} else if !st.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", d)
		}
	}

	// git diff --no-index exits 1 when the trees differ, which is the
	// expected case rather than an error; anything else is.
	cmd := exec.Command("git", "diff", "--no-index", "--no-renames", "--name-status", "-z", upstreamDir, treeDir)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); !ok || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git diff --no-index %s %s: %w", upstreamDir, treeDir, err)
		}
	}

	// -z output is a sequence of status NUL path NUL. The path is the one
	// git chose to print: the upstream side for a deletion, the tree side
	// otherwise. Either prefix is stripped, which is why both are absolute.
	fields := strings.Split(string(out), "\x00")
	seen := map[string]bool{}
	for i := 0; i+1 < len(fields); i += 2 {
		p := fields[i+1]
		for _, root := range []string{upstreamDir, treeDir} {
			if rel, ok := strings.CutPrefix(p, root+string(filepath.Separator)); ok {
				p = rel
				break
			}
		}
		seen[filepath.ToSlash(p)] = true
	}

	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// asExitError is errors.As for *exec.ExitError, kept local so the comparison
// above reads as what it is: distinguishing "the trees differ" from "git
// could not run".
func asExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// WithoutIgnored drops from paths, which are relative to treeDir, the ones
// git ignores there and does not track.
//
// DivergingPaths measures the disk, and a vendored tree's own test suite
// leaves files on it — upstream's machinedeployment tests write a log beside
// each golden file — which upstream's own ignore rules exclude and which
// nothing would ever commit. They are not divergence. A tracked file is never
// reported by git as ignored, whatever the rules say, so a file that was
// added by force stays measured; what is dropped is exactly what would not
// reach a commit.
func WithoutIgnored(treeDir string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return paths, nil
	}
	cmd := exec.Command("git", "-C", treeDir, "check-ignore", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		// Exit status 1 means no path is ignored, which is an answer.
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); !ok || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("git check-ignore in %s: %w", treeDir, err)
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if !ignored[p] {
			kept = append(kept, p)
		}
	}
	return kept, nil
}
