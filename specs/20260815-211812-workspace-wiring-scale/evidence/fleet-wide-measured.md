# The fleet-wide conversion, measured

Four measurements, taken the same way: a real kcp server, the real
`coremanager.SetupFleetControllers` (dev provider excluded — it needs a container
runtime), workspaces accumulated 1 → 2 → 4 → 8, sampled after every workspace has
engaged.

The first falsified the claim the conversion was written on. The other two are
what fixing the two causes it exposed is worth.

| | per-workspace goroutines | swept to |
|---|---|---|
| fleet-wide controllers, per-cluster watch registration | **51.7** | 8 |
| one watch registration per type | **8.1** | 8 |
| …and no per-cluster engagement | **5.1** | 8 |
| the same wiring, swept to 100 | **2.0** | 100 |

**26× fewer goroutines per workspace.**

The last row is not a further change to the code. It is the same wiring as the
row above it, measured over a range long enough to separate the slope from the
intercept — and it corrects the row above it. See "Eight points were too few".

## The claim, and why it was wrong

`SetupFleetControllers` originally said:

> Cluster and Machine were two of the five wired controllers and the larger
> share of the watches, and they leave this sum entirely: they are paid once for
> the process instead.

They do not, and did not. Making the controllers fleet-wide removed the
controller-level costs and left the watch-level ones exactly where they were.

| workspaces | 1 | 2 | 4 | 8 |
|---|---|---|---|---|
| goroutines | 111 | 227 | 309 | 473 |

Nine watches are registered per workspace by this set: `clustercache` (Cluster),
`cluster` (Cluster, Machine, MachineDeployment, MachinePool) and `machine`
(Machine, Cluster, MachineSet, MachineDeployment).

The breakdown at 8 workspaces said where they went:

| count | stack | per workspace |
|---|---|---|
| 73 | `informers.(*processorListener).pop` | 9 — one per watch |
| 72 | `mcsource.(*clusterKind).Start.func1` | 9 — one per watch |
| 72 | `mccontroller.(*mcController).func2.1` | 9 — one per watch |
| 30 | `controller.processNextWorkItem` | **0 — 3 controllers × 10 workers, constant** |
| 24 | `mcController.Engage.func1` | 3 — one per controller |
| 8 | `cache.(*ScopedCluster).Start` | 1 |
| 3 each | priorityqueue's five loops | **0 — constant** |

Two terms had genuinely collapsed, and show as constants. What had not was
per-watch, and it was ~45 of the 51.7.

## The cause

multicluster-runtime registers a watch **per engaged cluster**: as each cluster
joins, it adds an event handler to that cluster's cache. Under kcp's virtual
workspace the clusters are views over one shared informer — the profile shows it
directly, with a constant handful of reflector goroutines against 72 listeners —
so the informer is shared and the *registrations* are not.

A registration is not free. It is a client-go `processorListener`: two goroutines
and a 1024-slot ring buffer, per cluster per type, plus two more goroutines
multicluster-runtime adds around it.

That scales as watches × workspaces against an informer that is a single object.
It is the wrong shape for a shard at any size, which is what makes it worth
fixing rather than accepting.

## The fix

`util/multicluster.WildcardSource` registers **once per type** against a
fleet-spanning cache and demultiplexes per event, asking each object which
cluster it came from.

The handler still receives what a per-cluster registration would have given it: a
context naming the cluster and a queue that stamps requests with it. So
`EnqueueRequestForObject`, `EnqueueRequestForOwner` and Cluster API's map
functions — which list through a context-scoped client — all work unchanged.

Two things Cluster API cannot work out for itself are supplied rather than
assumed: the fleet-spanning cache, and a resolver saying which cluster an object
belongs to. Under kcp the cache is the local manager's — it is already built
against the APIExport's virtual workspace at `/clusters/*` for unrelated reasons
— and the resolver is `logicalcluster.From`, which reads the `kcp.io/cluster`
annotation. Neither fact enters Cluster API.

## After the watches were fixed

| workspaces | 1 | 2 | 4 | 8 |
|---|---|---|---|---|
| goroutines | 146 | 175 | 183 | 203 |

**Marginal cost of a workspace: 8.1 goroutines, 126 KiB.**

| count | stack | per workspace |
|---|---|---|
| 24 | `mcController.Engage.func1` | 3 — one per controller per cluster |
| 10 | `processorListener.run` | **0 — one per type, constant** |
| 10 | `processorListener.pop` | **0 — one per type, constant** |
| 8 | `cache.(*ScopedCluster).Start` | 1 |
| 8 | `providerwiring.(*Wiring).Engage.func1` | 1 — ours, engagement telemetry |

The listener count is now ten for the whole shard — five types, two goroutines
each — where it was 73 and climbing. That is the change.

The base cost rose, 111 → 146, because the local manager now runs the informers
that the per-cluster caches used to. That is the same work moved, paid once.

## And after dropping engagement

The largest remaining term was `mcController.Engage.func1`: one goroutine per
controller per engaged cluster, spawned on engagement whether or not that
controller has per-cluster sources. With wildcard registration it has none, so
all that goroutine does is wait for the engagement to end and delete a map entry.

It did not need an upstream change. A controller only pays it because the
multicluster builder produces one the manager engages; in wildcard mode the
builder now produces a plain controller on the local manager, and nothing is
lost — the per-cluster sources do not exist, and cluster resolution happens
through the manager on the reconcile path, which never consulted that map.

One thing had to be replicated: the reconciler wrapper that drops a request
naming a cluster the provider does not have. It matters *more* here, because a
wildcard source sees every cluster the endpoint serves including unengaged ones.
`mcreconcile.NewClusterNotFoundWrapper` is public, so it is the same wrapper.

| workspaces | 1 | 2 | 4 | 8 |
|---|---|---|---|---|
| goroutines | 141 | 165 | 169 | 177 |

**Marginal cost of a workspace: 5.1 goroutines, 123 KiB.**

| count | stack | per workspace |
|---|---|---|
| 10 | `processorListener.run` | **0 — one per type, constant** |
| 10 | `processorListener.pop` | **0 — one per type, constant** |
| 8 | `cache.(*ScopedCluster).Start` | 1 |
| 8 | `providerwiring.(*Wiring).Engage.func1` | 1 — ours |

`mcController.Engage.func1` is gone from the profile.

## What remains per workspace

Two of the 5.1 are attributable, and neither is watch registration:

- **1 — the provider's `ScopedCluster`**, which a fleet-wide controller no longer
  watches through but still reads through.
- **1 — this project's own engagement seam.** With no per-workspace setup left it
  exists only to count engaged workspaces, and a counter would not need a
  goroutine.

The rest is provider engagement bookkeeping — context propagation and select
loops — which the profile does not separate cleanly and which is small enough
that separating it has not been worth a measurement.

## Eight points were too few

Sweeping 1 → 100 (points 1, 2, 4, 8, 16, 32, 64, 100) on the wiring the 5.1 was
measured on:

| workspaces | 1 | 2 | 4 | 8 | 16 | 32 | 64 | 100 |
|---|---|---|---|---|---|---|---|---|
| goroutines | 141 | 165 | 169 | 177 | 193 | 225 | 289 | 361 |
| heap (MiB) | 11.9 | 12.3 | 12.5 | 12.7 | 19.3 | 32.4 | 58.6 | 61.0 |

Goroutines per added workspace, step by step:

| step | 1→2 | 2→4 | 4→8 | 8→16 | 16→32 | 32→64 | 64→100 |
|---|---|---|---|---|---|---|---|
| per workspace | 24.00 | **2.00** | **2.00** | **2.00** | **2.00** | **2.00** | **2.00** |

**Two goroutines per workspace, exactly, from two workspaces to a hundred.**

The 1→2 step is +24, and it is one-time startup rather than a workspace's cost:
informers starting, the first engagement's machinery. Over a sweep that ends at
8 it is amortised across seven workspaces and inflates the slope by 2.5×
(24 + 7×2 = 38, and 38/7 = 5.4). That is the whole of the difference between 5.1
and 2.0. **The 5.1 figure is withdrawn**; it measured a range too short to
separate the intercept from the slope.

The breakdown at 100 accounts for both goroutines directly:

| count | stack | per workspace |
|---|---|---|
| 100 | `providerwiring.(*Wiring).Engage.func1` | 1 — ours |
| 30 | `controller.processNextWorkItem` | **0 — constant** |
| 10 | `processorListener.run` | **0 — constant** |
| 10 | `processorListener.pop` | **0 — constant** |
| 3 each | priorityqueue's five loops | **0 — constant** |

**Ten informer listeners at a hundred workspaces.** Per-cluster registration
would have been about nine hundred. That is the wildcard claim validated at a
scale where it matters rather than inferred from eight points.

The second goroutine per workspace is the provider's `ScopedCluster`, which the
grouping does not separate cleanly at this size — it appears as its own group of
8 at eight workspaces, and the arithmetic requires it at a hundred.

## Heap: the first answer was the test harness

The same sweep reported ~840 KiB per workspace between 8 and 64, and a departure
point at 32 workspaces. **Both were artefacts of the measurement.**

A heap profile at 100 workspaces put **48 MB of a 62 MB live heap** in one
`bytes.Buffer`, reached through:

```
bytes.growSlice ← bytes.(*Buffer).Write ← io.copyBuffer
                ← os/exec.(*Cmd).writerDescriptor ← os/exec.(*Cmd).Start
```

That is the kcp test fixture (`kcp-dev/sdk/testing/server/fixture.go:345`)
accumulating the server's entire stdout in an in-process buffer for the lifetime
of the test, so it can print it if the test fails. The fixture's default
verbosity is `--v=4`, and log volume rises with workspace creation — so it
scaled with the independent variable and looked exactly like a per-workspace
cost. `bytes.Buffer` grows geometrically, which is where the "departure" came
from.

**The sweep now starts kcp with `--v=0`.** `BuildArgs` appends custom arguments
after its own and klog takes the last occurrence, so this overrides rather than
conflicts.

| workspaces | 1 | 2 | 4 | 8 | 16 | 32 | 64 | 100 |
|---|---|---|---|---|---|---|---|---|
| goroutines | 141 | 165 | 169 | 177 | 193 | 225 | 289 | 361 |
| heap (MiB) | 6.1 | 6.8 | 6.9 | 7.2 | 8.3 | 9.4 | 12.6 | 17.0 |

**~113 KiB per workspace, and no departure point within 100 workspaces.**

The goroutine column is identical to the noisy run, digit for digit, at every
point. Goroutines were never contaminated; heap was, entirely.

### What is left in it

A profile of the quiet run puts total live heap at 14.2 MB, of which
`bytes.growSlice` is still 4.1 MB — the same buffer, twelve times smaller.
Some part of the 113 KiB is therefore still the fixture rather than the wiring,
bounded above by about 40 KiB per workspace if every byte of that buffer were
workspace-driven, which it is not.

So **113 KiB per workspace is an upper bound**, and the honest reading is
"of order 100 KiB, no departure to 100 workspaces". The next largest identified
term is `rest.NewRESTClient` at 1.5 MB, about 15 KiB per workspace — the
per-workspace REST clients each scoped cluster carries. That is the earlier REST
mapper hypothesis, partially confirmed and much smaller than guessed.

## What this does not show

- **Idle workspaces only** (`idle-heavy`). Active workspaces were not swept.
- **A hundred workspaces.** Anything quoted above a hundred is extrapolation.
  Both slopes are flat across six consecutive doublings with no departure point,
  so extrapolating either is defensible — with the heap figure read as an upper
  bound, per the note above.
- **Correctness under wildcard registration is not measured here.** The source
  sees every workspace bound to the APIExport, including any the provider has not
  engaged; requests for those resolve to no cluster and are absorbed by
  multicluster-runtime's `ClusterNotFoundWrapper`, which the fork now applies
  itself. The fork's envtest covers the demultiplexing itself, but a test for the
  unengaged-workspace path would be worth having — and it matters more now that
  the controllers are not engaged at all, because engagement is no longer what
  gates a source from firing.

## What follows

**Neither term is now the obvious next thing to attack.** Two goroutines and of
order 100 KiB per workspace, both linear to 100 with no departure, is not where
the next bottleneck is likely to be. Of the two goroutines, one is this
project's own engagement seam — which, with no per-workspace setup left to run,
exists only to count engaged workspaces and could be a counter — and one is the
provider's scoped cluster.

**What is worth doing instead is finding the next constraint rather than
polishing this one.** Candidates the sweep has not touched: active rather than
idle workspaces, the parity controller set (14–15 watches against this set's
nine, though the wildcard change makes watch count a constant cost rather than a
per-workspace one), and whether kcp itself — not this process — is what binds
first at a thousand workspaces.

**And the harness needs the same scrutiny the system gets.** This measurement
was wrong for two rounds: once because eight points could not separate slope
from intercept, once because a test fixture's log buffer scaled with the
independent variable. Both were found by profiling rather than by reasoning, and
neither would have been found by re-reading the code.


The interposed cache (P2), as previously scoped, was aimed at exactly this cost —
and this removes it without an interposed cache, by not registering per cluster
in the first place. What P2 was for should be re-derived from these numbers
rather than carried forward on the old ones.
