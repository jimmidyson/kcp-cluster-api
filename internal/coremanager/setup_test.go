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

package coremanager

import (
	"errors"
	"testing"

	"github.com/jimmidyson/kcp-cluster-api/internal/providerwiring"
)

// TestSetupWebhooksRefusesASecondWorkspace covers FR-008.
//
// The manager argument is nil throughout: both paths under test return before
// touching it, which is the point — a second workspace is refused before
// anything is registered, rather than being registered and quietly discarded
// by controller-runtime's already-handled check.
func TestSetupWebhooksRefusesASecondWorkspace(t *testing.T) {
	ResetWebhookWorkspaceForTest()
	t.Cleanup(ResetWebhookWorkspaceForTest)

	// Stand in for the first workspace having been wired, without needing a
	// real manager to wire it onto.
	webhookWorkspace.Lock()
	webhookWorkspace.name = "tenant-a"
	webhookWorkspace.set = true
	webhookWorkspace.Unlock()

	err := SetupWebhooks("tenant-b", nil)
	if !errors.Is(err, providerwiring.ErrWebhooksAlreadyWired) {
		t.Errorf("SetupWebhooks(tenant-b) = %v, want it to wrap %v", err, providerwiring.ErrWebhooksAlreadyWired)
	}

	if err := SetupWebhooks("tenant-a", nil); err != nil {
		t.Errorf("SetupWebhooks(tenant-a) again = %v, want nil: repeating the same workspace is a no-op", err)
	}
}

// TestControllerOptionsSkipNameValidation covers the second half of FR-005.
//
// controller-runtime keeps controller names in a process-global set it never
// empties, so with validation left on, the second workspace to wire a
// controller named "cluster" fails outright — as does the second engagement of
// any one workspace after a rebind.
func TestControllerOptionsSkipNameValidation(t *testing.T) {
	opts := controllerOptions(10)

	if opts.SkipNameValidation == nil || !*opts.SkipNameValidation {
		t.Error("SkipNameValidation is not set: per-workspace controllers share names, so the second workspace would fail")
	}
	if opts.MaxConcurrentReconciles != 10 {
		t.Errorf("MaxConcurrentReconciles = %d, want 10", opts.MaxConcurrentReconciles)
	}
}
