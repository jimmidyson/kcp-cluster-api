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
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// ControlPlaneNodeLabel selects the nodes whose whole cost the run reports.
// kubeadm puts it on every control plane node, and it is the only thing that
// distinguishes them from workers without naming machines.
const ControlPlaneNodeLabel = "node-role.kubernetes.io/control-plane"

// ControlPlaneNodes is the nodes the control plane runs on.
func ControlPlaneNodes(ctx context.Context, cl client.Client) ([]string, error) {
	var nodes corev1.NodeList
	if err := cl.List(ctx, &nodes, client.HasLabels{ControlPlaneNodeLabel}); err != nil {
		return nil, fmt.Errorf("listing control plane nodes: %w", err)
	}
	out := make([]string, 0, len(nodes.Items))
	for i := range nodes.Items {
		out = append(out, nodes.Items[i].Name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no node carries %s: on a managed control plane the machines are not "+
			"in this cluster's node list, and what runs on them cannot be measured from here",
			ControlPlaneNodeLabel)
	}
	sort.Strings(out)
	return out, nil
}

// ControlPlaneUsage turns one scrape of the control plane's nodes into the
// components a report carries — one per pod, named by the pod.
//
// # Why every pod rather than a chosen few
//
// A control plane is what its machines are doing. Three API servers, three etcd
// members, the controller manager and the scheduler are all on those nodes, and
// so is whatever else the cluster puts there. Reporting a list somebody wrote
// down means the run cannot find a cost nobody predicted — and there was one:
// kube-controller-manager's garbage collector holds an informer per resource,
// so every Cluster API CRD is cached there too, and no run had ever looked.
//
// facts may be missing an entry, and a pod is still measured without one: the
// usage scrape and the pod list are two reads of a cluster that does not hold
// still between them.
func ControlPlaneUsage(usage map[string]PodUsage,
	facts map[string]deployedscale.PodFacts,
) []deployedscale.ComponentSample {
	keys := make([]string, 0, len(usage))
	for key := range usage {
		keys = append(keys, key)
	}
	// Sorted, so a report's rows are the same from one sample to the next and a
	// diff of two runs is a diff of numbers.
	sort.Strings(keys)

	out := make([]deployedscale.ComponentSample, 0, len(keys))
	for _, key := range keys {
		u := usage[key]
		_, pod, _ := strings.Cut(key, "/")

		podFacts, ok := facts[key]
		if !ok {
			podFacts = deployedscale.PodFacts{Name: pod}
		}
		// The node the scrape came from is authoritative: it is where the
		// figures were read, whatever a second list of pods says.
		if u.Node != "" {
			podFacts.Node = u.Node
		}
		out = append(out, deployedscale.ComponentSample{
			// The pod, not the component: three API servers are three
			// processes, and a report that called them all "kube-apiserver"
			// would keep only the last.
			Component: pod,
			Process: deployedscale.ProcessSample{
				ResidentBytes: u.WorkingSetBytes,
				CPUSeconds:    u.CPUSeconds,
			},
			Pod: podFacts,
		})
	}
	return out
}

// NodeUsage scrapes every pod on one node, stamped with the node it came from.
func (s *Sampler) NodeUsage(ctx context.Context, node string) (map[string]PodUsage, error) {
	raw, err := s.clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", node, "proxy", "metrics", "cadvisor").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading cadvisor on %s: %w", node, err)
	}
	usage, err := ParseNodeUsage(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	for key, u := range usage {
		u.Node = node
		usage[key] = u
	}
	return usage, nil
}

// ControlPlaneNodeUsage scrapes every pod on every control plane node, and the
// pod facts that say whether any of them has been restarted.
//
// One scrape per node rather than one per pod, and through the node proxy
// rather than the pod proxy — which is not a preference. The API server's own
// /metrics needs credentials and the pod proxy strips them: a request through
// it arrives as system:anonymous and is refused, which is why every recorded
// control-plane figure was one arbitrary instance behind the VIP. The node
// proxy carries the caller's identity, so this reads what the pod proxy cannot.
func (s *Sampler) ControlPlaneNodeUsage(ctx context.Context, cl client.Client,
) (ControlPlaneReadout, error) {
	nodes, err := ControlPlaneNodes(ctx, cl)
	if err != nil {
		return ControlPlaneReadout{}, err
	}
	readout := ControlPlaneReadout{Nodes: nodes, Usage: map[string]PodUsage{}}

	var why []string
	for _, node := range nodes {
		perPod, err := s.NodeUsage(ctx, node)
		if err != nil {
			readout.Missed = append(readout.Missed, node)
			why = append(why, fmt.Sprintf("%s (%v)", node, err))
			continue
		}
		for key, u := range perPod {
			readout.Usage[key] = u
		}
	}
	if len(readout.Usage) == 0 {
		return ControlPlaneReadout{}, fmt.Errorf("no control plane node could be scraped: %s",
			strings.Join(why, "; "))
	}
	readout.Samples = ControlPlaneUsage(readout.Usage, s.podFacts(ctx, cl, nodes))
	if len(readout.Missed) > 0 {
		// Named rather than silent: a control plane summed over two of its
		// three nodes is short by a whole machine, which is exactly the
		// misreading the node count exists to prevent.
		return readout, fmt.Errorf("%d of %d control plane nodes could not be scraped, so these "+
			"figures are short by whatever runs on them: %s",
			len(readout.Missed), len(nodes), strings.Join(why, "; "))
	}
	return readout, nil
}

// ControlPlaneReadout is one sample of the machines the control plane runs on.
//
// It carries what was measured *and what it covers* — the nodes found, the ones
// that answered — because a total without its coverage invites the reading the
// numbers cannot survive: that this is one machine's cost and the real figure
// is three times larger.
type ControlPlaneReadout struct {
	// Nodes are the control plane's machines, and Missed the ones that did not
	// answer this time.
	Nodes  []string
	Missed []string

	// Usage is every pod on those machines, keyed "namespace/pod".
	Usage map[string]PodUsage
	// Samples is the same thing as report components.
	Samples []deployedscale.ComponentSample
}

// RoleTotal is what one kind of process costs across the whole control plane.
type RoleTotal struct {
	Role     string
	Count    int
	Nodes    int
	Resident uint64
	CPU      float64
}

// Roles groups the readout by what each process is, largest first.
//
// This is the line's answer to "does that total cover all three machines": a
// reader sees `kube-apiserver x3` and knows it does, without opening the table
// or trusting the summing.
func (r ControlPlaneReadout) Roles() []RoleTotal {
	byRole := map[string]*RoleTotal{}
	nodesSeen := map[string]map[string]bool{}
	for _, u := range r.Usage {
		role := u.Role
		if role == "" {
			role = "unnamed"
		}
		total, ok := byRole[role]
		if !ok {
			total = &RoleTotal{Role: role}
			byRole[role] = total
			nodesSeen[role] = map[string]bool{}
		}
		total.Count++
		total.Resident += u.WorkingSetBytes
		total.CPU += u.CPUSeconds
		if u.Node != "" && !nodesSeen[role][u.Node] {
			nodesSeen[role][u.Node] = true
			total.Nodes++
		}
	}

	out := make([]RoleTotal, 0, len(byRole))
	for _, total := range byRole {
		out = append(out, *total)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resident != out[j].Resident {
			return out[i].Resident > out[j].Resident
		}
		return out[i].Role < out[j].Role
	})
	return out
}

// Describe is the line a rung carries about the control plane's machines.
//
// It leads with the coverage — how many nodes, how many of them answered —
// then the total, then each role with its replica count. The order is
// deliberate: the first thing a reader needs is what the number covers.
func (r ControlPlaneReadout) Describe() string {
	if len(r.Usage) == 0 {
		return "the control plane's nodes were **not measured**: nothing was read from them, so this " +
			"rung has no figure for what the machines the fleet's objects live on are doing"
	}

	// Every figure here comes from what was scraped rather than from the
	// samples built out of it: the readout is one source of truth, and a line
	// that could disagree with its own table is worse than no line.
	var (
		resident    uint64
		cpu         float64
		largest     string
		largestSize uint64
	)
	for key, u := range r.Usage {
		resident += u.WorkingSetBytes
		cpu += u.CPUSeconds
		if u.WorkingSetBytes > largestSize {
			_, pod, _ := strings.Cut(key, "/")
			largest, largestSize = pod, u.WorkingSetBytes
		}
	}

	scraped := len(r.Nodes) - len(r.Missed)
	var b strings.Builder
	if len(r.Missed) > 0 {
		fmt.Fprintf(&b, "%d of %d nodes (could not read %s)", scraped, len(r.Nodes),
			strings.Join(r.Missed, ", "))
	} else {
		fmt.Fprintf(&b, "%d nodes", len(r.Nodes))
	}
	fmt.Fprintf(&b, ", %d processes, %s resident in total, %.0f CPU-seconds",
		len(r.Usage), humanBytes(resident), cpu)

	roles := r.Roles()
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		parts = append(parts, fmt.Sprintf("%s x%d %s", role.Role, role.Count, humanBytes(role.Resident)))
	}
	fmt.Fprintf(&b, " — %s", strings.Join(parts, ", "))
	fmt.Fprintf(&b, "; largest single process %s at %s", largest, humanBytes(largestSize))

	// A role that is evidently replicated and yet short of a machine is either
	// a scrape that missed one or a control plane running degraded, and both
	// are worth attention on a run whose purpose is to find where something
	// breaks. A role on exactly one node is not flagged: that is what a
	// singleton looks like, and "x1" against the node count above says it
	// already.
	for _, role := range roles {
		if role.Nodes > 1 && role.Nodes < scraped {
			fmt.Fprintf(&b, " — **%s is on %d of the %d nodes read**", role.Role, role.Nodes, scraped)
		}
	}

	var restarted []string
	for _, s := range r.Samples {
		if s.Pod.RestartCount > 0 {
			restarted = append(restarted, fmt.Sprintf("%s x%d", s.Component, s.Pod.RestartCount))
		}
	}
	if len(restarted) > 0 {
		// A restart resets every counter above it, so a rung containing one is
		// not comparable with the rung below — and on a run aimed at a ceiling
		// it is usually the finding itself.
		fmt.Fprintf(&b, " — **restarted during this run**: %s", strings.Join(restarted, ", "))
	}
	return b.String()
}

// podFacts reads restart counts and last reasons for the pods on the given
// nodes, keyed the way the usage map is.
//
// Best effort: a missing fact costs a column, and a missing figure costs the
// measurement. What it is really for is one column — whether a process was
// killed — because a run aimed at a ceiling has to be able to say that the API
// server was OOM killed rather than that reconciliation stopped keeping up.
func (s *Sampler) podFacts(ctx context.Context, cl client.Client, nodes []string,
) map[string]deployedscale.PodFacts {
	on := map[string]bool{}
	for _, node := range nodes {
		on[node] = true
	}

	var pods corev1.PodList
	if err := cl.List(ctx, &pods); err != nil {
		return nil
	}
	out := map[string]deployedscale.PodFacts{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !on[pod.Spec.NodeName] {
			continue
		}
		// Whichever container the pod is named for: a static control plane pod
		// has one, and PodFactsFrom falls back to the pod's own status when the
		// name does not match.
		facts := deployedscale.PodFactsFrom(pod, containerNameOf(pod))
		out[pod.Namespace+"/"+pod.Name] = facts
	}
	return out
}

// containerNameOf is the container a pod's facts should be read from: the one
// named after the pod where there is one, and otherwise the first. A static
// control plane pod is kube-apiserver-<node> running a container called
// kube-apiserver.
func containerNameOf(pod *corev1.Pod) string {
	if len(pod.Spec.Containers) == 0 {
		return ""
	}
	for _, c := range pod.Spec.Containers {
		if strings.HasPrefix(pod.Name, c.Name) {
			return c.Name
		}
	}
	return pod.Spec.Containers[0].Name
}

// HealthOf turns pod facts into the samples Classify reads.
//
// Only the facts: a rung's health check runs every poll, for the length of a
// convergence, so it cannot be the full sample — that is a cAdvisor scrape per
// node plus several heap reads seconds apart. Classify looks at restarts and
// OOM kills and nothing else, so this fetches nothing else. The process figures
// stay zero because they were never read, and a zero that reached a report
// would be read as a process costing nothing.
func HealthOf(facts map[string]deployedscale.PodFacts) []deployedscale.ComponentSample {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]deployedscale.ComponentSample, 0, len(keys))
	for _, key := range keys {
		_, pod, _ := strings.Cut(key, "/")
		out = append(out, deployedscale.ComponentSample{Component: pod, Pod: facts[key]})
	}
	return out
}

// ControlPlaneHealth is the cheap check: has anything on the control plane's
// nodes died since the run started.
//
// # Why this exists separately from the sample
//
// A run aimed at a ceiling has to be able to say that the API server was OOM
// killed rather than that reconciliation stopped keeping up, and those are the
// same observation only if something looks. The rung's health check looked at
// the four managers and nothing else, so a control plane dying under the fleet
// was invisible to it — the rung would run to its step timeout and report that
// nothing had run out.
func (s *Sampler) ControlPlaneHealth(ctx context.Context, cl client.Client,
) ([]deployedscale.ComponentSample, error) {
	nodes, err := ControlPlaneNodes(ctx, cl)
	if err != nil {
		return nil, err
	}
	return HealthOf(s.podFacts(ctx, cl, nodes)), nil
}
