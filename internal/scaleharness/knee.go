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
	"fmt"
	"slices"
)

const (
	// DefaultKneeTolerance is how far a measurement may exceed the linear
	// projection before it counts as a departure.
	//
	// Not zero, and not configurable to zero: measurement noise on a real
	// server is easily a few percent, and a literal zero would report the
	// first rounding wobble as a knee. Understating capacity that way is a
	// quieter failure than overstating it, but it is still wrong.
	DefaultKneeTolerance = 0.25

	// DefaultMinPoints is the shortest sweep from which a knee may be claimed:
	// two to establish the trend, and at least two more to depart from it.
	//
	// A shorter sweep does not mean "cost is linear", it means the question
	// was not asked — which is why falling short reports could-not-run rather
	// than not-found.
	DefaultMinPoints = 4
)

// Point is one measured quantity at one workspace count.
type Point struct {
	Workspaces int
	Value      float64
}

// KneeOptions parameterises detection. Both fields are recorded in the result,
// because a knee quoted without them cannot be reproduced or compared.
type KneeOptions struct {
	// Tolerance is the fractional excess over the linear projection that
	// counts as a departure. Zero means DefaultKneeTolerance.
	Tolerance float64

	// MinPoints is the shortest sweep a knee may be claimed from. Zero means
	// DefaultMinPoints.
	MinPoints int
}

// KneeResult is the outcome of detection, carrying the parameters that produced
// it. Comparable, so a test can assert determinism directly.
type KneeResult struct {
	// Found reports whether cost departed from linear within the swept range.
	Found bool
	// Workspaces is the smallest count at which it did. Meaningful only when
	// Found.
	Workspaces int
	// CouldNotRun distinguishes "the sweep could not answer this" from "cost
	// was linear". Conflating them would let too short a sweep masquerade as
	// evidence of headroom.
	CouldNotRun bool
	// Reason explains a Found=false outcome.
	Reason string
	// Tolerance and Points are the parameters used, recorded per FR-030.
	Tolerance float64
	Points    int
}

// DetectKnee finds the smallest swept workspace count at which a measured
// quantity exceeds the linear projection from the sweep's two smallest points
// by more than the tolerance.
//
// The procedure is deliberately simple and stated in full, because its output
// becomes a published capacity figure. Anything cleverer — a fitted curve, a
// second-derivative test — would be harder to reproduce by hand and harder to
// argue with, and the ability to argue with it is the point.
//
// It does not extrapolate. A knee outside the swept range is not detected and
// not guessed at; that is what CouldNotRun and the caller's extrapolation
// labelling are for.
func DetectKnee(points []Point, opts KneeOptions) KneeResult {
	tolerance := opts.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultKneeTolerance
	}
	minPoints := opts.MinPoints
	if minPoints <= 0 {
		minPoints = DefaultMinPoints
	}

	result := KneeResult{Tolerance: tolerance, Points: len(points)}

	if len(points) < minPoints {
		result.CouldNotRun = true
		result.Reason = fmt.Sprintf("sweep has %d points, need at least %d to establish a trend and a departure from it",
			len(points), minPoints)
		return result
	}

	// Sorted so the caller need not care about collection order; two runs over
	// the same set must agree whatever sequence they arrived in.
	sorted := slices.Clone(points)
	slices.SortFunc(sorted, func(a, b Point) int { return a.Workspaces - b.Workspaces })

	first, second := sorted[0], sorted[1]
	if second.Workspaces == first.Workspaces {
		result.CouldNotRun = true
		result.Reason = "the two smallest points share a workspace count, so no trend can be projected from them"
		return result
	}

	slope := (second.Value - first.Value) / float64(second.Workspaces-first.Workspaces)
	intercept := first.Value - slope*float64(first.Workspaces)

	for _, p := range sorted[2:] {
		projected := intercept + slope*float64(p.Workspaces)
		if projected <= 0 {
			// A non-positive projection makes the ratio meaningless rather
			// than large. Skipping is right: this is a degenerate trend, not a
			// departure from one.
			continue
		}
		if p.Value > projected*(1+tolerance) {
			result.Found = true
			result.Workspaces = p.Workspaces
			return result
		}
	}

	result.Reason = fmt.Sprintf("no point exceeded the linear projection by more than %.0f%% within the swept range (up to %d workspaces)",
		tolerance*100, sorted[len(sorted)-1].Workspaces)
	return result
}
