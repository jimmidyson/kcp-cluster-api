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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDivergingPathsReportsEveryKindOfDifferenceOnce(t *testing.T) {
	upstream := t.TempDir()
	tree := t.TempDir()
	writeTree(t, upstream, map[string]string{
		"same.go":           "unchanged\n",
		"pkg/modified.go":   "before\n",
		"pkg/deleted.go":    "gone from the tree\n",
		".github/ci.yaml":   "inert in a subdirectory\n",
		"nested/.gitignore": "ignored.txt\n",
	})
	writeTree(t, tree, map[string]string{
		"same.go":            "unchanged\n",
		"pkg/modified.go":    "after\n",
		"pkg/added_test.go":  "new in the tree\n",
		".github/ci.yaml":    "inert in a subdirectory\n",
		"nested/.gitignore":  "ignored.txt\n",
		"nested/ignored.txt": "present on disk, whatever the ignore rule says\n",
	})

	got, err := DivergingPaths(upstream, tree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"nested/ignored.txt",
		"pkg/added_test.go",
		"pkg/deleted.go",
		"pkg/modified.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diverging paths\n got %v\nwant %v", got, want)
	}
}

func TestDivergingPathsIsEmptyForIdenticalTrees(t *testing.T) {
	upstream := t.TempDir()
	tree := t.TempDir()
	files := map[string]string{"a.go": "x\n", "dir/b.go": "y\n"}
	writeTree(t, upstream, files)
	writeTree(t, tree, files)

	got, err := DivergingPaths(upstream, tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("identical trees reported as diverging: %v", got)
	}
}

func TestDivergingPathsAcceptsRelativeDirectories(t *testing.T) {
	// The checker is run from the repository root with a path like
	// forks/cluster-api; the prefix it strips must be the resolved one.
	parent := t.TempDir()
	writeTree(t, filepath.Join(parent, "up"), map[string]string{"f": "1\n"})
	writeTree(t, filepath.Join(parent, "tree"), map[string]string{"f": "2\n", "g": "3\n"})
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}

	got, err := DivergingPaths("up", "tree")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"f", "g"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDivergingPathsFailsOnAMissingTree(t *testing.T) {
	if _, err := DivergingPaths(t.TempDir(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a tree that does not exist must be an error, not an empty divergence")
	}
}
