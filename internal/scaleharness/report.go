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
	"slices"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/sweep"
	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
)

// Point and Departure are internal/sweep's, aliased rather than redeclared.
//
// Committed runs in this package's format and samples taken by the live
// instrument describe the same quantity, and departure detection is one
// procedure applied to both. Two definitions of the pair would let the
// published sizing tables and the instrument's own verdict drift apart while
// both looked correct — which is the failure this alias exists to make
// impossible rather than merely unlikely.
type (
	Point     = sweep.Point
	Departure = sweep.Departure
)

// LoadMode records how a figure's load was produced.
//
// It travels with every number this package emits, because the two modes have
// different trustworthiness and the difference is invisible after the fact.
type LoadMode string

const (
	// ModeSynthetic means objects were generated for the measurement.
	//
	// Available before a service has users, which is when planning matters
	// most — and the mode that can under-measure, because generated objects
	// may fail validation or take cheap error paths rather than the reconcile
	// paths a real tenant exercises. An under-measured memory figure becomes
	// an under-provisioned limit, so this label is load-bearing rather than
	// bookkeeping.
	ModeSynthetic LoadMode = "synthetic"

	// ModeObserved means coefficients came from a running deployment's real
	// load. Always measures real work; yields nothing for a service that is
	// not deployed, or a fleet that does not vary.
	//
	// It is also weaker than synthetic in one specific way, and the asymmetry
	// runs opposite to what the names suggest: a running process cannot
	// currently attribute reconciles to the workspace that caused them,
	// because controller-runtime has no seam for wrapping a reconciler it did
	// not construct (research R13). Observed runs therefore see engagement
	// counts, failures and aggregate totals, but no per-workspace breakdown —
	// whereas synthetic runs attribute fully, since the harness generates the
	// load and knows where it sent it.
	ModeObserved LoadMode = "observed"
)

// SweepRun is one execution of the harness: one service, one profile, one
// mode, the points measured and the departure point derived from them.
type SweepRun struct {
	Service   string    `json:"service"`
	Profile   Profile   `json:"profile"`
	Mode      LoadMode  `json:"mode"`
	Points    []Point   `json:"points"`
	Departure Departure `json:"departure"`

	// Measurements are the raw per-point costs. Points is the single quantity
	// the departure point was taken over; these are everything that was recorded, so a
	// later question can be asked of an existing run without re-measuring.
	Measurements []Measurement `json:"measurements,omitempty"`

	// ListenersPerWorkspace is how many watches the service's controllers
	// registered for each workspace.
	//
	// Recorded because it changes the answer rather than describing it. Every
	// cost this run reports that scales with listeners — dispatch fan-out,
	// goroutines, informer bookkeeping — scales with this number too, so two
	// runs at different densities produce different coefficients for the same
	// system. A per-workspace figure quoted without it cannot be compared with
	// another, and cannot be applied to a wiring that registers a different
	// number.
	ListenersPerWorkspace int `json:"listenersPerWorkspace,omitempty"`
}

// Validate rejects a run that could not be safely quoted.
func (r SweepRun) Validate() error {
	var errs []error
	if r.Service == "" {
		errs = append(errs, errors.New("run names no service"))
	}
	switch r.Mode {
	case ModeSynthetic, ModeObserved:
	case "":
		errs = append(errs, errors.New("run has no load mode: an unlabelled figure must not be used for sizing"))
	default:
		errs = append(errs, fmt.Errorf("unrecognised load mode %q", r.Mode))
	}
	if err := r.Profile.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Outcome maps the run onto the project's existing three-outcome contract,
// rather than introducing a second one for callers to learn.
//
// The distinction that matters is between a sweep that measured linear cost —
// a real result, saying capacity is above the swept range — and a sweep too
// short to establish anything. Reporting the second as a pass would let an
// unrunnable measurement masquerade as evidence of headroom.
func (r SweepRun) Outcome() verify.Outcome {
	if r.Departure.CouldNotRun {
		return verify.OutcomeCouldNotRun
	}
	return verify.OutcomePass
}

// Extrapolated reports whether a workspace count lies outside the swept range,
// and so whether a figure quoted for it is a projection rather than a
// measurement.
func (r SweepRun) Extrapolated(workspaces int) bool {
	if len(r.Points) == 0 {
		return true
	}
	largest := slices.MaxFunc(r.Points, func(a, b Point) int { return a.Workspaces - b.Workspaces })
	return workspaces > largest.Workspaces
}

// Summary is the human-readable line, carrying the three things a figure
// cannot be interpreted without: which service, which shape, and how the load
// was produced.
func (r SweepRun) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s / %s / %s: %d points", r.Service, r.Profile.Name, r.Mode, len(r.Points))
	if r.ListenersPerWorkspace > 0 {
		fmt.Fprintf(&b, ", %d listeners per workspace", r.ListenersPerWorkspace)
	}

	switch {
	case r.Departure.CouldNotRun:
		fmt.Fprintf(&b, "; departure point could not be established (%s)", r.Departure.Reason)
	case r.Departure.Found:
		fmt.Fprintf(&b, "; departure point at %d workspaces (tolerance %.0f%%)", r.Departure.Workspaces, r.Departure.Tolerance*100)
	default:
		fmt.Fprintf(&b, "; no departure point (%s)", r.Departure.Reason)
	}
	return b.String()
}
