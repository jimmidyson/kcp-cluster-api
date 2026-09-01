---
title: Fleet Capacity Targets
description: Driving a stated fleet — clusters, nodes and spread — to its end state, to find out whether this environment can host it.
weight: 28
---

[Workspace resource usage](workspace-resource-usage.md) measures a *slope*:
what one more active workspace adds. That is the right instrument for a
coefficient, and the wrong one for the question somebody asks before running
this in anger — **can a fleet of the size I have in mind be hosted, and what
did it cost?**

At three workspaces the sweep says nothing about whether two hundred are
possible. And walking to two hundred one settled sample at a time would spend
the run settling: the fleet sweep takes about 45 seconds per workspace to reach
an initialized control plane, serially, which is over two hours before anything
has been measured.

So there is a second instrument. `TestFleetTarget`, in
`test/integration/scale`, provisions concurrently, samples at checkpoints, and
reports what a stated fleet cost to host.

## Stating a target

A target is three things, and all three are part of it:

```sh
# The default: 200 clusters, 50 nodes each, at two spreads.
task test:scale:local

# Tune it.
task test:scale:local CLUSTERS=32 NODES_PER_CLUSTER=5
task test:scale:local CLUSTERS=200 NODES_PER_CLUSTER=50 CONTROL_PLANE_NODES=1
task test:scale:local CLUSTERS=100 CLUSTERS_PER_WORKSPACE=1     # one spread only
```

| | |
|---|---|
| `CLUSTERS` | how many clusters the fleet holds in total |
| `NODES_PER_CLUSTER` | nodes each cluster reaches, **control plane included** |
| `CONTROL_PLANE_NODES` | how many of those are control plane machines |
| `CLUSTERS_PER_WORKSPACE` | how they are spread; a list runs each spread in turn |

The node count includes the control plane rather than sitting on top of it —
fifty nodes means fifty machines. The split is stated separately because the
two do not cost the same: on the in-memory backend a control plane machine gets
a fake etcd member and API server pod as well as a Node, where a worker gets a
Node and a fake kubelet.

This runs everything in one process and needs no cluster, which is what makes
it the quickest way to a number. To measure the same fleet as an installation
actually deploys it — one `Deployment` per manager, on kind or on any cluster —
see [Measuring a deployed fleet](deployed-fleet-measurement.md).

| Term | Default | Why it is part of the target |
|---|---|---|
| `CLUSTERS` | `200` | The unit somebody sizing a fleet has in mind. |
| `NODES_PER_CLUSTER` | `50` | Control plane included. |
| `CONTROL_PLANE_NODES` | `3` | The HA shape, and the expensive half of a node count. |
| `CLUSTERS_PER_WORKSPACE` | `1,10` | How they spread. Each entry is a sub-test with its own kcp server and its own report. |

The control plane and worker counts are stated separately rather than as one
node count because they do not cost the same. On the in-memory backend a
control plane machine gets a fake etcd member and API server pod alongside its
Node; a worker gets a Node and a fake kubelet. "50 nodes" is not enough to
reproduce a figure; "3 and 47" is.

### Two spreads of one fleet, deliberately

The default runs `200x1` and `20x10`. Both reach 200 clusters and both reach
10,000 nodes. What differs is the number of engagements, informer registrations
and shares of the wildcard cache — which is exactly the per-workspace term this
project exists to make small.

One shape measures a sum. The pair separates it into the part that scales with
workspaces and the part that scales with clusters, and nothing else here does:
no sweep varies the cluster count *inside* a workspace.

## Keeping it affordable

Four decisions, each of them also a statement about what is *not* being
measured:

- **The dev provider's in-memory backend.** A workload cluster is a process
  with a fake API server, etcd and kubelet rather than containers. The docker
  backend would measure Docker. Same choice, same reason, as the sweeps.
- **One process for all four providers.** Core, kubeadm bootstrap, kubeadm
  control plane and dev infrastructure co-located pay one engagement per
  workspace and build one `ClusterCache`, where four deployments pay four and
  build four. This makes the figure a **bound**, not an installation's cost —
  the largest caveat on everything below, and one that cannot be refined away
  in-process. Removing it means running the four as four `Deployment`s in a
  cluster: see [Measuring a deployed fleet](deployed-fleet-measurement.md),
  which is built but has not yet been run.
- **One worker `MachineDeployment` per cluster.** The target is a node count;
  several deployments would add `MachineSet` and `MachineDeployment`
  reconciling it does not ask for.
- **Concurrent provisioning.** `TARGET_CONCURRENCY` workspaces are bound and
  populated at once. Almost all of a workspace's time to its end state is
  waiting, and waiting for two hundred of them one at a time measures the
  harness.

## Checkpoints, and the end state

The run stops at percentages of the workspace target — `TARGET_CHECKPOINTS`,
default `10,25,50`, with the target itself always added as the last one — and
at each one settles and samples. Checkpoints are what make a run a curve rather
than a single number, and the curve is what says whether the cost stayed linear
on the way up.

That linearity is the open question the measurement exists to close.
`capacity.md` fits its candidate capacities to runs of 8 to 64 workspaces and
quotes figures up to 2,020, naming the gap itself: *"A confirming run at 256
would cut the largest factor by four."*

The end state waited for at each checkpoint is **every control plane ready with
all its replicas, and every `Machine` Ready** — the demo's own done-condition.
Not "control plane initialized", which is where the fleet sweep stops: for a
node count that is the moment before the worker machines exist, which is the
wrong moment to measure.

## Outcomes

This is evidence, not a gate. Like the ceiling and throughput measurements
beside it, it sits outside `verify` and `check` — making the done-condition
depend on a fleet-sized kcp environment would hold unrelated work hostage.

| Outcome | When |
|---|---|
| pass | The fleet reached the target, **or** part of it. The count is the deliverable. |
| could not run | Nothing reached the end state. There is no measurement, and this is never a pass. |
| fail | The instrument is broken: an unparseable target, a fixture that would not start. |

A run that hosted 140 of 200 workspaces measured something true about this
environment. It reports 140, with a note that any figure above it is an
extrapolation.

The reported count is the last **settled checkpoint**, not the most workspaces
that were ever up: a figure is only a measurement at a point where the process
was sampled. A checkpoint that timed out three-quarters of the way through is
not lost either — `stoppedBy` carries the poll's last reading, so the report
says both where the last good sample was and how far the failing checkpoint
got. There is no assertion to weaken here, because no requirement
states a budget for what a fleet of a given size may cost — inventing one in a
test would make it fail for a reason nobody had agreed on.

## Reports

Each shape writes `bin/scale-target-<shape>.md` with JSON beside it, in the
same format the sweeps use, so a run can be committed as evidence and read
beside them. `SCALE_REPORT_DIR` puts them somewhere else.

Shapes run as sub-tests of one binary, so every shape after the first takes its
baseline in a process that has already served an earlier one. The slopes are
unaffected — each is fitted across that shape's own active samples — but the
baseline rows are not comparable between shapes, and a report says so when it
applies.

The per-workspace figures are a least-squares fit across the checkpoints, as in
the sweep. The per-cluster figures are that fit *divided* by the clusters per
workspace, and are labelled `Derived` for exactly that reason: the checkpoints
vary the workspace count, not the clusters inside a workspace, so a per-cluster
figure from one shape is what a workspace's clusters cost between them rather
than a slope measured in its own right. Comparing the two spreads is what
separates the terms.

## The shape is shared, on purpose

Two suites now build the whole-fleet wiring — the sweep for a slope, this for a
target — and they must build the same one. Both take it from
`internal/fleetfixture`: the published type list and the four providers' setup
have one definition. Two copies would let the two suites disagree about the
process they both claim to describe, and the disagreement would be invisible,
because both reports would still look right.

The arithmetic that decides what to build and reads what a run achieved is in
`internal/scaletarget`, with unit tests. A target that does not multiply out to
the cluster count somebody asked for is caught in milliseconds rather than by a
run that spent an hour measuring the wrong fleet.
