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
	"path/filepath"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// Deleting an installation takes the credentials it wrote with it, because
// they are credentials for a shard that no longer exists. This is the only
// thing this command destroys that Kubernetes will not put back, so it deletes
// the files it named and nothing else in the directory it found them in.
func TestRemoveLocalKubeconfigsTakesItsOwnAndLeavesTherest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := options{
		kubeconfigOut: filepath.Join(dir, "kcp.kubeconfig"),
		parent:        demo.DefaultParent,
		users:         "alice,bob",
		workspaces:    2,
		runDemo:       true,
	}

	mine := []string{"kcp.kubeconfig", "workspaces.kubeconfig", "alice.kubeconfig", "bob.kubeconfig"}
	// A file somebody else put there, and one that only looks like ours.
	theirs := []string{"notes.txt", "carol.kubeconfig"}
	for _, name := range append(append([]string{}, mine...), theirs...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("preparing %s: %v", name, err)
		}
	}

	removed, err := removeLocalKubeconfigs(opts)
	if err != nil {
		t.Fatalf("removing the kubeconfigs: %v", err)
	}
	if removed != len(mine) {
		t.Errorf("removed %d files, want %d", removed, len(mine))
	}

	for _, name := range mine {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there", name)
		}
	}
	for _, name := range theirs {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed and is not this command's to remove: %v", name, err)
		}
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory was removed while it still held files: %v", err)
	}
}

// A delete after a delete removes nothing and says so, rather than failing on
// the files that are already gone.
func TestRemoveLocalKubeconfigsIsRepeatable(t *testing.T) {
	t.Parallel()

	opts := options{
		kubeconfigOut: filepath.Join(t.TempDir(), "nested", "kcp.kubeconfig"),
		parent:        demo.DefaultParent,
		users:         "alice",
		workspaces:    1,
	}

	removed, err := removeLocalKubeconfigs(opts)
	if err != nil {
		t.Fatalf("removing kubeconfigs that are not there: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d files from a directory that does not exist", removed)
	}
}

// Asking for no kubeconfig means none was written, so none is removed - and
// nothing walks a directory that was never this command's.
func TestRemoveLocalKubeconfigsWithoutOneDoesNothing(t *testing.T) {
	t.Parallel()

	removed, err := removeLocalKubeconfigs(options{kubeconfigOut: ""})
	if err != nil || removed != 0 {
		t.Errorf("got %d removed, %v; want 0, nil", removed, err)
	}
}
