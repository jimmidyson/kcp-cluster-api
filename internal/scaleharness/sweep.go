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
	"fmt"
	"math"
	"runtime"
	"slices"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Workspace is one provisioned workspace: its identity, and a client scoped to
// it.
//
// The name is not decoration. Delivery latency is attributed per workspace, and
// the controller that reports a delivery knows only which workspace it belongs
// to — so without an identity travelling alongside the client, a measurement
// could not be matched to the mutation that caused it.
type Workspace struct {
	Name   string
	Client client.Client
}

// ProvisionFunc makes n workspaces available.
//
// A function rather than an interface, and a seam rather than an
// implementation detail: provisioning needs a real kcp server, so it lives in
// the integration tests, while everything that reasons about the resulting
// numbers lives here and is unit-testable against fakes. Without the seam the
// sweep logic would only be exercisable in an environment that can run kcp,
// which is precisely the coupling that makes measurement code rot.
type ProvisionFunc func(ctx context.Context, workspaces int) ([]Workspace, error)

// Measurement is what one swept point cost.
//
// Heap rather than resident size, deliberately (FR-036): Go returns memory to
// the OS lazily and fragments, so resident size is not a clean function of what
// the process is holding. Live heap is the quantity that scales with
// workspaces; converting it to a resident-size budget is a separate step with
// its own stated multiplier.
type Measurement struct {
	Workspaces   int           `json:"workspaces"`
	HeapBytes    uint64        `json:"heapBytes"`
	Goroutines   int           `json:"goroutines"`
	LoadDuration time.Duration `json:"loadDuration"`
	Events       int           `json:"events"`

	// DeliveryP50 and DeliveryP99 are how long an event took to travel from
	// the write to the controller that wanted it.
	//
	// This, not LoadDuration, is where a fan-out cost appears. A write returns
	// once the API server accepts it, so the dispatch through every registered
	// listener happens after the writer has stopped looking.
	DeliveryP50 time.Duration `json:"deliveryP50,omitempty"`
	DeliveryP99 time.Duration `json:"deliveryP99,omitempty"`

	// DeliveriesMissed counts events that never reached a controller within
	// the timeout. Non-zero means dispatch is not keeping up, which is a
	// finding in itself and makes the percentiles above a lower bound.
	DeliveriesMissed int `json:"deliveriesMissed,omitempty"`
}

// SweepOptions parameterises one sweep.
type SweepOptions struct {
	// Service supplies the service-specific half: what objects to make, and
	// how to generate an event.
	Service Service

	// Profile is the shard shape being measured.
	Profile Profile

	// Workspaces are the counts to sweep, geometrically spaced.
	Workspaces []int

	// Provision makes the workspaces for a point.
	Provision ProvisionFunc

	// Mode records how the load was produced, and travels with every figure
	// the run yields.
	Mode LoadMode

	// Departure parameterises detection. Zero values take the documented defaults.
	Departure DepartureOptions

	// Metric selects the measured quantity the departure point is taken over. Nil means
	// heap bytes, which is the term that bounds how many workspaces a shard
	// holds.
	Metric func(Measurement) float64

	// Probe measures event delivery latency. Optional: nil skips it, and the
	// sweep then reports footprint without the dispatch cost — which is a
	// materially weaker measurement, since dispatch is where fleet size shows
	// up first.
	Probe *DeliveryProbe

	// DeliveryTimeout bounds how long a point waits for its events to arrive.
	// Zero means DefaultDeliveryTimeout.
	DeliveryTimeout time.Duration

	// ListenersPerWorkspace is how many watches the caller's wiring registers
	// per workspace. Recorded on the run rather than used by it: the harness
	// does not register watches, but every coefficient it produces depends on
	// how many the caller did.
	ListenersPerWorkspace int
}

// DefaultDeliveryTimeout bounds the wait for events to reach controllers.
//
// Generous on purpose: exceeding it is reported as missed deliveries rather
// than as a failure, so the cost of setting it high is a slow run, while the
// cost of setting it low is mistaking a slow fleet for a broken one.
const DefaultDeliveryTimeout = 60 * time.Second

// Sweep measures a service across a range of workspace counts and derives the
// departure point from the result.
//
// It does not decide anything. It produces the evidence a capacity figure is
// set from, and reports honestly when the evidence is insufficient — a sweep
// too short to establish a trend yields could-not-run rather than a
// reassuring absence of one.
func Sweep(ctx context.Context, opts SweepOptions) (SweepRun, error) {
	if opts.Service == nil {
		return SweepRun{}, fmt.Errorf("sweep needs a service: the harness cannot invent objects for one it does not know")
	}
	if err := opts.Profile.Validate(); err != nil {
		return SweepRun{}, fmt.Errorf("invalid profile: %w", err)
	}
	if opts.Provision == nil {
		return SweepRun{}, fmt.Errorf("sweep needs a way to provision workspaces")
	}

	metric := opts.Metric
	if metric == nil {
		metric = func(m Measurement) float64 { return float64(m.HeapBytes) }
	}

	counts := slices.Clone(opts.Workspaces)
	slices.Sort(counts)

	run := SweepRun{
		Service:               opts.Service.Name(),
		Profile:               opts.Profile,
		Mode:                  opts.Mode,
		ListenersPerWorkspace: opts.ListenersPerWorkspace,
	}

	for _, n := range counts {
		m, err := measurePoint(ctx, opts, n)
		if err != nil {
			// Returned rather than recorded and skipped: a point that could
			// not be measured leaves a gap the trend is projected across, and
			// a departure point derived from a sweep with holes in it is not reproducible.
			return SweepRun{}, fmt.Errorf("measuring %d workspaces: %w", n, err)
		}
		run.Measurements = append(run.Measurements, m)
		run.Points = append(run.Points, Point{Workspaces: n, Value: metric(m)})
	}

	run.Departure = FindDeparture(run.Points, opts.Departure)
	return run, nil
}

func measurePoint(ctx context.Context, opts SweepOptions, workspaces int) (Measurement, error) {
	clients, err := opts.Provision(ctx, workspaces)
	if err != nil {
		return Measurement{}, fmt.Errorf("provisioning: %w", err)
	}
	if len(clients) != workspaces {
		return Measurement{}, fmt.Errorf("provisioned %d workspaces, asked for %d", len(clients), workspaces)
	}

	for i, ws := range clients {
		if err := opts.Service.Populate(ctx, ws.Client, opts.Profile.ObjectsPerWorkspace); err != nil {
			return Measurement{}, fmt.Errorf("populating workspace %d: %w", i, err)
		}
	}

	// Reset before driving, so a point measures only its own events and not
	// stragglers from the point before it.
	if opts.Probe != nil {
		opts.Probe.Reset()
	}

	start := time.Now()
	events, timed, err := driveEvents(ctx, opts, clients)
	if err != nil {
		return Measurement{}, err
	}
	loadDuration := time.Since(start)

	var p50, p99 time.Duration
	var missed int
	if opts.Probe != nil && timed > 0 {
		timeout := opts.DeliveryTimeout
		if timeout <= 0 {
			timeout = DefaultDeliveryTimeout
		}
		latencies, err := opts.Probe.Await(ctx, timeout)
		if err != nil {
			// Not fatal. Undelivered events are a result — dispatch falling
			// behind is precisely the behaviour being hunted — so they are
			// recorded and the percentiles are reported as the lower bound
			// they now are.
			//
			// Counted against timed sends rather than against every event
			// issued: an event the probe declined to time was never expected
			// to arrive, and counting it here would report a shortfall the
			// system did not have.
			missed = timed - len(latencies)
		}
		p50, p99 = Percentile(latencies, 0.50), Percentile(latencies, 0.99)
	}

	heap, goroutines := sample()
	return Measurement{
		Workspaces:       workspaces,
		HeapBytes:        heap,
		Goroutines:       goroutines,
		LoadDuration:     loadDuration,
		Events:           events,
		DeliveryP50:      p50,
		DeliveryP99:      p99,
		DeliveriesMissed: missed,
	}, nil
}

// driveEvents applies the profile's declared event rate.
//
// The rate is an input, not an observation: it comes from the profile because
// the dispatch term is quadratic in workspace count and highly sensitive to it,
// so inferring it from whatever the system happened to do would bake an
// unstated workload assumption into a published figure.
//
// It returns both how many events were issued and how many of them the probe
// accepted for timing. The two differ when a profile issues several events per
// workspace faster than they are delivered, since latency is attributed by
// workspace and only one event per workspace can be in flight at a time.
func driveEvents(ctx context.Context, opts SweepOptions, clients []Workspace) (events, timed int, err error) {
	if opts.Profile.EventsPerWorkspacePerSecond <= 0 {
		return 0, 0, nil
	}

	// One round per workspace. Holding the rate over wall-clock time would
	// make a point's cost depend on how long the harness chose to run, which
	// is not a property of the shard.
	perWorkspace := int(math.Max(1, opts.Profile.EventsPerWorkspacePerSecond))

	for i, ws := range clients {
		for range perWorkspace {
			// Sent before the mutation, never after: the clock has to start
			// before the event exists, or a fast delivery could be recorded
			// against a start time that had not been taken yet.
			if opts.Probe != nil && opts.Probe.Sent(ws.Name) {
				timed++
			}
			if err := opts.Service.Touch(ctx, ws.Client); err != nil {
				return 0, 0, fmt.Errorf("generating an event in workspace %d: %w", i, err)
			}
			events++
		}
	}
	return events, timed, nil
}

// sample reads live heap and goroutine count.
//
// GC runs first so the figure is what the process is holding rather than what
// it has not yet collected. Without it, a later point looks larger simply for
// having allocated more garbage since the last cycle, which would manufacture a
// departure point out of collector timing.
func sample() (heapBytes uint64, goroutines int) {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc, runtime.NumGoroutine()
}
