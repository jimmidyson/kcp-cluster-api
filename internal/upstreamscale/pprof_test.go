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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A real /debug/pprof/heap?debug=1 body, trimmed in the middle. The sample
// lines are what pprof shows; the MemStats block after them is what this
// harness is actually there for.
const heapBody = `heap profile: 1052: 84378112 [30411: 2411589632] @ heap/1048576
1: 33554432 [1: 33554432] @ 0x4a1b2c 0x4a2f10
#	0x4a1b2b	bytes.growSlice+0x8b

# runtime.MemStats
# Alloc = 84378112
# TotalAlloc = 2411589632
# Sys = 246882312
# Lookups = 0
# Mallocs = 30411
# Frees = 29359
# HeapAlloc = 84378112
# HeapSys = 205914112
# HeapIdle = 108929024
# HeapInuse = 96985088
# HeapReleased = 51249152
# HeapObjects = 1052
# Stack = 3244032 / 3244032
# NextGC = 168756224
# NumGC = 14
# NumForcedGC = 1
# GCCPUFraction = 0.0012
`

const goroutineBody = `goroutine profile: total 1274
1 @ 0x43f0ce 0x44f8a5 0x9c2b31
#	0x9c2b30	sigs.k8s.io/controller-runtime/pkg/manager.(*runnableGroup).Start+0x1f0
`

// TestAPprofSampleReadsWhatTheMetricsEndpointDoesNotServe is the whole reason
// this package exists.
//
// controller-runtime's metrics registry is a bare prometheus.NewRegistry(), so
// a stock Cluster API manager serves workqueue and reconcile metrics and no
// go_goroutines, no go_memstats_* and no process_resident_memory_bytes. The
// managers in this repository only have those because internal/managermetrics
// adds them; upstream's do not, and upstream's are what this measures.
//
// What every upstream manager does have is --profiler-address. A heap profile
// asked for with debug=1 carries a runtime.MemStats dump, and asked for with
// gc=1 it runs a collection first — so one request gives the retained set, in
// the same post-collection form the kcp runs had to be rebuilt to produce.
func TestAPprofSampleReadsWhatTheMetricsEndpointDoesNotServe(t *testing.T) {
	got, err := ParseHeapProfile(strings.NewReader(heapBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.HeapAllocBytes != 84378112 {
		t.Errorf("HeapAlloc = %d, want 84378112", got.HeapAllocBytes)
	}
	if got.HeapSysBytes != 205914112 {
		t.Errorf("HeapSys = %d, want 205914112", got.HeapSysBytes)
	}
}

// TestAProfileWithoutMemStatsIsAnError. debug=1 is what produces the MemStats
// block; without it pprof returns a gzipped protobuf and this parser would
// otherwise report a process holding zero bytes, which is a measurement rather
// than a mistake as far as everything downstream can tell.
func TestAProfileWithoutMemStatsIsAnError(t *testing.T) {
	if _, err := ParseHeapProfile(strings.NewReader("heap profile: 0: 0 [0: 0] @ heap/1048576\n")); err == nil {
		t.Error("a profile with no MemStats block parsed into a sample")
	}
}

func TestGoroutineTotalIsTheFirstLine(t *testing.T) {
	got, err := ParseGoroutineProfile(strings.NewReader(goroutineBody))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got != 1274 {
		t.Errorf("goroutines = %d, want 1274", got)
	}
	if _, err := ParseGoroutineProfile(strings.NewReader("not a profile\n")); err == nil {
		t.Error("a body that is not a goroutine profile parsed into a count")
	}
}

// TestTheHeapRequestForcesACollection pins the two query parameters the whole
// measurement rests on. Without gc=1 the sample is the retained set plus
// whatever has not been swept, which is what made three kcp runs disagree by a
// factor of four; without debug=1 there is no MemStats block to read.
func TestTheHeapRequestForcesACollection(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case strings.Contains(r.URL.Path, "goroutine"):
			_, _ = w.Write([]byte(goroutineBody))
		default:
			_, _ = w.Write([]byte(heapBody))
		}
	}))
	defer server.Close()

	got, err := ScrapeProcess(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatalf("scraping: %v", err)
	}
	if got.Goroutines != 1274 || got.HeapAllocBytes != 84378112 {
		t.Errorf("sample = %+v", got)
	}
	want := []string{"/debug/pprof/heap?debug=1&gc=1", "/debug/pprof/goroutine?debug=1"}
	if len(asked) != 2 || asked[0] != want[0] || asked[1] != want[1] {
		t.Errorf("requested %v, want %v", asked, want)
	}
}
