# Implementation Plan: Workspace wiring that scales to a large fleet

**Branch**: `claude/controller-wrapper-scalability-99fkd2` | **Date**: 2026-08-15 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/20260815-211812-workspace-wiring-scale/spec.md`

## Summary

`core-manager` pays a per-event cost that grows with the number of workspaces it
serves, and a per-workspace onboarding cost that grows with the shard's total
contents. Both come from one place: each workspace registers its own listeners
on the shared wildcard informers, and `client-go` fans every event to every
listener and replays the whole store into each new one.

The plan's shape is dictated by the spec's **measurement gate** (FR-031): eight
requirements are conditional on a measured sweep showing their cost binds below
a capacity an operator would actually configure. So the sequence is
**observe → measure → decide → build only what binds**, and the first
deliverable is not a fix but a number.

The technical approach for the gated work, should it be found binding, is
interposition at `mgr.GetCache()` — verified as a public extension point
(research [R1](research.md#r1--the-cache-is-substitutable-at-mgrgetcache--verified)),
requiring no upstream change and no drift entry.

## Technical Context

**Language/Version**: Go 1.26.3

**Primary Dependencies**: `sigs.k8s.io/controller-runtime@v0.24.1`,
`sigs.k8s.io/multicluster-runtime@v0.24.1`,
`github.com/kcp-dev/multicluster-provider@v0.8.0`,
`sigs.k8s.io/cluster-api@v1.15.0-kcp.1` (patched fork), `github.com/kcp-dev/sdk@v0.32.3`.
Pinned set is a constraint, not a variable (spec Assumptions).

**Storage**: N/A — this is a controller process. State is the kcp shard's.

**Testing**: `task test:unit` (colocated `_test.go`), `task test:integration`
against a real kcp server (`task test:integration:kcp`). Vanilla envtest is
forbidden for anything workspace-shaped (Principle III). New: a scale harness as
a named task operation, reporting through `internal/verify`'s existing
three-outcome contract ([R11](research.md#r11--reuse-the-existing-three-outcome-contract--verified)).

**Target Platform**: Linux; one `core-manager` process per replica, replicas
scaled per regional kcp shard.

**Project Type**: Go module — controller manager binary plus internal packages.

**Performance Goals**: **Deliberately unset.** Per FR-021 and FR-026 every
numeric bound is an output of the harness, not an input to design. Setting one
here from source-reading estimates is the unverified claim Principle V
prohibits.

**Constraints**: public extension points only, no new `DRIFT.md` entry
(FR-023); tenancy and lifecycle guarantees of the per-workspace wiring feature
hold unchanged (FR-024); the admission ceiling of one workspace (G4) is
untouched and unrelieved.

**Scale/Scope**: per-shard capacity, to be measured. 100,000+ total is reached
by composing regional shards, explicitly not by one process.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle | Gate | Pre-design | Post-design |
|---|---|---|---|
| I — Divergence counted and temporary | Does this add a carried patch? | PASS — no upstream change planned; R1 verifies the seam is public | PASS — R5 explicitly refuses the one workaround that would have meant copying upstream code |
| II — Public extension points | Does anything need an internal or a missing hook? | PASS with a finding — R5 records REST-mapper sharing as blocked, to be raised not worked around | PASS — FR-008 is blocked on the finding's response, not routed around |
| III — Test-first against real kcp | Are both tiers planned? | PASS — unit for the cache/demux logic, integration against real kcp for engagement, sharding and isolation | PASS — see Phase 1 contracts and quickstart |
| IV — Done is a command | Is every acceptance a runnable command? | PASS — SC-001–SC-015 are harness or `task verify` outcomes; the harness reuses the three-outcome contract | PASS |
| V — Verify dependencies against source | Are claims verified or labelled? | PASS — research.md tags every entry VERIFIED / ASSUMED / OPEN with file and line | PASS |
| VI — Documentation ships with the change | Are both audiences covered? | PASS — P7 covers design and user docs, and FR-026's capacity figures need a published home | PASS |
| VII — History and review discipline | Conventional commits, squash, no agent co-author trailers | PASS | PASS |
| VIII — Build what is needed now, except at seams | Is scale work justified by need, not anticipation? | PASS — a stated requirement from the repository owner, **and** FR-031 turns the principle inward: gated work must be measured to bind before it is built | PASS — the plan's phase order enforces it rather than merely asserting it |

**No violations. Complexity Tracking is therefore omitted.**

One point deserves emphasis rather than a table row. Principle VIII's narrow
exception covers "correctness properties that are cheap to establish now,
structural to retrofit, and silent when violated" — tenancy boundaries and
lifecycle contracts. FR-002 (no cross-workspace event delivery), FR-012
(disengagement releases everything) and FR-015 (no double reconcile) fall
squarely in it, which is why they are **unconditional** while the performance
requirements around them are gated. Getting that split wrong in either direction
is the main way this plan could fail its own constitution.

## Project Structure

### Documentation (this feature)

```text
specs/20260815-211812-workspace-wiring-scale/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0 output — R1..R11, tagged VERIFIED/ASSUMED/OPEN
├── data-model.md        # Phase 1 output — entities and state
├── quickstart.md        # Phase 1 output — how to run and read the harness
├── contracts/           # Phase 1 output — Go interface contracts
│   ├── cache-interposition.md
│   ├── scale-harness.md
│   └── capacity-report.md
├── checklists/
│   └── requirements.md  # Spec quality checklist (3 iterations recorded)
└── tasks.md             # Phase 2 output — NOT created by /speckit-plan
```

### Source Code (repository root)

```text
cmd/
└── core-manager/          # flags for concurrency (FR-010), rate ceiling (FR-019),
                           # coordinator selection (FR-013)

internal/
├── providerwiring/        # the seam. workspaceManager gains a GetCache override
│                          # (R1) alongside its existing Add/GetWebhookServer ones.
│                          # Engagement retry (FR-007) lands here too.
├── clusterdemux/          # NEW, gated. Per-GVK event demultiplexing:
│                          # one registration per type, map[clusterName][]handler,
│                          # per-cluster replay via ByIndex. Only if P3 says build.
├── coremanager/           # SetupReconcilers: concurrency defaults (FR-010)
├── scaleharness/          # NEW, unconditional. Profiles, sweep, knee detection,
│                          # reporting through internal/verify's Outcome types.
├── workspacetelemetry/    # NEW, unconditional. Per-workspace attribution with
│                          # bounded cardinality (FR-017/FR-018; design in P1)
└── verify/                # existing three-outcome contract — reused, not forked

test/integration/
├── coremanager/           # existing
├── providerwiring/        # existing — extended for retry and churn
├── scale/                 # NEW — the sweep, against a real kcp
└── sharding/              # NEW — two managers, fencing, no double reconcile

docs/
├── site/content/en/docs/design/
│   ├── per-workspace-wiring.md    # existing — updated for the new cost model
│   └── workspace-scale.md         # NEW — cost model, the two limits (FR-016)
└── site/content/en/docs/user/
    └── capacity-planning.md       # NEW — the published capacity figures (FR-026)

Taskfile.yaml              # NEW target: task test:scale
```

**Structure Decision**: follows the repository's existing shape — thin `cmd/`,
implementation in `internal/`, integration tests under `test/integration/` per
subject, docs split design/user per Principle VI. Three new internal packages,
each with a single responsibility and a clear trigger:
`scaleharness` and `workspacetelemetry` are unconditional (nothing can be
measured or attributed without them); `clusterdemux` is created **only if** the
P3 gate says its cost binds.

## Implementation phases

These become `tasks.md` via `/speckit-tasks`. The ordering is not a preference
— P3 is a gate, and phases after it are conditional on its output.

### P1 — Observability foundation (unconditional)

**Why first**: the harness cannot report per-workspace load the process does not
expose, and today it exposes none — `SkipNameValidation`
(`internal/coremanager/setup.go:297`) makes every workspace's controllers share
a name, aggregating metrics across tenants by construction.

Delivers FR-017, FR-018; unblocks P2. Carries the open design decision from
[R7](research.md#r7--per-workspace-telemetry-without-unbounded-cardinality--open)
— attribution with bounded cardinality — which must be decided with evidence,
not assumed.

**Exit**: a running process reports engaged count, engagement progress and
failures, and per-workspace reconcile load, with cardinality bounded as
workspace count grows (SC-008 measurable).

### P2 — Scale harness (unconditional)

**First task, before harness design is fixed**: resolve
[R10](research.md#r10--can-the-environment-host-a-meaningful-sweep--open) —
measure how many workspaces the environment can actually create and bind against
a real kcp. This number constrains everything; designing the harness before it
exists risks a harness that cannot run.

Then: load profiles (idle-heavy and active-heavy per FR-026), the sweep across
geometrically spaced counts, knee detection per FR-030's defined procedure, and
reporting through `internal/verify`'s `Outcome` types with `task test:scale` as
the named operation.

**Exit**: SC-001. `task test:scale` produces engagement latency, per-event
delivery cost, per-workspace footprint and throughput at a configurable count
and profile, and reports "could not run" distinctly from "failed".

### P3 — The gate (unconditional, blocking)

Run the sweep on both profiles against the **current** implementation. Produce
the baseline, locate the knee per FR-030, derive candidate per-shard capacity
per FR-026/FR-027, and **record a determination for each of the eight gated
requirements**: build (the cost binds at or below a plausible capacity) or close
(it does not), with the figures either way.

**Exit**: SC-013's determinations exist and are committed. Nothing in P5 may
start before this.

**This phase can cancel work.** That is its purpose, not a failure mode.

### P4 — Unconditional correctness and robustness (parallel with P2/P3)

Independent of the gate, because each is wrong at any workspace count:

- FR-007 engagement retry with bounded backoff — today a failed engagement is
  logged and forgotten (`multicluster-provider` `pkg/provider/provider.go:365`),
  recovering only at the ~10h resync. SC-014.
- FR-010 concurrency default chosen for many-tenant operation, made
  configurable. Currently 10 per controller per workspace, inherited from
  single-tenant upstream `main.go`.
- FR-012 disengagement releases everything, verified under sustained churn.
  SC-009.
- FR-019 aggregate rate ceiling and backoff, pending
  [R8](research.md#r8--aggregate-backpressure--assumed)'s spike. SC-015.
- FR-028/FR-029 report position against stated capacity; degrade observably.

### P5 — Gated cost reduction (conditional on P3)

Only the requirements P3 marked *build*. If all are closed, this phase does not
exist and the feature is complete without it — a legitimate and cheap outcome.

If the event-delivery costs bind: `internal/clusterdemux`, interposed at
`workspaceManager.GetCache()` per R1, with per-cluster replay per R2. First TDD
cycle settles [R6](research.md#r6--hassynced-for-a-per-cluster-registration--open)
(`HasSynced` semantics) against a fake informer. FR-002's isolation property is
the invariant the whole design is checked against — it is unconditional and
constrains whatever is built here.

FR-008 additionally blocked on R5's Principle II finding; it must not be
delivered by copying upstream code.

**Exit**: SC-002–SC-006 for built requirements; recorded determinations for
closed ones. SC-010 before/after evidence from the same harness and profile.

### P6 — Horizontal scale (unconditional, sequenced after P5)

FR-013/FR-014/FR-015/FR-016. Entry criterion is
[R3](research.md#r3--the-sharded-coordinator-exists-and-is-pinned--verified-behaviour-assumed)'s
spike: does the sharded coordinator's per-cluster Lease fencing actually hold,
does it compose with the kcp `apiexport` provider, and what happens to leader
election and the elected-leader-dependent webhook workspace
(`cmd/core-manager/main.go:217`)?

Sequenced after P5 because sharding a super-linear cost still leaves each
replica paying it over its own share. **Exit**: SC-007.

### P7 — Documentation and published capacity (unconditional)

Principle VI. Design doc for the cost model and the two limits (FR-016, stated
where it will not be missed); user doc giving operators the capacity figures in
the units of FR-027; update `per-workspace-wiring.md`, whose cost description is
superseded. Record every closed gated requirement as a deferral with its trigger
(FR-025).

**Exit**: SC-011, SC-012, and FR-026's figures published where a shard planner
will find them.

## Risks

| Risk | Effect | Mitigation |
|---|---|---|
| Environment cannot host a meaningful sweep (R10) | The gate cannot get evidence; gated requirements have no determination | Resolve first in P2. Report "could not run" per FR-022 rather than passing. Fall back to synthetic load plus **labelled** extrapolation |
| Sharded coordinator fencing does not hold (R3) | FR-015 unmet; two replicas could reconcile one workspace | Spike is P6's entry criterion. Falling back to leader election costs throughput, not correctness, and is an honest documented outcome |
| Demux `HasSynced` subtly wrong (R6) | Controllers reconcile against a partial view — silent, and a tenancy-adjacent bug | Unit tests are the first TDD cycle; FR-002 isolation asserted at integration tier against real kcp |
| Telemetry cardinality unbounded (R7) | Fixing observability creates a new scale problem | Decide in P1 with the three candidates evaluated; SC-008 asserts boundedness, so it is measured not assumed |
| The gate closes most of the feature | Large planned effort evaporates | **Not a risk — the intended outcome when the evidence says so.** Principle VIII, applied to ourselves |

## Deliberate non-goals

Restated so deferral is a decision, not an omission (FR-025):

- **Webhook routing across workspaces (G4).** Untouched. Admission remains
  capped at one workspace regardless of how far reconciliation scales, so the
  capacity figures P7 publishes describe reconciliation only. **Trigger**: G4.
- **Rewriting reconcilers against `mcreconcile.Request`.** The structurally
  cleanest fix and explicitly rejected: it abandons unmodified upstream
  reconcilers, which is the repository's premise. **Trigger**: a decision to
  change that premise.
- **Sharding cache memory within an endpoint slice.** Not possible — R4. Only
  slice/`Partition` splitting divides it, and that is kcp topology, out of
  scope.
