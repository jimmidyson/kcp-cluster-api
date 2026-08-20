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

import "testing"

// TestPseudoVersion is here because the obvious pattern is wrong for the
// versions this project actually pins.
//
// A pseudo-version separates its timestamp with a hyphen only when the base
// version has no pre-release part. Every version here does — `-kcp.N` — so the
// separator is a dot, and a pattern written for the hyphen matches nothing and
// reports every branch pin as a tag.
func TestPseudoVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{
		{"v1.15.0-kcp.11.0.20260820160642-a0d8d117bab2", true},
		{"v1.15.0-0.20260820160642-a0d8d117bab2", true},
		{"v0.0.0-20260820160642-a0d8d117bab2", true},
		{"v1.15.0-kcp.11", false},
		{"v1.15.0", false},
		// Not a pseudo-version: a tag that happens to end in digits and hex.
		{"v1.15.0-kcp.20260820160642", false},
	} {
		if got := pseudoVersion.MatchString(tc.version); got != tc.want {
			t.Errorf("pseudoVersion.MatchString(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}
