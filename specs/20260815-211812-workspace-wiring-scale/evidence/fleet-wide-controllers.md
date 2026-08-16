# Could fleet-wide controllers cut the per-workspace cost?

The question: rather than instantiating one controller set per workspace, could
Cluster API be driven by controllers that are natively fleet-wide — and how much
would that save?

**Yes, and the ceiling is about 38× on goroutines.** But the options are not
ordered the way they look, one of them is *worse* than doing nothing new, and
the best one hits a correctness bug in Cluster API's own state that is not a
cost problem and cannot be measured away.

## First, a correction to the baseline

Earlier figures in this feature quote **211 goroutines per workspace**. That came
from a probe registering 19 watches across **19 controllers**. The wired set is
4 reconcilers plus `clustercache`, so watches are spread across roughly **4–5**
controllers, not 19.

Re-measured at that shape — 19 watches, 4 controllers, 2 workers:

| | Predicted by the R16 formula | Measured |
|---|---|---|
| Goroutines/workspace | 76 | **76.0** |

An exact out-of-sample confirmation of the decomposition, and a **2.8×
correction** to the baseline. Footprint at the real shape is **2.89 MiB** per
workspace, against the 3.54 MiB quoted from the 19-controller probe.

The correction runs in the direction that *increases* candidate capacity;
`capacity.md` is updated accordingly.

The exact watch census is **not** established: the dev infrastructure
reconcilers live in a module the scale test deliberately does not load (it
needs a container runtime). 19 watches across 4 controllers is the working
figure, taken from a static count of the core reconcilers' `SetupWithManager`.

## Where 76 goes

| Term | Goroutines | Share |
|---|---|---|
| Informer registrations (19 × 2) | 38 | **50%** |
| Controller machinery (4 × 7) | 28 | 37% |
| Workers (4 × 2) | 8 | 10% |
| Workspace engagement | 2 | 3% |

**This changes the conclusion drawn from the 19-controller probe.** There,
registrations were 18% of the cost and cache interposition looked like a minor
lever. At the real controller count they are **half of it**.

## Four options, measured or projected

### A. Status quo — 76 goroutines/workspace

One controller set per workspace, one registration per watch per workspace.

### B. Cache interposition — ~38/workspace, a 50% cut

FR-003 as already planned (R1, R2): interpose a cache at `mgr.GetCache()` so
per-workspace registrations become map entries instead of `processorListener`s.
Removes the 38, leaves the rest.

**No Cluster API changes at all.** It is the option already designed, already
verified to have a public seam, and it is worth about twice what the earlier
(mis-baselined) analysis suggested.

### C. `mcbuilder` fleet-wide controllers — ~58/workspace, and *worse* than B

The obvious move: `mcbuilder.TypedControllerManagedBy` with `mcreconcile.Request`,
one controller per type for the whole fleet. Controller machinery and workers
become O(1).

But it does not remove the registrations, and it adds its own per-cluster cost.
`mcController.Engage` (`pkg/controller/controller.go:115-172`) loops the
controller's sources calling `aware.ForCluster(name, cl)` and then `Watch` —
**one source registration per cluster, still**. Each goes through
`startWithinContext`, which spawns a goroutine per cluster per source, and
`Engage` spawns one more per cluster.

Projected: 38 (registrations) + 19 (`startWithinContext`) + 1 (engage) ≈ **58**.

So the multicluster-native rewrite — which costs every reconciler being rewritten
against `mcreconcile.Request`, the divergence Principle I counts — lands *above*
the cache interposition that needs no reconciler changes at all. That is worth
knowing before anyone reaches for it as the obvious answer.

### D. Fleet-wide controllers on a single fleet-wide registration — ~2/workspace

The version that actually reaches O(1): one controller per type **and** one
event-handler registration per type for the entire fleet, on the shared wildcard
informer, with the cluster derived from each object rather than from which
registration delivered it.

Per workspace this leaves only engagement: **2 goroutines and ~464 KiB**, both
already measured (R16, config E). Roughly **38× fewer goroutines** than today.

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
keep `ClusterCache` per workspace (cheap — it is not in the 19 watches, and the
per-workspace cost stays small), or change the keying upstream, which is
divergence.

**4. Event recorders are per workspace** (`GetEventRecorderFor`), and would need
cluster-aware routing.

## Reading

**B is underrated and should be re-scored.** It is already designed, needs no
Cluster API changes, and at the real controller count it halves the
per-workspace goroutine cost rather than shaving 18% off it. The earlier figure
was against the wrong baseline.

**C should not be pursued as a scaling measure.** It costs a full rewrite and
lands above B.

**D is the only thing that changes the order of the cost**, and it is a rewrite
whose real obstacle is not effort but the per-workspace-singleton assumptions
inside Cluster API — of which `clustercache`'s keying is one confirmed instance
and probably not the only one.

**B and D compose.** D's fleet-wide registration subsumes B's interposed cache;
doing B first is not wasted if D ever happens, because B's cache is where the
per-cluster demultiplexing lives either way.

None of this is a decision. It is what the seams and the numbers support, for
FR-003's determination to be made against.
