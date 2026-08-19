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
	webhookWorkspace.wired = map[string]bool{"core": true, "dev-infrastructure": true}
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

// TestDefaultMaxConcurrentReconcilesIsChosenForManyTenants covers FR-010.
//
// Upstream's core/main.go uses 10, which is the whole process's budget in a
// single-tenant deployment. Here it is paid once per controller *per
// workspace*: five controllers at 10 is fifty eagerly-started worker goroutines
// for every workspace, whether or not it has any objects. A default inherited
// from a single-tenant binary is a scaling defect rather than a tuning
// preference.
func TestDefaultMaxConcurrentReconcilesIsChosenForManyTenants(t *testing.T) {
	if DefaultMaxConcurrentReconciles >= 10 {
		t.Errorf("DefaultMaxConcurrentReconciles = %d: still upstream's single-tenant value, which is paid per workspace here",
			DefaultMaxConcurrentReconciles)
	}
	if DefaultMaxConcurrentReconciles < 1 {
		t.Errorf("DefaultMaxConcurrentReconciles = %d: a workspace must be able to make progress",
			DefaultMaxConcurrentReconciles)
	}
	// Throughput is linear in this number (evidence/reconcile-throughput.md),
	// so one worker means a workspace's backlog drains strictly serially — one
	// slow reconcile stalls every other object it owns. That is a per-tenant
	// failure mode rather than a footprint saving worth having.
	if DefaultMaxConcurrentReconciles < 2 {
		t.Errorf("DefaultMaxConcurrentReconciles = %d: a single worker drains a tenant's backlog serially",
			DefaultMaxConcurrentReconciles)
	}
}

// TestSetupOptionsDefaultsConcurrency covers the "configurable" half of FR-010:
// an operator must be able to raise it, and leaving it unset must not mean zero.
func TestSetupOptionsDefaultsConcurrency(t *testing.T) {
	if got := (SetupOptions{}).maxConcurrentReconciles(); got != DefaultMaxConcurrentReconciles {
		t.Errorf("unset concurrency = %d, want the default %d", got, DefaultMaxConcurrentReconciles)
	}
	if got := (SetupOptions{MaxConcurrentReconciles: 7}).maxConcurrentReconciles(); got != 7 {
		t.Errorf("configured concurrency = %d, want 7", got)
	}
}
