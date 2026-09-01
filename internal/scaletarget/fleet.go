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

package scaletarget

import (
	"errors"
	"fmt"
)

// Fleet is a target stated the way somebody asks for one: so many clusters,
// so many nodes in each.
//
// [Shape] and [Machines] are how a run is driven — workspaces multiplied by
// clusters, control plane replicas separated from workers — and both of those
// are things you have to know this project to write down. A person sizing a
// fleet knows how many clusters they want and how big each one is, so that is
// what this takes, and it derives the rest.
type Fleet struct {
	// Clusters is the total across every workspace.
	Clusters int
	// NodesPerCluster is the whole node count of one cluster, control plane
	// included. It is the number somebody says out loud — "fifty nodes" means
	// fifty machines, not fifty on top of a control plane.
	NodesPerCluster int
	// ControlPlaneNodes is how many of those are control plane machines.
	//
	// Part of the target rather than a detail of it: on the in-memory backend
	// a control plane machine costs a fake etcd member and API server pod as
	// well as a Node, where a worker costs a Node and a fake kubelet. Two runs
	// at the same node count and a different split are not the same
	// measurement.
	ControlPlaneNodes int
	// ClustersPerWorkspace is how the clusters are spread, and may name more
	// than one spread.
	//
	// A list because the interesting comparison is one fleet at two spreads:
	// 200 clusters one-per-workspace and ten-per-workspace hold the same
	// clusters and the same nodes, and the difference between what they cost
	// is the per-workspace term this project exists to make small. One entry
	// measures a sum; two separate it.
	ClustersPerWorkspace []int
}

// Machines is the topology one of this fleet's clusters is built with.
func (f Fleet) Machines() Machines {
	return Machines{ControlPlane: f.ControlPlaneNodes, Workers: f.NodesPerCluster - f.ControlPlaneNodes}
}

// Nodes is the whole fleet's node count.
func (f Fleet) Nodes() int { return f.Clusters * f.NodesPerCluster }

// Plans turns the fleet into one plan per spread.
func (f Fleet) Plans(percents []int) ([]Plan, error) {
	if f.Clusters < 1 {
		return nil, fmt.Errorf("clusters is %d: a fleet holds at least one", f.Clusters)
	}
	if f.NodesPerCluster < 1 {
		return nil, fmt.Errorf("nodes per cluster is %d: a cluster with no nodes measures cluster objects, not nodes",
			f.NodesPerCluster)
	}
	if f.ControlPlaneNodes > f.NodesPerCluster {
		return nil, fmt.Errorf("%d control plane nodes in a %d node cluster: the control plane is part of the node "+
			"count, not on top of it", f.ControlPlaneNodes, f.NodesPerCluster)
	}
	if len(f.ClustersPerWorkspace) == 0 {
		return nil, errors.New("no spread given: say how many clusters go in each workspace")
	}
	if err := f.Machines().Validate(); err != nil {
		return nil, err
	}

	plans := make([]Plan, 0, len(f.ClustersPerWorkspace))
	for _, per := range f.ClustersPerWorkspace {
		if per < 1 {
			return nil, fmt.Errorf("clusters per workspace is %d: a workspace holds at least one", per)
		}
		// Refused rather than rounded. Rounding up gives a run that holds more
		// clusters than was asked for and reports the number that was asked
		// for, which is the kind of wrong nobody checks.
		if f.Clusters%per != 0 {
			return nil, fmt.Errorf("%d clusters do not divide into %d per workspace: "+
				"pick a spread that divides, or a cluster count that does", f.Clusters, per)
		}
		plan, err := NewPlan(Shape{Workspaces: f.Clusters / per, ClustersPerWorkspace: per}, f.Machines(), percents)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}
