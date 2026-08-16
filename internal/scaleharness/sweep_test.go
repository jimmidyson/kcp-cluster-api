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
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fakeProvision(_ context.Context, workspaces int) ([]Workspace, error) {
	out := make([]Workspace, workspaces)
	for i := range out {
		out[i] = Workspace{
			Name:   fmt.Sprintf("ws-%d", i),
			Client: fake.NewClientBuilder().Build(),
		}
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
	opts.Provision = func(context.Context, int) ([]Workspace, error) {
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

// Delivery latency is the measurement that answers FR-001, so a sweep given a
// probe must actually populate it — and one whose events never arrive must say
// so rather than report a flattering silence.
func TestSweepRecordsDeliveryLatency(t *testing.T) {
	probe := NewDeliveryProbe()
	opts := sweepOpts(1, 2, 4, 8)
	opts.Probe = probe
	opts.DeliveryTimeout = 2 * time.Second

	// Stand in for the controllers: acknowledge every event as it is sent.
	opts.Service = &acknowledgingService{probe: probe}

	run, err := Sweep(t.Context(), opts)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, m := range run.Measurements {
		if m.Events == 0 {
			t.Fatalf("point %d generated no events", m.Workspaces)
		}
		if m.DeliveriesMissed != 0 {
			t.Errorf("point %d missed %d deliveries", m.Workspaces, m.DeliveriesMissed)
		}
		if m.DeliveryP99 < m.DeliveryP50 {
			t.Errorf("point %d has p99 %s below p50 %s", m.Workspaces, m.DeliveryP99, m.DeliveryP50)
		}
	}
}

func TestUndeliveredEventsAreRecordedNotHidden(t *testing.T) {
	opts := sweepOpts(1, 2, 4, 8)
	opts.Probe = NewDeliveryProbe()
	opts.DeliveryTimeout = 50 * time.Millisecond
	// No acknowledgement at all: every event goes missing.

	run, err := Sweep(t.Context(), opts)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, m := range run.Measurements {
		if m.DeliveriesMissed != m.Events {
			t.Errorf("point %d recorded %d missed of %d events; undelivered events must be visible",
				m.Workspaces, m.DeliveriesMissed, m.Events)
		}
	}
}

// A profile issuing several events per workspace generates more events than the
// probe can time, because latency is attributed by workspace and only one event
// per workspace is in flight at a time. The shortfall must be counted against
// what was timed, not against what was issued — otherwise a fleet delivering
// everything it was asked to would be reported as dropping half of it, which is
// precisely the false alarm that would send someone hunting a dispatch bug that
// is not there.
func TestUntimedEventsAreNotCountedAsMissed(t *testing.T) {
	probe := NewDeliveryProbe()
	opts := sweepOpts(1, 2, 4, 8)
	opts.Probe = probe
	opts.DeliveryTimeout = 2 * time.Second
	opts.Profile.EventsPerWorkspacePerSecond = 4
	opts.Service = &acknowledgingService{probe: probe}

	run, err := Sweep(t.Context(), opts)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, m := range run.Measurements {
		if m.Events != m.Workspaces*4 {
			t.Fatalf("point %d issued %d events, want %d", m.Workspaces, m.Events, m.Workspaces*4)
		}
		if m.DeliveriesMissed != 0 {
			t.Errorf("point %d reported %d missed deliveries though every timed event was acknowledged",
				m.Workspaces, m.DeliveriesMissed)
		}
	}
}

// A second event for a workspace already in flight must not restart its clock:
// the delivery that eventually arrives belongs to the first event, and timing it
// from the second would report a latency shorter than any event actually took.
func TestSendIsDeclinedWhileOneIsInFlight(t *testing.T) {
	p := NewDeliveryProbe()

	if !p.Sent("ws") {
		t.Fatal("first send was declined")
	}
	if p.Sent("ws") {
		t.Error("a second send was accepted while the first was still in flight")
	}

	p.Delivered("ws")
	if !p.Sent("ws") {
		t.Error("a send was declined after the previous one had been delivered")
	}
}

// A sweep without a probe still measures footprint. It must not fail, but it is
// a weaker measurement and the zero latencies say so.
func TestSweepWithoutAProbeStillMeasuresFootprint(t *testing.T) {
	run, err := Sweep(t.Context(), sweepOpts(1, 2, 4, 8))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, m := range run.Measurements {
		if m.DeliveryP50 != 0 || m.DeliveryP99 != 0 {
			t.Errorf("point %d reported latency without a probe", m.Workspaces)
		}
		if m.HeapBytes == 0 {
			t.Errorf("point %d measured no heap", m.Workspaces)
		}
	}
}

// acknowledgingService plays the part of the controllers: every event it is
// asked to generate is reported as delivered.
type acknowledgingService struct {
	configMapService
	probe *DeliveryProbe
}

func (s *acknowledgingService) Touch(ctx context.Context, c client.Client) error {
	if err := s.configMapService.Touch(ctx, c); err != nil {
		return err
	}
	// The probe keys on workspace, and this stand-in has only one outstanding
	// send at a time, so acknowledging the most recent is unambiguous.
	s.probe.DeliverAllOutstanding()
	return nil
}
