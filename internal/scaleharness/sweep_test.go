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
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fakeProvision(_ context.Context, workspaces int) ([]client.Client, error) {
	out := make([]client.Client, workspaces)
	for i := range out {
		out[i] = fake.NewClientBuilder().Build()
	}
	return out, nil
}

func sweepOpts(points ...int) SweepOptions {
	return SweepOptions{
		Service:    configMapService{prefix: "sweep"},
		Profile:    ActiveHeavy(),
		Workspaces: points,
		Provision:  fakeProvision,
		Mode:       ModeSynthetic,
	}
}

func TestSweepMeasuresEveryRequestedPoint(t *testing.T) {
	run, err := Sweep(t.Context(), sweepOpts(1, 2, 4, 8))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(run.Measurements) != 4 {
		t.Fatalf("measured %d points, want 4", len(run.Measurements))
	}
	for i, m := range run.Measurements {
		if m.Workspaces != []int{1, 2, 4, 8}[i] {
			t.Errorf("point %d is for %d workspaces, out of order", i, m.Workspaces)
		}
		if m.Goroutines <= 0 {
			t.Errorf("point %d recorded %d goroutines", i, m.Goroutines)
		}
		if m.HeapBytes == 0 {
			t.Errorf("point %d recorded no heap", i)
		}
	}
}

func TestSweepPopulatesAndDrivesEachWorkspace(t *testing.T) {
	profile := ActiveHeavy()
	svc := &countingService{}

	opts := sweepOpts(2, 4)
	opts.Service = svc
	opts.Profile = profile

	if _, err := Sweep(t.Context(), opts); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Every workspace at every point must be populated, or the shape measured
	// is not the profile that was asked for.
	if want := 2 + 4; svc.populated != want {
		t.Errorf("populated %d workspaces, want %d", svc.populated, want)
	}
	if svc.objects != (2+4)*profile.ObjectsPerWorkspace {
		t.Errorf("created %d objects, want %d", svc.objects, (2+4)*profile.ObjectsPerWorkspace)
	}
	if svc.touched == 0 {
		t.Error("no events were generated; a profile declaring an event rate measured none")
	}
}

// An idle profile declares no events. Driving them anyway would measure the
// active shape while reporting the idle one — and the idle figure is what
// bounds how many workspaces a shard holds.
func TestIdleProfileGeneratesNoEvents(t *testing.T) {
	svc := &countingService{}
	opts := sweepOpts(2)
	opts.Service = svc
	opts.Profile = IdleHeavy()

	if _, err := Sweep(t.Context(), opts); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if svc.touched != 0 {
		t.Errorf("generated %d events for the idle profile, want 0", svc.touched)
	}
	if svc.objects != 0 {
		t.Errorf("created %d objects for the idle profile, want 0", svc.objects)
	}
}

func TestSweepRunCarriesModeProfileAndService(t *testing.T) {
	run, err := Sweep(t.Context(), sweepOpts(1, 2, 4, 8))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if err := run.Validate(); err != nil {
		t.Errorf("a completed sweep produced an invalid run: %v", err)
	}
	if run.Service != (configMapService{}).Name() {
		t.Errorf("run names service %q", run.Service)
	}
	if run.Mode != ModeSynthetic {
		t.Errorf("run mode = %q, want synthetic", run.Mode)
	}
}

func TestSweepDerivesADepartureFromItsPoints(t *testing.T) {
	run, err := Sweep(t.Context(), sweepOpts(1, 2, 4, 8))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if len(run.Points) != len(run.Measurements) {
		t.Errorf("derived %d points from %d measurements", len(run.Points), len(run.Measurements))
	}
	if run.Departure.Points != len(run.Points) {
		t.Errorf("departure result recorded %d points, sweep had %d", run.Departure.Points, len(run.Points))
	}
	if run.Departure.Tolerance <= 0 {
		t.Error("departure result records no tolerance")
	}
}

// Too short a sweep must not silently yield a linear verdict — that would read
// as headroom that was never measured.
func TestShortSweepReportsCouldNotRun(t *testing.T) {
	run, err := Sweep(t.Context(), sweepOpts(1, 2))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if !run.Departure.CouldNotRun {
		t.Error("a two-point sweep did not report could-not-run")
	}
	if run.Outcome().String() != "could not run" {
		t.Errorf("outcome = %q, want could not run", run.Outcome().String())
	}
}

func TestSweepRejectsAnIncoherentProfile(t *testing.T) {
	opts := sweepOpts(1, 2, 4, 8)
	opts.Profile = Profile{Name: "", ObjectsPerWorkspace: -1}

	if _, err := Sweep(t.Context(), opts); err == nil {
		t.Error("Sweep accepted an invalid profile")
	}
}

func TestProvisioningFailureIsReportedNotSwallowed(t *testing.T) {
	opts := sweepOpts(1, 2, 4, 8)
	opts.Provision = func(context.Context, int) ([]client.Client, error) {
		return nil, errors.New("no workspaces today")
	}

	if _, err := Sweep(t.Context(), opts); err == nil {
		t.Error("Sweep hid a provisioning failure; a sweep that could not provision has measured nothing")
	}
}

// countingService records what the sweep asked of it.
type countingService struct {
	populated int
	objects   int
	touched   int
}

func (s *countingService) Name() string           { return "counting" }
func (s *countingService) WatchedTypes() []string { return []string{"v1/ConfigMap"} }

func (s *countingService) Populate(_ context.Context, _ client.Client, objects int) error {
	s.populated++
	s.objects += objects
	return nil
}

func (s *countingService) Touch(context.Context, client.Client) error {
	s.touched++
	return nil
}
