# ADR-0003: Should Cluster API become workspace-aware?

Status: **proposed** — this records a question and the evidence for answering
it. No decision has been made, and the decision is the project's rather than
this document's.

It reopens the alternative
[research R1](../specs/20260815-211812-workspace-wiring-scale/research.md)
rejected at the start of the workspace-scale feature, because the measurements
that feature produced have made the cost of that rejection quantifiable and the
cost of reversing it smaller than assumed.

## The question

The repository's premise, from `AGENTS.md` and Constitution Principle I, is a
**wrapper over unmodified upstream Cluster API controllers**. Every divergence
from upstream is counted and carried.

Is that premise a blocker to scalability? And if the premise instead were a
**workspace-aware Cluster API** — one controller set watching all workspaces
through the virtual workspace, so a single pool of concurrent reconcilers
covers the fleet — what changes?

## Answer to the first question: not a blocker, a density multiplier

The premise does not stop the system working at any scale. The deployment model
in [ADR-0002](adr-0002-shard-appliance-scaling.md) reaches its target by
composing bounded shards, and bounded shards work.

What the premise does is set **how bounded**. Per-workspace cost is now
measured exactly
([R16](../specs/20260815-211812-workspace-wiring-scale/evidence/goroutine-decomposition.md)):

```
goroutines/workspace = 2 + 7×controllers + 1×workers×controllers + 2×watches
```

Projected onto a 4 GiB core deployment with 30% headroom:

| | Goroutines/ws | Footprint/ws | Workspaces per core deployment |
|---|---|---|---|
| Today (3 controllers, 9 watches) | 47 | 2.38 MiB | ~1,200 |
| At core parity (~14 controllers, ~39 watches) | ~206 | ~4.0 MiB | ~700 |
| Workspace-aware (fleet-wide) | **2** | **464 KiB** | **~6,000** |

**Roughly 8× fewer workspaces per appliance at parity.** For a region of
100,000 workspaces that is on the order of 145 appliances against 17.

Every figure in the first two rows is built from measured coefficients; the
third is the measured cost of engagement with no controllers at all (R16 config
E), which is what a fleet-wide design would leave behind. None of them is a
measurement of a fleet-wide Cluster API, because none exists.

**So the premise is not a correctness or feasibility blocker. It is an
operational one, and its size is about an order of magnitude in appliance
count.**

## Answer to the second question: the change is 2.6% of the code, and it is wiring

This is the finding that reopens the question.
[R19](../specs/20260815-211812-workspace-wiring-scale/evidence/why-per-workspace.md)
establishes that per-workspace structure has exactly one cause:

1. `Reconcile` depends on **unexported** state (`recorder`, `externalTracker`,
   and four more on `machine.Reconciler`), so only `SetupWithManager` can
   construct a working reconciler.
2. `SetupWithManager` builds through `capicontrollerutil.NewControllerManagedBy`
   (`util/controller/builder.go:65`), which wraps
   `builder.ControllerManagedBy(m)` — one controller, one manager, plain
   `reconcile.Request`.
3. `reconcile.Request` is a `NamespacedName` with no room for a cluster, so a
   fleet-wide controller keyed on it would collide across workspaces.

Per-workspace managers are the adapter that satisfies all three.

### Counted

| | Lines |
|---|---|
| Core reconciler packages, non-test | 23,683 |
| Inside `SetupWithManager` | **617** |
| | **2.6%** |

**`Reconcile` — where all the behaviour lives — does not change at all.**
Neither do the map functions the reconcilers pass to their watches.

### And most of the rest already exists

| Need | Status |
|---|---|
| A request carrying the cluster | `mcreconcile.Request` |
| Adapting an unmodified reconciler | `mccontext.ReconcilerWithClusterInContext` |
| Lifting existing map functions | `TypedEnqueueRequestsFromMapFuncWithClusterPreservation` |
| One fleet-wide registration | `WildcardCache.GetSharedInformer` + `kcp.io/cluster` |
| A context-scoped client | **missing** — roughly 100 lines |

`capicontrollerutil.Builder` is already written throughout against the **Typed**
APIs — `TypedOptions[reconcile.Request]`,
`TypedEventHandler[client.Object, reconcile.Request]`,
`TypedSource[reconcile.Request]`, `TypedReconciler[reconcile.Request]`. Every
one is `reconcile.Request` where it could be a type parameter, and
`mcbuilder.TypedBuilder[request]` already exposes the same surface generically.

The change to that file is close to mechanical.

## What the premise buys, and what a narrower one keeps

The premise is not arbitrary. It is the product's value proposition: this is
*Cluster API* on kcp, not a reimplementation. Upstream bug fixes, features and
conformance arrive free; every divergence is maintenance forever, which is
exactly why Principle I counts them.

That value lives almost entirely in `Reconcile`. It does not live in
`SetupWithManager`, which is 2.6% of the code and is plumbing.

So the proposal is not to abandon the premise but to **narrow it**:

> from "unmodified upstream controllers"
> to "**unmodified upstream reconcile logic**"

Under the narrower premise the thing that makes this Cluster API is untouched,
and the thing that makes it single-tenant is replaceable.

## Why this is a contribution rather than a fork

The change makes Cluster API work with **any** `multicluster-runtime` provider,
not with kcp specifically. Both projects are `sigs.k8s.io`. A generic builder
benefits every multi-tenant Cluster API deployment.

That matters for two reasons. It is a coherent upstream proposal on its own
merits, so it is not a favour anyone is being asked for. And if accepted, the
divergence this repository carries drops to **zero** for this concern rather
than to 2.6% — which is a materially different long-term position than a fork.

It also joins two existing Principle II findings already queued to be raised:
[R5](../specs/20260815-211812-workspace-wiring-scale/research.md)'s per-workspace
REST mapper, and R17's serialized engagement. Three related asks are a better
conversation than three unrelated ones.

## Options

**A. Keep the premise unchanged.** Accept roughly 8× more appliances at parity.
Zero divergence. The deployment model still reaches its target; it just costs
more to operate.

**B. Change the premise; carry a local fork of the wiring.** Fastest to a
result. Carries a 2.6% divergence indefinitely, re-based on every Cluster API
release — and the wiring layer is exactly the part upstream changes when it adds
controllers.

**C. Change the premise; upstream the builder change first.** No permanent
divergence. Gated on someone else's review and release cycle, which R1 declined
to gate this feature on and was right to.

**D. Build against a local fork *shaped as the upstream proposal*, and upstream
in parallel.** The divergence exists but is deliberately the exact diff being
proposed, so it converges to zero if accepted and is a known quantity if not.

## Recommendation

**D**, with one sequencing point that makes the decision safer to defer.

The interposed cache — the P2 work already designed and gated as `build` for
FR-004 and FR-005 — is **valuable under every option**. It removes the O(total
objects) store replay and the process-wide `blockDeltas` stall regardless of
whether controllers are per-workspace or fleet-wide, and under a fleet-wide
design it is where the per-cluster demultiplexing has to live anyway.

So: **do the cache work now, and decide this afterwards with more evidence.**
Nothing about it is wasted by either answer, and it buys time to raise the
upstream proposal and see how it lands.

## What would change the recommendation

- **Upstream declines the builder change**, or wants a materially different
  shape. Then B and C collapse and the choice is A versus a permanent fork.
- **Appliance count turns out not to bind.** If a region genuinely tolerates
  ~145 appliances, the density argument evaporates and A is simply correct.
- **`clustercache`'s keying proves to be one of many.** It keys accessors by
  namespace and name only (`cluster_cache.go:362`), which fleet-wide is a
  cross-tenant correctness bug. If a survey finds several such
  per-workspace-singleton assumptions, the 2.6% figure understates the work and
  the balance shifts back toward A.

## What is not established

- **No fleet-wide Cluster API has been run.** Every figure for that column is
  composed from measurements of parts. The 2-goroutine-per-workspace floor is
  the measured cost of engagement with no controllers (R16 config E).
- **Memory at parity is projected, not measured.** The R16 formula covers
  goroutines; the ~4.0 MiB figure is built from the per-watch and per-controller
  memory terms plus the measured fixed costs.
- **CPU is not measured anywhere in this programme**, so nothing here says what
  either design does to reconcile throughput under real load — only what the
  worker structure permits
  ([R18](../specs/20260815-211812-workspace-wiring-scale/evidence/reconcile-throughput.md)).
- **The `external.ObjectTracker` work is unscoped.** It needs a cluster-aware
  equivalent for dynamic watches, and no one has written one.
