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

package upstreamscale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// fakeTarget is a side of the comparison with no cluster behind it, which is
// what lets the run loop itself be tested — it never was, being reachable only
// through an integration test that needs a real cluster and forty minutes.
type fakeTarget struct {
	name   string
	tenant string

	// failAt is the cluster count whose rung never converges, zero for none.
	failAt int
	// createErrAt is the cluster count whose creation half-fails.
	createErrAt int

	created  []string
	planned  []int
	tornDown []string
}

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Title(startClusters, nodes int) string {
	return fmt.Sprintf("%s: climbing from %d clusters at %d nodes each", f.name, startClusters, nodes)
}

func (f *fakeTarget) Facts() map[string]string {
	return map[string]string{"tenancy": f.tenant, "side": f.name}
}

func (f *fakeTarget) Prepare(context.Context) error { return nil }
func (f *fakeTarget) Controllers() []Controller     { return nil }
func (f *fakeTarget) Store() StoreLocation          { return StoreLocation{Namespace: "nowhere"} }

func (f *fakeTarget) ControlPlane(context.Context, int, time.Duration,
) ([]deployedscale.ComponentSample, string, error) {
	return []deployedscale.ComponentSample{{Component: f.name + "-control-plane"}}, "a control plane", nil
}

func (f *fakeTarget) Plan(clusters int) (Fleet, error) {
	f.planned = append(f.planned, clusters)
	return PlanFleet(FleetShape{
		Clusters: clusters, ClustersPerNamespace: 10, ControlPlaneMachines: 1,
	}), nil
}

func (f *fakeTarget) Create(_ context.Context, fleet Fleet, _ int) ([]string, error) {
	var made []string
	for _, ns := range fleet.Namespaces {
		made = append(made, ns.Name)
	}
	f.created = append(f.created, made...)
	if f.createErrAt != 0 && fleet.Shape.Clusters == f.createErrAt {
		// Half built: some tenants exist and the rung failed, which is the
		// case teardown must still cover.
		return made, errors.New("the infrastructure said no")
	}
	return made, nil
}

func (f *fakeTarget) Converged(_ context.Context, wantClusters, wantMachines int) (Convergence, error) {
	if f.failAt != 0 && wantClusters == f.failAt {
		return Convergence{ControlPlanesWant: wantClusters, MachinesWant: wantMachines}, nil
	}
	return Convergence{
		ControlPlanesReady: wantClusters, ControlPlanesWant: wantClusters,
		MachinesReady: wantMachines, MachinesWant: wantMachines,
		Done: true,
	}, nil
}

func (f *fakeTarget) Teardown(_ context.Context, created []string, _, _ time.Duration,
	_ func(string, ...any),
) error {
	f.tornDown = append(f.tornDown, created...)
	return nil
}

func testRunner(t *testing.T, target Target, start, max int) *Runner {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return &Runner{
		Target:       target,
		Host:         fake.NewClientBuilder().WithScheme(s).Build(),
		Sampler:      &Sampler{},
		Defragmenter: NewDefragmenter(nil, nil),
		Options: RunOptions{
			StartClusters: start, MaxClusters: max, NodesPerCluster: 1,
			CreateConcurrency: 4,
			SettleTolerance:   0.02,
			SettleTimeout:     time.Millisecond,
			StepTimeout:       10 * time.Millisecond,
			PollInterval:      time.Millisecond,
			TeardownTimeout:   time.Second,
		},
	}
}

// TestTheClimbStopsAtTheFirstRungThatDoesNotConverge, and does not go on to
// build the one above it. A ladder that kept climbing past a failure would be
// measuring a cluster that had already given up.
func TestTheClimbStopsAtTheFirstRungThatDoesNotConverge(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", failAt: 4}
	runner := testRunner(t, target, 2, 8)

	report, ceiling, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("a climb that reached a rung is not an error: %v", err)
	}
	if ceiling.LastGood == nil || ceiling.LastGood.Clusters != 2 {
		t.Fatalf("last good rung = %+v, want 2 clusters", ceiling.LastGood)
	}
	if ceiling.Failed == nil || ceiling.Failed.Clusters != 4 {
		t.Fatalf("failed rung = %+v, want 4 clusters", ceiling.Failed)
	}
	// The rung above the failure was never planned, let alone created.
	for _, planned := range target.planned {
		if planned == 8 {
			t.Error("the climb went on past a rung that did not converge")
		}
	}
	if d := ceiling.Describe(); !strings.Contains(d, "did not") {
		t.Errorf("the ceiling sentence does not name the failure: %q", d)
	}
	// And the report says what happened at both rungs.
	if _, ok := report.Facts["rung@2"]; !ok {
		t.Error("no timing for the rung that converged")
	}
	if _, ok := report.Facts["rung@4"]; !ok {
		t.Error("no timing for the rung that did not")
	}
}

// TestAHalfBuiltRungIsStillTornDown. Create returns what it made even when it
// fails, because the alternative is a rung's worth of tenants left on a cluster
// whose next run would measure them as its baseline.
func TestAHalfBuiltRungIsStillTornDown(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", createErrAt: 2}
	runner := testRunner(t, target, 2, 2)

	if _, _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("a run whose only rung could not be created reported success")
	}
	if len(runner.Created) == 0 {
		t.Fatal("the runner recorded nothing to tear down")
	}
	if err := runner.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(target.tornDown) != len(runner.Created) {
		t.Errorf("tore down %d of %d tenants", len(target.tornDown), len(runner.Created))
	}
}

// TestARunThatMeasuredNothingIsAnError, while one that found a ceiling is a
// result whichever rung it stopped at. That distinction is what the whole
// harness is built around, so it is checked rather than assumed.
func TestARunThatMeasuredNothingIsAnError(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace", failAt: 2}
	runner := testRunner(t, target, 2, 4)

	report, ceiling, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("a climb whose first rung failed reported success")
	}
	if ceiling.LastGood != nil {
		t.Errorf("something was reported as measured: %+v", ceiling.LastGood)
	}
	// The report still exists, because a failed climb's samples are how
	// anybody works out why.
	if report == nil || len(report.Samples) == 0 {
		t.Error("a failed climb produced no samples at all")
	}
}

// TestBothSidesProduceTheSameShapeOfReport.
//
// This is the comparison, in code. Two targets differing in everything a
// Target is allowed to differ in — the name, the tenancy unit, what the control
// plane is called — climbed by the same Runner must produce reports whose facts
// line up, or a reader diffing two runs is reading two different instruments
// again, which is the thing this refactor exists to stop.
func TestBothSidesProduceTheSameShapeOfReport(t *testing.T) {
	stock := &fakeTarget{name: "stock", tenant: "Namespace"}
	kcp := &fakeTarget{name: "kcp", tenant: "Workspace"}

	stockReport, _, err := testRunner(t, stock, 2, 4).Run(context.Background())
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	kcpReport, _, err := testRunner(t, kcp, 2, 4).Run(context.Background())
	if err != nil {
		t.Fatalf("kcp: %v", err)
	}

	for key := range stockReport.Facts {
		if _, ok := kcpReport.Facts[key]; !ok {
			t.Errorf("the stock report has %q and the kcp one does not", key)
		}
	}
	for key := range kcpReport.Facts {
		if _, ok := stockReport.Facts[key]; !ok {
			t.Errorf("the kcp report has %q and the stock one does not", key)
		}
	}

	if len(stockReport.Samples) != len(kcpReport.Samples) {
		t.Errorf("%d samples against %d: the two sides were not asked the same questions",
			len(stockReport.Samples), len(kcpReport.Samples))
	}
	for i := range stockReport.Samples {
		if a, b := stockReport.Samples[i].Label, kcpReport.Samples[i].Label; a != b {
			t.Errorf("sample %d is %q on one side and %q on the other", i, a, b)
		}
	}

	// And each still says which side it is, or two comparable reports are
	// indistinguishable.
	if stockReport.Facts["side"] == kcpReport.Facts["side"] {
		t.Error("the two reports do not say which side they are")
	}
}

// TestTheBaselineIsSampledBeforeAnythingIsCreated, because every slope a run
// reports is a difference against it.
func TestTheBaselineIsSampledBeforeAnythingIsCreated(t *testing.T) {
	target := &fakeTarget{name: "stock", tenant: "Namespace"}
	runner := testRunner(t, target, 2, 2)

	report, _, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(report.Samples) == 0 {
		t.Fatal("no samples")
	}
	first := report.Samples[0]
	if first.Clusters != 0 || !strings.Contains(first.Label, "baseline") {
		t.Errorf("the first sample is %q with %d clusters, want an empty baseline",
			first.Label, first.Clusters)
	}
}
