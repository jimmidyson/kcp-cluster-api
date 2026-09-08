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
	"context"
	"fmt"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// coreControlPlane is the Kubernetes control plane itself: the four static pods
// kubeadm writes. Everything else on a control plane node, static pod or not,
// is a sidecar to it.
var coreControlPlane = []string{"kube-apiserver", "etcd", "kube-controller-manager", "kube-scheduler"}

// IsCoreControlPlane reports whether a pod on a control plane node is the
// control plane rather than a sidecar to it, by the prefix kubeadm gives its
// static pods: kube-apiserver-<node>, etcd-<node>, and so on.
//
// # Why the static pod test is not enough here
//
// IsControlPlaneComponent draws the line at the mirror annotation, and that is
// the right line for "did the control plane die": a process the kubelet runs
// from disk is part of how the machine is a control plane. It is the wrong
// line for "is this death the ceiling", because kube-vip is a static pod too —
// CAREN writes its manifest beside the API server's — and kube-vip exiting is
// the VIP moving to another node, which is an incident, not the management
// cluster failing to manage itself. See DescribeIncident.
func IsCoreControlPlane(component string) bool {
	for _, core := range coreControlPlane {
		if strings.HasPrefix(component, core+"-") || component == core {
			return true
		}
	}
	return false
}

// SplitSidecars divides the deaths on the control plane's nodes into the
// control plane's own, which end a rung, and the sidecars', which are
// recorded on it. See RungResult.Incidents.
func SplitSidecars(since []deployedscale.ComponentSample) (core, sidecars []deployedscale.ComponentSample) {
	for _, c := range since {
		if IsCoreControlPlane(c.Component) {
			core = append(core, c)
			continue
		}
		sidecars = append(sidecars, c)
	}
	return core, sidecars
}

// DescribeIncident is the line a rung carries for a sidecar that died during
// it and came back, or has not yet.
//
// # Why this is not Classify
//
// Classify's restart line ends "so its samples are not comparable with the
// rungs below it", which is the right sentence for a manager: its heap and
// goroutine series are what the rung measures, and a restart resets them.
// kube-vip has no series worth comparing. What its restart means is that the
// API endpoint moved, every connection through it was cut, and for the
// seconds in between the fleet's controllers and the harness had no API
// server — and that is what the line says.
//
// # Why an incident and not a ceiling
//
// Four runs in a row ended their top rung on kube-vip losing its lease, nine
// and a half minutes into a rung whose predecessor took ten and three
// quarters to converge. Whether the fleet would have arrived is the question
// the rung exists to answer, and stopping at the first death threw the answer
// away every time. So the rung runs on, and the death is recorded where it
// happened, graded as what it is: an availability incident on a rung that may
// still converge, which makes that rung a fleet the cluster reached and not
// one to recommend. See Ceiling.LastClean.
func DescribeIncident(c deployedscale.ComponentSample) string {
	line := fmt.Sprintf("%s restarted %d time(s) — %s", c.Component, c.Pod.RestartCount, c.Pod.WhyItDied())
	if strings.HasPrefix(c.Component, "kube-vip") {
		line += fmt.Sprintf(" — kube-vip exits when it cannot renew %s, so the API endpoint moved to "+
			"another node and every connection through it was cut", KubeVIPLease)
	}
	if !c.Pod.Ready {
		line += ", and it had not come back when this was read"
	}
	return line
}

// rungIncidents collects the sidecar deaths one rung is charged with.
//
// Charged once. A sidecar's restart count is read against the run's baseline,
// so a restart in the 1000-cluster rung would otherwise be reported again by
// every rung above it; what an earlier rung has already recorded is subtracted
// here, and this rung records only what happened during it.
type rungIncidents struct {
	r      *Runner
	before map[string]int32
	noted  map[string]string
	order  []string
}

func (r *Runner) startIncidents() *rungIncidents {
	if r.charged == nil {
		r.charged = map[string]int32{}
	}
	before := make(map[string]int32, len(r.charged))
	for k, v := range r.charged {
		before[k] = v
	}
	return &rungIncidents{r: r, before: before, noted: map[string]string{}}
}

// poll reads the control plane's nodes and returns the incidents it saw for
// the first time on this poll, for logging. Every incident's line is
// refreshed on every poll, so the one the rung ends with says whether the
// process had come back.
//
// Errors are swallowed for the reason died's are: this is a diagnosis, and
// turning "could not read the pods" into a failure would bury the rung's own.
func (in *rungIncidents) poll(ctx context.Context) []string {
	r := in.r
	if r.controlPlaneAtStart == nil && r.besideAtStart == nil {
		return nil
	}
	facts, beside, err := r.Sampler.ControlPlaneFacts(ctx, r.Host)
	if err != nil {
		return nil
	}
	_, sidecars := SplitSidecars(HealthSince(r.controlPlaneAtStart, facts))
	sidecars = append(sidecars, HealthSince(r.besideAtStart, beside)...)

	var fresh []string
	for _, c := range sidecars {
		since := c.Pod.RestartCount - in.before[c.Component]
		if since <= 0 {
			continue
		}
		c.Pod.RestartCount = since
		line := DescribeIncident(c)
		if _, seen := in.noted[c.Component]; !seen {
			in.order = append(in.order, c.Component)
			fresh = append(fresh, line)
		}
		in.noted[c.Component] = line
		r.charged[c.Component] = in.before[c.Component] + since
	}
	return fresh
}

// close is what the rung records, in the order the incidents were first seen.
func (in *rungIncidents) close() []string {
	out := make([]string, 0, len(in.order))
	for _, name := range in.order {
		out = append(out, in.noted[name])
	}
	return out
}
