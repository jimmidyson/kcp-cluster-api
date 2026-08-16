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

package scaleharness

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Service is the service-specific half of scale characterisation.
//
// Everything else in this package — sweeping, fitting, knee detection,
// reporting — is service-agnostic, because the cost structure it measures is a
// property of the per-workspace wiring rather than of any one controller.
// Listener count, cached objects, workers and dispatch cost take the same form
// for anything built on the providerwiring seam; only the coefficients differ.
//
// So this interface is where service knowledge is confined, and it is
// deliberately small: what objects a workspace should hold, how to generate an
// event, and what the controller watches. That last one is not incidental — it
// sets the listener term of the resource model, which is what makes the model
// per-service rather than a constant.
//
// This is a seam, not a plugin system. There is one production implementation
// today, and the general-purpose characterisation utility the appliance model
// anticipates is explicitly not built here; its trigger is the second real
// controller. See contracts/service-characterisation.md.
type Service interface {
	// Name identifies the service in reports and in published capacity
	// figures, which are stated per service as well as per profile.
	Name() string

	// WatchedTypes reports the types this service's controllers watch, as
	// human-readable identifiers. Its length drives the listener term of the
	// resource model.
	WatchedTypes() []string

	// Populate creates the objects one workspace holds under a profile, using
	// a client already scoped to that workspace.
	Populate(ctx context.Context, c client.Client, objects int) error

	// Touch generates one event in a workspace, so that a profile's declared
	// event rate can be driven. It is one mutation, not a batch: the harness
	// owns the rate.
	Touch(ctx context.Context, c client.Client) error
}

// Profile is a named, reproducible shard shape.
//
// Capacity is stated per profile because idle and active workspaces are not
// interchangeable units of load: a shard holds far more of the former for the
// same memory, and quoting one number for both would size every deployment
// wrongly in one direction or the other.
type Profile struct {
	// Name distinguishes this shape in reports and published figures.
	Name string

	// ObjectsPerWorkspace is how many objects each workspace holds.
	ObjectsPerWorkspace int

	// EventsPerWorkspacePerSecond is the sustained mutation rate applied
	// during measurement.
	//
	// Declared, never inferred (FR-036). Total dispatch work scales with this
	// times workspace count times listeners per workspace, so an inferred
	// value would silently bake a workload assumption into a published
	// capacity figure.
	EventsPerWorkspacePerSecond float64
}

// IdleHeavy is the shape a kcp installation is expected to have most of:
// workspaces bound to the export and holding nothing.
//
// It is not a degenerate case. Idle workspaces are where the fixed
// per-workspace costs — listeners, workers, clients, mappers — show up
// undiluted by anything else, which makes this the profile that bounds how many
// workspaces a shard can hold.
func IdleHeavy() Profile {
	return Profile{Name: "idle-heavy"}
}

// ActiveHeavy is the opposite bound: every workspace holding objects and
// generating events.
//
// The rate here is a starting point for the sweep, not a claim about any real
// tenant. An operator whose fleet is busier should raise it and re-measure
// rather than scale the published figure, since the dispatch term is not
// linear in it.
func ActiveHeavy() Profile {
	return Profile{
		Name:                        "active-heavy",
		ObjectsPerWorkspace:         10,
		EventsPerWorkspacePerSecond: 1,
	}
}

// Validate rejects shapes that would produce a number nobody can interpret.
func (p Profile) Validate() error {
	var errs []error
	if p.Name == "" {
		errs = append(errs, errors.New("profile has no name: capacity is stated per profile and would be unattributable"))
	}
	if p.ObjectsPerWorkspace < 0 {
		errs = append(errs, fmt.Errorf("objects per workspace is %d", p.ObjectsPerWorkspace))
	}
	if p.EventsPerWorkspacePerSecond < 0 {
		errs = append(errs, fmt.Errorf("event rate is %v", p.EventsPerWorkspacePerSecond))
	}
	if p.ObjectsPerWorkspace == 0 && p.EventsPerWorkspacePerSecond > 0 {
		errs = append(errs, errors.New("an event rate is declared but there are no objects to mutate"))
	}
	return errors.Join(errs...)
}
