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

package deployedscale

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
)

// DefaultTolerance is how far a deployed figure may sit from the in-process
// one before the run is treated as a disagreement rather than as noise.
//
// Twenty percent, and it is a judgement rather than a measurement. The two
// instruments run the same Go code doing the same work, so a large divergence
// means one of them is wrong; but they do not run it identically — a deployed
// manager talks to kcp over a network, holds a connection pool sized for it,
// and is sampled by scraping rather than by reading its own runtime — and a
// tolerance tight enough to catch nothing but real disagreement would fail on
// those differences instead.
const DefaultTolerance = 0.20

// Reconciliation is one quantity measured both ways.
//
// # Why a second instrument has to be checked against the first
//
// This repository has already decided that two instruments measuring one
// process is worse than one instrument being wrong, because a disagreement
// between two measurements leaves neither side obviously at fault:
// internal/scaleharness stopped measuring for exactly that reason.
//
// A deployed run is a second instrument, so it is only admissible with a check
// that keeps it honest. The check is that the quantities both instruments can
// see — the Go runtime's own — must agree for the same fleet shape, because it
// is the same program doing the same work. Where they do not, the run is a
// finding about one of the instruments rather than a figure about the fleet.
type Reconciliation struct {
	Quantity string `json:"quantity"`
	// Component the figures belong to. The comparison is per deployment, and
	// comparing a deployed core-manager with an in-process run of all four
	// providers would be comparing different workloads.
	Component string `json:"component"`
	// Deployed and InProcess are per-workspace figures.
	Deployed  float64 `json:"deployed"`
	InProcess float64 `json:"inProcess"`
	// Ratio is deployed over in-process. One is perfect agreement.
	Ratio           float64 `json:"ratio"`
	Tolerance       float64 `json:"tolerance"`
	WithinTolerance bool    `json:"withinTolerance"`
	// Source names the committed in-process run this was checked against, so
	// the comparison is re-derivable rather than quoted.
	Source string `json:"source"`
	// Comparable is false when the two instruments did not measure the same
	// work, in which case the ratio is worth recording and is not a finding
	// about either of them. Why sets out which case this is.
	Comparable bool `json:"comparable"`
	// Why explains an incomparable pairing, in the report and to a reader who
	// finds the ratio surprising.
	Why string `json:"why,omitempty"`
}

// Incomparable marks a comparison between two instruments that did not measure
// the same work.
//
// # Why this exists rather than a wider tolerance
//
// The in-process sweeps stop at engagement: every workspace bound and holding
// its objects. A deployed run of all four providers goes further and takes
// every cluster to Ready, and a ready cluster costs the core manager a live
// ClusterCache — a connection, informers and their goroutines — for every
// workload cluster, which the reference never paid for.
//
// So the two disagree by construction, reproducibly and by a wide margin: 17.0
// goroutines per workspace deployed against 2.0 in process, the same 17.0 from
// independent runs at 2/4/8 and 3/5/10 workspaces. That is a well-conditioned
// measurement of something the reference is not measuring, and calling it a
// disagreement between instruments would be wrong in a way that widening the
// tolerance would only hide. The number is still reported; what changes is
// that it is not read as a fault.
func Incomparable(rec Reconciliation, why string) Reconciliation {
	rec.Comparable = false
	rec.Why = why
	return rec
}

// Reconcile compares one deployed per-workspace figure with an in-process one.
func Reconcile(quantity, component, source string, deployed, inProcess, tolerance float64) Reconciliation {
	r := Reconciliation{
		Quantity:  quantity,
		Component: component,
		Deployed:  deployed,
		InProcess: inProcess,
		Tolerance: tolerance,
		Source:    source,
	}
	// A zero in-process figure is not agreement and not a ratio; it is a
	// missing reference, and calling it "within tolerance" would let a run
	// with nothing to compare against report itself as reconciled.
	r.Comparable = true
	if inProcess == 0 {
		r.Ratio = 0
		r.WithinTolerance = false
		return r
	}
	r.Ratio = deployed / inProcess
	r.WithinTolerance = math.Abs(r.Ratio-1) <= tolerance
	return r
}

// SweepReference is the per-workspace figure a committed in-process sweep
// recorded, read back out of its JSON report.
//
// Reading the committed artefact rather than re-running the sweep is the point:
// the comparison is against evidence in the repository, so it is re-derivable
// by anyone and does not need a second measurement environment.
type SweepReference struct {
	// Path is the report the figures came from.
	Path string
	// DeploymentName is what the sweep says it measured, checked against the
	// component being compared. A deployed core-manager reconciled against the
	// bootstrap deployment's sweep would produce a confident wrong answer.
	DeploymentName         string
	GoroutinesPerWorkspace float64
	HeapBytesPerWorkspace  float64
}

// LoadSweepReference reads a committed sweep report.
func LoadSweepReference(path string) (SweepReference, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path from a flag, naming committed evidence.
	if err != nil {
		return SweepReference{}, fmt.Errorf("reading the in-process reference: %w", err)
	}

	var report struct {
		Facts map[string]string `json:"facts"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return SweepReference{}, fmt.Errorf("decoding %s: %w", path, err)
	}

	ref := SweepReference{Path: path, DeploymentName: report.Facts["deploymentName"]}

	// A fact that is absent, or that the sweep itself recorded as not
	// measured, is not a reference. The in-process instrument writes prose
	// there when a fit did not resolve, so parsing must fail rather than
	// coerce it to zero.
	goroutines, err := parseFact(report.Facts, "goroutinesPerWorkspace")
	if err != nil {
		return SweepReference{}, fmt.Errorf("%s: %w", path, err)
	}
	ref.GoroutinesPerWorkspace = goroutines

	// Heap is optional: the in-process instrument legitimately reports it as
	// not measured, and a missing heap reference should not make the
	// goroutine comparison unavailable too.
	if heap, err := parseFact(report.Facts, "heapBytesPerWorkspace"); err == nil {
		ref.HeapBytesPerWorkspace = heap
	}

	return ref, nil
}

func parseFact(facts map[string]string, key string) (float64, error) {
	raw, ok := facts[key]
	if !ok {
		return 0, fmt.Errorf("%s is not recorded", key)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is %q rather than a number, so there is nothing to reconcile against", key, raw)
	}
	return value, nil
}
