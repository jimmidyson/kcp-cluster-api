---

description: "Task list for workspace wiring that scales to a large fleet"
---

# Tasks: Workspace wiring that scales to a large fleet

**Input**: Design documents from `specs/20260815-211812-workspace-wiring-scale/`

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/)

**Tests**: Test tasks are included and are **not optional**. Constitution
Principle III makes test-first non-negotiable: a failing test, then the minimum
code to pass it. Unit tests are colocated `_test.go` files; integration tests run
against a **real kcp server** — vanilla envtest has no logical clusters and
cannot validate anything here.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US7)
- **`GATED(FR-xxx)`**: **This task only exists if T034's determination for that
  requirement is `build`.** If the determination is `close`, the task is struck
  and the determination is the deliverable.
- **Suffixed IDs** (`T027a`, `T078a`): tasks inserted after the review gate
  passed on `T001`–`T079`. Renumbering would silently invalidate the task
  references in [REVIEWERS.md](REVIEWERS.md) and in the dependency graph below,
  so insertions keep their neighbour's number with a suffix.

---

## ⚠️ Read this before starting

This feature's task list has an unusual property: **a third of it may never be
executed, by design.**

Phase 3 (US1) ends with eight gate determinations. Each says `build` or `close`
for one gated requirement, backed by measurement. Phases 4, 5 and 6 contain
tasks that are conditional on those verdicts. A `close` verdict deletes work —
that is Constitution Principle VIII applied to this feature's own contents, and
closing most of the list is a **good outcome**, not a failure.

Do not start any `GATED` task before T034 exists. Do not treat a `GATED` task as
ordinary work.

---

## Phase 1: Setup

**Purpose**: Package skeletons and the task-runner surface

- [ ] T001 [P] Create `internal/scaleharness/` package with doc.go stating its purpose, its reuse of `internal/verify`'s outcome contract, and that it is not a CI gate (per contracts/scale-harness.md non-goals)
- [ ] T002 [P] Create `internal/workspacetelemetry/` package with doc.go stating the cardinality constraint from FR-017
- [ ] T003 Add `test:scale` target to `Taskfile.yaml` accepting profile, workspace count, tolerance and duration as variables, alongside existing `test:unit` / `test:integration` / `verify`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Per-workspace attribution, plus the unconditional lifecycle
properties that must hold before anything changes how registrations work.

**⚠️ CRITICAL**: the harness cannot measure per-workspace load the process does
not expose. Today it exposes none — `SkipNameValidation`
(`internal/coremanager/setup.go:297`) makes every workspace's controllers share
a name, so metrics aggregate across tenants by construction. Nothing in Phase 3
works until this phase does.

- [ ] T004 Evaluate and decide the telemetry cardinality approach for R7 in `specs/20260815-211812-workspace-wiring-scale/research.md`, recording the decision against the three named candidates: bounded top-N with aggregate remainder; exemplar/log attribution with aggregate metrics; full labelling with an operator-set cap and explicit shedding. Record rationale and rejected alternatives
- [ ] T005 [P] Write failing unit tests for per-workspace attribution in `internal/workspacetelemetry/telemetry_test.go`: attribution is correct per workspace, and series count stays bounded as workspace count grows
- [ ] T006 Implement `internal/workspacetelemetry/telemetry.go` per T004's decision, satisfying FR-017 (attributable, bounded cardinality)
- [ ] T007 Wire telemetry into `internal/coremanager/setup.go` controller options and `internal/providerwiring/wiring.go` engagement paths, exposing engaged count, engagement progress and failures per FR-018
- [ ] T008 [P] Write failing unit test in `internal/providerwiring/wiring_test.go` asserting disengagement releases every acquired resource — event registrations, telemetry series, per-workspace clients (FR-012, **unconditional seam property**)
- [ ] T009 Make disengagement release everything T008 asserts in `internal/providerwiring/wiring.go` (FR-012)
- [ ] T010 [P] Make `MaxConcurrentReconciles` configurable in `internal/coremanager/setup.go` with a default chosen for many-tenant operation, replacing the hardcoded `10` inherited from single-tenant upstream `main.go`, and surface the flag in `cmd/core-manager/main.go` (FR-010)
- [ ] T011 [P] Update `internal/coremanager/setup_test.go`, which currently asserts `MaxConcurrentReconciles == 10`, to assert the new configurable default instead

**Checkpoint**: the process can attribute load per workspace with bounded
cardinality, and releases everything on disengagement. Measurement can begin.

---

## Phase 3: User Story 1 — An operator knows a shard's capacity before exceeding it (Priority: P1) 🎯 MVP

**Goal**: a measured, published, per-profile capacity figure, and eight gate
determinations that decide how much of the rest of this feature gets built.

**Independent Test**: run the harness across increasing workspace counts on a
fixed profile, confirm it locates the knee by FR-030's procedure, and confirm a
running process reports its position against stated capacity.

**This is the MVP and it is genuinely shippable alone.** A measured capacity
figure plus a documented deployment model is a complete, useful deliverable even
if every gated requirement closes.

### R10 first — the blocker within this phase

- [ ] T012 [US1] Measure achievable workspace ceiling against a real kcp in `test/integration/scale/ceiling_test.go`: how many workspaces this environment can create and bind, and creation cost per workspace, using `internal/kcpfixtures`' existing `PublishAPIExport`, `BindExport` and `WaitForAPIExportEndpointSlice`
- [ ] T013 [US1] Record T012's ceiling in `research.md` under R10, resolving it from OPEN. **Do not proceed to T014 before this number exists** — a harness designed against an unachievable range cannot run

### Harness

- [ ] T014 [P] [US1] Write failing unit tests for scale profile construction in `internal/scaleharness/profile_test.go` covering `idle-heavy` and `active-heavy` per data-model.md's Scale profile entity
- [ ] T015 [US1] Implement profiles in `internal/scaleharness/profile.go`: workspace count, objects per workspace by kind, event rate, active ratio
- [ ] T016 [P] [US1] Write failing unit tests for knee detection in `internal/scaleharness/knee_test.go`: geometric spacing, linear projection from the two smallest points, tolerance comparison, and the **no-knee-in-range case returning "could not run" rather than a value**
- [ ] T017 [US1] Implement knee detection in `internal/scaleharness/knee.go` per FR-030's defined procedure, recording tolerance and point count with every result so two runs of one profile agree
- [ ] T018 [P] [US1] Write failing unit tests in `internal/scaleharness/report_test.go` asserting results serialise through `internal/verify`'s `Outcome` / `Step` / `Result` types and that "could not run" is distinct from "failed" (FR-022)
- [ ] T019 [US1] Implement reporting in `internal/scaleharness/report.go` reusing `internal/verify` types and writing to `bin/verify-result.json`'s existing contract — **do not introduce a second reporting convention** (R11)
- [ ] T020 [US1] Implement measurement collection in `internal/scaleharness/measure.go` for engagement latency p50/p99, per-event delivery cost, per-workspace footprint, throughput, delivery pause during join, and cold start duration (FR-020)
- [ ] T021 [US1] Implement the sweep driver in `internal/scaleharness/sweep.go` running a profile across geometrically spaced counts and emitting a Sweep run per data-model.md
- [ ] T022 [US1] Wire the harness into `test/integration/scale/scale_test.go` against a real kcp server, and to the `task test:scale` target from T003

### Baseline and the gate

- [ ] T023 [US1] Run the sweep on `idle-heavy` against the current implementation; commit the baseline under `specs/20260815-211812-workspace-wiring-scale/evidence/baseline-idle-heavy.json`
- [ ] T024 [US1] Run the sweep on `active-heavy` against the current implementation; commit the baseline under `specs/20260815-211812-workspace-wiring-scale/evidence/baseline-active-heavy.json`
- [ ] T025 [US1] Derive candidate per-shard capacity per profile from T023/T024 in the units of FR-027 — watched object count and event rate, with workspace count as the derived secondary figure — recording headroom below the knee and whether the figure is extrapolated, in `specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md`

### Capacity reporting at runtime

- [ ] T026 [P] [US1] Write failing unit tests in `internal/scaleharness/capacity_test.go` for a **machine-readable** capacity surface: configured limit, observed load in FR-027's units, position between them, and which of the two limits is being approached (FR-028, FR-032). Assert it is consumable by a process other than the one measured — no log parsing (SC-016)
- [ ] T027 [US1] Implement the capacity surface in `internal/scaleharness/capacity.go`, surfaced through T006's telemetry, with a configurable capacity setting in `cmd/core-manager/main.go` (FR-028, FR-032). Distinguish throughput-bound from workspace-count/cache-bound, since the remedies differ — more replicas versus another shard (FR-016)
- [ ] T027a [US1] Review the capacity surface against FR-033: it must not assume a workspace's shard is permanent, since rebalancing is deferred rather than rejected ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A1). Record the review outcome in `evidence/capacity.md`
- [ ] T028 [US1] Make exceeding capacity observable rather than silent in `internal/scaleharness/capacity.go` and its telemetry surface (FR-029). **Do not implement enforcement**: refusing to engage would leave a bound workspace silently unreconciled, and admission-time enforcement needs G4, which is unbuilt

### 🚦 THE GATE

- [ ] T029 [US1] Record the determination for **FR-001** (per-event delivery cost independent of workspace count) in `evidence/determinations.md`: `build` or `close`, with the figures from T023/T024 either way, and for `close` the trigger that would reopen it
- [ ] T030 [P] [US1] Record the determination for **FR-003** (per-workspace fixed watch cost) in `evidence/determinations.md`
- [ ] T031 [P] [US1] Record the determinations for **FR-004** and **FR-005** (engagement cost independent of fleet contents; engagement does not suspend delivery) in `evidence/determinations.md`
- [ ] T032 [P] [US1] Record the determination for **FR-006** (concurrent engagement) in `evidence/determinations.md`
- [ ] T033 [P] [US1] Record the determination for **FR-008** (no repeated process-wide discovery) in `evidence/determinations.md`, noting it is **gated twice** — also blocked on R5's Principle II finding, and MUST NOT be delivered by copying upstream code
- [ ] T034 [US1] Record the determinations for **FR-009** and **FR-011** (idle workspace cost; idle release) in `evidence/determinations.md`, and confirm all eight determinations are present and evidenced — satisfying **FR-031**, which requires every gated requirement carry a recorded determination *before* implementation begins, and a `close` verdict record the figures that closed it

**Checkpoint (SC-001, SC-013)**: capacity is measured and published, and the
scope of Phases 4–6 is now decided by evidence. **No `GATED` task may start
before this checkpoint.**

---

## Phase 4: User Story 2 — Workspace count stops being a tax on every event (Priority: P1)

**Goal**: per-event delivery cost independent of engaged workspace count.

**Independent Test**: harness at two workspace counts an order of magnitude
apart, identical event workload, compare per-event cost.

**⚠️ Entirely conditional on T029/T030.** If both close, this phase does not exist.

- [ ] T035 [US2] `GATED(FR-001)` Write failing unit tests in `internal/clusterdemux/registry_test.go` for **C4 of contracts/cache-interposition.md — `HasSynced` semantics**: synced only after that cluster's replay completes; returns rather than hanging when replay races disengagement; waits for both when registered before the underlying informer syncs. **This is the first TDD cycle of the demux work** (R6)
- [ ] T036 [US2] `GATED(FR-001)` Write failing unit tests in `internal/clusterdemux/registry_test.go` for **C1 isolation** — a handler registered for workspace A is never invoked with an object from another cluster (FR-002, **unconditional invariant even here**)
- [ ] T037 [P] [US2] `GATED(FR-001)` Write failing unit tests for **C2** (one real registration per GVK regardless of workspace count) and **C6** (dispatch cost flat) in `internal/clusterdemux/registry_test.go`
- [ ] T038 [P] [US2] `GATED(FR-001)` Write failing unit tests for **C5** (removing a workspace removes its handlers; per-GVK entry released with its last workspace) in `internal/clusterdemux/registry_test.go` (FR-012, **unconditional**)
- [ ] T039 [US2] `GATED(FR-001)` Implement the per-GVK registry in `internal/clusterdemux/registry.go`: one upstream registration per GVK, `map[clusterName][]handler`, per-cluster sync state
- [ ] T040 [US2] `GATED(FR-003)` Implement the interposing cache in `internal/clusterdemux/cache.go` — delegating `Get`/`List`/`IndexField`/`WaitForCacheSync` unchanged to the scoped cache, interposing only `GetInformer`/`GetInformerForKind`. **Do not reimplement the read path**; that is the copying-upstream-code failure R5 refuses
- [ ] T041 [US2] `GATED(FR-001)` Add the `GetCache()` override to `workspaceManager` in `internal/providerwiring/wiring.go`, alongside its existing `Add` and `GetWebhookServer` overrides, documenting why as those two are documented
- [ ] T042 [US2] Add integration test in `test/integration/coremanager/isolation_test.go` against real kcp asserting no workspace observes another's events under the new routing (FR-002, **unconditional — runs whether or not the demux was built**)
- [ ] T043 [US2] `GATED(FR-001)` Re-run the sweep and record before/after evidence in `evidence/after-us2.json` against T023/T024 baselines, same profile same harness (FR-021, SC-002, SC-010)

**Checkpoint**: SC-002 for built requirements; recorded determinations for closed ones.

---

## Phase 5: User Story 3 — A workspace joins a busy shard without disturbing it (Priority: P1)

**Goal**: joining is cheap and disturbs nobody; failed engagement recovers by itself.

**Independent Test**: time-to-reconciling for a workspace joining at increasing
occupancy; event delivery latency for already-engaged workspaces during the join.

**Mixed conditionality** — T044–T046 are unconditional (FR-007 is wrong at any
workspace count); the rest are gated.

- [ ] T044 [P] [US3] Write failing unit test in `internal/providerwiring/wiring_test.go` for engagement retry with bounded backoff: a transiently failing engagement retries, succeeds when the failure clears, and repeated failure stays visible and bounded (FR-007, **unconditional**)
- [ ] T045 [US3] Implement engagement retry with bounded backoff in `internal/providerwiring/wiring.go` (FR-007). Today a failure returns into `multicluster-provider`'s `pkg/provider/provider.go:365`, which logs and forgets it — recovery waits for the next binding update or the ~10h resync
- [ ] T046 [US3] Add integration test in `test/integration/providerwiring/retry_test.go` against real kcp: a workspace whose engagement fails transiently is engaged automatically once the failure clears, without waiting for resync (SC-014)
- [ ] T047 [US3] `GATED(FR-004)` Implement per-cluster initial sync in `internal/clusterdemux/registry.go` via `indexer.ByIndex(kcpcache.ClusterIndexName, ...)`, replacing the full-store replay under `blockDeltas` (R2)
- [ ] T048 [US3] `GATED(FR-006)` Make engagement of distinct workspaces proceed concurrently rather than serialized behind the provider's single endpoint-watcher goroutine, in `internal/providerwiring/wiring.go`
- [ ] T049 [US3] `GATED(FR-008)` **BLOCKED — do not implement.** File the upstream proposal from R5 that `multicluster-provider` accept a caller-supplied `RESTMapper`, or that `Options.NewCluster` return `cluster.Cluster`. Record the filing in `research.md` under R5. Per Principle II the responses are another integration point, an upstream proposal, or accepting the limitation — **not** reimplementing the unexported forked cache reader
- [ ] T050 [US3] `GATED(FR-005)` Re-run the sweep; record before/after evidence in `evidence/after-us3.json` for time-to-first-reconcile and for absence of delivery pause during join (FR-021, SC-003, SC-004, SC-005)

**Checkpoint**: SC-003, SC-004, SC-005, SC-014.

---

## Phase 6: User Story 4 — An idle workspace costs almost nothing (Priority: P2)

**Goal**: a bound-but-empty workspace consumes a small, bounded share.

**Independent Test**: engage many workspaces holding no Cluster API objects;
measure resident footprint and goroutine count per workspace against the budget.

**⚠️ Entirely conditional on T034.**

- [ ] T051 [P] [US4] `GATED(FR-009)` Write failing unit tests in `internal/providerwiring/idle_test.go` for idle-workspace cost accounting against the budget set by T025
- [ ] T052 [US4] `GATED(FR-011)` Implement idle release in `internal/providerwiring/wiring.go`: a quiescent workspace releases non-essential cost while remaining able to notice a new object promptly
- [ ] T053 [US4] `GATED(FR-011)` Add integration test in `test/integration/scale/idle_test.go` against real kcp: an idle workspace's first Cluster API object starts reconciliation within the stated bound, and cost returns to the idle budget after quiescence
- [ ] T054 [US4] `GATED(FR-009)` Re-run the `idle-heavy` sweep; record before/after evidence in `evidence/after-us4.json` (FR-021, SC-006, SC-010)

**Checkpoint**: SC-006.

---

## Phase 7: User Story 5 — Replicas add throughput, not just standby (Priority: P2)

**Goal**: replicas per shard become capacity rather than cost.

**Independent Test**: multiple replicas against one shard — partition covers
every workspace exactly once, throughput rises with replica count, a killed
replica's share is recovered without overlap.

**Unconditional**, but has its own entry criterion.

- [ ] T055 [US5] **Entry criterion — R3 spike.** Verify in `test/integration/sharding/fencing_test.go` against real kcp that `multicluster-runtime`'s sharded coordinator per-cluster Lease fencing actually prevents double reconciliation, that it composes with the kcp `apiexport` provider's own engagement path (`pkg/provider/provider.go:441-460`), and what it requires of leader election. **R3 is ASSUMED until this passes — do not build on it first**
- [ ] T056 [US5] Record R3's spike result in `research.md`, moving it from ASSUMED to VERIFIED or recording the blocker
- [ ] T057 [US5] Wire the sharded coordinator into `cmd/core-manager/main.go` as a configurable alternative to the default `basic` coordinator, with lease and peer-registry settings exposed as flags (FR-013)
- [ ] T058 [US5] Resolve the interaction between the sharded coordinator and the elected-leader-dependent webhook workspace at `cmd/core-manager/main.go:217`, documenting the outcome
- [ ] T059 [US5] Add integration test in `test/integration/sharding/coverage_test.go`: every workspace reconciled by exactly one replica; throughput rises with replica count (FR-013)
- [ ] T060 [US5] Add integration test in `test/integration/sharding/failover_test.go`: a killed replica's share is taken over within the stated bound (FR-014), and **no workspace is ever actively reconciled by two replicas** during handover or when a replica is partitioned from peers but can still reach the shard (FR-015, **unconditional seam property**)
- [ ] T061 [US5] Re-run the sweep with multiple replicas; record throughput scaling evidence in `evidence/after-us5-sharded.json` (FR-021, SC-007)

**Checkpoint**: SC-007.

---

## Phase 8: User Story 6 — An operator can tell which workspace is hot (Priority: P2)

**Goal**: complete the observability begun in Phase 2 to the point an operator
can diagnose, not just measure.

**Independent Test**: generate disproportionate load in one workspace of a busy
shard; confirm it is identifiable from telemetry alone.

- [ ] T062 [P] [US6] Add integration test in `test/integration/scale/hotspot_test.go` against real kcp: a workspace generating disproportionate load is identifiable from reported telemetry alone (SC-008)
- [ ] T063 [US6] Add integration test in `test/integration/scale/cardinality_test.go` asserting telemetry volume and series count stay bounded as workspace count grows (SC-008)
- [ ] T064 [US6] Extend `internal/workspacetelemetry/telemetry.go` and its wiring in `internal/providerwiring/wiring.go` to expose the three FR-018 quantities Phase 2 did not cover: engagement progress, engagement failure counts by reason, and per-workspace reconcile load — the set an operator needs to size replicas

**Checkpoint**: SC-008.

---

## Phase 9: User Story 7 — A process at capacity does not overwhelm its shard (Priority: P3)

**Goal**: bounded aggregate request rate, and backoff under shard pressure.

**Independent Test**: workload at stated capacity against a constrained shard;
aggregate rate respects the ceiling and degrades without collapsing.

**Unconditional**, gated on a source read rather than on measurement.

- [ ] T065 [US7] **R8 source read.** Determine whether `rest.CopyConfig` preserves a shared `flowcontrol.RateLimiter` pointer such that one limiter governs every workspace's client, or detaches it. Record the finding in `research.md`, moving R8 from ASSUMED. This decides whether FR-019 is configuration or needs another mechanism
- [ ] T066 [P] [US7] Write failing unit test in `internal/coremanager/ratelimit_test.go` asserting two clients built from copies of one config share a limiter's token bucket, per T065's finding
- [ ] T067 [US7] Implement a configurable aggregate request-rate ceiling in `internal/coremanager/ratelimit.go` per T065's finding, surfaced as a flag in `cmd/core-manager/main.go`. Note `controller-runtime` sets `QPS = -1` (`pkg/client/config/config.go:101`), disabling client-side limiting entirely today
- [ ] T068 [US7] Implement backoff on shard pressure rather than retrying at full rate in `internal/coremanager/ratelimit.go` (FR-019)
- [ ] T069 [US7] Add integration test in `test/integration/scale/backpressure_test.go` against a constrained kcp: aggregate rate respects the ceiling and backs off under pressure (SC-015)

**Checkpoint**: SC-015.

---

## Phase 10: Polish, Documentation & Cross-Cutting

**Purpose**: Principle VI — documentation ships with the change; Principle VIII
— deferral is a recorded decision.

- [ ] T070 [P] Write `docs/site/content/en/docs/design/workspace-scale.md`: the cost model, what was measured, and **FR-016's two limits stated where they cannot be missed** — workspaces per shard bounds cached state, replicas per shard bounds throughput, and adding replicas does not reduce cached state because every replica caches every workspace in its endpoint slice (R4)
- [ ] T071 [P] Write `docs/site/content/en/docs/user/capacity-planning.md` publishing T025's per-profile capacity figures in FR-027's units, with workspace guidance as the derived secondary figure, plus headroom and whether each figure is extrapolated (FR-026, contracts/capacity-report.md). Document the machine-readable surface alongside the figures, and link [ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) as the architecture it feeds
- [ ] T072 Update `docs/site/content/en/docs/design/per-workspace-wiring.md`, whose cost description is superseded by this feature's measurements
- [ ] T073 [P] Record every `close` determination as a deferral with its trigger in `docs/conversion-plan.md` and the design doc (FR-025)
- [ ] T074 [P] Update `docs/conversion-plan.md`'s open question "How many workspaces this needs to scale to in practice" with the measured answer and the regional-shard deployment model
- [ ] T075 Run `task verify` and confirm `bin/verify-result.json` reports pass; run `task drift` and confirm `DRIFT.md` is unchanged — this feature is delivered entirely through public extension points and adds no carried patch (FR-023, SC-011)
- [ ] T076 Run the harness at workspace counts 1 and 2 and confirm the small case behaves as today on the same measurements, recording the comparison in `evidence/small-case.json` (SC-012)
- [ ] T077 Run the full [quickstart.md](quickstart.md) validation end to end

### Cross-cutting scale validation

> These two close acceptance gaps that no single story owns. Both depend only
> on Phase 3's harness and **may run as soon as it exists** — they are listed
> here because they validate the feature as a whole, not because they must wait.

- [ ] T078 Add integration test in `test/integration/scale/tenancy_at_capacity_test.go` against real kcp demonstrating that the per-workspace wiring feature's tenancy and lifecycle guarantees still hold **at stated per-shard capacity**, not merely at two workspaces: no workspace observes another's objects or events, everything registered for a workspace stops when it goes away, and no workspace-scoped value reaches process-global state (FR-024 — the spec requires these be *demonstrated* at capacity rather than assumed to survive)
- [x] T078a **G4 spike ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A3)** — **DONE.** Determined that an admission request *is* resolvable to its source workspace, via the `kcp.io/cluster` annotation applied at kcp's storage layer and, for creates, set explicitly by the admission plugin. Findings recorded as [R12 in research.md](research.md#r12--g4-spike-can-an-admission-request-be-resolved-to-its-workspace--verified-answer-is-yes); ADR-0002 A3 amended, including a correction — conversion is already multi-tenant-safe and was wrongly listed as part of the gap. G4 is contained work, not a redesign, and its real surface is one stateful handler
- [ ] T079 Add a sustained-churn measurement to `test/integration/scale/churn_test.go`: bind and unbind workspaces continuously for a stated duration at a stated rate, asserting goroutine count, resident memory and telemetry series count are flat at the end — a slow leak under churn is invisible to T008's unit test and to any single-shot measurement (SC-009, FR-012)

---

## Dependencies & Execution Order

### Phase dependencies

```text
Phase 1 Setup
    │
    ▼
Phase 2 Foundational (telemetry, lifecycle, concurrency)   ← BLOCKS everything
    │
    ▼
Phase 3 US1 Capacity ─── T012/T013 (R10 ceiling) ─── harness ─── baselines
    │                                                              │
    │                                                              ▼
    │                                                     🚦 T029–T034 THE GATE
    │                                                              │
    ├──────────────┬───────────────┬──────────────┐                │
    ▼              ▼               ▼              ▼                │
Phase 9 US7    Phase 5 US3     Phase 7 US5    Phase 8 US6          │
(unconditional) (T044–T046      (unconditional, (unconditional)    │
                 unconditional)  R3 spike first)                   │
                     │                                             │
                     └─────────────────┬───────────────────────────┘
                                       ▼
                          Phases 4, 5(rest), 6 — GATED tasks only
                                       │
                                       ▼
                              Phase 10 Polish & Docs
```

### Critical path

T001 → T003 → T004 → T006 → T007 → T012 → T013 → T014–T022 → T023/T024 → T025 → **T029–T034 (gate)** → gated work → T070–T077

### What can run in parallel with the gate

Phase 9 (US7 backpressure), Phase 5's T044–T046 (engagement retry), Phase 7
(US5 sharding), and Phase 8 (US6 hotspot diagnosis) are **unconditional**. They
depend only on Phase 2 and can proceed while Phase 3's measurement runs. Only
`GATED` tasks must wait.

### Within each story

- Failing test first, then minimum code to pass, then refactor (Principle III)
- Unit tests colocated; integration tests against real kcp, never envtest
- R6 (`HasSynced`) is the **first** TDD cycle of any demux work — T035 before T039

---

## Parallel Example: Phase 2 Foundational

```bash
# After T004's cardinality decision, these are independent:
Task: "T005 failing unit tests for per-workspace attribution"
Task: "T008 failing unit test for disengagement release"
Task: "T010 configurable MaxConcurrentReconciles"
```

## Parallel Example: the gate

```bash
# T029 first (FR-001 anchors the others), then these are independent:
Task: "T030 determination for FR-003"
Task: "T031 determinations for FR-004 and FR-005"
Task: "T032 determination for FR-006"
Task: "T033 determination for FR-008"
```

---

## Implementation Strategy

### MVP: User Story 1 alone

1. Phase 1 Setup
2. Phase 2 Foundational
3. Phase 3 US1 — ceiling, harness, baselines, capacity, **the gate**
4. **STOP and VALIDATE**: capacity is measured and published; eight
   determinations exist
5. Ship it

This is a real deliverable on its own: an operator can size a regional shard,
and the project knows — with evidence — which of its suspected costs actually
bind. **If every determination closes, the feature is complete here**, and the
remaining phases are correctly never built.

### Incremental delivery after the MVP

1. Unconditional work in parallel: US7 backpressure, US3's retry, US5 sharding,
   US6 diagnosis — none waits on the gate
2. Gated work strictly by determination, highest measured cost first
3. Phase 10 docs last, publishing figures and recording every closure as a
   deferral with its trigger

---

## Notes

- `[P]` = different files, no dependencies on incomplete tasks
- `GATED(FR-xxx)` = **conditional on T029–T034**; do not start before the gate
- FR-002 (isolation), FR-012 (release on disengage) and FR-015 (no double
  reconcile) are **unconditional even inside gated phases** — Principle VIII's
  seam exception, and the properties whose silent violation this project has
  already paid for once
- Commit after each task or logical group; Conventional Commit titles;
  `Assisted-By:` trailer permitted, `Co-Authored-By:` naming an agent and
  session URLs are forbidden (Principle VII)
- Read outcomes from `bin/verify-result.json`, not exit status
- Never weaken an assertion to get a green run — if a bound cannot be met, that
  is the finding to report (Principle IV)
