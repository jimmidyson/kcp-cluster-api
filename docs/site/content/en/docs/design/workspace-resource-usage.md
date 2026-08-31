---
title: Workspace Resource Usage
description: What a manager process costs per active workspace, measured rather than assumed.
weight: 27
---

One provider deployment serves many workspaces. Whether that scales is a
question about a curve: what does one more active workspace add, and what does
a departing one give back?

The [conversion plan][plan]'s "Scalability" section answers it with three
claims. Two were argued from `multicluster-provider`'s source, which is what
[Constitution Principle V][constitution] asks for wherever source settles a
question. This one it does not settle — a type signature cannot show you a
slope — and the third claim ("cheap relative to a duplicated cache, but not
free") had no number in it at all.

This page is the measurement. The instrument is `internal/sweep`; the sweeps
are in `test/integration/sweep`, against a real kcp server.

## Seven shapes, because one number would answer the wrong question

| Group | Shape | What it tells you |
|---|---|---|
| The floor | **Single type** — one controller *per workspace*, watching `Cluster`, whose reconciler does nothing but record that it ran | what this project's own per-workspace seam costs |
| One per deployment | **core-manager**, **kubeadm-bootstrap-manager**, **kubeadm-control-plane-manager**, **dev-infrastructure-manager**, **workspace-manager** — each wiring only its own controllers against its own `APIExport` | what a workspace costs each deployment, and what it costs the installation once they are added up |
| The ceiling | **The whole fleet** — all four providers in one process, building a ClusterClass based cluster in each workspace and taking it to an initialized control plane | what a workspace with a *running cluster* in it costs |

All of them go through the same harness: same instrument, same settling rules,
same assertions, different workload. The difference between them is therefore
attributable.

There is a shape per deployment rather than one for the fleet because
[that is how they deploy](provider-exports.md), and because an aggregate hides
things. A regression in any one provider would show up in a fleet number as
"the fleet got worse", with nothing to point at; a provider that stopped
scaling could sit behind three that had not. Measured apart, each deployment is
its own gate and the total is arithmetic — `task test:sweep` runs the five and
`cmd/sweeptotals` adds them up, refusing to print a total when one is missing.

### What a deployment measured alone cannot do

None of these providers works on its own. The dev provider waits for an owner
reference core writes; the bootstrap and control plane providers wait for a
Cluster an infrastructure provider has marked provisioned. So each sweep writes
those objects itself, standing in for the deployments it is isolating its
subject from.

It is worth being exact about what that costs. The terms that scale —
goroutines, watch streams and discovery requests per workspace — come from the
engagement and the wiring, so they are exactly what a deployment pays. The
request counts are representative rather than exact: a real installation's
other providers would write the same objects, and this process is not billed
for the writes it stood in for.

The deployment and fleet shapes use the dev provider's **in-memory** backend
deliberately. The docker backend provisions real containers per cluster, which
would measure image pulls and Docker rather than the manager, put a container
runtime on the critical path of a measurement that does not need one, and take
minutes per workspace. The in-memory backend runs the same reconcilers through
the same code paths against an in-process workload cluster, so what is measured
is the controller machinery per workspace — the thing that multiplies.

## What "active" means here

Every workspace in a sweep is bound, engaged, and holds objects that its
controllers have demonstrably acted on. What "acted on" means is the strongest
end state that shape can actually reach:

- **Single type**: the reconciler recorded each object.
- **Each deployment**: the end state that deployment can reach alone. Core's
  `Cluster` reconciler adopted the `Cluster` (its finalizer is on the object);
  the dev provider provisioned the infrastructure; the bootstrap provider wrote
  the machine's bootstrap data secret; the control plane provider generated the
  cluster's certificates and created the first replica's `Machine`. None of
  them can go further without the others, which is the point of measuring them
  apart.
- **The whole fleet**: `KubeadmControlPlane`
  `status.initialization.controlPlaneInitialized` became true, which means
  every provider ran in that workspace — core created the `Machine`, the
  bootstrap provider wrote its config, the dev provider stood the machine up,
  and the control plane provider saw it answer — and what they wrote landed in
  that workspace.

Each sample is taken after the goroutine count has held still for two seconds
and the garbage collector has run twice. A sweep that cannot settle fails
rather than reporting a number.

## Single type: 100 workspaces

Five `Cluster` objects each, `GOMAXPROCS=4`, Go 1.26.3, kcp v0.32.3.

| Workspaces | 1 | 11 | 21 | 41 | 61 | 81 | 100 |
|---|--:|--:|--:|--:|--:|--:|--:|
| Goroutines | 64 | 184 | 304 | 544 | 784 | 1024 | 1252 |
| Watch streams | 3 | 3 | 3 | 3 | 3 | 3 | 3 |
| Requests, cumulative | 10 | 10 | 10 | 10 | 11 | 11 | 13 |
| Step time | 4.4s | 2.2s | 2.5s | 2.5s | 2.5s | 2.9s | 2.5s |

Twelve goroutines per workspace, to the goroutine, at every one of a hundred
points — 64 at one workspace, 1,252 at a hundred, no bend anywhere. Three watch
streams throughout.

The request count is the interesting column. It rose by three across the whole
run, and none of that was a workspace: it is the three informers re-opening
their streams when kcp closed them, over fifteen minutes of elapsed time. After
the first workspace engaged, ninety-nine more required **no requests to the
shard at all**.

Engaging the hundredth workspace took as long as engaging the first. That
matters independently of memory: a cost that is flat in bytes and rising in
wall clock has still failed to scale. (The floor of about 2.3s is the
harness's own settling wait, not work.)

Those twelve goroutines are what a controller *per workspace* costs. No
provider deployment in this repository wires one any more — the next shape is
what they do instead — so read this as the cost of the seam, available to a
provider that needs per-workspace wiring, rather than as anyone's bill.

## Per deployment, and the total: 20 workspaces each

One object set per workspace, twenty workspaces, `GOMAXPROCS=4`, Go 1.26.3.

| Deployment | Goroutines/ws | Watch streams/ws | Discovery/ws | Requests/ws | Streams held | Retained/departure |
|---|--:|--:|--:|--:|--:|--:|
| `core-manager` | 2 | 0 | 3 | 7 | 8 | 0 |
| `dev-infrastructure-manager` | 2 | 0 | 3 | 8 | 6 | 0 |
| `kubeadm-bootstrap-manager` | 2 | 0 | 4 | 16 | 7 | 0 |
| `kubeadm-control-plane-manager` | 2 | 0 | 7 | 72 | 7 | 0 |
| `workspace-manager` | 7 | 0 | 3 | 5 | 4 | 1 |
| **An installation of all five** | **15** | **0** | **20** | **108** | **32** | **1** |

`bin/sweep-report-total.md` is this table, regenerated from the five reports by
`cmd/sweeptotals` on every sweep run.

The last row is the only one that is not a provider. `workspace-manager`
reconciles the *permission* to use Cluster API rather than any Cluster API
object — see [Workspace onboarding](workspace-onboarding.md) — which is why its
reconcile traffic is the smallest here and its goroutine figure the largest.
The next section is about that asymmetry.

**Streams held** is the one column that is per deployment rather than per
workspace: it is what that process holds open on the shard whether it serves
one workspace or twenty. Thirty-two is therefore what the shard sees from an
installation at rest, and most of it is the cost of the export split — `Cluster`
is watched by all four providers, once each through its own virtual workspace,
where a single export would have watched it once. Four of the thirty-two are
`workspace-manager`'s, and none of them is a Cluster API type: its own
endpoint slices, and the `apibindings`, `logicalclusters` and `clusterroles`
it reaches through permission claims.

Two of the thirty-two are what serving ClusterClass based clusters costs, and
they are the *whole* of what it costs at this level: the core deployment holds
eight streams where it held six, for the `ClusterClass` and `MachinePool` its
topology controllers watch. Every per-workspace column is unchanged — the same
2 goroutines, the same 3 discovery requests, the same 7 reconcile requests, the
same nothing retained on departure. Four more controllers in the process, and a
workspace costs what it did.

That is the fleet-wide wiring behaving as designed rather than a happy result:
a controller here is registered once for the shard, so adding one adds a fixed
term and no per-workspace term at all. What a managed topology does cost shows
up a level down, where the clusters are — see the fleet shape below.

### Engagement is uniform where the wiring is; reconciling is not

**Two goroutines per workspace in every provider deployment**, exactly, at all
twenty points, and both come back when the workspace leaves. That is the
engagement, and it is the same number whichever provider pays it, because
engaging a workspace is `multicluster-provider`'s work rather than the
provider's. (The control plane deployment's fit reads 2.1: one sample in that
run carried a single extra goroutine.)

**`workspace-manager` pays seven, and the extra five are the wiring rather
than the work.** Its figure is as flat and as linear as the others — 7.0 from
one workspace to twenty — and a goroutine profile diffed across five
engagements attributes it. Two stacks are the ones every deployment pays:
`multicluster-provider`'s `ScopedCluster.Start`, and this repository's
`providerwiring.Wiring.Engage`. The other five appear only here, and all five
are `multicluster-runtime` engaging a controller with a cluster:
`source.clusterKind.Start`, `mcController.Engage` and the goroutine
`startWithinContext` parks beside it, and the `processorListener` run/pop pair
kcp's informers start for the handler that source registers.

The cause is which wiring the controller was built with, and it is worth
stating because it is fixable rather than inherent. The four providers put
their watches on the shard's cache once, through this repository's
`capicontrollerutil.WildcardRegistry`, so engaging a workspace adds the
workspace to a watch that already exists. The role maintainer is built with
plain `mcbuilder.ControllerManagedBy(...).For(&APIBinding{})`, which gives
every engaged cluster its own source and its own handler registration. Moving
it onto the registry the providers use is what would bring it to two — that has
not been done, and until it is, seven is what a workspace costs this
deployment.

It is a sixtyfold improvement on the shape this page used to report, and not a
tuning: it is what making the controllers fleet-wide did. A workspace no longer
brings a controller, a workqueue and a rate limiter with it — the controllers
exist once for the whole shard, and engaging a workspace only adds it to the
set they already serve.

What the deployments do *not* share is what they then do with a workspace:

- **`kubeadm-control-plane-manager` costs an order of magnitude more per
  workspace than core** — 72 requests against core's 7, and 7 discovery
  requests against core's 3. That is its job showing up honestly: per workspace
  it generates four certificate authorities and an admin kubeconfig, then
  creates a `Machine`, an infrastructure machine and a bootstrap config. It is
  the deployment to watch, and in a single fleet-wide figure it was invisible.
- **`kubeadm-bootstrap-manager` is next**, at 16 requests and 4 discovery
  requests per workspace, for the certificates it generates and the bootstrap
  data secret it writes.
- **Core and the dev provider are the cheap ones**, at 7 and 8 requests.

Every one of them is exactly linear in workspaces, and none of them adds a
watch stream or a LIST per workspace. The control plane deployment went from 71
requests at one workspace to 1,431 at twenty — seventy-two per workspace with
no bend — and from 149 goroutines to 187.

Discovery is the term worth re-reading if a provider grows: it is the
`RESTMapper` `multicluster-provider` builds per engaged workspace, paid once at
engagement, and it varies with how many types that provider resolves — three
for core and the dev provider, four for bootstrap, seven for the control plane.
An earlier wiring saw this grow *faster* than the workspace count; with
fleet-wide controllers it does not, in any of the four.

## The whole fleet: 3 workspaces, each with a ClusterClass based cluster

All four providers in one process, one `ClusterClass` and one `Cluster` naming
it per workspace, each taken to an initialized control plane on the in-memory
backend.

| Workspaces | 1 | 2 | 3 |
|---|--:|--:|--:|
| Goroutines | 624 | 681 | 738 |
| Watch streams | 15 | 15 | 15 |
| Discovery, cumulative | 23 | 30 | 37 |
| Requests, cumulative | 472 | 967 | 1,440 |

| Per active workspace | All four deployments, added up | The same four co-located, with a cluster running |
|---|--:|--:|
| Goroutines | 8 | **57** |
| Watch streams | 0 | **0** |
| Retained after departure | 0 | **0** |
| Discovery requests | 17 | **7** |
| Reconcile requests | ~103 | **~484** |

**57 goroutines per workspace**, to the goroutine, at all three points. Fifteen
watch streams throughout, none of them addressed to a tenant's logical cluster.

### What changed when clusters became ClusterClass based

This shape is where it shows, because this is the shape that has clusters in
it. What this page reported before, for the same instrument at four workspaces:

| Per active workspace | Previously reported | Now |
|---|--:|--:|
| Goroutines | 45 | **57** |
| Watch streams | 0 | **0** |
| Retained after departure | 0 | **0** |
| Discovery requests | 7 | **7** |
| Reconcile requests | ~236 | **~484** |

**Read the two columns as before-and-after of one change, not as an experiment
isolating the topology.** Two things moved together: the process now wires four
more controllers, and the cluster it builds is a `Cluster` naming a class
rather than six objects written out by hand. Separating them would need a sweep
of the hand-built shape under the new wiring, and that has not been run — so
"the managed topology costs twelve goroutines" is a reading this measurement
does not support, however plausible it is.

What the measurement does say: a workspace that holds a ClusterClass based
cluster costs twelve more goroutines and roughly twice the reconcile requests
than a workspace holding a hand-built one did under the previous wiring. The
requests are unsurprising — a managed topology server-side applies every object
under the `Cluster` on every reconcile — and they are the term to watch as
clusters per workspace grows.

What did **not** move is the part that decides whether this scales: no watch
stream per workspace, and nothing retained when a workspace leaves. The cost is
per *cluster*, and it is paid where the clusters are.

The gap is not four engagements: co-locating them pays *one*, which is why this
shape's discovery per workspace is 7 where the four deployments together pay
17. What it pays instead is the **workload cluster** — this is the only shape
that stands one up. Each workspace's cluster gets an in-memory API server
serving it, a `ClusterCache` accessor connected to it, and the informers that
accessor runs; a goroutine profile taken across a departure finds exactly that
machinery (see below, where it turned out to be a deadlock rather than a
cost). It is a per-*cluster* cost rather than a per-workspace
one — the cost of the clusters a tenant asked for rather than of serving the
tenant.

Read against the per-deployment total, the two bracket a real installation:

- **Serving a workspace**: 15 goroutines and 20 discovery requests, spread
  across five processes, before any cluster exists in it. Eight of the
  goroutines and 17 of the requests are the four providers; the rest is
  `workspace-manager`, and see above for why its share is the one that could
  be made smaller by wiring rather than by doing less.
- **Running a ClusterClass based cluster in it**: 57 goroutines in one process
  — and that term lands wherever the infrastructure provider runs, not spread
  across the others.

Neither is a capacity model. What they establish is that the *workspace* term
is small and flat everywhere, and that what actually scales a process is how
many clusters it is running, not how many workspaces it is serving. A capacity
plan should size the infrastructure deployment against clusters and the other
three against workspaces.


## The claims, settled

**Watches are O(types), not O(types × workspaces) — holds, in every shape.**
Three streams served a hundred single-type workspaces; six served the dev
provider's twenty and eight served core's; seven served the bootstrap and
control plane deployments' twenty; the full fleet's fifteen did not move
either. Not
one stream in any sweep was addressed to a tenant's own logical cluster, which
is the sharper form of the claim: a flat count would also be produced by a
process that opened every per-tenant watch up front, and that is not what this
is. Both are asserted, so a regression fails the build.

**No cache or transport duplicated per workspace — holds.** No per-workspace
LISTs in any shape, and no new connections. The per-workspace `RESTMapper` is
the one duplicated thing, and its cost is discovery traffic rather than a
cache: nothing in the single-type shape, and three to seven requests per
workspace in a deployment depending on how many types that provider resolves.
Wiring the four topology controllers did not move it — core still pays three.

**Per-workspace controller overhead — quantified, and mostly gone.** 12
goroutines for one controller *per workspace* on one type, measured to a
hundred; **2** for a whole provider deployment, measured to twenty in each of
the four, because their controllers are per shard rather than per workspace.
`workspace-manager` sits between them at **7**, measured to twenty — fleet-wide
controllers wired per cluster rather than per shard; see above. A
replica serving a hundred workspaces holds on the order of 200 goroutines for
them whichever provider it runs, and an installation running all four plus
`workspace-manager` pays about 1,500 across its processes. That is the number that decides how many
workspaces one replica should serve.

**Why LIST is zero.** The initial read is not a separate LIST any more. A
current client-go informer opens a watch with `sendInitialEvents=true` and
receives its starting state through the stream. The instrument classifies that
as `watch-list` so that a report showing no LISTs does not look like a broken
measurement — and counts a stream by what is being watched rather than by how
it was opened, because an informer whose watch the shard closes re-opens it as
a plain watch. Without that, a sweep long enough to cross a re-establishment
reports watch growth that is really elapsed time. The hundred-workspace run is
where that showed up.

## What a workspace does not give back

Every provider shape gives back everything: **zero goroutines retained per
departed workspace**, in all four deployments over twenty departures each, and
in the fleet. `workspace-manager` retains **one**, for the reason in the
section below.

That is new, and it follows from the same change as the two-goroutine figure.
The cost that used to be retained was **two goroutines per event-handler
registration**:

- controller-runtime's `Kind` source adds an event handler to the informer it
  watches through (`pkg/internal/source/kind.go`) and has no path that ever
  removes it. In an ordinary deployment that is harmless, because the informer
  and the controller are stopped together.
- Here they are not. The informer belongs to the wildcard cache shared by every
  workspace, so it outlives any one workspace's controllers — and the handler,
  with the `processorListener` run/pop pair kcp's informers start for it,
  outlives them too.

Fleet-wide controllers register their handlers **once for the process**, not
once per workspace, so a workspace's departure has no registration to leak. The
single-type shape still retains 2 per departure, because it still wires a
controller per workspace: the leak is a property of that seam, not of the
provider. Both numbers are asserted, so neither can grow unnoticed.

`workspace-manager`'s one is a third seam, and a different one. Its controller
*is* fleet-wide, but `multicluster-runtime` engages it per cluster, wrapping
each source in `startWithinContext` — which starts a goroutine parked on the
**controller's** context, waiting to cancel the cluster's. Disengaging cancels
the cluster's context, which that goroutine is not waiting on, so it survives
until the controller stops: process lifetime. One source, one goroutine per
departed workspace, measured on multicluster-runtime v0.24.1. It is the same
wiring choice that makes its engagement seven rather than two, and the same
change would retire both.

The fleet shape retained **24 goroutines per departed workspace** until a
goroutine profile said what they were, and what they were was not a leak but a
**deadlock**.

Subtracting the profile taken at one active workspace from the one with one
workspace left showed three client-go informers still running — reflector,
resync, `processLoop`, `sharedProcessor`, and the controller-runtime
`Informers.Start` and `MergeChans` goroutines that go with them — which is a
`ClusterCache` accessor for a workload cluster, still connected after the
workspace that owned it had gone. Alongside them sat the cause: one
`clustercache.sendEventsToClusterSources` goroutine blocked acquiring
`clusterSourcesLock`, and another blocked *inside* the send while holding it.

`sendEventsToClusterSources` took the lock and then sent each event on an
unbuffered channel without releasing it. The send waits for the source's
consumer to take the event, and `clusterSources` is append-only with nothing to
tell the cache when a consumer's goroutine has ended — so a source whose
consumer had stopped reading blocked the sender forever, and with it every
connect, disconnect and `GetClusterSource` for every cluster in the fleet. The
accessors were not retained because nothing removed them; they were retained
because the reconcile that removes them could no longer run.

The fork now decides what to send under the lock and sends outside it, and
bounds each send so it cannot wait for a receive that is not coming. **Every
shape now retains zero goroutines per departed workspace**, the fleet included,
and each asserts it: a shape whose controllers are fleet-wide is allowed no
retention at all, and only the single-type shape — the one that still wires a
controller per workspace — carries a budget for the handler registrations
controller-runtime's `Kind` source never removes.

The remaining fixed cost is released when the wildcard cache itself stops: kcp
empties the `APIExportEndpointSlice` when the last `APIBinding` goes, the
provider stops watching that endpoint, and the shared cache goes with it. That
is the large drop at the last departure in every sweep.

### How the figure is arrived at

Retention is a difference between two samples: a teardown sample and the one
taken at the same workspace count on the way up. Both describe a process
serving *k* workspaces, so what separates them is what the workspaces that left
did not give back.

Two things stop that difference from being a coin flip. **Every workspace count
with both ends contributes an estimate**, and the lowest is taken — a goroutine
that has not gone yet inflates an estimate and a transient inflates it, while
nothing pushes one below what is genuinely still held, so a shape that really
retains per departure retains it in every pair while a one-off survives in
none. And **the fleet sweep runs three workspaces rather than two**, because
two give exactly one pair and therefore nothing to check that pair against.
That is not hypothetical: the assertion failed three times at 2.0 and 3.0 on a
single pair whose two ends differed by a handful of goroutines, on a process
whose heap had grown by half between them.

When the budget is exceeded the run prints the stacks that grew between the two
samples the figure came from, which is the same subtraction that found the
deadlock above — done by CI on the failing run rather than by hand on a later
one.

**What fixing the per-workspace seam would take**, for a provider that still
needs one: the per-workspace manager handed to a `SetupFunc` would have to hand
out a cache that records the handler registrations made through it and removes
them (`cache.Cache` already exposes `RemoveEventHandler`) when the workspace is
disengaged — the same shape as the `Add` interposition that already binds
runnables to a workspace's lifetime. That is a change to this project's own
seam, not to upstream. It is not built, and with no deployment using the seam
any more the trigger for building it has receded.

## Unbinding a workspace that still holds clusters

Deleting an `APIBinding` makes kcp delete every object of every bound type, all
at once. Cluster API's teardown is a sequence — a `Cluster` deletes its control
plane, which deletes its `Machine`s, which delete the `DevMachine`s underneath
them — and removed out of order it used not to complete at all. The
`DevMachine` reconciler needs its `Machine`, its `Cluster` and its `DevCluster`,
and when any of them had already gone it returned without requeueing or errored
forever. Its finalizer stayed, which held the `Machine`, which held the control
plane, which held the `Cluster`, which held the `APIBinding`. The workspace
never disengaged and nothing in it could be cleaned up.

That was found by measuring it: the fleet sweep timed out in its departure
phase, every time, until the shape was changed to wind each workspace down
before unbinding.

**It is fixed in the fork**, and in two parts, because the obvious half is not
enough on its own:

- A deleting `DevCluster` now waits for its `DevMachine`s. It has to outlive
  them — the docker backend deletes only the load balancer with the
  `DevCluster` and leaves each machine's container to that machine's own
  reconcile — so simply releasing the machines would have traded a deadlock for
  a container leak. With the order enforced by the controller rather than by
  its callers, deletion reaches each backend with everything it needs.
- A deleted `DevMachine` whose `Machine` or `Cluster` has gone anyway releases
  its finalizer. Backend state is keyed by the cluster, so once the `Cluster`
  is gone there is nothing left to reach and holding on only blocks whatever
  owns the object.

`test/integration/teardown` is the check from the outside: a workspace with a
running control plane, every `APIBinding` deleted without touching the cluster,
and every binding required to finish deleting. It takes about two minutes,
against a deadlock that never resolved.

The sweeps still delete their clusters before unbinding. That is measurement
hygiene rather than necessity now — a departure sample taken while a full
cluster teardown is in flight measures the teardown — and it is also what a
tenant winding a workspace down would do.

## Running it

```sh
task test:sweep                                    # all seven shapes, gated defaults
SWEEP_WORKSPACES=100 task test:sweep               # the wide single-type run
SWEEP_CONTROLPLANE_WORKSPACES=20 task test:sweep   # a wider run of one deployment
SWEEP_FLEET_WORKSPACES=4 task test:sweep           # a wider fleet run
```

The shapes are sized independently on purpose, so widening a cheap sweep cannot
silently widen an expensive one: `SWEEP_WORKSPACES`, `SWEEP_CORE_WORKSPACES`,
`SWEEP_BOOTSTRAP_WORKSPACES`, `SWEEP_CONTROLPLANE_WORKSPACES`,
`SWEEP_DEV_WORKSPACES`, `SWEEP_WORKSPACE_WORKSPACES` and
`SWEEP_FLEET_WORKSPACES`, each with an `_OBJECTS` counterpart. Reports land in
`bin/sweep-report{,-core,-bootstrap,-controlplane,-dev,-workspace,-fleet}.md`,
with JSON beside each, and `cmd/sweeptotals` writes
`bin/sweep-report-total.md` from the five deployment reports at the end of
every run. It fails when one of them is missing, because a sum of four of the
five is not what an installation pays and must not read as though it were.

The sweep is a step of `task verify` in its own right, so a run that could not
start a kcp server reports "could not run" rather than passing quietly. No
shape needs a container runtime.

Two more knobs exist for investigating a number that has moved:

- `SWEEP_GOROUTINE_PROFILE=<dir>` writes a goroutine profile beside every
  sample. Two profiles subtracted by hand are how the retention above was
  attributed to a specific event handler rather than guessed at, and how
  `workspace-manager`'s seven were split into the two every deployment pays and
  the five its wiring adds.
- `SWEEP_REPORT_DIR=<dir>` puts the reports somewhere other than `bin/`.

## Asking the other question

This page measures a slope. To ask instead whether a fleet of a stated size can
be hosted at all — 200 clusters of 50 nodes, say — see
[Fleet capacity targets](fleet-capacity-targets.md), which drives a stated
target to its end state and reports what it cost. The two instruments overlap
deliberately at their ends: the per-workspace slope a target run reports across
its checkpoints should agree with the sweep's, and a disagreement is a finding
about one of them.

## What these numbers are not

They are the shape of a curve measured on one machine against a single-shard
kcp server. The slopes are the point, and they are stable across runs and
across widths; the absolute figures are not a capacity model, which is why
every report records the conditions of its run alongside them.

Heap is reported rather than asserted, and should be read with care for two
reasons. The process being measured contains the harness measuring it,
including one fixture client per workspace, so the figure is an upper bound on
the manager's own retention rather than a clean reading of it. And a
per-workspace heap figure is a least-squares fit: the two certificate-writing
deployments grow their live heap in steps — the bootstrap one gained 13 MiB
between ten and twenty workspaces and about 1 MiB across the rest of the range
— so their fitted slope is a step divided by the swept range rather than a
per-workspace cost. Neither step has been attributed; goroutines and traffic
are the columns to plan against. Goroutines and traffic do not have
this problem — client-go shares one transport across configs that differ only
in path.

[plan]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md
[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
