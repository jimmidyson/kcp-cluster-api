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

package kcpconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoProductCodeCallsSetCluster keeps closed a class of bug that cost this
// repository three diagnoses.
//
// kcpclient.SetCluster appends a workspace to whatever host it is handed. That
// is right only if the caller's config addresses the bare server, and nothing
// in a *rest.Config says whether it does. Every call site was correct when it
// was written, because in-process code passes a fixture's base config — and two
// of them broke the moment the same code ran deployed, where the kubeconfig
// addresses root. Both failures surfaced as a 404 rendered "the server could
// not find the requested resource", three layers from the cause.
//
// So product code uses ForCluster, which normalises. Tests are left alone: they
// build from a kcp fixture's base config, which is bare by construction.
func TestNoProductCodeCallsSetCluster(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("finding the module root: %v", err)
	}

	var offenders []string
	for _, dir := range []string{"internal", "cmd", "providers"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err //nolint:wrapcheck // walk's own error.
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path) //nolint:gosec // paths from this repository.
			if err != nil {
				return err //nolint:wrapcheck // read error, reported as-is.
			}
			for i, line := range strings.Split(string(body), "\n") {
				// The call, not a mention of it: this package's own
				// documentation names it, and should go on being able to.
				if strings.Contains(line, "SetCluster(") && !strings.HasPrefix(strings.TrimSpace(line), "//") {
					rel, _ := filepath.Rel(root, path) //nolint:errcheck // both are absolute.
					offenders = append(offenders, rel+":"+itoa(i+1))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("product code calls SetCluster, which appends a workspace to a host that may already "+
			"have one. Use kcpconfig.ForCluster instead, which normalises:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err //nolint:wrapcheck // the caller reports it.
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
