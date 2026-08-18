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
	"math"
	"slices"
	"strings"
)

const (
	// DefaultGOGC is the collector setting the resident-size derivation assumes
	// when none is stated. It is Go's own default, so a deployment that sets
	// nothing gets arithmetic that matches it.
	DefaultGOGC = 100

	// DefaultNonHeapBytes is the process cost that is not live heap: goroutine
	// stacks, the runtime's own structures, the binary itself.
	//
	// A flat allowance rather than a fitted term, and deliberately so. It is not
	// flat in reality — goroutine stacks alone scale with workspace count, and
	// at 211 goroutines per workspace that is not a rounding error — but the
	// harness measures `HeapAlloc`, which does not include stacks, so there is
	// nothing in the data to fit it from. A stated allowance that a reader can
	// see and argue with is better than a coefficient that looks measured and
	// is not. Closing this properly needs the sweep to record `MemStats.Sys`.
	DefaultNonHeapBytes = 128 << 20

	// minFitPoints is the fewest points a fit may be claimed from.
	//
	// Three, not two: a line through two points passes through both exactly, so
	// its residual is zero whatever the underlying shape. There is no evidence
	// of linearity in a perfect fit that could not have been otherwise.
	minFitPoints = 3
)

// Shape is the fleet configuration a figure applies to.
//
// Every field here was held **fixed** across the sweep that produced a model.
// That is what makes the type necessary: the model has one coefficient, over
// workspace count, and these are the conditions under which that coefficient
// was observed. Two models with different shapes are measurements of different
// systems, however similar the code.
type Shape struct {
	Profile                     string  `json:"profile"`
	ListenersPerWorkspace       int     `json:"listenersPerWorkspace"`
	ObjectsPerWorkspace         int     `json:"objectsPerWorkspace"`
	EventsPerWorkspacePerSecond float64 `json:"eventsPerWorkspacePerSecond"`
}

// Regime is where a model is valid: the shape it was fitted under, how its load
// was produced, and the range of workspace counts actually measured.
type Regime struct {
	Shape         Shape    `json:"shape"`
	Mode          LoadMode `json:"mode"`
	Service       string   `json:"service"`
	MinWorkspaces int      `json:"minWorkspaces"`
	MaxWorkspaces int      `json:"maxWorkspaces"`
	Points        int      `json:"points"`
}

// Coefficients are one quantity's fitted cost: a fixed process term and a
// per-workspace term, with the fit's quality alongside them.
type Coefficients struct {
	// Base is the cost at zero workspaces — process overhead that a shard pays
	// once. Quoting a per-workspace figure without it overstates a small fleet
	// badly, since at the low end the fixed term dominates.
	Base float64 `json:"base"`

	// PerWorkspace is the marginal cost of one more workspace. This is the
	// number a shard's capacity is set from.
	PerWorkspace float64 `json:"perWorkspace"`

	// RSquared is how much of the variation the line accounts for. Recorded
	// because a coefficient from a poor fit is not less precise, it is not
	// evidence: it must be visible next to the number rather than discoverable
	// by re-running the fit.
	RSquared float64 `json:"rSquared"`
}

// At evaluates the fitted line.
func (c Coefficients) At(workspaces int) float64 {
	return c.Base + c.PerWorkspace*float64(workspaces)
}

// Model is a fitted resource model over workspace count (FR-034).
//
// # Why one variable and not the five the plan named
//
// The intended structure was `memory ≈ base + a·W + b·objects + c·W·maxConcurrent`
// with a matching CPU expression. Only W varied in the sweeps that exist:
// objects per workspace, listeners, event rate and reconcile concurrency were
// each held fixed so that the workspace term could be isolated.
//
// Fitting the full expression to that data would produce four coefficients from
// one varying input. They would have standard errors, they would look like
// measurements, and they would be arbitrary — any split of the observed cost
// between the fixed-input terms fits equally well. So the model carries only
// the term its data supports, records the rest as the regime it is valid in,
// and refuses to project along them. Adding a term is a matter of sweeping that
// dimension, not of extending the arithmetic.
type Model struct {
	Regime     Regime       `json:"regime"`
	Heap       Coefficients `json:"heap"`
	Goroutines Coefficients `json:"goroutines"`

	// Process is the footprint the runtime obtained from the OS, fitted the
	// same way. Present only when the run recorded it.
	//
	// This is what a container limit should come from, and it is a
	// measurement rather than a derivation — which matters because the
	// derivation it replaces was wrong in a direction that under-provisions.
	// Live heap omits goroutine stacks entirely, and at hundreds of goroutines
	// per workspace those stacks scale with the fleet, so a flat allowance for
	// them understates a large shard by a term that grows.
	Process *Coefficients `json:"process,omitempty"`

	// Stack is the goroutine-stack part of Process, fitted separately so the
	// growing term is visible rather than buried in a total.
	Stack *Coefficients `json:"stack,omitempty"`

	// Unmodelled names the costs this model does not carry, so that a reader
	// who needs one is not left to infer its absence from a missing field. A
	// sizing table is exactly where an invented number does damage.
	Unmodelled []string `json:"unmodelled,omitempty"`
}

// Prediction is a model's answer for one fleet size, carrying everything
// FR-035 and FR-039 require travel with a published figure.
type Prediction struct {
	Workspaces int      `json:"workspaces"`
	Mode       LoadMode `json:"mode"`

	// HeapBytes is live heap. ResidentBytes is what to size a container from.
	HeapBytes     float64 `json:"heapBytes"`
	ResidentBytes float64 `json:"residentBytes"`
	Goroutines    float64 `json:"goroutines"`

	// StackBytes is the goroutine-stack share of the footprint, present when
	// the run measured it. Worth surfacing on its own because it is the term
	// that grows with the fleet and is absent from live heap, so a reader
	// checking a sizing figure against their own heap profile would otherwise
	// find it unaccountably large.
	StackBytes float64 `json:"stackBytes,omitempty"`

	// ResidentBasis says where ResidentBytes came from: a fitted measurement
	// of what the runtime obtained from the OS, or the stated derivation from
	// live heap. They are not equally trustworthy and the difference must not
	// be invisible in a published table.
	ResidentBasis string `json:"residentBasis"`

	// Extrapolated reports that this fleet size was never measured, and
	// ExtrapolationFactor how far beyond the measured range it lies. One means
	// inside it.
	Extrapolated        bool    `json:"extrapolated"`
	ExtrapolationFactor float64 `json:"extrapolationFactor"`
}

// Fit derives coefficients from a run's measurements by ordinary least squares.
//
// Least squares rather than the two-point projection FindDeparture uses, and
// the difference is intentional: departure detection asks whether later points
// leave the trend the earliest ones set, so it must not let those later points
// influence the trend. A model wants the opposite — the best line through
// everything measured.
func Fit(run SweepRun) (Model, error) {
	if err := run.Validate(); err != nil {
		return Model{}, fmt.Errorf("cannot fit an invalid run: %w", err)
	}
	if run.Mode == "" {
		return Model{}, errors.New("run has no load mode: a fitted figure that does not state how its load was produced must not be used for sizing")
	}
	if len(run.Measurements) < minFitPoints {
		return Model{}, fmt.Errorf("run has %d points, need at least %d to fit a line with any residual to check it against",
			len(run.Measurements), minFitPoints)
	}

	ms := slices.Clone(run.Measurements)
	slices.SortFunc(ms, func(a, b Measurement) int { return a.Workspaces - b.Workspaces })

	xs := make([]float64, len(ms))
	heap := make([]float64, len(ms))
	goroutines := make([]float64, len(ms))
	process := make([]float64, len(ms))
	stack := make([]float64, len(ms))
	haveProcess := true
	for i, m := range ms {
		xs[i] = float64(m.Workspaces)
		heap[i] = float64(m.HeapBytes)
		goroutines[i] = float64(m.Goroutines)
		process[i] = float64(m.ProcessBytes)
		stack[i] = float64(m.StackBytes)
		if m.ProcessBytes == 0 {
			// A run predating the measurement, not a process occupying
			// nothing. Fitting a line through zeroes would emit coefficients
			// that look measured and are not, so the whole term is dropped and
			// its absence stated.
			haveProcess = false
		}
	}

	heapFit, err := leastSquares(xs, heap)
	if err != nil {
		return Model{}, fmt.Errorf("fitting heap: %w", err)
	}
	goroutineFit, err := leastSquares(xs, goroutines)
	if err != nil {
		return Model{}, fmt.Errorf("fitting goroutines: %w", err)
	}

	var processFit, stackFit *Coefficients
	if haveProcess {
		p, err := leastSquares(xs, process)
		if err != nil {
			return Model{}, fmt.Errorf("fitting process footprint: %w", err)
		}
		s, err := leastSquares(xs, stack)
		if err != nil {
			return Model{}, fmt.Errorf("fitting stacks: %w", err)
		}
		processFit, stackFit = &p, &s
	}

	return Model{
		Regime: Regime{
			Shape: Shape{
				Profile:                     run.Profile.Name,
				ListenersPerWorkspace:       run.ListenersPerWorkspace,
				ObjectsPerWorkspace:         run.Profile.ObjectsPerWorkspace,
				EventsPerWorkspacePerSecond: run.Profile.EventsPerWorkspacePerSecond,
			},
			Mode:          run.Mode,
			Service:       run.Service,
			MinWorkspaces: ms[0].Workspaces,
			MaxWorkspaces: ms[len(ms)-1].Workspaces,
			Points:        len(ms),
		},
		Heap:       heapFit,
		Goroutines: goroutineFit,
		Process:    processFit,
		Stack:      stackFit,
		Unmodelled: unmodelled(haveProcess),
	}, nil
}

func unmodelled(haveProcess bool) []string {
	out := []string{
		"CPU: the harness records no CPU time, so there is nothing to fit; " +
			"the planned base + d·events·W + e·reconciles/s expression needs a sweep that measures it",
	}
	if !haveProcess {
		out = append(out, "process footprint: this run predates the recording of MemStats.Sys, so "+
			"sizing must fall back to ResidentBytes' stated derivation from live heap, "+
			"which does not account for goroutine stacks growing with the fleet")
	}
	return out
}

// Predict evaluates the model for a fleet size, refusing shapes it never
// measured.
//
// The refusal is the substance of FR-034's "MUST decline to project across a
// discontinuity it has not observed". A caller asking about a different
// listener count or a different event rate is asking about a dimension this
// sweep held fixed; answering would mean assuming the cost is flat in it, and
// the whole reason listeners are recorded on a run is that it is not.
func (m Model) Predict(shape Shape, workspaces int) (Prediction, error) {
	if workspaces <= 0 {
		return Prediction{}, fmt.Errorf("cannot predict for %d workspaces", workspaces)
	}
	if shape != m.Regime.Shape {
		return Prediction{}, fmt.Errorf(
			"model was fitted at %s and cannot project to %s: those dimensions were held fixed during measurement, "+
				"so the model has no coefficient for them — sweep the differing dimension instead",
			describeShape(m.Regime.Shape), describeShape(shape))
	}

	heap := m.Heap.At(workspaces)
	factor := 1.0
	extrapolated := false
	if m.Regime.MaxWorkspaces > 0 && workspaces > m.Regime.MaxWorkspaces {
		extrapolated = true
		factor = float64(workspaces) / float64(m.Regime.MaxWorkspaces)
	}
	// Below the measured range is extrapolation too, and the more dangerous
	// direction: the fixed process term dominates at small fleets, which is the
	// warm-up region R14 found inflates a projection taken from it.
	if workspaces < m.Regime.MinWorkspaces {
		extrapolated = true
		factor = float64(m.Regime.MinWorkspaces) / float64(workspaces)
	}

	// The measured footprint wins where it exists. The derivation from live
	// heap is a fallback for runs taken before it was recorded, and it is the
	// weaker of the two in the direction that matters: it accounts for
	// goroutine stacks with a flat allowance, and stacks grow with the fleet.
	resident := ResidentBytes(heap, DefaultGOGC, DefaultNonHeapBytes)
	basis := fmt.Sprintf("derived from live heap at GOGC=%d", DefaultGOGC)
	var stack float64
	if m.Process != nil {
		resident = m.Process.At(workspaces)
		basis = "fitted from measured process footprint (MemStats.Sys)"
	}
	if m.Stack != nil {
		stack = m.Stack.At(workspaces)
	}

	return Prediction{
		Workspaces:          workspaces,
		Mode:                m.Regime.Mode,
		HeapBytes:           heap,
		ResidentBytes:       resident,
		StackBytes:          stack,
		ResidentBasis:       basis,
		Goroutines:          m.Goroutines.At(workspaces),
		Extrapolated:        extrapolated,
		ExtrapolationFactor: factor,
	}, nil
}

// Summary states the model and the conditions that make it applicable, in one
// line, because a coefficient quoted without them is not interpretable.
func (m Model) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s / %s / %s, %d listeners per workspace, fitted over %d..%d workspaces (%d points): ",
		m.Regime.Service, m.Regime.Shape.Profile, m.Regime.Mode,
		m.Regime.Shape.ListenersPerWorkspace, m.Regime.MinWorkspaces, m.Regime.MaxWorkspaces, m.Regime.Points)
	fmt.Fprintf(&b, "heap %.0f B + %.0f B/workspace (R²=%.4f), goroutines %.0f + %.1f/workspace (R²=%.4f)",
		m.Heap.Base, m.Heap.PerWorkspace, m.Heap.RSquared,
		m.Goroutines.Base, m.Goroutines.PerWorkspace, m.Goroutines.RSquared)
	return b.String()
}

func describeShape(s Shape) string {
	return fmt.Sprintf("profile=%s listeners=%d objects/workspace=%d events/workspace/s=%g",
		s.Profile, s.ListenersPerWorkspace, s.ObjectsPerWorkspace, s.EventsPerWorkspacePerSecond)
}

// ResidentBytes converts live heap to the resident size a container should be
// sized from (FR-036).
//
// # The derivation, stated rather than assumed
//
//	resident ≈ live · (1 + GOGC/100) + nonHeap
//
// Go's collector lets the heap grow to `live · (1 + GOGC/100)` before running,
// so a process holding L bytes of reachable data occupies roughly twice that at
// the default GOGC=100 just before a cycle. Sizing a limit from live heap alone
// would put the limit below the point at which the collector was going to run,
// and the process would be killed doing exactly what it was configured to do.
//
// This is deliberately an over-estimate of the steady state and an
// under-estimate of the worst case. It ignores allocation spikes between
// cycles, and it ignores that the runtime returns memory to the OS lazily, so
// resident size lags heap size downward. It is arithmetic to size from, not a
// prediction of any particular instant — which is why it is stated here for a
// reader to disagree with rather than folded silently into a coefficient.
//
// Setting GOMEMLIMIT changes this relationship entirely, by making the heap
// goal a function of the limit rather than of live heap. A deployment using it
// should size from the limit and use this only to check the limit is above live
// heap with margin.
func ResidentBytes(liveHeap float64, gogc int, nonHeap uint64) float64 {
	if gogc <= 0 {
		gogc = DefaultGOGC
	}
	return liveHeap*(1+float64(gogc)/100) + float64(nonHeap)
}

// Validation is one held-out point: what the model predicted for a measurement
// it was not shown, against what that measurement actually was.
type Validation struct {
	Workspaces int `json:"workspaces"`

	MeasuredHeapBytes  float64 `json:"measuredHeapBytes"`
	PredictedHeapBytes float64 `json:"predictedHeapBytes"`
	HeapErrorFraction  float64 `json:"heapErrorFraction"`

	MeasuredGoroutines     float64 `json:"measuredGoroutines"`
	PredictedGoroutines    float64 `json:"predictedGoroutines"`
	GoroutineErrorFraction float64 `json:"goroutineErrorFraction"`

	// Extrapolated reports whether the held-out point lay outside the range of
	// the points that remained. The endpoints do, and their errors are the
	// interesting ones: they are the only evidence in a sweep about how the
	// model behaves where it has not measured, which is the case every
	// published projection is.
	Extrapolated bool `json:"extrapolated"`
}

// HoldOut fits the model repeatedly, excluding one measured point each time,
// and reports how well the remaining points predicted the excluded one
// (FR-035).
//
// This is what separates a model from a restatement of its inputs. A fit
// evaluated on the data it was fitted to will always look good; the only
// question worth asking is what it says about a point it never saw.
//
// Every point gets a turn, including the endpoints. Validating only the
// interior would be easier and would omit the two cases that matter most —
// predicting past the largest measured fleet is precisely what a published
// sizing figure does.
func HoldOut(run SweepRun) ([]Validation, error) {
	if len(run.Measurements) < minFitPoints+1 {
		return nil, fmt.Errorf("run has %d points; holding one out leaves %d, and a fit needs %d",
			len(run.Measurements), len(run.Measurements)-1, minFitPoints)
	}

	ms := slices.Clone(run.Measurements)
	slices.SortFunc(ms, func(a, b Measurement) int { return a.Workspaces - b.Workspaces })

	var out []Validation
	for i, held := range ms {
		remaining := slices.Clone(ms)
		remaining = slices.Delete(remaining, i, i+1)

		reduced := run
		reduced.Measurements = remaining
		reduced.Points = nil

		m, err := Fit(reduced)
		if err != nil {
			return nil, fmt.Errorf("fitting without %d workspaces: %w", held.Workspaces, err)
		}

		// Predicted directly from the coefficients rather than through Predict:
		// a held-out endpoint is outside the reduced fit's range by
		// construction, and refusing to evaluate it would make exactly the two
		// most informative validations impossible.
		predHeap := m.Heap.At(held.Workspaces)
		predGoroutines := m.Goroutines.At(held.Workspaces)

		out = append(out, Validation{
			Workspaces:             held.Workspaces,
			MeasuredHeapBytes:      float64(held.HeapBytes),
			PredictedHeapBytes:     predHeap,
			HeapErrorFraction:      relativeError(float64(held.HeapBytes), predHeap),
			MeasuredGoroutines:     float64(held.Goroutines),
			PredictedGoroutines:    predGoroutines,
			GoroutineErrorFraction: relativeError(float64(held.Goroutines), predGoroutines),
			Extrapolated: held.Workspaces < m.Regime.MinWorkspaces ||
				held.Workspaces > m.Regime.MaxWorkspaces,
		})
	}
	return out, nil
}

func relativeError(measured, predicted float64) float64 {
	if measured == 0 {
		if predicted == 0 {
			return 0
		}
		return math.Inf(1)
	}
	return math.Abs(predicted-measured) / math.Abs(measured)
}

// leastSquares fits y = base + slope·x and reports R².
func leastSquares(xs, ys []float64) (Coefficients, error) {
	n := float64(len(xs))
	var sumX, sumY float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
	}
	meanX, meanY := sumX/n, sumY/n

	var sxx, sxy float64
	for i := range xs {
		dx := xs[i] - meanX
		sxx += dx * dx
		sxy += dx * (ys[i] - meanY)
	}
	if sxx == 0 {
		return Coefficients{}, errors.New("every point has the same workspace count, so no trend in workspace count can be fitted")
	}

	slope := sxy / sxx
	base := meanY - slope*meanX

	var ssRes, ssTot float64
	for i := range xs {
		resid := ys[i] - (base + slope*xs[i])
		ssRes += resid * resid
		dy := ys[i] - meanY
		ssTot += dy * dy
	}
	// A flat quantity has no variation to explain. The line through it is
	// exact, so reporting R²=1 is right, whereas the usual 1 - ssRes/ssTot
	// would divide by zero.
	rSquared := 1.0
	if ssTot > 0 {
		rSquared = 1 - ssRes/ssTot
	}

	return Coefficients{Base: base, PerWorkspace: slope, RSquared: rSquared}, nil
}
