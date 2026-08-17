# The fleet-wide conversion, measured

Three measurements, taken the same way: a real kcp server, the real
`coremanager.SetupFleetControllers` (dev provider excluded — it needs a container
runtime), workspaces accumulated 1 → 2 → 4 → 8, sampled after every workspace has
engaged.

The first falsified the claim the conversion was written on. The other two are
what fixing the two causes it exposed is worth.

| | per-workspace goroutines | per-workspace heap |
|---|---|---|
| fleet-wide controllers, per-cluster watch registration | **51.7** | 345 KiB |
| one watch registration per type | **8.1** | 126 KiB |
| …and no per-cluster engagement | **5.1** | 123 KiB |

**10× fewer goroutines per workspace, and 2.8× less heap.**

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

## What this does not show

- **Idle workspaces only** (`idle-heavy`). Active workspaces were not swept.
- **Eight workspaces.** Anything quoted above eight is extrapolation. At 5.1 per
  workspace the extrapolation is far less load-bearing than it was at 51.7, but
  it is still an extrapolation.
- **Correctness under wildcard registration is not measured here.** The source
  sees every workspace bound to the APIExport, including any the provider has not
  engaged; requests for those resolve to no cluster and are absorbed by
  multicluster-runtime's `ClusterNotFoundWrapper`, which the fork now applies
  itself. The fork's envtest covers the demultiplexing itself, but a test for the
  unengaged-workspace path would be worth having — and it matters more now that
  the controllers are not engaged at all, because engagement is no longer what
  gates a source from firing.

## What follows

The interposed cache (P2), as previously scoped, was aimed at exactly this cost —
and this removes it without an interposed cache, by not registering per cluster
in the first place. What P2 was for should be re-derived from these numbers
rather than carried forward on the old ones.
