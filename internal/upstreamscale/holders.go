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

	coordinationv1 "k8s.io/api/coordination/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubeVIPLease is the Lease kube-vip elects its VIP holder through, under the
// default name CAREN's template leaves it at. Its holder is the node the
// cluster's API endpoint currently resolves to.
const KubeVIPLease = "plndr-cp-lock"

// controlPlaneLeases are the leases whose holder decides what one control
// plane node's loss costs, in the order the line reads them.
var controlPlaneLeases = []string{"kube-controller-manager", "kube-scheduler", KubeVIPLease}

// Placement is where a control plane's single points of failure sat at one
// moment: the raft leader every write goes through, the leases that decide
// which node runs the controller manager and the scheduler, and the node the
// VIP resolves to.
//
// # Why a rung records this
//
// A control plane node died under a 2000-cluster fleet, and it was the node
// holding the raft leader, the controller manager's lease and kube-vip's VIP at
// once, so the fleet lost its API endpoint, its garbage collector and its
// store's leader in one event. Nothing in the report said so; establishing it
// took three commands by hand afterwards, against a cluster that had already
// re-elected everything. Where the three sit is not a fault — leader election
// puts them wherever it puts them, and a production cluster is no different —
// but it decides what a node's loss costs, and a run that met one needs to know
// what was on it.
type Placement struct {
	// EtcdLeader is the leader member's pod, and EtcdLeaderNode the node it
	// runs on where the store's pods are in the host cluster. Empty when no
	// member claimed leadership at the time.
	EtcdLeader     string            `json:"etcdLeader,omitempty"`
	EtcdLeaderNode string            `json:"etcdLeaderNode,omitempty"`
	Leases         map[string]string `json:"leases,omitempty"`
}

// ReadPlacement reads the leases and finds the leader among members. Errors
// are swallowed: this is a fact attached to a sample, and a lease that is not
// there is a fact of its own.
func ReadPlacement(ctx context.Context, cl client.Client, store StoreLocation, members map[string]Etcd) Placement {
	out := Placement{EtcdLeader: EtcdLeaderOf(members), Leases: map[string]string{}}
	if out.EtcdLeader != "" {
		if pods, err := StorePods(ctx, cl, store); err == nil {
			for i := range pods {
				if pods[i].Name == out.EtcdLeader {
					out.EtcdLeaderNode = pods[i].Spec.NodeName
				}
			}
		}
	}
	for _, name := range controlPlaneLeases {
		var lease coordinationv1.Lease
		if err := cl.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: name}, &lease); err != nil {
			continue
		}
		if holder := holderNode(lease); holder != "" {
			out.Leases[name] = holder
		}
	}
	return out
}

// EtcdLeaderOf names the member that says it is the leader, or "" when none
// does. The first in name order if more than one claims it, which a cluster
// mid-election can briefly say.
func EtcdLeaderOf(members map[string]Etcd) string {
	names := make([]string, 0, len(members))
	for name, m := range members {
		if m.IsLeader {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// holderNode is the node behind a lease's holder identity. The Kubernetes
// components write <node>_<uuid>; kube-vip writes the node.
func holderNode(lease coordinationv1.Lease) string {
	if lease.Spec.HolderIdentity == nil {
		return ""
	}
	node, _, _ := strings.Cut(*lease.Spec.HolderIdentity, "_")
	return node
}

// Describe is the placement line a sample carries, or "" when nothing was
// found to place.
func (p Placement) Describe() string {
	var parts []string
	if p.EtcdLeader != "" {
		part := "etcd leader " + p.EtcdLeader
		if p.EtcdLeaderNode != "" {
			part += " on " + p.EtcdLeaderNode
		}
		parts = append(parts, part)
	}
	for _, name := range controlPlaneLeases {
		holder, ok := p.Leases[name]
		if !ok {
			continue
		}
		if name == KubeVIPLease {
			parts = append(parts, fmt.Sprintf("kube-vip (%s) on %s", name, holder))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s lease on %s", name, holder))
	}
	if len(parts) == 0 {
		return ""
	}
	line := strings.Join(parts, "; ")
	// The case worth a sentence of its own: one node whose loss takes the
	// store's leader, the cluster's controllers and its API endpoint together.
	if p.EtcdLeaderNode != "" && p.Leases["kube-controller-manager"] == p.EtcdLeaderNode &&
		p.Leases[KubeVIPLease] == p.EtcdLeaderNode {
		line += fmt.Sprintf(" — the raft leader, the controller manager and the VIP share %s, so "+
			"losing that node loses all three at once", p.EtcdLeaderNode)
	}
	return line
}
