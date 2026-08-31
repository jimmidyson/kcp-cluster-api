# Feature Specification: A fleet target a run is driven to

**Feature Branch**: `claude/scale-test-200-clusters-vfin19`

**Created**: 2026-08-31

**Status**: Draft

**Input**: "I'd like to add a scale test with a target of 200 clusters and 50
nodes per cluster. This should keep resource requirements as low as possible
and use in memory dev clusters."

## Purpose

Everything this repository measures today measures a *slope*.
`task test:sweep` walks a small fleet up one workspace at a time and reports
what one more costs; `task test:scale` reports how many workspaces this
environment can create and bind, and how a backlog drains against worker count.
Both are the right instruments for the questions they ask, and neither answers
the one somebody asks when they are about to run this: **can a fleet of the
size I have in mind be hosted, and what did it cost?**

The gap is not hypothetical, and the evidence names it. `capacity.md` quotes
candidate capacities up to 2,020 workspaces from a model fitted to runs of 8 to
64 workspaces, and lists the caveat itself:

> **64 workspaces measured; up to 2,020 quoted.** Every candidate above is an
> extrapolation, and the factor is in the table because it is the number to
> discount by. A confirming run at 256 would cut the largest factor by four.

This feature builds the instrument that takes that run, and states its first
target as a fleet rather than as a workspace count: **200 clusters of 50 nodes
each — 10,000 nodes — measured at two spreads.**

## The target, and why it is stated this way

| Term | Value | Why it is part of the target rather than a detail |
|---|---|---|
| Clusters | 200 | The unit a person asking for capacity has in mind. |
| Nodes per cluster | 50 (3 control plane, 47 workers) | A node count alone does not reproduce a figure: on the in-memory backend a control plane machine costs a fake etcd member and API server pod as well as a Node, where a worker costs a Node and a fake kubelet. Three is the HA shape. |
| Spread | `200x1` **and** `20x10` | Two spreads of one fleet. |

**The two spreads are the point.** Two hundred workspaces holding one cluster
each and twenty holding ten each reach the same 200 clusters and the same
10,000 nodes. What differs is the number of engagements, informer registrations
and shares of the wildcard cache — which is precisely the per-workspace term
this project exists to make small. One shape measures a sum. The pair separates
it into the term that scales with workspaces and the term that scales with
clusters, and that separation is not otherwise available: no sweep here varies
the cluster count inside a workspace.

## Keeping the cost down

The request asked for the lowest resource requirements that still measure the
thing. Four decisions do that work, and each of them is a choice about what is
*not* being measured:

1. **The dev provider's in-memory backend**, not the docker one. A workload
   cluster is a process with a fake API server, etcd and kubelet rather than
   containers. The docker backend would measure Docker; this measures the
   manager. It is the same choice the sweeps make, for the same reason.
2. **One process for all four providers** — core, kubeadm bootstrap, kubeadm
   control plane and dev infrastructure. Four deployments would pay four
   engagements per workspace and build four `ClusterCache`s; co-locating them
   pays one and builds one. It is therefore a **bound**, not a deployment, and
   nothing read off it may be quoted as an installation's cost.
3. **One worker `MachineDeployment` per cluster.** The target is a node count.
   Spreading it over several deployments would add `MachineSet` and
   `MachineDeployment` reconciling the node count does not ask for.
4. **Concurrent provisioning.** Not a resource decision but a feasibility one:
   the fleet sweep takes about 45 seconds per workspace to reach an initialized
   control plane, serially. At 200 workspaces that is over two hours of a run
   that is mostly waiting. Binding and populating a bounded number of
   workspaces at once is what makes the target reachable at all.

## What the instrument does

`TestFleetTarget`, in `test/integration/scale/target_test.go`, driven by
`task test:scale:local`.

- Publishes the fleet's `APIExport` against a real kcp server, creates every
  workspace up front, then walks to the target through **checkpoints** —
  percentages of the workspace target at which it stops, settles and samples.
  Checkpoints are what make the run a curve rather than one number, and the
  curve is what says whether the cost stayed linear on the way to 200.
- Waits at each checkpoint for the whole fleet to reach the end state: **every
  control plane ready with all its replicas, and every `Machine` Ready**. Not
  "control plane initialized", which is the fleet sweep's end state and would
  measure the moment before the worker machines exist — the wrong moment for a
  node count.
- Reports the same `bin/<name>.{md,json}` shape the sweeps write, so a run is
  committable as evidence and readable beside them.

## Outcomes

The three-outcome contract, and it is load-bearing here rather than ceremonial:

| Outcome | When |
|---|---|
| pass | The fleet reached the target, **or** reached part of it. The count is the deliverable. |
| could not run | Nothing reached the end state. There is no measurement to report, and this is never a pass. |
| fail | The instrument itself is broken — an unparseable target, a fixture that would not start. |

A run that hosted 140 of 200 workspaces measured something true about this
environment, and reports it with a note that any figure above 140 is an
extrapolation. That is a result, not a failure. This is the ceiling
measurement's contract, adopted deliberately rather than reinvented.

**It is not a gate.** Like `task test:scale` it sits outside `verify` and
`check`, for the reason that task already gives: making the done-condition
depend on a multi-workspace kcp environment holds unrelated work hostage. There
is no assertion to weaken here, because no requirement states a budget for what
a fleet of a given size may cost, and inventing one in a test would make it fail
for a reason nobody had agreed on.

## Where the shared shape lives

Two suites now measure the whole-fleet wiring: the sweep, for a slope, and this,
for a target. They must measure the same one, so the wiring and the published
type list moved into `internal/fleetfixture` and both suites build from it. Two
copies would let the two disagree about the process they both claim to describe,
and the disagreement would be invisible — both reports would look right.

The arithmetic that decides what to build and reads what a run achieved lives in
`internal/scaletarget`, with unit tests. A target that does not multiply out to
the cluster count somebody asked for should be caught in milliseconds, not by a
run that spent an hour measuring the wrong fleet.

## Out of Scope

- **A measured figure for the 200-cluster target.** This feature ships the
  instrument. The run is the next thing, and until it has been taken there is
  no number: per AGENTS.md a figure that was not measured is reported as "not
  measured", never predicted into the gap. What is in `evidence/` is two
  four-cluster runs that demonstrate the instrument works and that the pair of
  spreads separates the per-workspace term from the per-cluster one — not a
  capacity figure, and its README says so at the top.
- **CPU.** The harness records no CPU time, as `capacity.md` already notes. A
  node count does not change that.
- **The docker backend at this size.** 200 real workload clusters is a
  different feature with a different environment.
- **A departure point.** The checkpoints support a linearity check but the run
  is designed around a target, not around finding where the cost breaks. If a
  departure appears in the checkpoints it is a finding to take back to the
  sweep, which is the instrument for it.
- **Retention and teardown.** The sweep measures what a departing workspace
  fails to give back. Repeating it here would be a second instrument measuring
  one process, which this repository has already decided against.

## Verification

- `task test:unit` covers `internal/scaletarget`.
- `go vet -tags=integration ./...` covers the instrument's wiring.
- `task test:sweep` must still pass: the sweep now builds its fleet shape from
  `internal/fleetfixture`, and a change there is a change to the project's
  primary instrument.
- `task test:scale:local TARGET_SHAPES=2x1 WORKER_MACHINES=2 CONTROL_PLANE_MACHINES=1`
  is the small run that exercises every path in minutes.
