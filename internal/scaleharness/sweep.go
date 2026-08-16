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

// ProvisionFunc makes n workspaces available and returns a client scoped to
// each.
//
// A function rather than an interface, and a seam rather than an
// implementation detail: provisioning needs a real kcp server, so it lives in
// the integration tests, while everything that reasons about the resulting
// numbers lives here and is unit-testable against fakes. Without the seam the
// sweep logic would only be exercisable in an environment that can run kcp,
// which is precisely the coupling that makes measurement code rot.
type ProvisionFunc func(ctx context.Context, workspaces int) ([]client.Client, error)

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

	// Knee parameterises detection. Zero values take the documented defaults.
	Knee KneeOptions

	// Metric selects the measured quantity the knee is taken over. Nil means
	// heap bytes, which is the term that bounds how many workspaces a shard
	// holds.
	Metric func(Measurement) float64
}

// Sweep measures a service across a range of workspace counts and derives the
// knee from the result.
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
		Service: opts.Service.Name(),
		Profile: opts.Profile,
		Mode:    opts.Mode,
	}

	for _, n := range counts {
		m, err := measurePoint(ctx, opts, n)
		if err != nil {
			// Returned rather than recorded and skipped: a point that could
			// not be measured leaves a gap the trend is projected across, and
			// a knee derived from a sweep with holes in it is not reproducible.
			return SweepRun{}, fmt.Errorf("measuring %d workspaces: %w", n, err)
		}
		run.Measurements = append(run.Measurements, m)
		run.Points = append(run.Points, Point{Workspaces: n, Value: metric(m)})
	}

	run.Knee = DetectKnee(run.Points, opts.Knee)
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

	for i, c := range clients {
		if err := opts.Service.Populate(ctx, c, opts.Profile.ObjectsPerWorkspace); err != nil {
			return Measurement{}, fmt.Errorf("populating workspace %d: %w", i, err)
		}
	}

	start := time.Now()
	events, err := driveEvents(ctx, opts, clients)
	if err != nil {
		return Measurement{}, err
	}
	loadDuration := time.Since(start)

	heap, goroutines := sample()
	return Measurement{
		Workspaces:   workspaces,
		HeapBytes:    heap,
		Goroutines:   goroutines,
		LoadDuration: loadDuration,
		Events:       events,
	}, nil
}

// driveEvents applies the profile's declared event rate.
//
// The rate is an input, not an observation: it comes from the profile because
// the dispatch term is quadratic in workspace count and highly sensitive to it,
// so inferring it from whatever the system happened to do would bake an
// unstated workload assumption into a published figure.
func driveEvents(ctx context.Context, opts SweepOptions, clients []client.Client) (int, error) {
	if opts.Profile.EventsPerWorkspacePerSecond <= 0 {
		return 0, nil
	}

	// One round per workspace. Holding the rate over wall-clock time would
	// make a point's cost depend on how long the harness chose to run, which
	// is not a property of the shard.
	perWorkspace := int(math.Max(1, opts.Profile.EventsPerWorkspacePerSecond))

	var events int
	for i, c := range clients {
		for range perWorkspace {
			if err := opts.Service.Touch(ctx, c); err != nil {
				return 0, fmt.Errorf("generating an event in workspace %d: %w", i, err)
			}
			events++
		}
	}
	return events, nil
}

// sample reads live heap and goroutine count.
//
// GC runs first so the figure is what the process is holding rather than what
// it has not yet collected. Without it, a later point looks larger simply for
// having allocated more garbage since the last cycle, which would manufacture a
// knee out of collector timing.
func sample() (heapBytes uint64, goroutines int) {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc, runtime.NumGoroutine()
}
