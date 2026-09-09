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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// inheritedGrace is how far a process may predate a run before its baseline is
// treated as somebody else's.
//
// Generous, because starting a run involves patching the managers and waiting
// for them to settle, and a process that came up two minutes before the first
// sample is this run's by any reasonable reading. The case being caught is a
// process measured in hours, not one measured in minutes.
const inheritedGrace = 10 * time.Minute

// Inherited names the processes whose baseline is a previous run's.
//
// # What this is worth
//
// An API server that had served a 1500-cluster fleet the day before held
// 4.91 GiB of live heap against an API with no Clusters, no Machines and no
// events left in it. The same process restarted, on the same cluster, with the
// same CRDs installed and the same nothing to serve, held 216 MiB.
//
// Twenty-three times. So better than nine tenths of every memory figure the
// run had recorded was the fleet before it, and the report said nothing —
// which turned into a conclusion, stated with a percentage: the control plane
// was "82% full at 1500 clusters" and the node was therefore the binding
// constraint. Most of that fullness was a fleet that no longer existed.
//
// A run cannot fix this on its own — restarting an API server needs the
// container runtime on the node, which is not something a measurement should
// reach for. What it can do is refuse to present the number quietly. A
// baseline is what everything else is measured against, and one inherited from
// another run is the other run, still sitting there.
//
// # Why every process and not just the API server
//
// A manager that has been up for a day carries the same thing for the same
// reason, and the managers are half of what the report is about. The check is
// on the sample rather than on the component, so a process that publishes
// process_start_time_seconds is covered whatever it is.
func Inherited(components []deployedscale.ComponentSample, runStart time.Time) []string {
	cutoff := runStart.Add(-inheritedGrace)

	var old []string
	for _, c := range components {
		if !c.Process.StartedBefore(cutoff) {
			continue
		}
		old = append(old, fmt.Sprintf("%s (running %s)",
			c.Component, c.Process.Age(runStart).Round(time.Minute)))
	}
	sort.Strings(old)
	return old
}

// emptyAPIServerCeiling is more resident memory than an API server serving
// nothing has any business holding.
//
// Measured on this cluster: a kube-apiserver with Cluster API's CRDs installed
// and no Clusters, Machines or events left in it held 345 MiB of runtime memory
// once restarted, and the three on a freshly built cluster read 483 to 571 MiB
// resident. Two gigabytes is four times that, and the case being caught is not
// close: the API servers that had served 2000 clusters an hour earlier read
// 21 to 23 GiB each at the next run's baseline.
const emptyAPIServerCeiling = 2 << 30

// InheritedControlPlane names the API servers whose baseline is a previous
// run's, judged by size rather than by age.
//
// Inherited reads a process start time, and the control plane's samples come
// from the kubelet's cAdvisor endpoint, which reports memory and CPU and no
// start time — so a run whose API servers were never restarted took its
// baseline at 54.8 GiB of API server against an empty API, and the check that
// exists for exactly this said nothing. An API server holding gigabytes at zero
// clusters is holding the fleet before; nothing else it could be doing costs
// that.
func InheritedControlPlane(components []deployedscale.ComponentSample) []string {
	var old []string
	for _, c := range components {
		if !strings.HasPrefix(c.Component, "kube-apiserver") || c.Process.ResidentBytes < emptyAPIServerCeiling {
			continue
		}
		old = append(old, fmt.Sprintf("%s (%s resident with no fleet to serve)",
			c.Component, humanBytes(c.Process.ResidentBytes)))
	}
	sort.Strings(old)
	return old
}

// emptyManagerCeiling is more resident memory than a Cluster API manager
// reconciling nothing has any business holding.
//
// Measured: the four managers freshly started read 21 to 31 MiB resident at
// the baseline. The case being caught read 6.7 GiB, with 82 MiB of live heap:
// the previous day's fleet, still held by the runtime.
const emptyManagerCeiling = 1 << 30

// InheritedManagers names the managers whose baseline is a previous run's,
// judged by size, for the reason InheritedControlPlane judges the API servers
// that way: the managers are read through pprof, which carries no start time,
// so a run whose managers were not restarted took its baseline with the core
// manager at 6.7 GiB resident and said nothing. Judging by the container's
// start time was tried and named every long-lived process on the control
// plane's nodes, which is noise; the size is the finding.
func InheritedManagers(components []deployedscale.ComponentSample, controllers []Controller) []string {
	managers := make(map[string]bool, len(controllers))
	for _, c := range controllers {
		managers[c.Deployment] = true
	}
	var old []string
	for _, c := range components {
		if !managers[c.Component] || c.Process.ResidentBytes < emptyManagerCeiling {
			continue
		}
		old = append(old, fmt.Sprintf("%s (%s resident with no fleet to reconcile)",
			c.Component, humanBytes(c.Process.ResidentBytes)))
	}
	sort.Strings(old)
	return old
}

// DescribeInherited is the note a baseline carries when it is not its own.
func DescribeInherited(old []string) string {
	if len(old) == 0 {
		return ""
	}
	return fmt.Sprintf("this baseline is not this run's: %s were already running before it began, "+
		"so their heap holds whatever they served last. An API server that had served 1500 clusters "+
		"the day before read 4.91 GiB against an empty API and 216 MiB once restarted, so a figure "+
		"taken this way can be twenty times the fleet's own cost — restart them and take the "+
		"baseline again before quoting anything absolute from this run",
		strings.Join(old, ", "))
}
