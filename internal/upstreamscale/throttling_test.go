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
	"strings"
	"testing"
)

// A trimmed kubelet /metrics/cadvisor exposition. Two pods, so the parser has
// to pick one, and a POD-level series with an empty container label, which
// cAdvisor emits for the pod sandbox and which would double every number if it
// were counted.
const cadvisorBody = `# HELP container_cpu_cfs_periods_total Number of elapsed enforcement period intervals.
# TYPE container_cpu_cfs_periods_total counter
container_cpu_cfs_periods_total{container="",namespace="capd-system",pod="capd-controller-manager-abc"} 9000
container_cpu_cfs_periods_total{container="manager",namespace="capd-system",pod="capd-controller-manager-abc"} 4000
container_cpu_cfs_periods_total{container="manager",namespace="capi-system",pod="capi-controller-manager-xyz"} 7000
# TYPE container_cpu_cfs_throttled_periods_total counter
container_cpu_cfs_throttled_periods_total{container="",namespace="capd-system",pod="capd-controller-manager-abc"} 900
container_cpu_cfs_throttled_periods_total{container="manager",namespace="capd-system",pod="capd-controller-manager-abc"} 1000
container_cpu_cfs_throttled_periods_total{container="manager",namespace="capi-system",pod="capi-controller-manager-xyz"} 0
# TYPE container_cpu_cfs_throttled_seconds_total counter
container_cpu_cfs_throttled_seconds_total{container="manager",namespace="capd-system",pod="capd-controller-manager-abc"} 42.5
container_cpu_cfs_throttled_seconds_total{container="manager",namespace="capi-system",pod="capi-controller-manager-xyz"} 0
# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{container="",namespace="capd-system",pod="capd-controller-manager-abc"} 9.0e+09
container_memory_working_set_bytes{container="manager",namespace="capd-system",pod="capd-controller-manager-abc"} 4.0e+09
container_memory_working_set_bytes{container="manager",namespace="capi-system",pod="capi-controller-manager-xyz"} 1.0e+09
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{container="",namespace="capd-system",pod="capd-controller-manager-abc"} 999
container_cpu_usage_seconds_total{container="manager",namespace="capd-system",pod="capd-controller-manager-abc"} 612.5
container_cpu_usage_seconds_total{container="manager",namespace="capi-system",pod="capi-controller-manager-xyz"} 71.25
`

// TestThrottlingIsReadPerPod is the check on the ladder's most interesting
// verdict.
//
// A Guaranteed component has a CPU limit, a CPU limit means CFS throttling, and
// a throttled reconciler is slow for a reason that has nothing to do with
// Cluster API. "The fleet did not arrive and nothing died" is the one failure
// mode this run most wants to believe, and it is exactly the one throttling
// counterfeits — so it is measured rather than argued about.
func TestThrottlingIsReadPerPod(t *testing.T) {
	got, err := ParseThrottling(strings.NewReader(cadvisorBody), "capd-system", "capd-controller-manager-abc")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if got.ThrottledSeconds != 42.5 {
		t.Errorf("throttled seconds = %v, want 42.5", got.ThrottledSeconds)
	}
	// 1000 of 4000 periods. The pod sandbox series, with its empty container
	// label and its own 900/9000, must not be in this.
	if got.Periods != 4000 || got.ThrottledPeriods != 1000 {
		t.Errorf("periods = %d/%d, want 1000/4000 — the pod sandbox series was counted",
			got.ThrottledPeriods, got.Periods)
	}
	if f := got.Fraction(); f < 0.24 || f > 0.26 {
		t.Errorf("fraction = %v, want 0.25", f)
	}
	if !got.Significant() {
		t.Error("a quarter of every period throttled was not called significant")
	}

	quiet, err := ParseThrottling(strings.NewReader(cadvisorBody), "capi-system", "capi-controller-manager-xyz")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if quiet.Fraction() != 0 || quiet.Significant() {
		t.Errorf("an unthrottled pod reported %+v", quiet)
	}
}

// TestAPodWithNoSeriesIsAnError rather than a pod that was never throttled.
// Reporting "0% throttled" for a pod the scrape could not see would retire the
// only evidence that could overturn a "did not keep up" verdict.
func TestAPodWithNoSeriesIsAnError(t *testing.T) {
	if _, err := ParseThrottling(strings.NewReader(cadvisorBody), "capd-system", "not-a-pod"); err == nil {
		t.Error("a pod with no series parsed into a measurement of zero throttling")
	}
}

func TestFractionIsZeroWithNoPeriods(t *testing.T) {
	var none Throttling
	if none.Fraction() != 0 {
		t.Errorf("fraction = %v with no periods", none.Fraction())
	}
}

// TestUsageComesFromTheSameScrapeAsThrottling. The first two real runs reported
// every controller's resident memory and CPU time as zero: resident was read
// from metrics.k8s.io, and the cluster under test has no metrics-server the
// sampler can reach, while CPU time was never read at all.
//
// Both are in the cAdvisor exposition this harness already scrapes for
// throttling, so they come from that one read rather than from a component the
// measurement would otherwise have to install on the cluster it is measuring.
// Resident matters because a container limit is set against it: it is how a
// reader sees the next rung's OOM kill coming instead of discovering it.
func TestUsageComesFromTheSameScrapeAsThrottling(t *testing.T) {
	got, err := ParseCadvisor(strings.NewReader(cadvisorBody), "capd-system", "capd-controller-manager-abc")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	// The manager container's, not the pod sandbox's 9 GiB and 999s.
	if got.WorkingSetBytes != 4_000_000_000 {
		t.Errorf("working set = %d, want 4e9 — the pod sandbox series was counted", got.WorkingSetBytes)
	}
	if got.CPUSeconds != 612.5 {
		t.Errorf("cpu seconds = %v, want 612.5", got.CPUSeconds)
	}
	// And the throttling from the same pass, so one scrape serves both.
	if got.Periods != 4000 || got.ThrottledPeriods != 1000 {
		t.Errorf("throttling was lost from the combined read: %+v", got.Throttling)
	}
}

// TestUsageIsNotSilentlyZero. A pod the scrape could not see must not report a
// working set of zero: zero reads as a controller costing nothing, which is
// the direction nobody checks.
func TestUsageIsNotSilentlyZero(t *testing.T) {
	if _, err := ParseCadvisor(strings.NewReader(cadvisorBody), "capd-system", "not-a-pod"); err == nil {
		t.Error("a pod with no series parsed into a measurement of zero cost")
	}
}
