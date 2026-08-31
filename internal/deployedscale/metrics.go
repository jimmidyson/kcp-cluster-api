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
	"errors"
	"fmt"
	"io"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Metric names read from a manager's endpoint.
//
// The first two are what internal/sweep samples in-process, and are therefore
// the only quantities by which a deployed run and an in-process run of the
// same fleet can be checked against each other. The rest are what a deployed
// run adds: resident memory, which is what a container limit is set against
// and which no in-process instrument can see, and CPU, which
// `capacity.md` says outright is not modelled at all.
const (
	MetricGoroutines = "go_goroutines"
	MetricHeapAlloc  = "go_memstats_heap_alloc_bytes"
	MetricHeapSys    = "go_memstats_sys_bytes"
	MetricResident   = "process_resident_memory_bytes"
	MetricCPUSeconds = "process_cpu_seconds_total"
)

// ProcessSample is one manager's own view of itself, scraped from its metrics
// endpoint.
//
// # Resident memory, and what it is not
//
// ResidentBytes is the process's resident set, from the process collector
// reading /proc. It is not the container's working set, which is what the OOM
// killer actually accounts and which additionally includes page cache charged
// to the cgroup. For these managers the two are close — a single-process
// container doing almost no file I/O — but they are not the same number, and a
// limit derived from this one is derived from a stated proxy rather than from
// the quantity the kernel kills on. An OOMKill is detected from the pod's own
// status rather than inferred from this, so the distinction costs nothing
// where it would matter most.
type ProcessSample struct {
	Goroutines     int     `json:"goroutines"`
	HeapAllocBytes uint64  `json:"heapAllocBytes"`
	HeapSysBytes   uint64  `json:"heapSysBytes"`
	ResidentBytes  uint64  `json:"residentBytes"`
	CPUSeconds     float64 `json:"cpuSeconds"`
}

// ResidentToHeapRatio is the multiplier
// `specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md` says is
// needed and never states: it reports live heap and says converting that to a
// resident-size budget is "a separate step with its own stated multiplier".
//
// This is that step, measured on one process at one moment rather than
// assumed. Zero when there is no heap to divide by, which is a process that
// has not started rather than a ratio of zero.
func (s ProcessSample) ResidentToHeapRatio() float64 {
	if s.HeapAllocBytes == 0 {
		return 0
	}
	return float64(s.ResidentBytes) / float64(s.HeapAllocBytes)
}

// ParseProcessSample reads a Prometheus text exposition into a sample.
//
// Every metric is required. A missing one means the endpoint is served by a
// build whose registry lacks the runtime collectors — the state
// internal/managermetrics exists to correct — and a sample with a silent zero
// in it would be reported as a process using no memory rather than as a
// measurement that did not happen.
func ParseProcessSample(r io.Reader) (ProcessSample, error) {
	// Constructed rather than zero-valued: TextParser's zero value is
	// documented as invalid and panics on the first metric name it sees.
	// UTF8Validation is the library's own current default, and the scheme is
	// passed here rather than set on the package global that
	// model.NameValidationScheme exposes — a library that mutated a global
	// validation setting would change the behaviour of anything else in the
	// binary that parses metrics, including the managers themselves.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return ProcessSample{}, fmt.Errorf("parsing the metrics exposition: %w", err)
	}

	value := func(name string) (float64, error) {
		family, ok := families[name]
		if !ok {
			return 0, fmt.Errorf("%s is not served: the process is not registering the Go runtime collectors "+
				"(see internal/managermetrics), so this run has nothing to reconcile against an in-process one", name)
		}
		metrics := family.GetMetric()
		if len(metrics) != 1 {
			return 0, fmt.Errorf("%s has %d series, want exactly one unlabelled gauge", name, len(metrics))
		}
		m := metrics[0]
		switch {
		case m.GetGauge() != nil:
			return m.GetGauge().GetValue(), nil
		case m.GetCounter() != nil:
			return m.GetCounter().GetValue(), nil
		case m.GetUntyped() != nil:
			return m.GetUntyped().GetValue(), nil
		default:
			return 0, fmt.Errorf("%s is neither a gauge nor a counter", name)
		}
	}

	var errs []error
	read := func(name string) float64 {
		v, err := value(name)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}

	sample := ProcessSample{
		Goroutines:     int(read(MetricGoroutines)),
		HeapAllocBytes: uint64(read(MetricHeapAlloc)),
		HeapSysBytes:   uint64(read(MetricHeapSys)),
		ResidentBytes:  uint64(read(MetricResident)),
		CPUSeconds:     read(MetricCPUSeconds),
	}
	if err := errors.Join(errs...); err != nil {
		return ProcessSample{}, err
	}
	return sample, nil
}
