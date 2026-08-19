---
title: Workspace Resource Usage
description: What one manager process costs per active workspace, measured rather than assumed.
weight: 27
---

{{% pageinfo color="warning" %}}
**These figures describe an earlier wiring.** They were taken against
per-workspace controllers wired by `coremanager.SetupReconcilers`, in a process
running the core and dev infrastructure providers together. Both halves of that
have since changed: controllers are fleet-wide (a workspace was then measured at
**2.0 goroutines**, flat to a hundred and fully reclaimed on departure), and
each provider is a deployment of its own consuming its own `APIExport`, so a
workspace is engaged once per deployment. The method below is current and the
instrument still runs; the numbers are a record of what the wiring cost when it
was that wiring. See [One APIExport per provider](provider-exports.md).
{{% /pageinfo %}}

One `core-manager` process serves many workspaces. Whether that scales is a
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

## Two shapes, because one number would answer the wrong question

| Shape | What it wires | What it tells you |
|---|---|---|
| **Single type** | one controller watching `Cluster`, reconciler does nothing but record that it ran | what the wiring and the shared cache cost per workspace |
| **Core reconciler set** | `coremanager.SetupReconcilers` — ClusterCache, `Cluster`, `Machine`, `DevCluster`, `DevMachine` — on the dev provider's in-memory backend | what a deployment actually pays |

Both run through the same harness: same instrument, same settling rules, same
assertions, different workload. The difference between them is therefore
attributable. The first isolates a change in this project's own code from a
change in what upstream's reconcilers watch; only the second sizes a
deployment.

The core shape uses the dev provider's **in-memory** backend deliberately. The
docker backend provisions real containers per cluster, which would measure
image pulls and Docker rather than the manager, put a container runtime on the
critical path of a measurement that does not need one, and take minutes per
workspace. The in-memory backend runs the same reconcilers through the same
code paths against an in-process workload cluster, so what is measured is the
controller machinery per workspace — the thing that multiplies.

## What "active" means here

Every workspace in a sweep is bound, engaged, and holds objects that its
controllers have demonstrably acted on. In the single-type shape that is the
reconciler recording each object; in the core shape it is `Cluster`
`status.initialization.infrastructureProvisioned` becoming true, which means
the `Cluster` reconciler resolved a contract-versioned reference, the
`DevCluster` reconciler acted on it, and the status landed back in that
workspace.

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

## Core reconciler set

One `Cluster`/`DevCluster` pair per workspace, provisioned through the
in-memory backend.

| Workspaces | 1 | 5 | 10 | 15 | 20 |
|---|--:|--:|--:|--:|--:|
| Goroutines | 251 | 811 | 1511 | 2211 | 2911 |
| Watch streams | 8 | 8 | 8 | 8 | 8 |
| Discovery, cumulative | 13 | 35 | 71 | 118 | 172 |
| Requests, cumulative | 45 | 159 | 310 | 472 | 641 |

| Per active workspace | Single type | Core reconciler set |
|---|--:|--:|
| Goroutines | 12 | **140** |
| Watch streams | 0 | **0** |
| Retained after departure | 2 | **30** |
| Discovery requests | 0 | **5–11, and rising — see below** |

140 goroutines per workspace, exactly linear to twenty: 251 at one workspace,
2,911 at twenty. That is the figure to size a replica against, and it is a
little over eleven times the single-type shape, which is what five controllers
instead of one buys.

The eight streams are the two the provider needs for discovery (`apibindings`
across all workspaces, `apiexportendpointslices` in the workspace that owns the
export) plus one per watched type: `clusters`, `machines`, `machinesets`,
`machinedeployments`, `devclusters`, `devmachines`. Eight at twenty workspaces,
eight at one.

Two costs appear here that the single-type shape does not have. Both are per
workspace rather than per shard, and one of them is the only quantity in either
sweep that is not flat or linear:

- **Reconcile writes**: about 30 requests per workspace, which is the
  reconcilers doing their job on that workspace's objects. Writes scale with
  objects, which is the point of having them; the claim under test is about
  watches and LISTs, and those stayed flat.
- **Discovery, which grows faster than the workspace count.**
  `multicluster-provider` builds a `RESTMapper` per engaged workspace, and this
  reconciler set resolves enough types to make it do work. The per-workspace
  cost is not constant: it was about 5.5 requests per workspace over the first
  five, and about 10.8 over the last five of twenty. The likely mechanism is
  mappers refreshing on a miss as reconcile activity across all workspaces
  grows, but that has not been confirmed against source and is stated here as
  an observation rather than an explanation.

  In absolute terms this is still small — 172 requests to serve twenty
  workspaces, against a shard that saw eight watch streams — so it is not a
  problem today. It is the one curve here that would matter at a much larger
  workspace count, it is recorded on every run as
  `discoveryRequestsPerWorkspace`, and it is worth confirming before anyone
  plans a replica around hundreds of production-shape workspaces.

## The claims, settled

**Watches are O(types), not O(types × workspaces) — holds, in both shapes.**
Three streams served a hundred single-type workspaces; eight served the full
reconciler set. Not one stream in either sweep was addressed to a tenant's own
logical cluster, which is the sharper form of the claim: a flat count would
also be produced by a process that opened every per-tenant watch up front, and
that is not what this is. Both are asserted, so a regression fails the build.

**No cache or transport duplicated per workspace — holds.** No per-workspace
LISTs in either shape, and no new connections. The per-workspace `RESTMapper`
is the one duplicated thing, and its cost is discovery traffic rather than a
cache: nothing in the single-type shape, and the growing figure above in the
production one.

**Per-workspace controller overhead — quantified.** 12 goroutines for one
controller on one type, measured to a hundred workspaces; 140 for the five
controllers `cmd/core-manager` wires, measured to twenty. Both exactly linear,
with no bend at the top of either range. A process serving a hundred workspaces
of the production shape would hold on the order of 14,000 goroutines — large,
not a per-shard cost, and the number that decides how many workspaces one
replica should serve, which is what nobody could previously state.

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

The wiring in `internal/providerwiring` reclaims its own per-workspace cost
completely: the unit-tier sweep measures exactly two goroutines per workspace
and exactly zero left behind after sixteen have come and gone, with no kcp
server involved.

Below that layer, **two goroutines per event-handler registration** survive the
workspace that made them:

- controller-runtime's `Kind` source adds an event handler to the informer it
  watches through (`pkg/internal/source/kind.go`) and has no path that ever
  removes it. In an ordinary deployment that is harmless, because the informer
  and the controller are stopped together.
- Here they are not. The informer belongs to the wildcard cache shared by every
  workspace, so it outlives any one workspace's controllers — and the handler,
  with the `processorListener` run/pop pair kcp's informers start for it,
  outlives them too.

The unit is the registration, not the type: several controllers commonly watch
the same type and each registers its own handler. That is why the single-type
shape retains 2 (one registration) and the core reconciler set retains 30
(fifteen registrations across six types). Both were exact and reproducible
across runs, and both are asserted, so the number cannot grow unnoticed.

Departures otherwise return what they cost — 10 of 12 goroutines in the
single-type shape, 110 of 140 in the core shape, at every step of every
teardown. The retained share is released when the wildcard cache itself stops:
kcp empties the `APIExportEndpointSlice` when the last `APIBinding` goes, the
provider stops watching that endpoint, and the shared cache goes with it. That
is the large drop at the last departure in every sweep.

So this accumulates with workspace **churn**, not with the number of
workspaces currently served. At 30 goroutines per engagement of a production
workspace it is not slow, and it is the one part of the specification's "a
workspace that unbinds stops costing anything" that does not hold.

**What fixing it would take**: the per-workspace manager handed to a
`SetupFunc` would have to hand out a cache that records the handler
registrations made through it and removes them (`cache.Cache` already exposes
`RemoveEventHandler`) when the workspace is disengaged — the same shape as the
`Add` interposition that already binds runnables to a workspace's lifetime.
That is a change to this project's own seam, not to upstream. It is not built:
the trigger is a deployment with enough workspace churn for it to matter, or
the first operator question this figure cannot answer.

## Running it

```sh
task test:sweep                                # both shapes, gated defaults
SWEEP_WORKSPACES=100 task test:sweep           # the wide single-type run
SWEEP_CORE_WORKSPACES=20 task test:sweep       # a wider production-shape run
```

The two shapes are sized independently on purpose: `SWEEP_WORKSPACES` and
`SWEEP_OBJECTS` size the single-type sweep, `SWEEP_CORE_WORKSPACES` and
`SWEEP_CORE_OBJECTS` the core one, so widening the cheap sweep cannot silently
widen the expensive one. Reports land in `bin/sweep-report.md` and
`bin/sweep-report-coremanager.md`, with JSON beside each.

The sweep is a step of `task verify` in its own right, so a run that could not
start a kcp server reports "could not run" rather than passing quietly. Neither
shape needs a container runtime.

Two more knobs exist for investigating a number that has moved:

- `SWEEP_GOROUTINE_PROFILE=<dir>` writes a goroutine profile beside every
  sample. Two profiles subtracted by hand are how the retention above was
  attributed to a specific event handler rather than guessed at.
- `SWEEP_REPORT_DIR=<dir>` puts the reports somewhere other than `bin/`.

## What these numbers are not

They are the shape of a curve measured on one machine against a single-shard
kcp server. The slopes are the point, and they are stable across runs and
across widths; the absolute figures are not a capacity model, which is why
every report records the conditions of its run alongside them.

Heap is reported rather than asserted, and should be read with care: the
process being measured contains the harness measuring it, including one fixture
client per workspace, so the figure is an upper bound on the manager's own
retention rather than a clean reading of it. Goroutines and traffic do not have
this problem — client-go shares one transport across configs that differ only
in path.

[plan]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md
[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
