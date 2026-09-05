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
func ControlPlaneUsage(usage map[string]ContainerUsage,
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

// NodeUsage scrapes every pod on one node.
func (s *Sampler) NodeUsage(ctx context.Context, node string) (map[string]ContainerUsage, error) {
	raw, err := s.clientset.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/nodes", node, "proxy", "metrics", "cadvisor").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading cadvisor on %s: %w", node, err)
	}
	return ParseNodeUsage(strings.NewReader(string(raw)))
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
) ([]deployedscale.ComponentSample, error) {
	nodes, err := ControlPlaneNodes(ctx, cl)
	if err != nil {
		return nil, err
	}

	usage := map[string]ContainerUsage{}
	var failed []string
	for _, node := range nodes {
		perPod, err := s.NodeUsage(ctx, node)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", node, err))
			continue
		}
		for key, u := range perPod {
			usage[key] = u
		}
	}
	if len(usage) == 0 {
		return nil, fmt.Errorf("no control plane node could be scraped: %s", strings.Join(failed, "; "))
	}
	if len(failed) > 0 {
		// Named rather than silent: a control plane summed over two of its
		// three nodes is short by a whole machine.
		return ControlPlaneUsage(usage, s.podFacts(ctx, cl, nodes)),
			fmt.Errorf("%d control plane node(s) could not be scraped, so these figures are short by "+
				"whatever runs on them: %s", len(failed), strings.Join(failed, "; "))
	}
	return ControlPlaneUsage(usage, s.podFacts(ctx, cl, nodes)), nil
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

// DescribeControlPlaneUsage is the line a rung carries about the machines the
// control plane runs on.
//
// The total first, because that is what a node budget is spent against, and the
// largest single process after it, because that is what the node it sits on has
// to fit.
func DescribeControlPlaneUsage(samples []deployedscale.ComponentSample) string {
	if len(samples) == 0 {
		return "the control plane's nodes were **not measured**: nothing was read from them, so this " +
			"rung has no figure for what the machines the fleet's objects live on are doing"
	}

	var (
		resident uint64
		cpu      float64
		largest  deployedscale.ComponentSample
	)
	for _, s := range samples {
		resident += s.Process.ResidentBytes
		cpu += s.Process.CPUSeconds
		if s.Process.ResidentBytes > largest.Process.ResidentBytes {
			largest = s
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d processes on the control plane's nodes: %s resident in total, %.0f CPU-seconds",
		len(samples), humanBytes(resident), cpu)
	fmt.Fprintf(&b, "; largest is %s at %s", largest.Component, humanBytes(largest.Process.ResidentBytes))

	var restarted []string
	for _, s := range samples {
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
