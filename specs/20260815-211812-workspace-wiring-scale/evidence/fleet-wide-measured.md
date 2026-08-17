# The fleet-wide conversion, measured

Two measurements, taken the same way: a real kcp server, the real
`coremanager.SetupFleetControllers` (dev provider excluded — it needs a container
runtime), workspaces accumulated 1 → 2 → 4 → 8, sampled after every workspace has
engaged.

The first falsified the claim the conversion was written on. The second is what
fixing the cause it exposed is worth.

| | per-workspace goroutines | per-workspace heap |
|---|---|---|
| fleet-wide controllers, per-cluster watch registration | **51.7** | 345 KiB |
| fleet-wide controllers, one watch registration per type | **8.1** | 126 KiB |

**6.4× fewer goroutines per workspace, and 2.7× less heap.**

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

## After

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

## What remains per workspace

About 5 of the 8.1 is attributable and none of it is registration:

- **3 — `mcController.Engage.func1`**, one goroutine per controller per engaged
  cluster. multicluster-runtime spawns it on engagement whether or not the
  controller has any per-cluster sources, which with wildcard registration it no
  longer does. This looks removable upstream and is the largest remaining term.
- **1 — the provider's `ScopedCluster`**, which a fleet-wide controller no longer
  watches through but still reads through.
- **1 — this project's own engagement telemetry runnable.** It exists to count
  engaged workspaces, and could be a counter rather than a goroutine.

## What this does not show

- **Idle workspaces only** (`idle-heavy`). Active workspaces were not swept.
- **Eight workspaces.** Anything quoted above eight is extrapolation. At 8.1 per
  workspace the extrapolation is far less load-bearing than it was at 51.7, but
  it is still an extrapolation.
- **Correctness under wildcard registration is not measured here.** The source
  sees every workspace bound to the APIExport, including any the provider has not
  engaged; requests for those resolve to no cluster and are absorbed by
  multicluster-runtime's `ClusterNotFoundWrapper`, which mcbuilder enables by
  default. The fork's envtest covers the demultiplexing itself, but a test for
  the unengaged-workspace path would be worth having.

## What follows

The interposed cache (P2), as previously scoped, was aimed at exactly this cost —
and this removes it without an interposed cache, by not registering per cluster
in the first place. What P2 was for should be re-derived from these numbers
rather than carried forward on the old ones.
