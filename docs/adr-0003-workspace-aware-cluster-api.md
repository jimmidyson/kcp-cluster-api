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
result. Under the additive strategy of response 1 the divergence is *new files*
rather than modified lines, so it never conflicts textually — but it can go
silently stale, and it is still divergence that `DRIFT.md` must record. The
bookkeeping counts paths either way; what changes is the cost of living with
them.

**C. Change the premise; upstream the builder change first.** No permanent
divergence. Gated on someone else's review and release cycle, which R1 declined
to gate this feature on and was right to.

**D. Build against a local fork *shaped as the upstream proposal*, and upstream
in parallel.** The divergence exists but is deliberately the exact diff being
proposed, so it converges to zero if accepted and is a known quantity if not.
Combined with additive files and per-controller conversion (responses 1 and 3),
it is also incremental: no step is a big-bang rewrite, and each is priced by the
R16 formula before it is taken.

## Recommendation

**D**, with one sequencing point that makes the decision safer to defer — and
with the additive-file strategy of response 1 below, which makes D's divergence
cheaper than the 2.6% figure suggests.

The interposed cache — the P2 work already designed and gated as `build` for
FR-004 and FR-005 — is **valuable under every option**. It removes the O(total
objects) store replay and the process-wide `blockDeltas` stall regardless of
whether controllers are per-workspace or fleet-wide, and under a fleet-wide
design it is where the per-cluster demultiplexing has to live anyway.

So: **do the cache work now, and decide this afterwards with more evidence.**
Nothing about it is wasted by either answer, and it buys time to raise the
upstream proposal and see how it lands.

## The three risks, and the responses to them

The project's response to each, recorded because they change the balance rather
than restate it.

### 1. Upstream declining — mitigated by additive files, not build tags

**Response**: minimise the change and use build tags for workspace-aware
controllers, so rebases stay cheap in the fork.

**Accepted, and there is a stronger form of it.** Build tags select between
implementations of the *same* symbol, which means either editing upstream's file
or extracting a function out of it — both leave modified hunks for `git rebase`
to conflict on, and the extracted function is exactly what upstream edits when
it adds a watch.

The change can instead be **purely additive**, because Go visibility is
per-package:

```
core/reconcilers/cluster/cluster_controller.go            # untouched
core/reconcilers/cluster/cluster_controller_workspace.go  # NEW: SetupWithMulticlusterManager
```

A new file *in the same package* can set `recorder`, `externalTracker`,
`controller`, `hookCache` and `predicateLog` — the unexported fields that made
this impossible from outside. **The visibility problem disappears entirely**,
and upstream's `SetupWithManager` continues to exist untouched beside it.

Purely additive files have **no conflicting hunks on rebase at all**. They
conflict only if upstream adds a file of the same name. That is a materially
better position than 2.6% of modified lines, and it applies to the builder too:
a new `util/controller/builder_workspace.go` alongside the existing one, rather
than making the existing type generic.

**This is not hypothetical — it is already how the fork carries divergence.**
`jimmidyson/cluster-api` branch `kcp/v1.15` is one commit over its recorded base
`281e4e3`, and that commit is **+112 / −0**:

| Path | Change |
|---|---|
| `controllers/external/metadata.go` | **added** — 73 lines |
| `internal/contract/version.go` | modified — 39 added, **0 deleted** |

One new file, and one modification that only appends. `DRIFT.md` can describe
the fork's contract as "upstream at base commit plus recorded patches, nothing
else" precisely because nothing has yet been rewritten in place. The additive
strategy proposed here extends an existing practice rather than introducing one.

**The hazard this trades into, and it must not be waved away.** A textual
conflict is loud; a purely additive parallel implementation goes **silently
stale**. If upstream adds a `Watches(&clusterv1.Foo{}, …)` to
`SetupWithManager`, nothing conflicts — our parallel setup simply stops watching
something it should, and the symptom is a controller that quietly does not
reconcile on an event.

That needs a guard, and it is buildable: a check that the two setups register
the **same set of types**, failing when they diverge. The controller census in
`specs/…/evidence/controller-census.md` was produced by exactly that kind of
static read, so the mechanism already exists in prototype. **Any additive
strategy MUST ship that guard**; without it the strategy trades a visible cost
for an invisible one.

### 2. Appliance count — an open question, and the instinct is recorded as a prior

**Response**: this needs validating later; instinct says it will bind, though
perhaps not to the degree projected.

**Recorded as the feature's most important open question**, and explicitly *not*
resolved by anything measured here. The projections come from measured
coefficients, but whether ~145 appliances against ~17 is a problem is an
operational judgement about running a region, which no sweep can answer.

What would settle it: a cost model for one appliance — the kcp shard, its
controller deployments, and the operational overhead of the unit — against a
target region size. That is a different exercise from this feature's, and it
should not be folded into it.

Until it is done, the projections stand as projections and this ADR stays
proposed. **The instinct that it will bind is a stated prior, not evidence**,
and is recorded as such so that a later reader can tell the two apart.

### 3. More per-workspace singletons than `clustercache` — handled by not converting them

**Response**: `clustercache` will not be the only one, but the same additive
approach handles them.

**Accepted, and there is a second answer that removes the risk rather than
managing it: a controller that carries a per-workspace-singleton assumption can
simply stay per-workspace.**

The conversion is **per controller**, not per project. Each one either:

- converts to fleet-wide, and contributes **nothing** per workspace; or
- stays per-workspace, and contributes `7 + workers + 2×watches` per workspace,
  by the R16 formula.

So the migration is incremental and every step is priced in advance. Keeping
`clustercache` per workspace — one controller, one watch, four workers — costs
**15 goroutines per workspace** against core's 53 today: still a 3.5×
improvement, with the cross-tenant accessor bug never arising because the
accessors never meet.

At core parity the same arithmetic: converting 12 of 16 controllers and leaving
4 per-workspace gives roughly **70 goroutines per workspace against 236**. A
3.4× improvement while touching two thirds of the set.

**This is what most reduces the risk of the whole proposal.** It is not
all-or-nothing, there is no big-bang rewrite, and a survey finding ten
singletons rather than one changes the *ceiling* of the improvement, not its
viability.

## What would still change the recommendation

- **Upstream declining *and* the additive strategy proving unworkable** — for
  instance if the parallel-setup drift guard cannot be made reliable, so
  staleness would be silent in production.
- **The appliance-count question resolving against the premise change** — if a
  region genuinely tolerates the projected appliance count, A is simply correct.
- **A survey finding that the *cheap* controllers are the ones with singleton
  assumptions**, so the incremental path converts only the ones that do not
  matter.

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
