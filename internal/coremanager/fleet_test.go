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
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/jimmidyson/kcp-cluster-api/internal/fleet"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
)

// The contract between this repository and the fork, checked by the compiler.
//
// The fork's fleet-wide setup functions take a
// clustercache.MulticlusterClusterSourceFunc and cannot construct one; this is
// the only implementation, and its signature has to keep matching across two
// repositories that are versioned separately. A mismatch here is the failure
// that would otherwise appear as an unrelated-looking build error the next time
// the pin moves.
var _ clustercache.MulticlusterClusterSourceFunc = fleet.NewClusterCaches().Source

func TestFleetMaxConcurrentReconcilesDefaults(t *testing.T) {
	g := NewWithT(t)

	g.Expect(SetupOptions{}.fleetMaxConcurrentReconciles()).To(Equal(DefaultFleetMaxConcurrentReconciles))
	g.Expect(SetupOptions{FleetMaxConcurrentReconciles: -1}.fleetMaxConcurrentReconciles()).To(Equal(DefaultFleetMaxConcurrentReconciles))
	g.Expect(SetupOptions{FleetMaxConcurrentReconciles: 3}.fleetMaxConcurrentReconciles()).To(Equal(3))

	// The two knobs are independent: setting the per-workspace one must not
	// move the shared pool, which is the confusion a single field would create.
	g.Expect(SetupOptions{MaxConcurrentReconciles: 3}.fleetMaxConcurrentReconciles()).To(Equal(DefaultFleetMaxConcurrentReconciles))
	g.Expect(SetupOptions{FleetMaxConcurrentReconciles: 3}.maxConcurrentReconciles()).To(Equal(DefaultMaxConcurrentReconciles))

	// The two defaults are deliberately different numbers, for the reasons on
	// each. A change that accidentally aliased them would pass every other test
	// here.
	g.Expect(DefaultFleetMaxConcurrentReconciles).ToNot(Equal(DefaultMaxConcurrentReconciles))
}

// TestSetupFleetControllersRequiresItsCollaborators covers the two arguments
// that have no sensible zero value.
//
// Both are nil-checked rather than left to panic because both are supplied by
// the caller's wiring rather than by a constructor, which is the situation that
// produced two nil dereferences in the fork — found by envtest, missed by the
// compiler and by go vet.
func TestSetupFleetControllersRequiresItsCollaborators(t *testing.T) {
	g := NewWithT(t)

	err := SetupFleetControllers(context.Background(), nil, fleet.NewClusterCaches(), SetupOptions{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("multi-cluster manager"))
}

// TestSetupWorkspaceComponentsRequiresItsCollaborators is the same for the
// per-workspace half. The returned SetupFunc is what providerwiring calls, so
// its failures are per-workspace rather than fatal — which is why they have to
// be errors rather than panics.
func TestSetupWorkspaceComponentsRequiresItsCollaborators(t *testing.T) {
	g := NewWithT(t)

	setup := SetupWorkspaceComponents(nil, nil, SetupOptions{})
	err := setup(context.Background(), "ws", nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("fleet.ClusterCaches"))

	setup = SetupWorkspaceComponents(fleet.NewClusterCaches(), nil, SetupOptions{})
	err = setup(context.Background(), "ws", nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("DevInfrastructure"))
}
