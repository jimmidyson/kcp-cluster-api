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
	"math"
	"strings"
	"testing"
)

// linearRun builds a run whose cost is exactly base + per·W, so a fit that does
// not recover the coefficients it was given has a defect rather than noise.
func linearRun(baseHeap, perHeap float64, baseGoroutines, perGoroutines float64, counts ...int) SweepRun {
	run := SweepRun{
		Service:               "test",
		Profile:               Profile{Name: "active-heavy", ObjectsPerWorkspace: 10, EventsPerWorkspacePerSecond: 1},
		Mode:                  ModeSynthetic,
		ListenersPerWorkspace: 19,
	}
	for _, n := range counts {
		run.Measurements = append(run.Measurements, Measurement{
			Workspaces: n,
			HeapBytes:  uint64(baseHeap + perHeap*float64(n)),
			Goroutines: int(baseGoroutines + perGoroutines*float64(n)),
		})
	}
	return run
}

func TestFitRecoversCoefficients(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64)

	m, err := Fit(run)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	if got, want := m.Heap.PerWorkspace, 1.5*(1<<20); math.Abs(got-want) > 1024 {
		t.Errorf("heap per workspace = %.0f, want %.0f", got, want)
	}
	if got, want := m.Heap.Base, float64(8<<20); math.Abs(got-want) > 1024 {
		t.Errorf("heap base = %.0f, want %.0f", got, want)
	}
	if got, want := m.Goroutines.PerWorkspace, 211.0; math.Abs(got-want) > 0.01 {
		t.Errorf("goroutines per workspace = %.3f, want %.0f", got, want)
	}
	if m.Heap.RSquared < 0.999 {
		t.Errorf("R² = %.4f on exactly linear data, want ~1", m.Heap.RSquared)
	}
}

// The regime is not decoration: it is what says whether a figure applies to the
// fleet someone is about to size.
func TestFitRecordsItsRegime(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64)

	m, err := Fit(run)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	if m.Regime.MinWorkspaces != 8 || m.Regime.MaxWorkspaces != 64 {
		t.Errorf("measured range = %d..%d, want 8..64", m.Regime.MinWorkspaces, m.Regime.MaxWorkspaces)
	}
	if m.Regime.Mode != ModeSynthetic {
		t.Errorf("regime mode = %q, want synthetic", m.Regime.Mode)
	}
	if m.Regime.Shape.ListenersPerWorkspace != 19 {
		t.Errorf("regime listeners = %d, want 19", m.Regime.Shape.ListenersPerWorkspace)
	}
	if m.Regime.Shape.Profile != "active-heavy" {
		t.Errorf("regime profile = %q, want active-heavy", m.Regime.Shape.Profile)
	}
}

// An unlabelled figure must not be usable for sizing (FR-039), and a model is
// the most reusable figure this package produces — so the check belongs at the
// fit, not at whoever eventually publishes it.
func TestFitRefusesAnUnlabelledRun(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64)
	run.Mode = ""

	if _, err := Fit(run); err == nil {
		t.Error("Fit accepted a run with no load mode")
	}
}

func TestFitRefusesTooFewPoints(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16)

	if _, err := Fit(run); err == nil {
		t.Error("Fit accepted two points; a line through two points has no residual and so no evidence it is a line")
	}
}

func TestPredictionInsideTheMeasuredRangeIsNotExtrapolated(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	p, err := m.Predict(m.Regime.Shape, 48)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if p.Extrapolated {
		t.Error("a point inside the measured range was labelled extrapolated")
	}
	if p.ExtrapolationFactor != 1 {
		t.Errorf("extrapolation factor = %v inside the range, want 1", p.ExtrapolationFactor)
	}
	if want := float64(8<<20) + 1.5*(1<<20)*48; math.Abs(p.HeapBytes-want) > 1<<10 {
		t.Errorf("predicted heap = %.0f, want %.0f", p.HeapBytes, want)
	}
}

// Projecting past what was measured is the whole point of a model, so it must
// be allowed — and must say how far past, since that is what a reader needs to
// discount it by.
func TestPredictionBeyondTheRangeCarriesItsExtrapolationFactor(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	p, err := m.Predict(m.Regime.Shape, 1000)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !p.Extrapolated {
		t.Error("a point 15× beyond the measured range was not labelled extrapolated")
	}
	if got, want := p.ExtrapolationFactor, 1000.0/64.0; math.Abs(got-want) > 0.01 {
		t.Errorf("extrapolation factor = %.3f, want %.3f", got, want)
	}
	if p.Mode != ModeSynthetic {
		t.Errorf("prediction mode = %q; a figure must carry how its load was produced", p.Mode)
	}
}

// The dimensions the sweep never varied are the ones a model knows nothing
// about. Listener count, objects per workspace and event rate were each held
// fixed, so their coefficients were never observed — projecting along them
// would emit a number with no measurement behind it, which is worse than
// emitting none.
func TestModelDeclinesToProjectAlongAnUnobservedDimension(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	for _, tc := range []struct {
		name  string
		shape Shape
	}{
		{"different listener count", func() Shape { s := m.Regime.Shape; s.ListenersPerWorkspace = 5; return s }()},
		{"different objects per workspace", func() Shape { s := m.Regime.Shape; s.ObjectsPerWorkspace = 100; return s }()},
		{"different event rate", func() Shape { s := m.Regime.Shape; s.EventsPerWorkspacePerSecond = 50; return s }()},
		{"different profile", func() Shape { s := m.Regime.Shape; s.Profile = "something-else"; return s }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Predict(tc.shape, 32); err == nil {
				t.Errorf("model projected across %s without having measured it", tc.name)
			}
		})
	}
}

func TestPredictRejectsANonPositiveFleet(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if _, err := m.Predict(m.Regime.Shape, 0); err == nil {
		t.Error("Predict accepted a fleet of zero workspaces")
	}
}

// FR-036: memory is modelled as live heap, and the step to resident size is
// stated arithmetic rather than an unspoken assumption — because sizing a
// container from live heap would under-provision it by whatever the collector
// is allowed to accumulate.
func TestResidentSizeIsDerivedFromLiveHeapUnderStatedGC(t *testing.T) {
	live := float64(100 << 20)

	got := ResidentBytes(live, DefaultGOGC, DefaultNonHeapBytes)
	want := live*(1+float64(DefaultGOGC)/100) + float64(DefaultNonHeapBytes)
	if math.Abs(got-want) > 1 {
		t.Errorf("resident = %.0f, want %.0f", got, want)
	}
	if got <= live {
		t.Error("resident size is not above live heap; a container sized from it would be under-provisioned")
	}

	// A tighter GOGC trades CPU for a smaller footprint, and the model must
	// follow the setting rather than assume the default.
	if tight := ResidentBytes(live, 50, DefaultNonHeapBytes); tight >= got {
		t.Errorf("GOGC=50 gave %.0f, not below GOGC=100's %.0f", tight, got)
	}
}

func TestPredictionCarriesResidentSize(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	p, err := m.Predict(m.Regime.Shape, 64)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if p.ResidentBytes <= p.HeapBytes {
		t.Errorf("resident %.0f is not above live heap %.0f", p.ResidentBytes, p.HeapBytes)
	}
}

// Where the process footprint was measured, sizing must come from it rather
// than from the derivation. The derivation accounts for goroutine stacks with a
// flat allowance and stacks grow with the fleet, so preferring it over a real
// measurement would under-provision a large shard by a term that grows.
func TestMeasuredFootprintIsPreferredOverTheDerivation(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64)
	for i := range run.Measurements {
		w := run.Measurements[i].Workspaces
		run.Measurements[i].ProcessBytes = uint64(64<<20) + uint64(w)*(4<<20)
		run.Measurements[i].StackBytes = uint64(w) * (2 << 20)
	}

	m, err := Fit(run)
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if m.Process == nil || m.Stack == nil {
		t.Fatal("a run recording process footprint produced no fitted footprint")
	}
	if got, want := m.Process.PerWorkspace, float64(4<<20); math.Abs(got-want) > 1024 {
		t.Errorf("process per workspace = %.0f, want %.0f", got, want)
	}

	p, err := m.Predict(m.Regime.Shape, 64)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if want := float64(64<<20) + 64*float64(4<<20); math.Abs(p.ResidentBytes-want) > 1<<20 {
		t.Errorf("resident = %.0f, want the fitted measurement %.0f", p.ResidentBytes, want)
	}
	if !strings.Contains(p.ResidentBasis, "measured") {
		t.Errorf("resident basis = %q, want it to say the figure was measured", p.ResidentBasis)
	}
	if p.StackBytes <= 0 {
		t.Error("prediction carries no stack figure though the run measured one")
	}
}

// A run taken before the footprint was recorded must fall back to the
// derivation and say so, not fit a line through zeroes and present the result
// as a measurement.
func TestARunWithoutAFootprintFallsBackAndSaysSo(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if m.Process != nil {
		t.Error("fitted a process footprint from a run that recorded none")
	}

	p, err := m.Predict(m.Regime.Shape, 64)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if !strings.Contains(p.ResidentBasis, "derived") {
		t.Errorf("resident basis = %q, want it to say the figure was derived", p.ResidentBasis)
	}
	if !strings.Contains(strings.Join(m.Unmodelled, " "), "process footprint") {
		t.Errorf("unmodelled terms do not record the missing footprint: %v", m.Unmodelled)
	}
}

// Held-out validation is the only thing that makes a fit a claim rather than a
// restatement of its own inputs.
func TestHoldOutValidationPredictsAnExcludedPoint(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64, 128)

	vs, err := HoldOut(run)
	if err != nil {
		t.Fatalf("HoldOut: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("held-out validation produced no results")
	}
	for _, v := range vs {
		if v.HeapErrorFraction > 0.001 {
			t.Errorf("holding out %d workspaces predicted heap %.0f against measured %.0f (%.3f%% error) on exactly linear data",
				v.Workspaces, v.PredictedHeapBytes, v.MeasuredHeapBytes, v.HeapErrorFraction*100)
		}
	}
}

// Every point must get a turn. Validating only the interior would exclude the
// largest point, which is the one whose prediction a reader most needs to trust
// — it is the closest thing the sweep has to an extrapolation.
func TestHoldOutExcludesEveryPointInTurn(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64, 128)

	vs, err := HoldOut(run)
	if err != nil {
		t.Fatalf("HoldOut: %v", err)
	}
	if len(vs) != len(run.Measurements) {
		t.Fatalf("validated %d points of %d", len(vs), len(run.Measurements))
	}

	held := map[int]bool{}
	for _, v := range vs {
		held[v.Workspaces] = true
		if v.Extrapolated != (v.Workspaces == 128 || v.Workspaces == 8) {
			t.Errorf("holding out %d: extrapolated = %v; the endpoints are outside the remaining fit's range and the interior is not",
				v.Workspaces, v.Extrapolated)
		}
	}
	for _, m := range run.Measurements {
		if !held[m.Workspaces] {
			t.Errorf("point %d was never held out", m.Workspaces)
		}
	}
}

// Removing a point from a four-point sweep leaves three, which is the minimum a
// fit needs. A shorter sweep cannot be validated at all, and must say so rather
// than report a validation it did not perform.
func TestHoldOutRefusesASweepTooShortToValidate(t *testing.T) {
	run := linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32)

	if _, err := HoldOut(run); err == nil {
		t.Error("HoldOut validated a three-point sweep, which leaves two points to fit from")
	}
}

// A model with no CPU coefficients must not pretend otherwise. The harness does
// not measure CPU, so any CPU figure would be invented — and a sizing table is
// exactly where an invented number does damage.
func TestModelStatesWhatItDoesNotModel(t *testing.T) {
	m, err := Fit(linearRun(8<<20, 1.5*(1<<20), 100, 211, 8, 16, 32, 64))
	if err != nil {
		t.Fatalf("Fit: %v", err)
	}

	summary := m.Summary()
	for _, want := range []string{"active-heavy", "synthetic", "19 listeners", "8..64"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary omits %q: %s", want, summary)
		}
	}
	if len(m.Unmodelled) == 0 {
		t.Error("model claims to have modelled everything; CPU was never measured")
	}
	if !strings.Contains(strings.ToLower(strings.Join(m.Unmodelled, " ")), "cpu") {
		t.Errorf("unmodelled terms do not mention CPU: %v", m.Unmodelled)
	}
}
