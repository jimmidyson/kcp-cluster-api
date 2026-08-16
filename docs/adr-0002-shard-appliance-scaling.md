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
shard, because relieving one requires moving a logical cluster between shards,
which is kcp's capability and not this project's.

Consequences, stated plainly rather than discovered later:

- A shard that fills with long-lived tenants stays full. Churn is the only
  relief.
- The scaling signal must **lead** demand. Provisioning a shard is slow and
  stateful, so the threshold is a lead-time threshold, not a reactive one. A
  signal that fires when the shard is full has already failed.
- The capacity and utilisation surface MUST NOT be designed in a way that
  precludes rebalancing later.

**Trigger to revisit**: kcp gaining supported workspace migration between
shards, or a measured case of a shard filling and staying full.

Our half of rebalancing is more tractable than the conversion plan assumed.
`multicluster-provider`'s `endpointSliceUpdate`
(`pkg/provider/provider.go:259-293`) reconciles the watched endpoint set on
every slice change — adding watches for new URLs and cancelling those no longer
listed — so an appliance already reacts live to topology changes. The plan
recorded this as unverified and to be confirmed in a spike; it is now verified by
reading the source. What remains unknown is kcp's half: whether a logical
cluster can move at all.

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

1. **Can a logical cluster move between shards in kcp at all?** Decides whether
   A1's deferral is temporary or permanent. Note kcp v0.32.3 carries a
   `logicalclustermigration` reconciler, so this may be closer than assumed —
   worth reading before the next scaling decision.
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
