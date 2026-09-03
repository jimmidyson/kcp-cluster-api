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

// Package upstreamscale measures stock upstream Cluster API on an ordinary
// Kubernetes cluster: released images, installed by clusterctl, with the
// in-memory DevCluster backend so that what is pushed is Cluster API's own
// machinery rather than a cloud's provisioning latency.
//
// It shares the report, the fits and the evidence conventions with
// internal/deployedscale — the rules about what may be called a measurement do
// not change with what is being measured — and differs in how a sample is
// taken. See ScrapeProcess.
package upstreamscale

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Paths this package reads. Both parameters on the heap path are load-bearing:
// see ScrapeProcess.
const (
	heapPath      = "/debug/pprof/heap?debug=1&gc=1"
	goroutinePath = "/debug/pprof/goroutine?debug=1"
)

// ScrapeProcess reads one manager's goroutine count and post-collection heap
// through its pprof endpoint.
//
// # Why not /metrics, as everything else here does
//
// controller-runtime's metrics registry is a bare prometheus.NewRegistry()
// rather than the default registerer, so it carries none of the collectors the
// default one has: a stock Cluster API manager serves workqueue, reconcile and
// client-go metrics and no go_goroutines, no go_memstats_* and no
// process_resident_memory_bytes at all. The managers in this repository have
// them only because internal/managermetrics adds them. Upstream's do not, and
// upstream's are the subject here.
//
// Every upstream manager does take --profiler-address, and pprof gives both
// missing quantities:
//
//   - heap?debug=1 writes a runtime.MemStats dump after the samples, which
//     carries HeapAlloc and HeapSys.
//   - heap?gc=1 runs a collection before writing, so HeapAlloc is the retained
//     set rather than the retained set plus whatever has not been swept. Three
//     kcp runs disagreed by a factor of four for want of exactly this, and the
//     fix there had to be retrofitted; here it is the first request the harness
//     ever makes.
//   - goroutine?debug=1 opens with the total.
//
// What pprof cannot give is resident memory, which is the number a container
// limit is set against. That comes from the cluster — see ScrapePodMemory.
func ScrapeProcess(ctx context.Context, client *http.Client, base string) (deployedscale.ProcessSample, error) {
	base = strings.TrimSuffix(base, "/")

	heap, err := fetch(ctx, client, base+heapPath)
	if err != nil {
		return deployedscale.ProcessSample{}, err
	}
	sample, err := ParseHeapProfile(strings.NewReader(heap))
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("reading the heap profile from %s: %w", base, err)
	}

	goroutines, err := fetch(ctx, client, base+goroutinePath)
	if err != nil {
		return deployedscale.ProcessSample{}, err
	}
	total, err := ParseGoroutineProfile(strings.NewReader(goroutines))
	if err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("reading the goroutine profile from %s: %w", base, err)
	}
	sample.Goroutines = total
	return sample, nil
}

func fetch(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("building the request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a response body.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: HTTP %d: %s", url, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	if readErr != nil {
		return "", fmt.Errorf("reading %s: %w", url, readErr)
	}
	return string(body), nil
}

// ParseHeapProfile reads the runtime.MemStats block a debug=1 heap profile
// carries after its samples.
//
// The sample lines above it are ignored on purpose. They are a sampled profile
// — one allocation in every 512 KiB by default — and adding them up gives an
// estimate of the heap, where MemStats gives the runtime's own count of it.
func ParseHeapProfile(r io.Reader) (deployedscale.ProcessSample, error) {
	var sample deployedscale.ProcessSample
	var sawAlloc, sawSys bool

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		key, value, ok := memStat(scanner.Text())
		if !ok {
			continue
		}
		switch key {
		case "HeapAlloc":
			sample.HeapAllocBytes, sawAlloc = value, true
		case "HeapSys":
			sample.HeapSysBytes, sawSys = value, true
		}
	}
	if err := scanner.Err(); err != nil {
		return deployedscale.ProcessSample{}, fmt.Errorf("reading the profile: %w", err)
	}
	if !sawAlloc || !sawSys {
		return deployedscale.ProcessSample{}, errors.New(
			"no runtime.MemStats block in the heap profile: ask for it with debug=1, or this is " +
				"the gzipped protobuf form, which carries a sampled profile and no MemStats")
	}
	return sample, nil
}

// memStat matches the "# HeapAlloc = 123" lines runtime/pprof writes. The
// "# Stack = 3244032 / 3244032" form is deliberately not matched: it has two
// values and neither is wanted here.
func memStat(line string) (key string, value uint64, ok bool) {
	rest, found := strings.CutPrefix(line, "# ")
	if !found {
		return "", 0, false
	}
	name, number, found := strings.Cut(rest, " = ")
	if !found {
		return "", 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(number), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(name), n, true
}

// ParseGoroutineProfile reads the total from a debug=1 goroutine profile's
// first line, which is "goroutine profile: total 1274".
func ParseGoroutineProfile(r io.Reader) (int, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return 0, fmt.Errorf("reading the profile: %w", err)
		}
		return 0, errors.New("the goroutine profile was empty")
	}
	rest, ok := strings.CutPrefix(scanner.Text(), "goroutine profile: total ")
	if !ok {
		return 0, fmt.Errorf("not a goroutine profile: first line was %q", scanner.Text())
	}
	total, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0, fmt.Errorf("reading the goroutine total: %w", err)
	}
	return total, nil
}
