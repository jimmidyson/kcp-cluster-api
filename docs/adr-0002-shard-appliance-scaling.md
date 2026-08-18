# ADR-0002: The shard appliance, and how a region scales

Status: accepted (decisions A1–A3); the architecture it describes is a target,
not a built thing.

This records the intended end state for scalability: a region grows by adding
**shard appliances** of known capacity, and the system either scales itself or
says that it needs to be scaled. It supersedes nothing in
[ADR-0001](adr-0001-per-workspace-manager-pool.md); it sits above it, and
depends on it.

## Context

[ADR-0001](adr-0001-per-workspace-manager-pool.md) settled how one process
serves many workspaces. It did not settle how many, nor what to do when that
number is reached. The conversion plan left the question open in as many words
— "how many workspaces this needs to scale to in practice" — and the
[workspace-scale specification](../specs/20260815-211812-workspace-wiring-scale/spec.md)
is answering it by measurement.

The answer alone is not enough. A measured limit that lives in a document is a
number a human must remember to check. The intent here is that the **system**
holds it: a shard knows its own capacity, reports its position against it, and
a region grows by adding another appliance rather than by making an existing
one bigger.

## The appliance is a rack, not a box

A **shard appliance** is a unit of regional capacity comprising:

- a kcp shard, and
- a set of **independently scaled controller units**, each with its own
  `APIExport`, its own `APIExportEndpointSlice`, its own replica count, and its
  own identity and RBAC.

A controller unit is one deployment serving one service: core Cluster API
controllers, the infrastructure provider, the bootstrap and control-plane
providers, and — as they arrive — unrelated services such as VMs-as-a-service or
databases-as-a-service.

This decomposition is deliberate, and it is not merely organisational. Each unit
already selects its own slice through `--endpoint-slice-name`, so the seam
exists; using it deliberately is what makes the units independently sizeable.

A region is one or more appliances. **Horizontal scale at regional level is
another appliance, not a bigger one** — but within an appliance, each controller
unit scales on its own terms, because their cost drivers are unrelated. Core
controllers scale with `Cluster` and `Machine` counts; an infrastructure
provider scales with real infrastructure; a database service scales with
databases. Blending them into one number would size all of them wrongly.

## Three scaling axes

| Axis | Divides | Mechanism |
|---|---|---|
| Add a shard | kcp capacity, and everything on it | `Partition` / endpoint-slice topology |
| Add replicas to a controller unit | that unit's reconcile throughput | sharded coordinator — HRW assignment with per-cluster Lease fencing |
| Separate `APIExport`s per service | **which types a unit caches at all** | one export per controller unit |

**Only the third axis reduces memory.** This follows from something already
verified: every replica caches every workspace in its endpoint slice, and does
so whether or not it is doing work (`controller-runtime@v0.24.1`
`pkg/manager/internal.go:446` starts caches; `:477` starts leader-election
runnables afterwards). Replicas therefore buy throughput and availability, never
memory. But a unit's wildcard cache covers only the types in *its* `APIExport`,
so separating services means each unit stops caching the others' objects
entirely.

A scaling decision therefore requires knowing **which** limit is being
approached and **for which unit** — which is why capacity is stated per unit, in
the units that consume it (watched object count, event rate), rather than as one
blended workspace number.

### What this does not solve

**Within one shard and one `APIExport`, cache memory is irreducible.** Not by
replicas, and not by partitions, which select shards rather than workspaces. The
remaining levers are fewer workspaces per shard, or a narrower export. This is
what ultimately bounds a controller unit.

**Cluster API's core and infrastructure providers cannot fully separate.**
`external.ObjectTracker` makes the `Cluster` and `Machine` reconcilers add
dynamic watches on whatever `infrastructureRef` and `bootstrap.configRef` point
at, so the core unit necessarily caches `DevCluster`/`DevMachine`. Genuinely
independent services decouple cleanly; contract-coupled Cluster API providers
decouple only partially. That is upstream's design, not something to engineer
around.

**Permission claims cut against the separation, and this is now urgent.**
[ADR-0001](adr-0001-per-workspace-manager-pool.md)'s D3 claims *all* `Secret`
and `ConfigMap` objects, recording the trigger: "narrow it with a selector later
once real usage patterns are known." **That trigger has arrived.** Under service
separation every unit claiming all Secrets means every unit caches every Secret
in every workspace, which would silently undo much of the memory benefit
separation buys. Invisible at two workspaces; likely one of the largest terms in
the model at a full shard.

## Decisions

### A1 — Placement is additive now; rebalancing is deferred

A new appliance absorbs **new** workspaces. It does not relieve an existing hot
shard.

**The rationale for this changed once kcp's migration machinery was read**, and
the original reason was wrong. This decision was first taken on the assumption
that moving a logical cluster between shards might not be possible at all. It
is: kcp v0.32.3 ships a complete `logicalclustermigration` reconciler, a
`LogicalClusterMigration` API, a dedicated virtual workspace and front-proxy
filters. The deferral stands on narrower and more concrete grounds — see
"Migration, as it actually exists" below.

Consequences, stated plainly rather than discovered later:

- A shard that fills with long-lived tenants stays full. Churn is the only
  relief.
- The scaling signal must **lead** demand. Provisioning a shard is slow and
  stateful, so the threshold is a lead-time threshold, not a reactive one. A
  signal that fires when the shard is full has already failed.
- The capacity and utilisation surface MUST NOT be designed in a way that
  precludes rebalancing later.

**Trigger to revisit**: the `LogicalClusterMigration` feature gate graduating
past alpha and defaulting on, or a measured case of a shard filling and staying
full.

#### Migration, as it actually exists

Verified against kcp v0.32.3:

- **Alpha, and off by default.** `LogicalClusterMigration` is registered at
  `{Default: false, PreRelease: featuregate.Alpha}`
  (`pkg/features/kcp_features.go:150-152`). Building the appliance's scaling
  story on it today would mean depending on an experimental, disabled feature.
- **Admin-initiated, never automatic.** Nothing in kcp creates a
  `LogicalClusterMigration`; the package documentation says it "is created by an
  admin anywhere". So kcp supplies the *mechanism* and the *decision* would be
  ours — which fits A2's progression exactly: the regional controller that
  eventually provisions appliances is also the thing that would decide to
  rebalance.
- **Disruptive, not live.** Every client request to a migrating logical cluster
  is rejected with `503` and `Retry-After: 1`, except the system shard-admin
  group (`WithBlockMigratingLogicalClusters`,
  `pkg/server/filters/migratinglogicalcluster.go`). Active connections to the
  cluster are cancelled, informer stores purged, and origin data finally deleted
  **directly from etcd**, bypassing the apiserver (`deleteOriginData`,
  `datacleanup.go`).
- **kcp is defending against controllers like ours.** The filter's comment is
  explicit: migration "requires that no client except other shards can access
  the logical cluster, otherwise operators running with admin rights might
  modify objects after they were migrated, producing an inconsistent state."

#### What an appliance would see, and why that is encouraging

The `APIBinding` lives in the migrating workspace. So it leaves the origin
shard's wildcard view and appears in the destination's, which this project's
provider turns into a **disengage** followed by an **engage** — precisely the
lifecycle `internal/providerwiring` already implements, tests, and binds
runnables to. Where origin and destination belong to different appliances, that
is one appliance releasing a workspace and another adopting it: the appliance
model working as intended, with no new mechanism on our side.

Our half of the topology is likewise already in place.
`multicluster-provider`'s `endpointSliceUpdate`
(`pkg/provider/provider.go:259-293`) reconciles the watched endpoint set on
every slice change, adding watches for new URLs and cancelling those no longer
listed. The conversion plan recorded this as unverified pending a spike; it is
now verified by source.

**So rebalancing would need less new code from us than expected** — the
decision logic and the creation of migration objects, not the lifecycle
handling.

#### What must be verified before relying on it

1. Raw etcd deletion of the origin's data emits DELETE watch events. Cluster API
   reconcilers should be inert (a reconcile whose `Get` returns `NotFound`
   returns without action), but the docker/dev provider's in-memory backend keys
   listeners by cluster name and is the likely exception. **Unverified.**
2. Writes during the migration window fail with `503`. Reconcile errors and
   backoff should be safe, but this is noise a hot shard would see across every
   workspace being moved.
3. Whether a wildcard watch survives `cancelLogicalClusterConnections`, which
   cancels per-logical-cluster connections while our reads come from a
   `/clusters/*` watch.

### A2 — The scaling signal is advisory, and shaped for autonomy

An appliance reports its utilisation and raises a **scaling-required** signal.
It does not provision anything.

The signal is designed so an autonomous controller can consume it unchanged
later — machine-readable, with the limit, the observed load, the lever
indicated (replicas or appliance), and hysteresis — but the first consumer is a
human or an existing platform autoscaler.

Rationale: provisioning a kcp shard is heavyweight, stateful and high
blast-radius. Automating it before the signal has been trusted in practice
inverts the usual order and makes the first failure an expensive one. This is
the standard autoscaler progression, and there is no reason to skip it here.

**Trigger to revisit**: the signal proving accurate in operation, and a
provisioning mechanism existing to drive.

### A3 — G4 is an appliance gate; its spike has run, and G4 is contained work

An appliance is meant to be self-contained. Today `core-manager` serves
admission for **one workspace or none** — `SetupWebhooks` refuses a second with
`ErrWebhooksAlreadyWired`, because controller-runtime's webhook builder skips an
already-registered path rather than rejecting it, which would leave the first
workspace's handlers serving every tenant.

**An appliance that cannot do admission for its own tenants is not an
appliance.** G4 was correctly out of scope for making reconciliation cheaper; it
is not optional for shipping a box. The decision taken was therefore not "build
G4 now" but "find out what G4 costs now".

**The spike has run.** Findings are recorded in full as
[R12](../specs/20260815-211812-workspace-wiring-scale/research.md#r12--g4-spike-can-an-admission-request-be-resolved-to-its-workspace--verified-answer-is-yes);
the load-bearing results:

- **The identity is present.** Every object decoded from storage carries a
  `kcp.io/cluster` annotation, applied by kcp's forked apiserver at the storage
  layer (`pkg/storage/etcd3/store_kcp.go:169-193`), and kcp explicitly sets it on
  the incoming object for creates, where no stored object exists
  (`SetClusterAnnotation`). So an admission request is resolvable from
  `object`, falling back to `oldObject`.
- **The fan-in is kcp's design.** For `APIBinding`-provided types kcp reads
  webhook configuration from the **`APIExport`'s** workspace, not the consumer's
  (`getSourceClusterForGroupResource`). One configuration in the provider
  workspace serves every tenant, by design.
- **G4's real surface is one handler.** Of the wired admission handlers only
  `coreadmission.Cluster` holds workspace-scoped state; `Machine`, `DevCluster`
  and `DevMachine` are empty structs, for which serving every workspace from one
  registration is correct rather than a defect.

**Correction to this ADR as originally written.** It claimed an appliance cannot
serve the `v1beta1`↔`v1beta2` conversion webhook. **That was wrong.**
controller-runtime's conversion handler holds only a scheme and a converter
registry (`pkg/builder/webhook.go:317`) — it is a pure function of the object and
is already multi-tenant-safe. Conversion *is* mandatory for Cluster API's
multi-version schemas (`apibinding_reconcile.go:792` makes it a hard error), but
it is not part of the gap. The gap is defaulting and validation for the one
stateful handler.

G4 is therefore **contained work, not a redesign**: register the webhook paths
once per process, and resolve the stateful handler's client per request from the
object annotation against the pool of engaged workspaces. It retains the human
review checkpoint the conversion plan gives it, because a defect there is
cross-tenant bleed rather than an ordinary bug — and because the spike's own
remaining unknowns (an unengaged workspace, a request during engagement) are
precisely where that bleed would occur.

### A4 — Capacity is delivered as a fitted resource model, per controller unit

The deliverable is not a single measured number but a **resource model with
fitted coefficients**, produced per controller unit and per load profile, with
two consumers: published sizing tables for planning, and the appliance's own
runtime capacity signal (A2). The operator sizes with the model, and the box
then reports its position against the same arithmetic.

**Why a model rather than a table of measurements.** The cost structure is known
analytically from source reading, not inferred from a black box, so coefficients
are fitted to a *structural* form:

```
memory ≈ base + a·W + b·objects_cached + c·W·maxConcurrentReconciles [ + ClusterCache terms ]
CPU    ≈ base + d·events_total·W + e·reconciles_per_second
```

Two consequences worth stating. The CPU term is **quadratic in disguise** —
total events scale with workspace count, and each event costs O(W) to dispatch —
which is the departure point expressed algebraically. And if the workspace-scale feature's
gated demux work is built, that term collapses to linear, so **the model must be
re-fitted afterwards**; that before-and-after refit is exactly the evidence
SC-010 already requires.

**Per unit, additively.** Each controller unit gets its own coefficients. This
resolves where workload-cluster cost belongs without a separate rule:
`ClusterCache` holds a connection and cache per workload cluster, so its terms
appear in the models of the units that hold it — core and infrastructure — and
are simply absent from a database or VM service's model.

**Extrapolation is permitted, bounded, and labelled.** Structural models
extrapolate defensibly where a curve fit does not, but three limits are
binding:

1. **Never project across an unobserved departure point.** The model is valid within its
   measured regime; past a discontinuity it has not seen, it must decline rather
   than emit a number.
2. **Model live heap, not RSS.** Go's resident size is not a clean function of
   allocation — GOGC, fragmentation and lazy return intervene. Derive RSS from
   live heap with a stated multiplier and stated GC settings.
3. **Per-workspace event rate is a declared profile parameter, never inferred.**
   The quadratic term is highly sensitive to it, and inferring it would silently
   encode a workload assumption the operator does not share.

**Accuracy is measured, not asserted.** Coefficients are fitted on a subset of
sweep points and validated against a **held-out** point that was not fitted,
with the prediction error recorded. Without that, a resource model is curve
fitting with extra ceremony; with it, every published figure carries a stated
accuracy at a stated extrapolation factor. This is the runnable acceptance
condition Principle IV requires, and it is what makes a recommendation
trustworthy rather than merely plausible.

### A5 — Scale characterisation is a seam, generalised when the second service arrives

Adding a service to an appliance (A4's controller units) means knowing what it
costs before committing capacity to it. The intent is a utility that can be
pointed at a controller and return its scaling characteristics and thresholds.

**This generalises, and for a reason worth stating.** The cost structure
established by source reading is a property of the *architecture* — the
per-workspace wiring over a shared wildcard cache — not of any particular
controller. Listener count, cached object count, worker count and dispatch cost
have the same form for every controller built on `providerwiring`'s `SetupFunc`
seam. Only the coefficients differ. So characterising a new service is fitting
known-shape coefficients, not discovering an unknown function, and threshold
derivation is the departure point procedure operating on a curve rather than on service
semantics.

**Two measurement modes, and every figure records which produced it:**

- **Synthetic** — generate objects from the service's `APIResourceSchema`
  OpenAPI, drive a sweep. Works before a service has users, which is when
  planning matters most. Its weakness is real: generated objects may fail
  validation or take cheap error paths rather than genuine reconcile paths, so
  synthetic figures can under-measure. A figure that does not say it is
  synthetic is not usable for sizing.
- **Observation** — fit coefficients from a running deployment's natural
  variation in workspace and object counts. Always measures real work, but
  yields nothing for a service that is not yet deployed and nothing from a fleet
  that does not vary.

**Built as a seam now; generalised at the second caller.** Constitution
Principle VIII prohibits building an abstraction ahead of a second real caller,
and today there is one controller. So the harness is built for `core-manager`
with the service-specific parts — object synthesis, profile definition, watch-set
reporting — behind a narrow interface, and generalised into a utility when the
conversion plan's P1 (the bootstrap provider port) arrives as the second caller.

The trigger is real rather than hypothetical: P1, P2 and P3 are planned second,
third and fourth callers. **Trigger to generalise: P1.**

**Consequence for the seam.** The capacity and telemetry surface (A4, and the
scale feature's FR-017/FR-018/FR-028/FR-032) should be part of the
`providerwiring` contract rather than bespoke to `core-manager`. Then adding a
service means implementing `SetupFunc` and getting scale characterisation for
free — which is how every other obligation at that seam already works. A
controller not built on the seam falls back to generic controller-runtime
metrics and a correspondingly weaker model.

## What this requires of work already under way

The [workspace-scale specification](../specs/20260815-211812-workspace-wiring-scale/spec.md)
is this ADR's prerequisite: an appliance cannot declare a capacity nobody has
measured. One change follows from this ADR and has been folded into that
specification rather than deferred, because it is cheap now and structural
later:

**Capacity must be machine-readable, not merely published.** The specification
originally treated capacity as a figure in operator documentation. An appliance's
capacity is read by a controller, so the declared limit, the observed load and
the position between them are part of the process's reported surface. Publishing
the figure for humans remains necessary; it is no longer sufficient.

## Consequences

- Regional capacity planning becomes arithmetic over uniform units.
- Appliance count becomes a cost and operations variable — more boxes means more
  kcp shards, identities and control planes to run.
- Telemetry must aggregate to a regional view, since a scaling decision is
  regional while the measurement is per-appliance. This constrains the
  per-workspace telemetry design the scale feature is deciding.
- Each appliance needs its own identity and RBAC provisioning, per ADR-0001's
  D5.
- The appliance's admission gap now has a plan rather than a question mark, and
  the gap is narrower than this ADR first stated: conversion already works for
  every tenant; defaulting and validation for one handler do not.
- The conversion webhook must be reachable from kcp at a **bare URL** — the
  bound CRD's client config carries no service reference
  (`apibinding_reconcile.go:801-808`). That is a deployment constraint on the
  appliance: the box must expose a resolvable, CA-trusted URL to its own shard.

## Open questions

1. ~~**Can a logical cluster move between shards in kcp at all?**~~ **Resolved:
   yes** — kcp v0.32.3 implements it, alpha and default-off, admin-initiated and
   disruptive. See "Migration, as it actually exists" under A1. A1's deferral is
   therefore **temporary**, and its trigger is now a concrete external event (the
   feature gate graduating) rather than an open capability question. What remains
   open is narrower: the three verification items listed there, chiefly whether
   the origin's raw etcd deletion is inert for the dev infrastructure provider.
2. ~~**Does an `AdmissionReview` carry resolvable workspace identity?**~~
   **Resolved** by A3's spike: yes, via the `kcp.io/cluster` annotation. See
   [R12](../specs/20260815-211812-workspace-wiring-scale/research.md#r12--g4-spike-can-an-admission-request-be-resolved-to-its-workspace--verified-answer-is-yes).
   What remains is G4's own design: the policy for a request naming a workspace
   that is not engaged, and for requests arriving mid-engagement. Both must fail
   closed.
3. **What provisions an appliance?** A2 defers the mechanism deliberately. When
   it is built, it must own shard provisioning, topology, identity and rollback.
4. **How is utilisation aggregated regionally** without the cardinality problem
   the scale feature is already solving per-process? Now compounded by A4: the
   aggregation is per controller unit as well as per appliance.
5. **How narrow can permission claims be?** D3's "claim all Secrets and
   ConfigMaps" is now a measured concern rather than a deferred tidy-up — see
   "What this does not solve". Narrowing needs to know which secrets each
   controller unit actually reads, which the scale harness is positioned to
   observe.
6. **How many controller units is too many?** Each is a deployment, an
   `APIExport`, an identity and a set of claims. Service separation reduces
   cached state per unit but multiplies the operational surface, and the
   crossover point is unmeasured.
