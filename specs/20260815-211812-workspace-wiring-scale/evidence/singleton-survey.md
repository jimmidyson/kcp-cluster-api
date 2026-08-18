# Survey: cluster-blind state in the packages a fleet-wide design would share

[ADR-0003](../../../docs/adr-0003-workspace-aware-cluster-api.md) listed, as one
of three things that could change its recommendation, "a survey finding that
`clustercache`'s namespace-and-name keying is one of many per-workspace-singleton
assumptions rather than the only one".

**It is one of several.** This is that survey, run before writing any code
against the decision.

## Method

Every map keyed by an identity that carries no logical cluster —
`types.NamespacedName`, `client.ObjectKey`, `reconcile.Request` — in the
packages a fleet-wide controller would share across workspaces. Test and fake
files excluded.

```
util/controller  controllers/external  controllers/clustercache  core/reconcilers
```

## Found

| Location | State | Cluster-blind? | Containable? |
|---|---|---|---|
| `util/controller/consistency.go:67` | `writes map[types.NamespacedName]*ownerRecord` | **yes** | **not reachable yet** — see below |
| `controllers/clustercache/cluster_cache.go:362` | `clusterAccessors map[client.ObjectKey]*clusterAccessor` | **yes** | yes |
| `controllers/clustercache/cluster_cache.go:397` | `lastEventSentTimeByCluster map[client.ObjectKey]time.Time` | **yes** | yes |
| `util/controller/controller.go:167` | `reconcileCacheEntry.Key()` → `Request.String()` | **no** | n/a |
| `controllers/external/tracker.go` | dynamic watches enqueue plain `ctrl.Request` | **yes** | no — needs a cluster-aware equivalent |

### The one that is safe, and why it is worth stating

`reconcileCacheEntry.Key()` returns `r.Request.String()`. That looks like a
collision — two workspaces' `default/my-cluster` sharing a rate-limit entry —
and it is not, **provided the builder is genericised properly**.
`mcreconcile.Request` overrides `String()`:

```go
return "cluster://" + r.ClusterName.String() + string(types.Separator) + r.Request.String()
```

So the key carries the cluster once the field's type is the request type
parameter rather than `reconcile.Request`. It is safe by inheritance from a
correct generic conversion, not safe on its own — if the conversion left that
field concrete, the collision would be real and silent.

### The two that are containable

Both `clustercache` maps are in one component, and ADR-0003's response 3 already
covers it: **`ClusterCache` can simply stay per-workspace.** One controller, one
watch, four workers — about 15 goroutines per workspace by the R16 formula,
against core's 53 today. The accessors never meet, so the cross-tenant bug never
arises.

### The one that must eventually convert

`realConsistencyStore.writes` is keyed by `types.NamespacedName` and is
constructed **inside `Build()`**, one per controller. A fleet-wide controller has
exactly one, shared by every workspace.

What it does: records the resourceVersion of each write, so the next reconcile
can be deferred until the cache has observed it. Collided across workspaces, one
tenant's write defers another tenant's reconcile against a resourceVersion that
will never appear in its view — **a hang, not a wrong answer**, which is the
worse failure mode of the two because it is silent.

It cannot be left per-workspace: it is not a component the wiring chooses, it is
internal to the builder being converted. But it is **not reachable by the
controllers being converted first** — see "Scoping" below, which is what makes it
a documented constraint rather than a blocker.

**Two routes, and the choice is not obvious:**

1. **Substitute an implementation.** `consistencyStore` is an *interface*
   (`consistency.go:37`) with `realConsistencyStore` as one implementation, so a
   cluster-aware one could be added in a new file — additive, no modification.
   **But the interface gives it nothing to key on**: `WroteAt(owner, ownerUID,
   gvkt, rv)` and `Clear(owner, ownerUID)` take no context and no cluster. A
   substitute cannot see which workspace it is recording for.

2. **Key by UID instead.** `WroteAt` and `Clear` already receive `ownerUID`, and
   UIDs are globally unique, so keying on them removes the collision without
   needing the cluster at all. **But `EnsureReady(ctx, owner)` has no UID**, so
   the read path could not find what the write path stored.

Either route therefore requires **changing the interface** — adding a cluster or
a context to its methods, and updating the call sites in `controller.go`. That
is a modification to existing files, not an addition.

### And the interface is harder to change than it looks

`DeferNextReconcileUntilCacheUpToDate` and `ClearConsistencyStore` are not
internal. They are on the **public `Controller` interface**, and reconcilers call
them from inside `Reconcile` — 10+ sites across `machinedeployment` and
`machineset`.

So making the cluster explicit there would change *reconcile* code, which is
exactly what ADR-0003's narrowed premise — "unmodified upstream reconcile logic"
— exists to protect. The cheap-looking fix costs the thing the whole decision
was taken to keep.

The alternative is deriving rather than wiring: `DeferNextReconcileUntilCacheUpToDate`
receives a `metav1.Object`, which carries the `kcp.io/cluster` annotation (R12),
so it could resolve its own cluster. `ClearConsistencyStore` receives only a
`client.ObjectKey` and a UID, and could not.

**Neither route is free, and the choice does not have to be made yet — see
below.**

### Scoping: it is real, and not yet reachable

Two facts move this from "blocking" to "documented constraint":

- **No wired controller uses the store.** `cluster`, `machine` and
  `clustercache` have **zero** call sites between them. Only `machinedeployment`
  and `machineset` use it, and both are Phase 3 parity controllers that
  `setup.go` explicitly defers.
- **An empty store is a harmless no-op.** `EnsureReady` returns `nil, nil` when
  there is no record for the owner (`consistency.go`), so a controller that
  never calls `WroteAt` cannot collide with another — there is nothing stored to
  collide over.

**So converting `cluster` and `machine` fleet-wide does not require solving
this.** The collision becomes reachable exactly when `machineset` or
`machinedeployment` is converted, and not before.

That is the Principle VIII answer: do not build the fix now, and do not let the
constraint be rediscovered later. It is recorded here, and any conversion of
those two controllers MUST resolve it first.

## What this means for the scope estimate

**ADR-0003's "the change to that file is close to mechanical" was wrong, and is
corrected there.** `util/controller/builder.go` is not a thin pass-through: it
carries a reconciler wrapper, an exponential rate limiter, metrics registration,
log-constructor defaulting and the consistency store, all typed on
`reconcile.Request`. Genericising it is a real piece of work, and one of its
collaborators needs an interface change.

**The wiring style is decided: explicit.** Where a cluster must be threaded
through, it is passed as an argument rather than carried in `context`. That is a
project preference, and it is also the reason the `consistencyStore` question is
awkward — implicit context would have made it a smaller signature change, and
explicit wiring makes the cost visible instead of hiding it in a value nobody
can see at the call site. Visible is the point.

**It does not change the decision.** The 2.6% figure counted
`SetupWithManager` bodies, which is still the right measure of how much
*reconciler* code changes — none of it. What the survey corrects is the estimate
of how much *builder* code changes, which the ADR understated.

**And it does not change the containment argument.** Per ADR-0003 response 3,
each component either converts or stays per-workspace, priced by the R16 formula
either way. This survey moves one item — `consistencyStore` — from "could stay"
to "must convert", and leaves `clustercache` where it was.

## What this survey does not cover

- **Only four package trees.** The bootstrap and control-plane providers, and
  any third-party infrastructure provider, are not surveyed. Each would need the
  same pass before being run fleet-wide.
- **Only map keys.** Cluster-blind state can also hide in package-level
  variables, in caches keyed by a string built from name alone, or in
  `sync.Map`s whose key type is not visible to this search. A grep for map
  literals finds the shape it was asked for and no other.
- **Nothing here is a runtime observation.** Every entry is a source read. None
  of these collisions has been demonstrated by a test, and demonstrating the
  `consistencyStore` hang in particular would be worth doing before relying on
  the fix.
