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
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The departure point is where capacity comes from, so it has to be a procedure rather
// than a judgement: same points and same tolerance must give the same answer
// to anyone who runs it.

func TestDepartureFoundWhereCostDepartsFromLinear(t *testing.T) {
	// Linear through 8/16/32, then a sharp departure at 64.
	points := []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 320},
		{Workspaces: 64, Value: 1280}, // 2x the linear projection of 640
	}

	got := FindDeparture(points, DepartureOptions{Tolerance: 0.25})

	if !got.Found {
		t.Fatalf("no departure point found in an obviously super-linear series: %s", got.Reason)
	}
	if got.Workspaces != 64 {
		t.Errorf("departure point at %d workspaces, want 64", got.Workspaces)
	}
}

func TestNoDepartureWhenCostStaysLinear(t *testing.T) {
	points := []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 321},
		{Workspaces: 64, Value: 639},
	}

	got := FindDeparture(points, DepartureOptions{Tolerance: 0.25})

	if got.Found {
		t.Errorf("departure point reported at %d for a linear series; a spurious departure point understates capacity", got.Workspaces)
	}
	if got.Reason == "" {
		t.Error("no reason recorded for the absent departure point: a bare false is not a result somebody can act on")
	}
}

// FR-030's whole point: two runs of one profile must agree.
func TestDepartureIsReproducible(t *testing.T) {
	points := []Point{
		{Workspaces: 4, Value: 40},
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 200},
		{Workspaces: 32, Value: 900},
	}
	opts := DepartureOptions{Tolerance: 0.2}

	first := FindDeparture(points, opts)
	for range 10 {
		if again := FindDeparture(points, opts); again != first {
			t.Fatalf("FindDeparture is not deterministic: %+v then %+v", first, again)
		}
	}
}

// A sweep too short to project a trend has not measured anything. Reporting
// "no departure point" there would be indistinguishable from "cost is linear", which is
// the false negative that would silently overstate capacity.
func TestTooFewPointsCannotRun(t *testing.T) {
	for _, points := range [][]Point{
		nil,
		{{Workspaces: 8, Value: 80}},
		{{Workspaces: 8, Value: 80}, {Workspaces: 16, Value: 160}},
		{{Workspaces: 8, Value: 80}, {Workspaces: 16, Value: 160}, {Workspaces: 32, Value: 320}},
	} {
		got := FindDeparture(points, DepartureOptions{Tolerance: 0.25, MinPoints: 4})
		if got.Found {
			t.Errorf("%d points produced a departure point; too short a sweep cannot establish one", len(points))
		}
		if !got.CouldNotRun {
			t.Errorf("%d points: want CouldNotRun, got a plain not-found — an unrunnable sweep is not a measurement of linearity", len(points))
		}
	}
}

// The tolerance and the point count are part of the answer, not settings that
// produced it: a departure point quoted without them cannot be reproduced or compared.
func TestResultCarriesItsParameters(t *testing.T) {
	points := []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 320},
		{Workspaces: 64, Value: 1280},
	}

	got := FindDeparture(points, DepartureOptions{Tolerance: 0.25})

	if got.Tolerance != 0.25 {
		t.Errorf("Tolerance = %v, want 0.25", got.Tolerance)
	}
	if got.Points != 4 {
		t.Errorf("Points = %d, want 4", got.Points)
	}
}

func TestPointsNeedNotBeSuppliedInOrder(t *testing.T) {
	ordered := []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 320},
		{Workspaces: 64, Value: 1280},
	}
	shuffled := []Point{ordered[2], ordered[0], ordered[3], ordered[1]}

	if got, want := FindDeparture(shuffled, DepartureOptions{Tolerance: 0.25}), FindDeparture(ordered, DepartureOptions{Tolerance: 0.25}); got != want {
		t.Errorf("order changed the answer: %+v vs %+v", got, want)
	}
}

// A tolerance of zero would make measurement noise look like a departure point, so it is
// defaulted rather than taken literally.
func TestZeroToleranceIsDefaulted(t *testing.T) {
	points := []Point{
		{Workspaces: 8, Value: 80},
		{Workspaces: 16, Value: 160},
		{Workspaces: 32, Value: 320.5}, // noise, not a departure point
		{Workspaces: 64, Value: 641},
	}

	got := FindDeparture(points, DepartureOptions{})

	if got.Found {
		t.Errorf("departure point at %d from rounding noise; a zero tolerance must not be taken literally", got.Workspaces)
	}
	if got.Tolerance <= 0 {
		t.Errorf("Tolerance = %v, want the default to be recorded", got.Tolerance)
	}
}

// Property S1 of contracts/service-characterisation.md: the machinery that
// sweeps, fits and detects must carry no service knowledge, or characterising
// the next controller means rewriting it rather than supplying an
// implementation. Asserted rather than asserted-about, because "we intend to
// keep this generic" decays silently.
//
// Two directories, because the machinery lives in two packages: this one is the
// live instrument, and internal/scaleharness fits models to what it recorded.
// Scanning only the directory the test happens to sit in would leave half the
// property unguarded, which is how an assertion like this stops meaning
// anything.
func TestAgnosticMachineryImportsNoServiceSpecifics(t *testing.T) {
	for _, dir := range []string{".", filepath.Join("..", "scaleharness")} {
		assertNoServiceImports(t, dir)
	}
}

func assertNoServiceImports(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			imported := strings.Trim(imp.Path.Value, `"`)
			// Matched on the upstream module path, not on the substring
			// "cluster-api": this repository is itself kcp-cluster-api, so a
			// substring test flags every internal import and the check becomes
			// noise that gets deleted. Cluster API is reached as
			// sigs.k8s.io/cluster-api even through the fork's replace
			// directive, so this is the path that matters.
			if strings.HasPrefix(imported, "sigs.k8s.io/cluster-api") {
				t.Errorf("%s imports %q: the service-agnostic harness must not know about a particular service", path, imported)
			}
		}
	}
}
