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
	"testing"

	"k8s.io/client-go/rest"
)

func TestForClusterNeverDoublesTheClusterPath(t *testing.T) {
	const want = "https://kcp.scale.svc:6443/clusters/2fj3k"

	for name, host := range map[string]string{
		"bare server":         "https://kcp.scale.svc:6443",
		"trailing slash":      "https://kcp.scale.svc:6443/",
		"root scoped":         "https://kcp.scale.svc:6443/clusters/root",
		"already a workspace": "https://kcp.scale.svc:6443/clusters/1abcd",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ForCluster(&rest.Config{Host: host}, "2fj3k"); got.Host != want {
				t.Errorf("from %q got %q, want %q", host, got.Host, want)
			}
		})
	}
}

func TestBaseStripsTheWorkspace(t *testing.T) {
	got := Base(&rest.Config{Host: "https://kcp.scale.svc:6443/clusters/root", BearerToken: "t"})
	if got.Host != "https://kcp.scale.svc:6443" {
		t.Errorf("got host %q", got.Host)
	}
	if got.BearerToken != "t" {
		t.Error("the copy lost the bearer token")
	}
}

// TestBaseCopies pins that the caller's config is untouched. SetCluster
// assigns in place, and a helper that did the same would scope every later
// client to the first workspace one was built for.
func TestBaseCopies(t *testing.T) {
	cfg := &rest.Config{Host: "https://kcp.scale.svc:6443/clusters/root"}
	_ = Base(cfg)
	_ = ForCluster(cfg, "2fj3k")
	if cfg.Host != "https://kcp.scale.svc:6443/clusters/root" {
		t.Errorf("the caller's config was mutated to %q", cfg.Host)
	}
}

func TestBaseOfNilIsNil(t *testing.T) {
	if Base(nil) != nil {
		t.Error("Base(nil) should stay nil so a caller's own check still fires")
	}
}
