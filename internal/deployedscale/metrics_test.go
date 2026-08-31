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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/jimmidyson/kcp-cluster-api/internal/managermetrics"
)

const exposition = `# HELP go_goroutines Number of goroutines that currently exist.
# TYPE go_goroutines gauge
go_goroutines 885
# HELP go_memstats_heap_alloc_bytes Number of heap bytes allocated and still in use.
# TYPE go_memstats_heap_alloc_bytes gauge
go_memstats_heap_alloc_bytes 4.4564864e+07
# HELP go_memstats_sys_bytes Number of bytes obtained from system.
# TYPE go_memstats_sys_bytes gauge
go_memstats_sys_bytes 8.1305096e+07
# HELP process_resident_memory_bytes Resident memory size in bytes.
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 1.35168e+08
# HELP process_cpu_seconds_total Total user and system CPU time spent in seconds.
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 42.71
# HELP controller_runtime_reconcile_total Total number of reconciliations per controller
# TYPE controller_runtime_reconcile_total counter
controller_runtime_reconcile_total{controller="cluster",result="success"} 1204
`

func TestParseProcessSample(t *testing.T) {
	got, err := ParseProcessSample(strings.NewReader(exposition))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	want := ProcessSample{
		Goroutines:     885,
		HeapAllocBytes: 44564864,
		HeapSysBytes:   81305096,
		ResidentBytes:  135168000,
		CPUSeconds:     42.71,
	}
	if got != want {
		t.Errorf("sample = %+v, want %+v", got, want)
	}
}

// TestResidentToHeapRatio is the figure capacity.md says is needed and never
// states: the step from a live-heap measurement to a container limit.
func TestResidentToHeapRatio(t *testing.T) {
	s := ProcessSample{HeapAllocBytes: 44564864, ResidentBytes: 135168000}
	got := s.ResidentToHeapRatio()
	if got < 3.0 || got > 3.1 {
		t.Errorf("ratio = %v, want about 3.03", got)
	}

	// No heap is a process that has not started, not a ratio of zero dressed
	// up as a measurement.
	if r := (ProcessSample{ResidentBytes: 1}).ResidentToHeapRatio(); r != 0 {
		t.Errorf("ratio with no heap = %v, want 0", r)
	}
}

// TestParseRefusesAnEndpointWithoutTheRuntimeCollectors is the failure this
// whole feature would otherwise hit silently: controller-runtime's registry
// carries none of these by default, so an un-updated build serves a valid
// exposition with nothing in it worth reconciling.
func TestParseRefusesAnEndpointWithoutTheRuntimeCollectors(t *testing.T) {
	const controllerOnly = `# HELP controller_runtime_reconcile_total Total number of reconciliations per controller
# TYPE controller_runtime_reconcile_total counter
controller_runtime_reconcile_total{controller="cluster",result="success"} 1204
`
	_, err := ParseProcessSample(strings.NewReader(controllerOnly))
	if err == nil {
		t.Fatal("an exposition with no runtime metrics parsed into a sample")
	}
	for _, want := range []string{MetricGoroutines, MetricResident, "managermetrics"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	if _, err := ParseProcessSample(strings.NewReader("this is not an exposition {{{")); err == nil {
		t.Error("malformed input parsed")
	}
}

// TestAgainstTheRealEndpoint closes the loop: what internal/managermetrics
// registers is served in the format this parser reads. Two halves written
// against a shared assumption would otherwise agree with each other and not
// with reality.
func TestAgainstTheRealEndpoint(t *testing.T) {
	managermetrics.Register()

	srv := httptest.NewServer(promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("scraping: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // a test's response body.

	got, err := ParseProcessSample(resp.Body)
	if err != nil {
		t.Fatalf("parsing what a manager actually serves: %v", err)
	}
	if got.Goroutines <= 0 {
		t.Errorf("goroutines = %d, want a real count", got.Goroutines)
	}
	if got.HeapAllocBytes == 0 || got.ResidentBytes == 0 {
		t.Errorf("memory came back empty: %+v", got)
	}
	if r := got.ResidentToHeapRatio(); r <= 0 {
		t.Errorf("ratio = %v on a live process", r)
	}
}
