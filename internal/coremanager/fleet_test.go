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

	"sigs.k8s.io/cluster-api/controllers/clustercache"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// The contract inside the fork, checked from here because here is where both
// halves meet.
//
// The fleet-wide setup functions take a MulticlusterClusterSourceFunc, and the
// fleet-wide ClusterCache's method is the only thing that supplies one. They are
// declared in the same package but are wired together only by this binary, so a
// signature drift between them would otherwise surface the next time the pin
// moves, as a build error a long way from its cause.
var _ func(clustercache.MulticlusterClusterCache) clustercache.MulticlusterClusterSourceFunc = func(cc clustercache.MulticlusterClusterCache) clustercache.MulticlusterClusterSourceFunc {
	return cc.GetMulticlusterClusterSource
}

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

	err := SetupFleetControllers(context.Background(), nil, &capicontrollerutil.WildcardRegistry{}, &DevInfrastructure{}, SetupOptions{})
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("multi-cluster manager"))
}
