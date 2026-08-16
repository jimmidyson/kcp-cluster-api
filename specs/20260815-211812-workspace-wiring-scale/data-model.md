# Phase 1 Data Model: Workspace wiring that scales to a large fleet

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md)

This feature stores nothing persistently — `core-manager` is a controller
process and the shard owns all durable state. The entities below are therefore
of two kinds, and the distinction matters:

- **Runtime entities** — in-memory structures whose *cost* is the subject of the
  feature. Their fields are listed because the field count per workspace is
  precisely what FR-003 and FR-009 bound.
- **Evidence entities** — the harness's inputs and outputs. These are
  serialised, reviewed like code, and are what SC-013 publishes.

---

## Runtime entities

### Engagement

One workspace's wired state. Created when the provider engages a workspace,
destroyed when it disengages.

| Field | Meaning | Notes |
|---|---|---|
| workspace | Logical cluster name | The identity everything else keys on |
| group | Runnable group bound to the engagement's context | Exists today (`providerwiring.runnableGroup`) |
| registrations | This workspace's watch registrations | The multiplying unit — see below |
| controllers | Per-workspace controller set | Cost driven by `MaxConcurrentReconciles` (FR-010) |
| state | See lifecycle below | |

**Lifecycle** (extends what exists today; the retry states are new per FR-007):

```text
                  ┌──────────────────────────────────────┐
                  │                                      │
  (bind) ──> Pending ──> Engaging ──> Engaged ──> Disengaging ──> (gone)
                            │                                  ▲
                            │ failure                          │
                            ▼                                  │
                        Backoff ────────────────────────────────
                            │  (bounded retry, FR-007)
                            └──> Failed (visible, bounded resources)
```

Today the `Backoff` and `Failed` states do not exist: a failed engagement is
logged and the workspace forgotten until the next binding update or the ~10h
resync (`multicluster-provider` `pkg/provider/provider.go:365`). SC-014 asserts
the new path.

**Invariants** (unconditional, per Principle VIII's seam exception):

- Everything acquired in `Engaging` is released in `Disengaging` — FR-012.
- `Disengaging` is safe to enter from any state, including concurrently with
  `Engaging` — the existing race the wiring already handles.
- No field of an `Engagement` may be reachable from process-global state —
  the tenancy invariant carried from the per-workspace wiring feature.

### Watch registration

One workspace's interest in one type. **The unit that currently multiplies**,
and the subject of FR-001 and FR-003.

| Field | Today | Under interposition (gated, if P3 says build) |
|---|---|---|
| backing structure | A `client-go` `processorListener` | A map entry in a per-GVK registry |
| goroutines | 2 (`shared_informer.go:1063-1064`) | 0 — dispatch runs on the single per-GVK listener |
| buffer | 1024-slot ring, preallocated (`:1279`) | none |
| events observed | Every event of that type, fleet-wide, then filtered | Only its own cluster's |
| initial sync | Full store replay under `blockDeltas` | `ByIndex(ClusterIndexName, …)` for its cluster only |

The right-hand column is a **candidate**, conditional on the P3 gate. It is
recorded here because FR-003 requires the per-workspace fixed cost be *stated*,
and stating it means naming what the cost is made of.

### Registration registry (gated — only if built)

Process-wide, one entry per watched GVK.

| Field | Meaning |
|---|---|
| gvk | The watched type |
| upstream | The single real registration on the shared informer |
| byCluster | `map[clusterName][]handler` — the demultiplexing index |
| synced | Per-cluster replay completion, backing `HasSynced` — see [R6](research.md#r6--hassynced-for-a-per-cluster-registration--open) |

**Invariant (FR-002, unconditional)**: a handler in `byCluster[A]` is never
invoked for an object whose logical cluster is not `A`. This is the isolation
property; it constrains the design rather than being produced by it.

### Replica share

The subset of a shard's workspaces one replica reconciles. **Deliberately
distinct from the subset it caches**, which is all of them
([R4](research.md#r4--replicas-do-not-divide-cached-state--verified)). FR-016
exists to stop these being conflated when a deployment is sized.

---

## Evidence entities

### Scale profile

A named, reproducible shard shape the harness can construct. Capacity is stated
per profile (FR-026) because idle and active workspaces are not interchangeable
units of load.

| Field | Meaning |
|---|---|
| name | e.g. `idle-heavy`, `active-heavy` |
| workspaceCount | Workspaces to create and bind |
| objectsPerWorkspace | Cluster API objects per workspace, by kind |
| eventRate | Sustained mutation rate applied during measurement |
| activeRatio | Proportion holding any objects at all |

At minimum the two profiles named in FR-026's resolution of Q1: idle-heavy and
active-heavy.

### Sweep run

One execution of the harness at one profile across several workspace counts.

| Field | Meaning |
|---|---|
| profile | Which profile |
| points | Geometrically spaced workspace counts (FR-030) |
| tolerance | Deviation threshold for departure detection (FR-030) |
| measurements | Per point: engagement latency, per-event delivery cost, per-workspace footprint, throughput |
| outcome | `pass` / `fail` / `could not run` — reusing `internal/verify.Outcome` ([R11](research.md#r11--reuse-the-existing-three-outcome-contract--verified)) |
| departure point | Smallest point exceeding the linear projection from the two smallest by more than tolerance; absent if none found within the swept range |

**`tolerance` and `points` are recorded with the result**, so two runs of one
profile yield the same figure — that is what makes FR-030 a procedure rather
than an inspection.

**A sweep whose range never reaches a departure point is not a pass.** It reports
`could not run` for the departure point, per FR-022, and any capacity claim derived from it
is an extrapolation and must be labelled one.

### Capacity figure

The published output. One per profile.

| Field | Meaning |
|---|---|
| profile | Which shape this figure describes |
| watchedObjects, eventRate | The units capacity is stated in (FR-027) |
| workspaceGuidance | Derived workspace count, explicitly secondary |
| derivedFrom | The sweep run, including tolerance and point count |
| headroom | Margin between the departure point and the stated figure |
| extrapolated | Whether measurement reached this range or it was projected |

### Gate determination

One per gated requirement. **The artifact that lets implementation start.**

| Field | Meaning |
|---|---|
| requirement | FR-001, FR-003, FR-004, FR-005, FR-006, FR-008, FR-009 or FR-011 |
| verdict | `build` — the cost binds at or below plausible capacity; or `close` — it does not |
| evidence | The measurements supporting the verdict, either way |
| trigger | For `close`: what would reopen it (FR-025) |

Eight of these, all present, before P5 begins (SC-013). A `close` verdict is a
successful outcome, not a gap: it is Principle VIII applied to this feature's
own contents.

---

## What this feature does not model

- **Workspace placement onto shards** — kcp's, out of scope.
- **Workload cluster state** — `ClusterCache`'s, proportional to real
  infrastructure rather than workspace count.
- **Admission state** — G4 is unbuilt; admission remains capped at one
  workspace and this feature does not touch it.
