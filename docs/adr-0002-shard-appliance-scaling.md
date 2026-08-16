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

## The appliance

A **shard appliance** is a complete, self-contained unit of capacity:

- a kcp shard,
- the project's `APIExport`, its `APIExportEndpointSlice`, and the `Partition`
  selecting that shard,
- `core-manager` replicas sized for the shard,
- the provider controllers, their identity and RBAC.

A region is one or more appliances. **Horizontal scale is another appliance, not
a bigger one.** This is the whole point: an appliance has a known, measured,
uniform capacity, so regional capacity planning becomes arithmetic rather than
estimation.

That uniformity is a real property rather than an aspiration, and it follows
from something already verified: every `core-manager` replica caches every
workspace in its endpoint slice, and it does so whether or not it is doing work
(`controller-runtime@v0.24.1` `pkg/manager/internal.go:446` starts caches;
`:477` starts leader-election runnables afterwards). So an appliance's binding
limit is a **memory** limit, and it is the same on every replica in the box.

## Two scaling axes, and which lever to pull

These are separate, and the distinction is the scaling logic, not a
technicality:

| Bound by | Lever | Why the other lever fails |
|---|---|---|
| Reconcile throughput | Add replicas **within** the appliance | A new appliance does not help work that is already assigned to this shard |
| Workspace count or memory | Add an **appliance** | Replicas do not divide cached state — every replica holds the whole shard |

A scaling decision therefore requires knowing *which* limit is being approached,
which is why the capacity report is stated in the units that consume capacity
(watched object count, event rate) rather than as a single workspace number.

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
   the scale feature is already solving per-process?
