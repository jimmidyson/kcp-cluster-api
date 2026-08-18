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

package sweep

import (
	"fmt"
	"slices"
)

const (
	// DefaultTolerance is how far a measurement may exceed the linear
	// projection before it counts as a departure.
	//
	// Not zero, and not configurable to zero: measurement noise on a real
	// server is easily a few percent, and a literal zero would report the
	// first rounding wobble as a departure point. Understating capacity that way is a
	// quieter failure than overstating it, but it is still wrong.
	DefaultTolerance = 0.25

	// DefaultMinPoints is the shortest sweep from which a departure point may be claimed:
	// two to establish the trend, and at least two more to depart from it.
	//
	// A shorter sweep does not mean "cost is linear", it means the question
	// was not asked — which is why falling short reports could-not-run rather
	// than not-found.
	DefaultMinPoints = 4
)

// PointsOf projects samples onto the (workspaces, cost) pairs FindDeparture
// works in.
//
// It exists so that a sweep answers two different questions from one set of
// samples: PerWorkspace fits a slope through all of them, which says what a
// workspace costs on average, and FindDeparture asks whether that slope holds —
// which is the question a capacity figure actually turns on. A mean slope
// through a curve is still a number, and still wrong.
func PointsOf(samples []Sample, measure func(Sample) float64) []Point {
	points := make([]Point, 0, len(samples))
	for _, s := range samples {
		points = append(points, Point{Workspaces: s.Workspaces, Value: measure(s)})
	}
	return points
}

// Point is one measured quantity at one workspace count.
type Point struct {
	Workspaces int     `json:"workspaces"`
	Value      float64 `json:"value"`
}

// DepartureOptions parameterises detection. Both fields are recorded in the result,
// because a departure point quoted without them cannot be reproduced or compared.
type DepartureOptions struct {
	// Tolerance is the fractional excess over the linear projection that
	// counts as a departure. Zero means DefaultTolerance.
	Tolerance float64

	// MinPoints is the shortest sweep a departure point may be claimed from. Zero means
	// DefaultMinPoints.
	MinPoints int
}

// Departure is the outcome of detection, carrying the parameters that produced
// it. Comparable, so a test can assert determinism directly.
type Departure struct {
	// Found reports whether cost departed from linear within the swept range.
	Found bool `json:"found"`
	// Workspaces is the smallest count at which it did. Meaningful only when
	// Found.
	Workspaces int `json:"workspaces,omitempty"`
	// CouldNotRun distinguishes "the sweep could not answer this" from "cost
	// was linear". Conflating them would let too short a sweep masquerade as
	// evidence of headroom.
	CouldNotRun bool `json:"couldNotRun,omitempty"`
	// Reason explains a Found=false outcome.
	Reason string `json:"reason,omitempty"`
	// Tolerance and Points are the parameters used, recorded per FR-030.
	Tolerance float64 `json:"tolerance"`
	Points    int     `json:"points"`
}

// FindDeparture finds the smallest swept workspace count at which a measured
// quantity exceeds the linear projection from the sweep's two smallest points
// by more than the tolerance.
//
// # On the name
//
// Performance engineering calls this the "knee of the curve", and that term is
// standard enough to have a literature and tooling behind it. It is
// deliberately not used here, for two reasons.
//
// It describes a different phenomenon. The classic knee is response time
// against utilisation — a queueing curve — where the bend is contested: an
// M/M/1 response-time curve is a smooth hyperbola, so a "knee" found on it is
// partly an artifact of the chosen axes. What is measured here is resource cost
// against workspace count, looking for evidence that an O(W) algorithmic term
// has begun to dominate. That is a real change in growth rate with a cause we
// can point at in a dependency's source, not an asymptote.
//
// And it is a shape metaphor in a document an operator reads to size a
// deployment. "Cost departs from linear at N workspaces" says what was found;
// "the knee is at N" requires knowing what curve is being pictured.
//
// The procedure is deliberately simple and stated in full, because its output
// becomes a published capacity figure. Anything cleverer — a fitted curve, a
// second-derivative test — would be harder to reproduce by hand and harder to
// argue with, and the ability to argue with it is the point.
//
// It does not extrapolate. A departure point outside the swept range is not detected and
// not guessed at; that is what CouldNotRun and the caller's extrapolation
// labelling are for.
func FindDeparture(points []Point, opts DepartureOptions) Departure {
	tolerance := opts.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	minPoints := opts.MinPoints
	if minPoints <= 0 {
		minPoints = DefaultMinPoints
	}

	result := Departure{Tolerance: tolerance, Points: len(points)}

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
