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

package kcpfixtures

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestPathMissingNamesWhatItLookedFor is the failure path FR-006
// exists for. The layout of Cluster API's manifests is not stable across
// releases - the CRD bases moved between minor versions - so a dependency
// bump that relocates them must fail here, saying what was expected, rather
// than resolving nothing and letting the caller publish an empty API export.
func TestManifestPathMissingNamesWhatItLookedFor(t *testing.T) {
	const rel = "core/config/crd/bases/this-manifest-does-not-exist.yaml"

	got, err := ManifestPath(ModuleClusterAPI, rel)
	if err == nil {
		t.Fatalf("ManifestPath(%q) = %q, want an error: a missing manifest must not resolve", rel, got)
	}
	if got != "" {
		t.Errorf("ManifestPath returned path %q alongside an error; it must return no path rather than a best guess", got)
	}

	// The message has to be actionable: which manifest, which module, and the
	// absolute location searched. Without those a reader cannot tell a moved
	// layout from a typo.
	for _, want := range []string{rel, ModuleClusterAPI, "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestManifestPathDoesNotSearch guards the "no fallback" half of FR-006: a
// manifest that exists somewhere else in the module must not be found by a
// path that does not name its real location.
func TestManifestPathDoesNotSearch(t *testing.T) {
	// The Cluster CRD exists at core/config/crd/bases/, not at the root.
	const wrongDir = "cluster.x-k8s.io_clusters.yaml"

	if got, err := ManifestPath(ModuleClusterAPI, wrongDir); err == nil {
		t.Errorf("ManifestPath(%q) = %q, want an error: resolution must not search for a matching filename elsewhere in the module", wrongDir, got)
	}
}

func TestModuleDirUnknownModule(t *testing.T) {
	const unknown = "example.com/definitely/not/in/the/build/list"

	if got, err := ModuleDir(unknown); err == nil {
		t.Errorf("ModuleDir(%q) = %q, want an error", unknown, got)
	}
}

// The same contract under a manifest root: a module this build was not
// compiled against does not resolve to a path just because a directory name
// can be joined together. Without this, ModuleDir means one thing outside a
// container and another inside one.
func TestModuleDirUnknownModuleUnderAManifestRoot(t *testing.T) {
	t.Setenv(ManifestRootEnv, t.TempDir())
	resetModuleDirCache()

	const unknown = "example.com/definitely/not/in/the/build/list"
	got, err := ModuleDir(unknown)
	if err == nil {
		t.Fatalf("ModuleDir(%q) = %q, want an error", unknown, got)
	}
	if !strings.Contains(err.Error(), ManifestRootEnv) {
		t.Errorf("error %q does not name %s, which is where it looked", err, ManifestRootEnv)
	}
}

// A container has no Go toolchain and no module cache, so `go list -m` has
// nothing to answer with - and the binary that publishes the APIExports reads
// CRD manifests out of the pinned modules. The image copies them in at build
// time, out of the same module cache the binaries were compiled against, and
// says where they went.
func TestModuleDirTakesTheManifestRootFromTheEnvironment(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, ModuleClusterAPI, "core", "config", "crd", "bases")
	if err := os.MkdirAll(want, 0o750); err != nil {
		t.Fatalf("preparing the manifest root: %v", err)
	}
	manifest := filepath.Join(want, "cluster.x-k8s.io_clusters.yaml")
	if err := os.WriteFile(manifest, []byte("kind: CustomResourceDefinition\n"), 0o600); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}

	t.Setenv(ManifestRootEnv, root)
	resetModuleDirCache()

	got, err := ManifestPath(ModuleClusterAPI, "core/config/crd/bases/cluster.x-k8s.io_clusters.yaml")
	if err != nil {
		t.Fatalf("resolving a manifest under %s: %v", ManifestRootEnv, err)
	}
	if got != manifest {
		t.Errorf("resolved %q, want %q", got, manifest)
	}
}

// The override changes where manifests come from, not whether a missing one is
// an error. An image built without a manifest the code publishes has to fail
// naming it, exactly as a moved upstream layout does.
func TestModuleDirManifestRootStillRefusesAMissingManifest(t *testing.T) {
	t.Setenv(ManifestRootEnv, t.TempDir())
	resetModuleDirCache()

	const rel = "core/config/crd/bases/cluster.x-k8s.io_clusters.yaml"
	if got, err := ManifestPath(ModuleClusterAPI, rel); err == nil {
		t.Errorf("ManifestPath(%q) = %q, want an error", rel, got)
	}
}
