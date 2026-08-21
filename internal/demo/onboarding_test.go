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

package demo

import (
	"strings"
	"testing"
)

// The table's whole point is the difference between the two middle columns:
// what the workspace was given, and what its tenant chose. A renderer that ran
// them together would be reporting nothing.
func TestRenderOnboardingTableSeparatesGivenFromChosen(t *testing.T) {
	var sb strings.Builder
	err := RenderOnboardingTable(&sb, []OnboardingStatus{{
		Workspace: "root:capi-demo:alice:capi-demo-1",
		Owner:     "alice",
		Onboarded: []string{"cluster-api-core", "cluster-api-workspace"},
		Enabled:   []string{"cluster-api-dev-infrastructure"},
		EnabledBy: "alice",
		APIGroups: []string{"cluster.x-k8s.io", "infrastructure.cluster.x-k8s.io"},
	}})
	if err != nil {
		t.Fatalf("RenderOnboardingTable() = %v", err)
	}

	got := sb.String()
	for _, want := range []string{"alice", "core, workspace", "dev-infrastructure", "core, infrastructure"} {
		if !strings.Contains(got, want) {
			t.Errorf("the table does not mention %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cluster-api-core") {
		t.Errorf("the table repeats the prefix every export shares:\n%s", got)
	}
}

func TestRenderOnboardingTableSaysWhenThereIsNoTenant(t *testing.T) {
	var sb strings.Builder
	if err := RenderOnboardingTable(&sb, []OnboardingStatus{{Workspace: "root:capi-demo-1"}}); err != nil {
		t.Fatalf("RenderOnboardingTable() = %v", err)
	}
	// A blank cell in a table reads as a rendering bug rather than as "there
	// is no owner", which is what a run with no users means.
	if strings.Count(sb.String(), "-") < 3 {
		t.Errorf("a workspace with no owner and nothing enabled renders as blanks:\n%s", sb.String())
	}
}

// A reader scanning the claims table is looking for the ones nobody wrote
// down, because those are the ones that say onboarding a provider took no
// manifest edit.
func TestRenderClaimsTableSaysWhichClaimsWereDiscovered(t *testing.T) {
	var sb strings.Builder
	err := RenderClaimsTable(&sb, []ClaimStatus{
		{
			Export: "cluster-api-core", Resource: "secrets", From: "(built in)",
			Verbs: []string{"get"},
		},
		{
			Export: "cluster-api-core", Resource: "devclusters.infrastructure.cluster.x-k8s.io",
			From: "cluster-api-dev-infrastructure", Verbs: []string{"get", "create"}, Discovered: true,
		},
	})
	if err != nil {
		t.Fatalf("RenderClaimsTable() = %v", err)
	}

	got := sb.String()
	if !strings.Contains(got, "discovered") {
		t.Errorf("the table does not say which claims were discovered:\n%s", got)
	}
	if !strings.Contains(got, "declared") {
		t.Errorf("the table does not say which claims were declared:\n%s", got)
	}
	if !strings.Contains(got, "get,create") {
		t.Errorf("the table does not say what a claim grants:\n%s", got)
	}
}
