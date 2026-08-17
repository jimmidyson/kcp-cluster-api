# Why there are per-workspace goroutines at all

The question: why is anything per workspace? Why not one controller set across
the whole shard, using `multicluster-provider` reconcile semantics —
`mcreconcile.Request` instead of `reconcile.Request`?

**Nothing about event delivery requires it.** The per-workspace structure is not
a design anyone chose for its properties; it falls out of one constraint in
upstream Cluster API, and that constraint is smaller and more tractable than it
looks.

## The mechanism, verified

Three facts, read in `cluster-api@v1.15.0-kcp.1`:

**1. `Reconcile` depends on state only `SetupWithManager` can set.**

```go
type Reconciler struct {
	Client       client.Client        // exported
	APIReader    client.Reader        // exported
	ClusterCache clustercache.ClusterCache
	...
	recorder        record.EventRecorder   // unexported
	externalTracker external.ObjectTracker // unexported
}
```

`machine.Reconciler` has six unexported fields; `cluster.Reconciler` has two.
They are used well outside setup — `cluster_controller_phases.go:60` calls
`r.externalTracker.Watch(...)`, `:243` and `:345` call `r.recorder.Eventf(...)`,
`cluster_controller_status.go:48` likewise.

So **an external package cannot construct a working reconciler.** It must call
`SetupWithManager`.

**2. `SetupWithManager` hardcodes a single-cluster controller.**

Every one of the 15 core reconcilers builds through the same helper:

```go
c, err := capicontrollerutil.NewControllerManagedBy(mgr, predicateLog).
	For(&clusterv1.Machine{}).
	Watches(&clusterv1.Cluster{}, handler.EnqueueRequestsFromMapFunc(clusterToMachines)).
	Build(ctx, r)
```

`util/controller/builder.go:65` — `NewControllerManagedBy(m manager.Manager, …)`
wraps `builder.ControllerManagedBy(m)`. One controller, bound to one manager,
producing plain `reconcile.Request`.

**3. `reconcile.Request` has no room for a cluster.**

It is a `NamespacedName`. A fleet-wide controller keyed on it could not
distinguish workspace A's `default/my-cluster` from workspace B's — they would
collide in the workqueue and one would be deduplicated away. That is a
correctness failure, not a cost one.

### Therefore

Give each workspace its own manager, whose `GetClient()` and `GetCache()` are
scoped to it, and call `SetupWithManager` once per workspace. Each call builds
its own controller, with its own workqueue, its own eagerly-started workers, and
its own event-handler registrations.

**That is where 75 goroutines per workspace come from.** Nobody chose the
number. It is the arithmetic of "one manager per workspace" — which is the only
shape that satisfies `SetupWithManager`'s contract while keeping tenants apart.

## What multicluster semantics would need — and mostly already have

The interesting part is how little is actually missing.

| Need | Status |
|---|---|
| A request type carrying the cluster | **exists** — `mcreconcile.Request` embeds `reconcile.Request` + `ClusterName`, so queue keys never collide |
| Adapting an unmodified upstream reconciler | **exists** — `mccontext.ReconcilerWithClusterInContext` puts the cluster in `ctx` and calls `Reconcile(ctx, req.Request)` |
| Reusing the reconcilers' existing map functions | **exists** — `TypedEnqueueRequestsFromMapFuncWithClusterPreservation` (`multicluster-runtime/pkg/handler/enqueue_mapped.go:41`) lifts a plain `MapFunc` and attaches the source cluster |
| One informer registration for the whole fleet | **exists** — `WildcardCache.GetSharedInformer` is public; objects carry `kcp.io/cluster` (R12) |
| A client that resolves the cluster from `ctx` | **missing** — ~100 lines, and nothing prevents it |

**`Reconcile` itself needs no changes at all.** Neither do the map functions.
That is the thing worth underlining: the multicluster machinery was designed for
exactly this, and the reconcile logic — thousands of lines, where all the
behaviour lives — is untouched.

## The blocker is one function

`capicontrollerutil.NewControllerManagedBy`, at `util/controller/builder.go:65`.

And it is already most of the way there. CAPI's `Builder` is a thin wrapper over
controller-runtime's, written throughout against the **Typed** APIs:

```go
options   controller.TypedOptions[reconcile.Request]
Watches(object client.Object, eventHandler handler.TypedEventHandler[client.Object, reconcile.Request], …)
WatchesRawSource(src source.TypedSource[reconcile.Request])
Build(ctx context.Context, r reconcile.TypedReconciler[reconcile.Request]) (Controller, error)
```

Every one of those is `reconcile.Request` where it could be a type parameter.
`mcbuilder.TypedBuilder[request]` exposes the same method surface, already
generic.

**So the upstream ask is not "make Cluster API multi-tenant".** It is: make
CAPI's own builder wrapper generic over the request type, as its dependency
already is. All 15 reconcilers build through it, so one change reaches all of
them without any of them being edited.

That is a far more tractable proposal than a fork, and it is the Principle II
path — raise it rather than work around it.

## What remains beyond the builder

Two things would still need work, and one of them is a correctness bug rather
than an adaptation:

- **`external.ObjectTracker`** adds watches at runtime for `infrastructureRef`,
  `controlPlaneRef` and `bootstrap.configRef`, enqueuing plain `ctrl.Request`s
  with no cluster. It needs a cluster-aware equivalent — the same lifting the
  builder would do, applied to the dynamic path.
- **`clustercache` keys accessors by `map[client.ObjectKey]`** —
  `cluster_cache.go:362`, namespace and name only. Safe today because there is
  one instance per workspace; fleet-wide, two `Cluster` objects both named
  `default/dev` in different workspaces would share an accessor, meaning one
  tenant's controller talking to another tenant's workload cluster. This is a
  correctness bug and no measurement ranks against it.

## How much code the change touches

| | Lines |
|---|---|
| Core reconciler packages, non-test | 23,683 |
| Inside `SetupWithManager` | **617** |
| | **2.6%** |

`Reconcile` — where all the behaviour lives, and where the value of running
upstream unmodified actually sits — does not change at all.

That reframes the premise question, and
[ADR-0003](../../../docs/adr-0003-workspace-aware-cluster-api.md) takes it up:
the choice is not between "unmodified Cluster API" and "a fork", but between
"unmodified controllers" and "unmodified **reconcile logic**".

## What it would be worth

| | Per workspace, wired census | At core parity |
|---|---|---|
| Today | 75 goroutines, 2.83 MiB | ~206 goroutines |
| Fleet-wide | **2 goroutines, 464 KiB** | **2 goroutines** |

And three separate problems collapse into it, each measured independently:

1. **Footprint** — 37× fewer goroutines today, 118× at parity, and it stops
   growing with the controller set (`fleet-wide-controllers.md`).
2. **The deployment multiplier** — core and each infrastructure provider engage
   workspaces separately, so the ~880 KiB fixed per-workspace cost is paid once
   per deployment. Fleet-wide registration roughly halves it
   (`split-deployments.md`).
3. **Throughput** — workers stop being a static 2-per-workspace partition and
   become a shared pool. A bursting tenant can use the whole pool instead of its
   own two, and the pool is ~50 goroutines instead of 8,000
   (`reconcile-throughput.md`).

Three independent lines of evidence, converging on one change, whose blocker is
one function signature in a dependency this project already forks.

## What this does not claim

- **Not that it is easy.** The client, the tracker and the `clustercache` keying
  are real work, and the last is a correctness question rather than an
  engineering one.
- **Not that upstream will take it.** The proposal is small and well-shaped, but
  it is still someone else's decision on someone else's timeline, which is why
  R1 declined to gate this feature on it.
- **Not measured end to end.** Every figure here is composed from measurements
  of the parts. Nothing has run a fleet-wide Cluster API reconciler; the
  2-goroutines-per-workspace figure is the measured cost of engagement with no
  controllers at all (config E), which is what fleet-wide would leave behind.
