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

package managermetrics

import (
	"slices"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

func gathered(t *testing.T) []string {
	t.Helper()
	families, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering: %v", err)
	}
	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}
	return names
}

// TestRegisterAddsTheQuantitiesAnInProcessRunReports is the whole point of the
// package: these three are what internal/sweep samples, and therefore the only
// ones by which a deployed run can be reconciled with an in-process one.
func TestRegisterAddsTheQuantitiesAnInProcessRunReports(t *testing.T) {
	before := gathered(t)
	for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "process_resident_memory_bytes"} {
		if slices.Contains(before, name) {
			t.Fatalf("%s was already registered before Register: this test cannot show that Register added it", name)
		}
	}

	Register()

	after := gathered(t)
	for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "process_resident_memory_bytes"} {
		if !slices.Contains(after, name) {
			t.Errorf("%s is not served after Register; a deployed run has nothing to reconcile against", name)
		}
	}
}

// TestRegisterIsIdempotent guards the failure the sync.Once exists for:
// registering a collector twice returns AlreadyRegisteredError, and a manager
// that refused to start over it would be a worse outcome than duplicate
// metrics.
func TestRegisterIsIdempotent(t *testing.T) {
	Register()
	Register()
	Register()

	names := gathered(t)
	count := 0
	for _, name := range names {
		if name == "go_goroutines" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("go_goroutines is served %d times, want once", count)
	}
}

func TestOptionsCarriesTheBindAddressAndRegisters(t *testing.T) {
	opts := Options(":9999")
	if opts.BindAddress != ":9999" {
		t.Errorf("BindAddress = %q, want :9999", opts.BindAddress)
	}
	if !slices.Contains(gathered(t), "go_goroutines") {
		t.Error("Options did not register the runtime collectors, so the endpoint it describes is not worth scraping")
	}

	// "0" is controller-runtime's way of disabling the server, and must pass
	// through untouched: a deployment that wanted no endpoint must get none.
	if got := Options("0").BindAddress; got != "0" {
		t.Errorf("BindAddress = %q, want 0 to pass through as the disable value", got)
	}
}
