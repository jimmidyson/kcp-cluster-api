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
	"fmt"

	"sigs.k8s.io/cluster-api/feature"
)

// featureGateDefaults are the gates this project defaults differently from
// upstream, and why.
//
// Both are consequences of what this project's APIExports publish, which is a
// deliberately smaller set than a single-tenant installation's CRDs: a gate
// whose types are not published is not merely unused here, it is a controller
// that never starts. controller-runtime blocks a controller's startup on every
// registered source's cache sync, including for a kind the server does not
// serve, so an unserved watched type does not skip the watch — it hangs.
var featureGateDefaults = map[string]bool{
	// A cluster in this project is a ClusterClass based cluster: the demo, the
	// integration tests and the documentation all build one from a class and a
	// topology rather than from objects written out by hand. Off, the four
	// topology reconcilers are not wired and such a Cluster is inert — it
	// reaches no phase, because nothing creates its infrastructure or its
	// control plane.
	//
	// Upstream defaults it off because for a single-tenant installation it is
	// one way of using Cluster API among several. Here it is the way.
	"ClusterTopology": true,

	// Upstream defaults MachinePool *on*, and the core Cluster reconciler
	// watches MachinePools when it is. This project publishes no MachinePool
	// CRD — it is outside ADR-0001's D3 scope — so leaving it on stalls the
	// core provider's startup on a cache sync that never completes.
	//
	// It was previously left at upstream's default and turned off by each
	// caller that got far enough to notice: the demo, and every integration
	// test that starts a manager. A deployment got no such correction, which
	// made a documented operator responsibility out of a defect.
	"MachinePool": false,
}

// SetFeatureGateDefaults applies this project's feature gate defaults.
//
// Call it before flag parsing, so that --feature-gates still wins: these are
// defaults rather than decisions, and an operator who publishes the CRDs a gate
// needs is entitled to turn it back on.
//
// A process that parses no flags — a test, the demo — calls it and gets the
// same wiring a deployment has, which is the point: the thing under test should
// not be configured differently from the thing that ships.
func SetFeatureGateDefaults() error {
	for gate, enabled := range featureGateDefaults {
		if err := feature.MutableGates.Set(fmt.Sprintf("%s=%t", gate, enabled)); err != nil {
			return fmt.Errorf("setting the %s feature gate default: %w", gate, err)
		}
	}
	return nil
}

// MustSetFeatureGateDefaults is SetFeatureGateDefaults for a caller with
// nowhere to return an error — a main() before its flags are defined.
//
// It panics rather than logging, because every failure it can have is a
// programming error: the gate names above are constants, and a name the
// upstream feature set does not know is a build this project cannot serve.
func MustSetFeatureGateDefaults() {
	if err := SetFeatureGateDefaults(); err != nil {
		panic(err)
	}
}
