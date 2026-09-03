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
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Throttling is how much of a container's time the kernel took away from it.
//
// # Why this is measured at all
//
// Guaranteed resources mean a CPU limit, a CPU limit means CFS throttling, and
// a throttled reconciler is slow for a reason that has nothing to do with
// Cluster API. The ladder's most interesting verdict — "the fleet did not
// arrive and nothing died" — is precisely the one throttling counterfeits, so a
// rung that fails that way is reported with this beside it and the reader can
// tell a Cluster API ceiling from a limit this harness chose.
type Throttling struct {
	// ThrottledSeconds is wall time the container was runnable and not run.
	ThrottledSeconds float64 `json:"throttledSeconds"`
	// Periods and ThrottledPeriods are the CFS enforcement intervals, which
	// give the fraction. Seconds alone do not: a long-lived process
	// accumulates them, and what matters is the share.
	Periods          int64 `json:"periods"`
	ThrottledPeriods int64 `json:"throttledPeriods"`
}

// Fraction is the share of enforcement periods in which the container hit its
// ceiling. Zero when nothing has been enforced yet, which is a process that has
// not run rather than one that was never throttled.
func (t Throttling) Fraction() float64 {
	if t.Periods == 0 {
		return 0
	}
	return float64(t.ThrottledPeriods) / float64(t.Periods)
}

// throttlingThreshold is the share above which throttling is worth putting in
// front of a reader.
//
// A judgement, and this is the reasoning: a controller that hits its ceiling in
// a few percent of periods is being shaped by its limit at the margins, which is
// what a limit is for. One throttled in a fifth of them is spending a fifth of
// its scheduling opportunities waiting for quota, and no verdict about Cluster
// API keeping up can be drawn over the top of that.
const throttlingThreshold = 0.05

// Significant reports whether this is enough throttling to undermine a verdict
// about reconciliation keeping up.
func (t Throttling) Significant() bool { return t.Fraction() > throttlingThreshold }

// Describe is what a failed rung carries beside its classification.
func (t Throttling) Describe() string {
	return fmt.Sprintf("%.1f%% of CFS periods throttled (%d of %d, %.1fs)",
		100*t.Fraction(), t.ThrottledPeriods, t.Periods, t.ThrottledSeconds)
}

// ContainerUsage is what one pod cost and how much CPU the kernel took away
// from it, from a single cAdvisor scrape.
//
// # Why these three together
//
// The first two real runs reported every controller's resident memory and CPU
// time as zero. Resident was read from metrics.k8s.io and the cluster under
// test has no metrics-server the sampler can reach; CPU time was never read at
// all, because pprof does not carry it. Both are in the exposition this
// harness already scrapes for throttling, so they come from that read rather
// than from a component the measurement would have to install on the cluster
// it is measuring — which would be another controller reconciling against the
// API server whose cost is the subject of the run.
//
// Resident is the one that changes what a reader can conclude: a container
// limit is enforced against the working set, so this is how the next rung's
// OOM kill is seen coming rather than discovered.
type ContainerUsage struct {
	Throttling

	// WorkingSetBytes is what the limit is enforced against, summed over the
	// pod's containers — the same quantity metrics-server serves, from the
	// same source it reads.
	WorkingSetBytes uint64 `json:"workingSetBytes"`
	// CPUSeconds is cumulative container CPU time since the pod started.
	CPUSeconds float64 `json:"cpuSeconds"`
}

// ParseThrottling reads one pod's CFS accounting out of a cAdvisor exposition.
func ParseThrottling(r io.Reader, namespace, pod string) (Throttling, error) {
	usage, err := ParseCadvisor(r, namespace, pod)
	return usage.Throttling, err
}

// ParseCadvisor reads one pod's containers out of a kubelet cAdvisor
// exposition, which is served at
// /api/v1/nodes/<node>/proxy/metrics/cadvisor through the API server.
//
// Series with an empty container label are the pod sandbox's own accounting and
// are skipped: counting them alongside the containers double-counts every pod.
func ParseCadvisor(r io.Reader, namespace, pod string) (ContainerUsage, error) {
	var out ContainerUsage
	found := false

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, ok := series(line)
		if !ok {
			continue
		}
		switch name {
		case "container_cpu_cfs_throttled_seconds_total",
			"container_cpu_cfs_periods_total",
			"container_cpu_cfs_throttled_periods_total",
			"container_memory_working_set_bytes",
			"container_cpu_usage_seconds_total":
		default:
			continue
		}
		if labels["namespace"] != namespace || labels["pod"] != pod || labels["container"] == "" {
			continue
		}
		found = true
		switch name {
		case "container_cpu_cfs_throttled_seconds_total":
			out.ThrottledSeconds += value
		case "container_cpu_cfs_periods_total":
			out.Periods += int64(value)
		case "container_cpu_cfs_throttled_periods_total":
			out.ThrottledPeriods += int64(value)
		case "container_memory_working_set_bytes":
			out.WorkingSetBytes += uint64(value)
		case "container_cpu_usage_seconds_total":
			out.CPUSeconds += value
		}
	}
	if err := scanner.Err(); err != nil {
		return ContainerUsage{}, fmt.Errorf("reading the cadvisor exposition: %w", err)
	}
	if !found {
		// Not zero, for either figure. A pod the scrape could not see reported
		// as "never throttled" would retire the only evidence that could
		// overturn a verdict about reconciliation keeping up, and one reported
		// as costing nothing is wrong in the direction nobody checks.
		return ContainerUsage{}, fmt.Errorf("no cAdvisor series for pod %s/%s: it may be on another "+
			"node, or the scrape was of the wrong one", namespace, pod)
	}
	return out, nil
}

// series splits one Prometheus exposition line into its name, its labels and
// its value.
func series(line string) (name string, labels map[string]string, value float64, ok bool) {
	open := strings.IndexByte(line, '{')
	shut := strings.LastIndexByte(line, '}')
	if open < 0 || shut < open {
		return "", nil, 0, false
	}
	name = line[:open]
	labels = map[string]string{}
	for _, pair := range splitLabels(line[open+1 : shut]) {
		key, val, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		labels[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(line[shut+1:]), 64)
	if err != nil {
		return "", nil, 0, false
	}
	return name, labels, value, true
}

// splitLabels splits on commas that are not inside a quoted value. Container
// and pod names do not contain commas, but image and id labels do, and a naive
// split puts the rest of the line into a key nobody asked for.
func splitLabels(s string) []string {
	var out []string
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inQuotes = !inQuotes
			current.WriteByte(c)
		case c == ',' && !inQuotes:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}
