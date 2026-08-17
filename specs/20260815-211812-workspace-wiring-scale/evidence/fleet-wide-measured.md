# The fleet-wide conversion, measured

The wiring is now fleet-wide: one ClusterCache, one Cluster controller, one
Machine controller, serving every workspace. The claim written into
`SetupFleetControllers` when that landed was:

> Cluster and Machine were two of the five wired controllers and the larger
> share of the watches, and they leave this sum entirely: they are paid once for
> the process instead.

**That claim is false, and this is the measurement that falsifies it.**

## What was measured

`task test:scale` with `-wiring=fleet`: a real kcp server, the real
`coremanager.SetupFleetControllers` (dev provider excluded — it needs a container
runtime), workspaces accumulated 1 → 2 → 4 → 8, sampled at each point after
every workspace has engaged.

Run: `specs/.../evidence/baseline-idle-heavy-fleet-1watch.json`.

| workspaces | heap | goroutines |
|---|---|---|
| 1 | 11.8 MiB | 111 |
| 2 | 12.5 MiB | 227 |
| 4 | 13.1 MiB | 309 |
| 8 | 14.2 MiB | 473 |

**Marginal cost of a workspace: 51.7 goroutines, 345 KiB heap.**

Nine watches are registered per workspace by this set: `clustercache` (Cluster),
`cluster` (Cluster, Machine, MachineDeployment, MachinePool) and `machine`
(Machine, Cluster, MachineSet, MachineDeployment).

## Where they go

From `-goroutine-breakdown`, at 8 workspaces:

| count | stack | per workspace |
|---|---|---|
| 73 | `informers.(*processorListener).pop` | 9 — one per watch |
| 72 | `mcsource.(*clusterKind).Start.func1` | 9 — one per watch |
| 72 | `mccontroller.(*mcController).func2.1` | 9 — one per watch |
| 30 | `controller.processNextWorkItem` | **0 — 3 controllers × 10 workers, constant** |
| 24 | `mcController.Engage.func1` | 3 — one per controller |
| 8 | `cache.(*ScopedCluster).Start` | 1 |
| 8 | `providerwiring.(*Wiring).Engage.func1` | 1 |
| 3 each | priorityqueue's five loops | **0 — 3 controllers, constant** |

## What this shows

**The conversion did what it was designed to do, and that turns out not to be
the expensive half.**

Two terms genuinely collapsed. Workers are 30 for the process — three
controllers of ten — where per-workspace wiring pays that per workspace. The
priority queue's five goroutines per controller are 15 for the process rather
than 15 per workspace. Those are constants now, and they show up as constants in
the profile.

What did not collapse is the per-*watch* cost, and it is the larger term at
realistic watch counts. Every engaged workspace still gets its own event-handler
registration per watched type, and multicluster-runtime charges more for one
than controller-runtime does: an informer listener (`pop`, and its `run`
partner), plus `clusterKind.Start.func1`, plus `mcController.func2.1` — against
controller-runtime's listener alone. Roughly four to five goroutines per
workspace-watch where there were two.

At nine watches that is about 45 of the 51.7, and it is why the total barely
moved.

## What it does not show

**No like-for-like per-workspace figure.** A synthetic run configured to imitate
the old shape — three controllers, nine watches, ten workers, all per workspace
— reported 73.0 goroutines per workspace, but its probe controllers never
started their workers: the profile has zero `processNextWorkItem` goroutines
where it should have 240. That figure is therefore an undercount of unknown
size, and it is not quoted here as the comparison. Establishing the comparison
needs a harness whose controllers demonstrably reach steady state, which this
one did not.

The direct measurement above does not depend on it. What was being checked is
whether the fleet-wide wiring removes the per-workspace goroutine cost, and it
answers that on its own: it does not.

**Idle workspaces only.** The `idle-heavy` profile. Active workspaces were not
swept.

**Eight workspaces.** Any figure quoted above eight is extrapolation.

## What follows

The **interposed cache is now the load-bearing item, not a follow-up.** Every
goroutine that still scales with workspace count is a registration on a shared
informer, which is precisely and only what interposing a cache can remove. The
controller conversion was necessary — the worker and queue terms are real, and
they are gone — but on its own it buys less than the sum it left behind.

Two things worth checking before designing that work:

1. **multicluster-runtime's per-cluster source is two goroutines per
   workspace-watch on top of the informer listener.** Whether both are
   necessary, or whether one source could serve many engaged clusters, is a
   question for that project rather than this one — and it moves half the
   remaining cost.
2. **Nine watches is the wired set, not the parity set.** The census puts parity
   at 14–15 watches across five controllers. The per-watch term scales with that
   and the constant terms do not, so parity makes this ratio worse, not better.
