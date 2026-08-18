# Could fleet-wide controllers cut the per-workspace cost?

The question: rather than instantiating one controller set per workspace, could
Cluster API be driven by controllers that are natively fleet-wide — and how much
would that save?

**Yes — up to 37× today and 118× at feature parity.** But the two mechanisms
that get there are separable, neither alone gets close, and the one that
completes the job hits a correctness bug in Cluster API's own state that is not
a cost problem and cannot be measured away.

## First, a correction to the baseline

Earlier figures in this feature quote **211 goroutines per workspace**. That came
from a probe registering 19 watches across **19 controllers**.

The census is now established by reading every `SetupWithManager` —
see `controller-census.md`. It is **5 controllers and 14–15 informer-backed
watches**, plus 4 channel-backed raw sources and dynamic watches that appear at
runtime.

Measured at that shape — 14 watches, 5 controllers, 2 workers:

| | Predicted by the R16 formula | Measured |
|---|---|---|
| Goroutines/workspace | 75 | **75.0** |

An exact out-of-sample confirmation of the decomposition, and a **2.8×
correction** to the baseline. Footprint at the wired shape is **2.83 MiB** per
workspace, against the 3.54 MiB quoted from the 19-controller probe.

The correction runs in the direction that *increases* candidate capacity;
`capacity.md` is updated accordingly.

**But it describes a walking skeleton.** Feature parity would wire about 16
controllers and 45 watches — roughly **236 goroutines per workspace**, 3.1×
today. `controller-census.md` has the trajectory, and it matters for every
option below.

## Where 75 goes, today and at parity

| Term | Wired today (5 ctl, 14 watch) | At parity (16 ctl, 45 watch) |
|---|---|---|
| Informer registrations | 28 (**37%**) | 90 (38%) |
| Controller machinery | 35 (**47%**) | 112 (47%) |
| Workers | 10 (13%) | 32 (14%) |
| Workspace engagement | 2 (3%) | 2 (1%) |

**Controller machinery is the largest single share, at both scales**, and the
proportions barely move as the set grows — watches and controllers grow
together. Registrations are 37%, not the 50% an earlier estimate of this
document claimed from a guessed 4-controller/19-watch shape.

## Four options, measured or projected

### A. Status quo — 75 goroutines/workspace today, ~236 at parity

One controller set per workspace, one registration per watch per workspace.

### B. Cache interposition — ~47/workspace, a 37% cut

FR-003 as already planned (R1, R2): interpose a cache at `mgr.GetCache()` so
per-workspace registrations become map entries instead of `processorListener`s.
Removes the 28 registration goroutines, leaves the controller and worker terms.

**No Cluster API changes at all.** It is the option already designed and already
verified to have a public seam. But it removes exactly one of the two terms, and
not the larger one.

### C. `mcbuilder` fleet-wide controllers — ~43/workspace, a 43% cut

The obvious move: `mcbuilder.TypedControllerManagedBy` with `mcreconcile.Request`,
one controller per type for the whole fleet. Controller machinery and workers
become O(1).

But it does not remove the registrations, and it adds its own per-cluster cost.
`mcController.Engage` (`pkg/controller/controller.go:115-172`) loops the
controller's sources calling `aware.ForCluster(name, cl)` and then `Watch` —
**one source registration per cluster, still**. Each goes through
`startWithinContext`, which spawns a goroutine per cluster per source, and
`Engage` spawns one more per cluster.

Projected at the wired census: 28 (registrations) + 14 (`startWithinContext`)
+ 1 (engage) ≈ **43**.

**An earlier version of this document said C was worse than B. That was computed
from the estimated 4-controller/19-watch shape and is wrong.** At the real
census C edges B, 43 against 47, and the gap widens at parity — 136 against 146.
The ordering is genuinely sensitive to the census, which is why the census had
to be read rather than estimated.

But C still costs a rewrite of every reconciler against `mcreconcile.Request` —
the divergence Principle I counts — for a 4-goroutine advantage over a cache
change that touches no reconciler. On its own it is not worth it. What makes it
interesting is what it composes with.

### D. Fleet-wide controllers on a single fleet-wide registration — ~2/workspace

The version that actually reaches O(1): one controller per type **and** one
event-handler registration per type for the entire fleet, on the shared wildcard
informer, with the cluster derived from each object rather than from which
registration delivered it.

Per workspace this leaves only engagement: **2 goroutines and ~464 KiB**, both
already measured (R16, config E). Roughly **37× fewer** than today's 75, and
**118× fewer** than parity's 236 — and, unlike every other option, it does not
degrade as the controller set grows, because nothing in it is per workspace.

Every seam it needs is public and was checked:

| Need | Where it exists |
|---|---|
| Reach the shared wildcard cache | `provider.Options.NewCluster` is handed it (`pkg/cache/cluster.go:41`) and can capture it |
| One informer per type | `WildcardCache.GetSharedInformer(obj)`, public (`pkg/cache/wildcard.go:134`) |
| Cluster identity on each object | the `kcp.io/cluster` annotation, verified present in R12 |
| Adapt an upstream reconciler | `mccontext.ReconcilerWithClusterInContext` (`pkg/context/cluster.go`), upstream |
| Per-cluster reads scoped correctly | the forked `cacheReader` already indexes by `ClusterIndexName` (R2) |

## What D actually costs, and the one thing that is not a cost

**1. A context-scoped client must be built.** Upstream reconcilers capture
`r.Client` once in `SetupWithManager`. A fleet-wide controller needs a
`client.Client` that resolves the logical cluster from the request context on
every call and delegates to that cluster's client. None exists in
`multicluster-runtime` or `multicluster-provider` — checked.

This is small and it works, because controller-runtime's convention passes `ctx`
to every client call and `ReconcilerWithClusterInContext` already puts the
cluster there. Perhaps a hundred lines. It is the enabling piece.

**2. `external.ObjectTracker` adds watches at runtime.** Both core reconcilers
set `externalTracker` with `Cache: mgr.GetCache()`, and it enqueues plain
`ctrl.Request`s carrying no cluster name (R1 recorded the mechanism). Fleet-wide,
those requests would be unattributable. It needs a cluster-aware equivalent.

**3. `clustercache` collides across workspaces — and this is a correctness bug,
not a cost.** `cluster_cache.go:362` keys accessors as
`map[client.ObjectKey]*clusterAccessor` — namespace and name only, no logical
cluster.

Today that is safe: `SetupReconcilers` runs per workspace, so each workspace has
its own `ClusterCache` and the keys never meet. Fleet-wide there would be one,
and two `Cluster` objects both named `default/dev` in different workspaces would
**share an accessor** — meaning one tenant's controller talking to another
tenant's workload cluster.

This is exactly the class of failure Principle VIII's seam warning is about, and
it is not something a measurement can rank against goroutine counts. Options:
keep `ClusterCache` per workspace — it is one controller and one watch of the
census, so retaining it costs about 9 goroutines per workspace and preserves
correctness — or change the keying upstream, which is divergence.

**4. Event recorders are per workspace** (`GetEventRecorderFor`), and would need
cluster-aware routing.

## Reading

**D is B and C together, and that is the useful way to see it.** The
per-workspace cost has exactly two large terms, and each option removes one:

| | Registrations | Controller machinery | Per workspace, wired | At parity |
|---|---|---|---|---|
| A. Status quo | — | — | **75** | 236 |
| B. Interposed cache | removed | — | **47** | 146 |
| C. Fleet-wide controllers | — | removed | **43** | 136 |
| D. Both | removed | removed | **2** | 2 |

Neither half alone gets below about 60% of the cost. Together they leave
engagement, which is 2 goroutines and 464 KiB and is already minimal. D is not a
third design — it is what happens when both terms go.

**That makes the sequencing question the real one.** B is already designed,
verified against a public seam, and needs nothing from Cluster API. It is the
half to do first, and it is not wasted if C follows: an interposed cache is
where the per-cluster demultiplexing has to live under C anyway, because C's
fleet-wide registration still needs each event attributed to a workspace.

**C's obstacle is not effort.** It is the per-workspace-singleton assumptions
inside Cluster API, of which `clustercache`'s `map[client.ObjectKey]` keying is
one confirmed instance and probably not the only one. That one is a correctness
bug rather than a cost, and it has a cheap containment — keep `ClusterCache`
per workspace, about 9 goroutines.

**The parity trajectory argues for deciding now rather than later.** Every
controller added between here and Phase 3 costs 7 goroutines per workspace plus
its watches, and adds another `SetupWithManager` to convert if C is ever chosen.
The set roughly triples on the way to parity; the conversion cost triples with
it.

None of this is a decision. It is what the seams and the numbers support, for
FR-003's determination to be made against.
