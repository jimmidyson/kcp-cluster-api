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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Modules this project resolves manifests from. They are separate Go modules
// inside the Cluster API repository and must be pinned together.
const (
	ModuleClusterAPI     = "sigs.k8s.io/cluster-api"
	ModuleClusterAPITest = "sigs.k8s.io/cluster-api/test"
)

var (
	moduleDirMu    sync.Mutex
	moduleDirCache = map[string]string{}
)

// ModuleDir returns the on-disk directory of a module in this module's build
// list, as reported by the Go toolchain. Because the module is resolved from
// the same build list the code compiles against, anything read from the
// returned directory is guaranteed to come from the pinned version — there is
// no separate copy that could disagree with it.
func ModuleDir(module string) (string, error) {
	moduleDirMu.Lock()
	defer moduleDirMu.Unlock()
	if dir, ok := moduleDirCache[module]; ok {
		return dir, nil
	}

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module).Output()
	if err != nil {
		var stderr string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("resolving module %s: %w: %s", module, err, stderr)
	}

	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("resolving module %s: the Go toolchain reported no directory; "+
			"the module is in the build list but not downloaded", module)
	}
	moduleDirCache[module] = dir
	return dir, nil
}

// ManifestPath resolves relPath inside the given module and confirms it
// exists.
//
// It deliberately does not search, fall back, or return a best guess. The
// layout of Cluster API's manifests is not stable across releases — the CRD
// bases moved between minor versions — so a dependency bump that relocates
// them must fail here, naming what was expected, rather than silently
// resolving nothing or picking up a different version's copies.
func ManifestPath(module, relPath string) (string, error) {
	dir, err := ModuleDir(module)
	if err != nil {
		return "", err
	}

	full := filepath.Join(dir, relPath)
	if _, err := os.Stat(full); err != nil {
		return "", fmt.Errorf("manifest %s not found in module %s (looked in %s): %w; "+
			"if the dependency was recently updated, the manifest layout may have moved and "+
			"this path needs updating rather than working around", relPath, module, full, err)
	}
	return full, nil
}

// MustManifestPaths resolves several manifests in one module, returning the
// first error encountered.
func MustManifestPaths(module string, relPaths ...string) ([]string, error) {
	paths := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		p, err := ManifestPath(module, rel)
		if err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}
