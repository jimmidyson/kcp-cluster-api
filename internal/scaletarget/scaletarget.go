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

// Package scaletarget describes a fleet a scale run is aiming at, and reads
// what a run achieved against it.
//
// The measurement itself needs a real kcp server and takes a long time; the
// arithmetic that says what to build and whether the run got there does not.
// Keeping the two apart is the same separation internal/scaleharness makes and
// for the same reason: a target that is wrong — a shape that does not multiply
// out to the cluster count somebody asked for, a checkpoint list that never
// reaches the target — should be caught by a unit test in milliseconds rather
// than by a sixty-minute run that measured the wrong fleet.
package scaletarget

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jimmidyson/kcp-cluster-api/internal/verify"
)

// Shape is how a cluster count is spread over workspaces.
//
// Two runs holding the same number of clusters can cost very different
// amounts depending on how they are spread, and the difference is the whole
// subject of this project: a workspace costs an engagement, a set of informer
// registrations and a share of the wildcard cache, and a cluster inside an
// already-engaged workspace costs none of those. So the spread is part of the
// target rather than an implementation detail of reaching it, and a figure
// quoted without it cannot be compared with another.
type Shape struct {
	// Workspaces is how many workspaces bind the export.
	Workspaces int
	// ClustersPerWorkspace is how many Clusters each of them holds.
	ClustersPerWorkspace int
}

// Clusters is the total the shape multiplies out to.
func (s Shape) Clusters() int { return s.Workspaces * s.ClustersPerWorkspace }

// String is the form ParseShapes accepts, so a shape read out of a report can
// be handed back to a run.
func (s Shape) String() string {
	return strconv.Itoa(s.Workspaces) + "x" + strconv.Itoa(s.ClustersPerWorkspace)
}

// Validate rejects a shape that describes no fleet.
func (s Shape) Validate() error {
	var errs []error
	if s.Workspaces < 1 {
		errs = append(errs, fmt.Errorf("workspaces is %d: a shape holds at least one workspace", s.Workspaces))
	}
	if s.ClustersPerWorkspace < 1 {
		errs = append(errs, fmt.Errorf("clusters per workspace is %d: a shape holds at least one cluster in each workspace",
			s.ClustersPerWorkspace))
	}
	return errors.Join(errs...)
}

// ParseShapes reads a comma-separated list of `<workspaces>x<clusters>`.
//
// The multiplication is written out rather than derived from a cluster count
// and a spread, because the two readings of "200 clusters, ten per workspace"
// — twenty workspaces, or two hundred workspaces holding ten each — differ by
// a factor of ten in what they cost, and a run that measured the second while
// its report said the first would be wrong in the direction nobody checks.
func ParseShapes(s string) ([]Shape, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("no shapes given")
	}

	var shapes []Shape
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		ws, cpw, ok := strings.Cut(part, "x")
		if !ok {
			return nil, fmt.Errorf("shape %q is not <workspaces>x<clustersPerWorkspace>", part)
		}
		workspaces, err := strconv.Atoi(strings.TrimSpace(ws))
		if err != nil {
			return nil, fmt.Errorf("shape %q: workspace count: %w", part, err)
		}
		clusters, err := strconv.Atoi(strings.TrimSpace(cpw))
		if err != nil {
			return nil, fmt.Errorf("shape %q: clusters per workspace: %w", part, err)
		}
		shape := Shape{Workspaces: workspaces, ClustersPerWorkspace: clusters}
		if err := shape.Validate(); err != nil {
			return nil, fmt.Errorf("shape %q: %w", part, err)
		}
		shapes = append(shapes, shape)
	}
	return shapes, nil
}

// Machines is what one cluster's topology asks for.
type Machines struct {
	// ControlPlane is the KubeadmControlPlane's replica count.
	//
	// It costs more than a worker of the same count, and visibly so on the
	// in-memory backend: each control plane machine gets a fake etcd member
	// and API server pod alongside its Node, where a worker gets a Node
	// alongside a fake kubelet. A node count quoted without the split is
	// therefore not enough to reproduce a figure.
	ControlPlane int
	// Workers is the single MachineDeployment's replica count.
	//
	// One deployment rather than several: the target is a node count, and
	// spreading it over several deployments would add MachineSet and
	// MachineDeployment reconciling that the node count does not ask for.
	Workers int
}

// PerCluster is the node count one cluster reaches.
func (m Machines) PerCluster() int { return m.ControlPlane + m.Workers }

// Validate rejects a topology Cluster API would not bring up.
func (m Machines) Validate() error {
	var errs []error
	if m.ControlPlane < 0 || m.Workers < 0 {
		errs = append(errs, fmt.Errorf("machine counts are negative: %d control plane, %d workers", m.ControlPlane, m.Workers))
	}
	// Workers join a control plane, so asking for them without one asks for a
	// cluster that never converges rather than a cheaper one. The demo's own
	// options reject the same combination.
	if m.Workers > 0 && m.ControlPlane < 1 {
		errs = append(errs, fmt.Errorf("%d workers with no control plane machine: workers join a control plane, so this never converges", m.Workers))
	}
	if m.PerCluster() < 1 {
		errs = append(errs, errors.New("the cluster has no machines: a node count of zero measures cluster objects, not nodes"))
	}
	return errors.Join(errs...)
}

// Checkpoints turns a list of percentages into the workspace counts a run
// stops at to take a sample.
//
// Samples on the way up are what make a run more than one number. A single
// measurement at the target says what the target cost; a series says whether
// the cost stayed linear getting there, which is the question
// `specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md` leaves
// open above 64 workspaces and the reason a run this size is worth taking.
//
// The target is always the last checkpoint whether or not the percentages say
// so, because a run that stopped short of what it was asked for and reported
// its last checkpoint as the target would be quietly wrong.
func Checkpoints(workspaces int, percents []int) ([]int, error) {
	if workspaces < 1 {
		return nil, fmt.Errorf("workspaces is %d: there is nothing to check point", workspaces)
	}

	seen := make(map[int]struct{}, len(percents)+1)
	out := make([]int, 0, len(percents)+1)
	add := func(n int) {
		if _, dup := seen[n]; dup {
			return
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}

	for _, pct := range percents {
		if pct < 1 || pct > 100 {
			return nil, fmt.Errorf("checkpoint %d%% is outside 1-100", pct)
		}
		// Rounded up, so a small percentage of a small fleet is one workspace
		// rather than none: a checkpoint that samples nothing is a sample
		// taken at the previous checkpoint under a different label.
		at := (workspaces*pct + 99) / 100
		add(at)
	}
	add(workspaces)

	slices.Sort(out)
	return out, nil
}

// ParsePercents reads a comma-separated percentage list.
func ParsePercents(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		pct, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("checkpoint %q is not a percentage: %w", part, err)
		}
		out = append(out, pct)
	}
	return out, nil
}

// Plan is one shape, its topology and the checkpoints a run of it stops at.
type Plan struct {
	Shape       Shape
	Machines    Machines
	Checkpoints []int
}

// NewPlan validates a target and works out where a run of it samples.
func NewPlan(shape Shape, machines Machines, percents []int) (Plan, error) {
	if err := errors.Join(shape.Validate(), machines.Validate()); err != nil {
		return Plan{}, err
	}
	checkpoints, err := Checkpoints(shape.Workspaces, percents)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Shape: shape, Machines: machines, Checkpoints: checkpoints}, nil
}

// Nodes is how many machines the whole fleet reaches at the target.
func (p Plan) Nodes() int { return p.Shape.Clusters() * p.Machines.PerCluster() }

// Verdict is what a run achieved, read against what it was aiming at.
type Verdict struct {
	// Outcome is the project's three-outcome contract, not a boolean. See
	// AGENTS.md, "Done is a command": a run that could not be taken is never
	// a pass, and is not the same thing as a run that fell short.
	Outcome verify.Outcome
	// Reached and Target are workspace counts.
	Reached int
	Target  int
	// StoppedBy is why the run went no further, in the words of whatever
	// stopped it.
	StoppedBy string
	// Note is what a reader has to be told about the figures, and is empty
	// when there is nothing to say.
	Note string
}

// Classify reads a run's result against its target.
//
// Falling short is a result, not a failure: the number is the deliverable, and
// a run that hosted 140 of 200 workspaces measured something true about this
// environment. Reaching nothing is the one outcome that is not a result — there
// is no measurement to report — and it is "could not run" rather than a
// measurement of zero.
func Classify(reached, target int, stoppedBy string) Verdict {
	v := Verdict{Reached: reached, Target: target, StoppedBy: stoppedBy}
	switch {
	case reached < 1:
		v.Outcome = verify.OutcomeCouldNotRun
		v.Note = "no workspace reached the target state, so nothing was measured"
	case reached < target:
		v.Outcome = verify.OutcomePass
		v.Note = fmt.Sprintf("the target of %d workspaces was not reached: %d did. Any figure quoted above %d "+
			"from this run is an extrapolation", target, reached, reached)
	default:
		v.Outcome = verify.OutcomePass
	}
	return v
}
