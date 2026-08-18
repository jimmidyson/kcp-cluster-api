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
	"errors"
	"fmt"
)

// Profile is a named, reproducible shard shape.
//
// Capacity is stated per profile because idle and active workspaces are not
// interchangeable units of load: a shard holds far more of the former for the
// same memory, and quoting one number for both would size every deployment
// wrongly in one direction or the other.
type Profile struct {
	// Name distinguishes this shape in reports and published figures.
	Name string `json:"name"`

	// ObjectsPerWorkspace is how many objects each workspace holds.
	ObjectsPerWorkspace int `json:"objectsPerWorkspace"`

	// EventsPerWorkspacePerSecond is the sustained mutation rate applied
	// during measurement.
	//
	// Declared, never inferred (FR-036). Total dispatch work scales with this
	// times workspace count times listeners per workspace, so an inferred
	// value would silently bake a workload assumption into a published
	// capacity figure.
	EventsPerWorkspacePerSecond float64 `json:"eventsPerWorkspacePerSecond"`
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
