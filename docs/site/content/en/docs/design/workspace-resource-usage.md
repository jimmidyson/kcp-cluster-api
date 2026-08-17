---
title: Workspace Resource Usage
description: What one manager process costs per active workspace, measured rather than assumed.
weight: 27
---

One `core-manager` process serves many workspaces. Whether that scales is a
question about a curve: what does one more active workspace add, and what
does a departing one give back?

The [conversion plan][plan]'s "Scalability" section answers it with three
claims. Two were argued from `multicluster-provider`'s source, which is what
[Constitution Principle V][constitution] asks for wherever source settles a
question. This one it does not settle — a type signature cannot show you a
slope — and the third claim ("cheap relative to a duplicated cache, but not
free") had no number in it at all.

This page is the measurement. The instrument is `internal/sweep`; the sweep
itself is `test/integration/sweep`, which runs against a real kcp server.

## What "active" means here

Every workspace in the sweep is bound, engaged, and holds objects that a real
controller — built with controller-runtime's ordinary builder, against that
workspace's own manager — has reconciled. A sweep over workspaces that were
merely bound would measure the cheapest possible case and prove nothing about
the one that matters.

Each measurement point is taken after the goroutine count has held still for
two seconds and the garbage collector has run twice. A sweep that cannot
settle fails rather than reporting a number.

## What was measured

Four active workspaces, five `Cluster` objects each, one watched type,
`GOMAXPROCS=4`, Go 1.26.3, kcp v0.32.3. Reproduce with `task test:sweep`;
the report lands in `bin/sweep-report.md` and `bin/sweep-report.json`.

| Step | Workspaces | Goroutines | Heap | Watch streams | Lists | Discovery | Requests |
|---|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 8 | 9.1 MiB | 0 | 0 | 3 | 3 |
| 1 active | 1 | 64 | 9.6 MiB | 3 | 0 | 7 | 10 |
| 2 active | 2 | 76 | 9.7 MiB | 3 | 0 | 7 | 10 |
| 3 active | 3 | 88 | 9.8 MiB | 3 | 0 | 7 | 10 |
| 4 active | 4 | 100 | 9.9 MiB | 3 | 0 | 7 | 10 |
| 3 left | 3 | 90 | 9.9 MiB | 3 | 0 | 7 | 10 |
| 2 left | 2 | 80 | 9.9 MiB | 3 | 0 | 7 | 10 |
| 1 left | 1 | 70 | 9.8 MiB | 3 | 0 | 7 | 10 |
| 0 left | 0 | 31 | 9.7 MiB | 3 | 0 | 7 | 10 |

Per additional active workspace:

| Quantity | Cost |
|---|---|
| Goroutines | **12** |
| Retained heap | **~106 KiB** |
| Watch streams | **0** |
| LIST requests | **0** |
| Discovery requests | **0** |
| Requests of any kind | **0** |

Traffic figures are cumulative from the start of the run, so a zero in that
column means the quantity did not move as workspaces were added — not that it
never happened.

### The same sweep, three times wider

`SWEEP_WORKSPACES=12` on the same machine, to check that four points were not
four points on a curve that bends:

| Workspaces | 1 | 2 | 4 | 6 | 8 | 10 | 12 |
|---|--:|--:|--:|--:|--:|--:|--:|
| Goroutines | 64 | 76 | 100 | 124 | 148 | 172 | 196 |
| Heap (MiB) | 15.9 | 16.0 | 16.1 | 16.3 | 16.5 | 16.8 | 17.0 |
| Watch streams | 3 | 3 | 3 | 3 | 3 | 3 | 3 |
| Requests, cumulative | 10 | 10 | 10 | 10 | 10 | 10 | 10 |

Twelve goroutines per workspace, to the goroutine, at every point. Serving
twelve active workspaces cost the shard the same ten requests as serving one:
after the first workspace engaged, adding eleven more required no further
traffic at all.

## Claim 1: watches are O(types), not O(types × workspaces) — holds

Three streams served four active workspaces, and the same three served one:

| Verb | Logical cluster | Resource |
|---|---|---|
| watch-list | `*` | `apis.kcp.io/apibindings` |
| watch-list | `root` | `apis.kcp.io/apiexportendpointslices` |
| watch-list | `*` | `cluster.x-k8s.io/clusters` |

Two are the provider's own discovery — which workspaces have bound, and where
the virtual workspace endpoints are. The third is the shared wildcard cache's
informer for the one type these controllers watch. Not one stream is addressed
to a tenant's logical cluster, which is the sharper form of the same claim: a
flat count would also be produced by a process that opened every per-tenant
watch up front, and that is not what this is.

The sweep asserts both: watch streams per workspace below 0.5, and zero
watches addressed to any tenant's logical cluster.

**Why LIST is zero.** The initial read is not a separate LIST any more. A
current client-go informer opens a watch with `sendInitialEvents=true` and
receives its starting state through the stream, which the instrument
classifies as `watch-list` for exactly this reason: a report showing no LISTs
at all should not look like a broken measurement.

## Claim 2: no cache or transport duplicated per workspace — holds

An added workspace costs about 106 KiB of retained heap and no new
connections, discovery, or LISTs. A duplicated cache would be visible in every
one of those columns; a duplicated transport would be visible in the request
counts. Neither is.

The per-workspace `RESTMapper` that `multicluster-provider` builds for each
engaged workspace (`pkg/cache.NewScopedCluster`) is lazy, and the sweep shows
it: discovery traffic does not move as workspaces are added.

Heap is reported rather than asserted. The process being measured contains
the harness measuring it, including one fixture client per workspace, so the
figure is an upper bound on the manager's own retention rather than a clean
reading of it.

## Claim 3: per-workspace controller overhead — 12 goroutines, ~106 KiB

The claim was that this exists but is cheap. It exists and it is: 12
goroutines and about 106 KiB per active workspace, exactly linear across the
sweep, for one controller watching one type.

Twelve goroutines per workspace against a fixed cost of roughly 50 for the
manager and provider means a process serving a hundred workspaces of this
shape holds on the order of 1,250 goroutines — large but unremarkable — and
the memory cost is dominated by the objects being cached, not by the
per-workspace machinery.

## What a workspace does not give back

The wiring in `internal/providerwiring` reclaims its own per-workspace cost
completely. The unit-tier sweep measures exactly two goroutines per workspace
(one watching for disengagement, one running the workspace's runnable) and
exactly zero left behind after sixteen workspaces have come and gone, with no
kcp server involved.

Below that layer, two goroutines per departed workspace per watched type are
**not** released:

- controller-runtime's `Kind` source adds an event handler to the informer it
  watches through (`pkg/internal/source/kind.go`) and has no path that ever
  removes it. In an ordinary deployment that is harmless, because the informer
  and the controller are stopped together.
- Here they are not. The informer belongs to the wildcard cache shared by
  every workspace, so it outlives any one workspace's controllers — and the
  handler, with the `processorListener` run/pop pair kcp's informers start for
  it, outlives them too.

The wider run shows it as arithmetic: each departure gave back 10 of the 12
goroutines the workspace had cost, all the way down from twelve workspaces to
one.

The retained goroutines are released when the wildcard cache itself stops,
which is what the large drop at the last departure in the table above is: kcp
empties the `APIExportEndpointSlice` when the last `APIBinding` goes, the
provider stops watching that endpoint, and the shared cache goes with it.

For a process serving workspaces that come and go, this accumulates with
churn rather than with the number of workspaces currently served. At two
goroutines per engagement it is slow, and it is not silent any more: the sweep
reports the figure and fails if it grows beyond what is measured here.

**What fixing it would take**: the per-workspace manager handed to a
`SetupFunc` would have to hand out a cache that records the handler
registrations made through it and removes them (`cache.Cache` already exposes
`RemoveEventHandler`) when the workspace is disengaged — the same shape as the
`Add` interposition that already binds runnables to a workspace's lifetime.
That is a change to this project's own seam, not to upstream. It is not built:
the trigger is a deployment with enough workspace churn for it to matter, or
the first operator question that this figure cannot answer.

## Running it

```sh
task test:sweep                          # four workspaces, the gated default
SWEEP_WORKSPACES=16 task test:sweep      # a quantifying run
SWEEP_OBJECTS=50 task test:sweep         # more objects per workspace
```

The sweep is a step of `task verify` in its own right, so a run that could not
start a kcp server reports "could not run" rather than passing quietly.

Two more knobs exist for investigating a number that has moved:

- `SWEEP_GOROUTINE_PROFILE=<dir>` writes a goroutine profile beside every
  sample. Two profiles subtracted by hand are how the retention above was
  attributed to a specific event handler rather than guessed at.
- `SWEEP_REPORT_DIR=<dir>` puts the report somewhere other than `bin/`.

## What these numbers are not

They are the shape of a curve measured on one machine against a single-shard
kcp server, with one watched type and five objects per workspace. The slopes
are the point and they are stable across runs; the absolute figures are not a
capacity model, which is why the report records the conditions of the run
alongside them.

[plan]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md
[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
