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

### A3 — G4 is an appliance gate, and its spike comes first

An appliance is meant to be self-contained. Today `core-manager` serves
admission for **one workspace or none** — `SetupWebhooks` refuses a second with
`ErrWebhooksAlreadyWired`, because controller-runtime's webhook builder skips an
already-registered path rather than rejecting it, which would leave the first
workspace's handlers serving every tenant.

So an appliance serving many tenants currently cannot provide, for those
tenants:

- Cluster API's defaulting,
- Cluster API's validation,
- the `v1beta1`↔`v1beta2` conversion webhook — which is not optional while both
  versions are served.

**An appliance that cannot do admission for its own tenants is not an
appliance.** G4 was correctly out of scope for making reconciliation cheaper; it
is not optional for shipping a box.

The decision is not "build G4 now" but "find out what G4 costs now": spike
whether an incoming `AdmissionReview` carries enough identity for kcp's routing
to resolve it to its source workspace. That answer separates a contained piece
of work from a redesign, and it is unverified today. G4 retains the human review
checkpoint the conversion plan gives it, because a defect there is cross-tenant
bleed rather than an ordinary bug.

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
- Until A3's spike resolves, the appliance's admission gap is a stated
  limitation rather than a plan.

## Open questions

1. **Can a logical cluster move between shards in kcp at all?** Decides whether
   A1's deferral is temporary or permanent.
2. **Does an `AdmissionReview` carry resolvable workspace identity?** A3's
   spike. Gates the appliance roadmap.
3. **What provisions an appliance?** A2 defers the mechanism deliberately. When
   it is built, it must own shard provisioning, topology, identity and rollback.
4. **How is utilisation aggregated regionally** without the cardinality problem
   the scale feature is already solving per-process?
