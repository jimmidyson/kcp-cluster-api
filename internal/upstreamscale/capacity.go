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
	"math"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Capacity is what the measured runs say a management cluster of a given
// size can hold, written down as arithmetic so that a run can be told before
// it starts that it is asking for more than its cluster is expected to give.
//
// # Where the numbers come from
//
// Every constant is a fit over the runs of 8 and 9 September 2026 on a
// stacked kubeadm control plane, three nodes, kube-vip in ARP mode, fresh API
// servers, etcd fenced, the managers Guaranteed on the workers, and clusters
// of ten nodes each. Two node sizes were climbed: 32 GiB, where 1000 was
// clean and 1500 was the edge, and 64 GiB, where 3500 was clean and the
// busiest node stood at 54 GiB of 62.
//
// The control plane's ceiling is one node, not the sum. The node holding the
// VIP and the controller manager's lease carries the largest API server and
// does two to three times the CPU of the others, so the model is of that
// node. The hottest API server's resident set fitted to the fleet's Machines
// runs at 13 GiB plus 1 GiB per thousand, with the 64 GiB run's points at
// 10,000 Machines reading 23.7 GiB and at 35,000 reading 49.4. etcd beside it
// holds its heap and its backend file, 1.5 GiB plus three quarters of a
// gigabyte per ten thousand Machines; kube-controller-manager grows at
// 50 MiB per thousand; and the CNI, kube-vip, the scheduler and the kubelet
// take about 1.5 GiB between them. Headroom is 90% of allocatable: the 32 GiB
// node that held 1000 was at 82%, the 64 GiB node that held 3500 at 87%, and
// the 32 GiB node that failed 2000 would have needed 97%.
//
// The managers' ceiling is their own memory limits, which the prepare tool
// sets and GOMEMLIMIT holds them to. What the fleet costs each one is its live
// heap, read with a forced collection, which grew with the clusters at the
// rates below; a Go process whose live heap is more than half its limit is
// spending its CPU collecting rather than reconciling, so the requirement is
// a limit of at least twice the predicted live heap plus what the process
// holds at zero clusters. At 3500 clusters the core manager's 3.3 GiB live
// heap had it at 93% of an 8 GiB limit and the control plane manager's
// 2.2 GiB at 96% of 6 GiB, which is where this line was drawn.
//
// # What it is not
//
// A prediction for this topology and this provider. A real infrastructure
// provider costs what it costs, and the DevCluster provider's slope is that
// of an in-memory backend holding every fake node. A different cluster shape
// changes the object count per cluster, which is why the control plane is
// modelled on Machines rather than clusters. And it says nothing about CPU,
// disk or the network, none of which was the ceiling in any run so far.
type Capacity struct {
	// APIServerBaseBytes and APIServerPerMachineBytes describe the hottest
	// API server's resident set against the fleet's Machines.
	APIServerBaseBytes       float64
	APIServerPerMachineBytes float64
	// EtcdBaseBytes and EtcdPerMachineBytes describe one member's heap and
	// backend file.
	EtcdBaseBytes       float64
	EtcdPerMachineBytes float64
	// ControllerManagerPerMachineBytes is kube-controller-manager's growth.
	ControllerManagerPerMachineBytes float64
	// NodeOverheadBytes is everything else on a control plane node.
	NodeOverheadBytes float64
	// Headroom is the fraction of a node's allocatable memory the model
	// allows the control plane to fill.
	Headroom float64

	// ManagerLiveHeapPerClusterBytes is each manager's live heap growth per
	// cluster, keyed by Controller.Name. A manager not listed is not judged.
	ManagerLiveHeapPerClusterBytes map[string]float64
	// ManagerLiveHeapBaseBytes is what a manager's live heap holds with no
	// fleet, keyed the same way.
	ManagerLiveHeapBaseBytes map[string]float64
	// ManagerLimitOverLiveHeap is how many times its predicted live heap a
	// manager's limit has to be.
	ManagerLimitOverLiveHeap float64
}

const gib = float64(1 << 30)

// MeasuredCapacity is the fit described on Capacity.
func MeasuredCapacity() Capacity {
	return Capacity{
		APIServerBaseBytes:               13 * gib,
		APIServerPerMachineBytes:         1 * gib / 1000,
		EtcdBaseBytes:                    1.5 * gib,
		EtcdPerMachineBytes:              0.75 * gib / 10000,
		ControllerManagerPerMachineBytes: 0.05 * gib / 1000,
		NodeOverheadBytes:                1.5 * gib,
		Headroom:                         0.9,
		ManagerLiveHeapPerClusterBytes: map[string]float64{
			"core":                  1.05 * gib / 1000,
			"kubeadm-bootstrap":     0.27 * gib / 1000,
			"kubeadm-control-plane": 0.63 * gib / 1000,
			"devcluster":            1.0 * gib / 1000,
		},
		ManagerLiveHeapBaseBytes: map[string]float64{
			"core": 0.1 * gib, "kubeadm-bootstrap": 0.05 * gib,
			"kubeadm-control-plane": 0.1 * gib, "devcluster": 0.4 * gib,
		},
		ManagerLimitOverLiveHeap: 2,
	}
}

// ControlPlaneNodeBytes is what the busiest control plane node is expected to
// hold at a fleet of this many Machines.
func (c Capacity) ControlPlaneNodeBytes(machines int) float64 {
	m := float64(machines)
	return c.APIServerBaseBytes + c.APIServerPerMachineBytes*m +
		c.EtcdBaseBytes + c.EtcdPerMachineBytes*m +
		c.ControllerManagerPerMachineBytes*m +
		c.NodeOverheadBytes
}

// MachinesForNode is the largest fleet, in Machines, a control plane node of
// this allocatable memory is expected to hold.
func (c Capacity) MachinesForNode(allocatable uint64) int {
	fixed := c.APIServerBaseBytes + c.EtcdBaseBytes + c.NodeOverheadBytes
	perMachine := c.APIServerPerMachineBytes + c.EtcdPerMachineBytes + c.ControllerManagerPerMachineBytes
	room := float64(allocatable)*c.Headroom - fixed
	if room <= 0 || perMachine <= 0 {
		return 0
	}
	return int(math.Floor(room / perMachine))
}

// ManagerLiveHeapBytes is a manager's predicted live heap at this many
// clusters, and whether the model knows the manager at all.
func (c Capacity) ManagerLiveHeapBytes(name string, clusters int) (float64, bool) {
	slope, ok := c.ManagerLiveHeapPerClusterBytes[name]
	if !ok {
		return 0, false
	}
	return c.ManagerLiveHeapBaseBytes[name] + slope*float64(clusters), true
}

// ClustersForManager is the largest fleet a manager with this memory limit is
// expected to hold, or -1 when the model does not know the manager.
func (c Capacity) ClustersForManager(name string, limit uint64) int {
	slope, ok := c.ManagerLiveHeapPerClusterBytes[name]
	if !ok {
		return -1
	}
	room := float64(limit)/c.ManagerLimitOverLiveHeap - c.ManagerLiveHeapBaseBytes[name]
	if room <= 0 || slope <= 0 {
		return 0
	}
	return int(math.Floor(room / slope))
}

// NodeMemory is one node's name and allocatable memory.
type NodeMemory struct {
	Name        string
	Allocatable uint64
}

// ManagerLimit is one manager's name and memory limit.
type ManagerLimit struct {
	Name  string
	Limit uint64
}

// Expectation is the model applied to one cluster and one ladder.
type Expectation struct {
	Clusters, Machines int
	// SmallestNode is the control plane node with the least allocatable
	// memory, which is the one the ladder's top rung has to fit on.
	SmallestNode NodeMemory
	// NodeNeeds is what that node is expected to hold at the top rung.
	NodeNeeds float64
	// NodeMachines is the fleet it is expected to hold.
	NodeMachines int
	// Managers is each known manager's limit, predicted live heap at the top
	// rung, and the fleet its limit is expected to hold.
	Managers []ManagerExpectation
}

// ManagerExpectation is one manager against the model.
type ManagerExpectation struct {
	ManagerLimit
	LiveHeap float64
	Clusters int
}

// Expect applies the model to a ladder whose top rung is this many clusters
// and Machines, on control plane nodes of these sizes, with managers at these
// limits.
func (c Capacity) Expect(clusters, machines int, nodes []NodeMemory, managers []ManagerLimit) Expectation {
	e := Expectation{Clusters: clusters, Machines: machines, NodeNeeds: c.ControlPlaneNodeBytes(machines)}
	for _, n := range nodes {
		if e.SmallestNode.Name == "" || n.Allocatable < e.SmallestNode.Allocatable {
			e.SmallestNode = n
		}
	}
	e.NodeMachines = c.MachinesForNode(e.SmallestNode.Allocatable)
	for _, m := range managers {
		live, ok := c.ManagerLiveHeapBytes(m.Name, clusters)
		if !ok {
			continue
		}
		e.Managers = append(e.Managers, ManagerExpectation{
			ManagerLimit: m, LiveHeap: live, Clusters: c.ClustersForManager(m.Name, m.Limit),
		})
	}
	sort.Slice(e.Managers, func(i, j int) bool { return e.Managers[i].Name < e.Managers[j].Name })
	return e
}

// Short names what the ladder asks for that the cluster is not expected to
// give, or "" when it is expected to reach the top rung.
func (e Expectation) Short() string {
	var short []string
	if e.SmallestNode.Name != "" && e.Machines > e.NodeMachines {
		short = append(short, fmt.Sprintf("control plane node %s (%s allocatable) is expected to hold about "+
			"%d Machines and the top rung has %d: its API server, etcd and controller manager would want "+
			"about %s of it", e.SmallestNode.Name, humanBytes(e.SmallestNode.Allocatable), e.NodeMachines,
			e.Machines, humanBytes(uint64(e.NodeNeeds))))
	}
	for _, m := range e.Managers {
		if m.Clusters >= 0 && e.Clusters > m.Clusters {
			short = append(short, fmt.Sprintf("the %s manager's %s limit is expected to hold about %d clusters "+
				"and the top rung has %d: its live heap would be about %s, and a limit under twice that is "+
				"a manager collecting rather than reconciling", m.Name, humanBytes(m.Limit), m.Clusters,
				e.Clusters, humanBytes(uint64(m.LiveHeap))))
		}
	}
	return strings.Join(short, "; ")
}

// Describe is the fact a report carries about what its cluster was expected
// to hold, whichever way the run went.
func (e Expectation) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "from the measured runs, a stacked control plane node with %s allocatable is expected to hold "+
		"about %d Machines", humanBytes(e.SmallestNode.Allocatable), e.NodeMachines)
	if e.SmallestNode.Name != "" {
		fmt.Fprintf(&b, " (%s is the smallest here)", e.SmallestNode.Name)
	}
	for _, m := range e.Managers {
		fmt.Fprintf(&b, "; the %s manager's %s limit about %d clusters", m.Name, humanBytes(m.Limit), m.Clusters)
	}
	fmt.Fprintf(&b, ". The top rung asks for %d clusters and %d Machines", e.Clusters, e.Machines)
	if short := e.Short(); short != "" {
		fmt.Fprintf(&b, ", which is past that: %s. The fit is described on upstreamscale.Capacity; a run that "+
			"reaches the top rung cleanly regardless is the evidence that moves it", short)
	} else {
		b.WriteString(", within that")
	}
	return b.String()
}

// ReadNodeMemory lists the control plane nodes with their allocatable memory.
func ReadNodeMemory(ctx context.Context, cl client.Client) ([]NodeMemory, error) {
	var nodes corev1.NodeList
	if err := cl.List(ctx, &nodes, client.HasLabels{ControlPlaneNodeLabel}); err != nil {
		return nil, fmt.Errorf("listing control plane nodes: %w", err)
	}
	out := make([]NodeMemory, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := NodeMemory{Name: nodes.Items[i].Name}
		if memory := nodes.Items[i].Status.Allocatable.Memory(); memory != nil {
			//nolint:gosec // A node's allocatable memory is not negative.
			n.Allocatable = uint64(memory.Value())
		}
		// A node that has not reported its allocatable memory is not a node
		// with none, and judging it as one would warn on every fresh node.
		if n.Allocatable > 0 {
			out = append(out, n)
		}
	}
	return out, nil
}

// ManagerLimits is each controller's memory limit as the prepare tool set it,
// read from the deployment rather than from the table so that a limit raised
// by flag is the one judged.
func ManagerLimits(ctx context.Context, cl client.Client, controllers []Controller) ([]ManagerLimit, error) {
	out := make([]ManagerLimit, 0, len(controllers))
	for _, c := range controllers {
		limit, err := c.DeployedMemoryLimit(ctx, cl)
		if err != nil {
			return nil, err
		}
		if limit == 0 {
			continue
		}
		out = append(out, ManagerLimit{Name: c.Name, Limit: limit})
	}
	return out, nil
}
