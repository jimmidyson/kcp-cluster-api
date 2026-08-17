# Feature Specification: Active workspace sweeps

**Feature Branch**: `claude/kcp-workspace-sweeps-w63epp`

**Created**: 2026-08-17

**Status**: Draft

**Input**: The "Scalability" section of
[`docs/conversion-plan.md`](../../docs/conversion-plan.md), and Constitution
Principle V (verify dependencies against source) where a claim about a
dependency cannot be settled by reading source.

## Purpose

The design this project rests on is a bet about resource usage: that one
process can serve many kcp workspaces because reads come from one wildcard
cache shared across all of them, so watches and startup LISTs are O(types)
rather than O(types × workspaces), and only the per-workspace controller
overhead — a workqueue, a rate limiter, some goroutines — multiplies.

That bet is written down as three claims and has never been measured. Two of
them were argued from `multicluster-provider`'s source, which is what
Principle V asks for wherever source settles the question. This one it does
not: whether a cost multiplies is a property of a curve, and a curve is not
visible in a type signature. The third claim — per-workspace overhead is
"cheap relative to a duplicated cache, but not free" — has no number in it at
all, which makes it unusable for the decisions it is supposed to inform:
how many workspaces a replica can serve, whether Phase 4's idle-workspace
eviction is worth building, and what a regression would even look like.

This feature makes those claims measurable, measures them, and leaves behind
the instrument that measures them again.

The workspaces in the sweep are **active**: each holds objects that a real
controller reconciles through that workspace's own manager. A sweep over
workspaces that are merely bound would measure the cheapest possible case,
and the claim being tested is about the expensive one.

## Out of Scope

- **A performance budget.** This feature measures and asserts the design's
  own claims. It does not invent a threshold for goroutines, heap or
  discovery traffic that nobody has agreed to; unbudgeted quantities are
  reported, not bounded. Trigger for setting budgets: a capacity requirement
  that states one.
- **Throughput or latency.** Reconcile rate, workqueue depth and API latency
  are a different question with a different instrument. This is about what
  serving W workspaces costs while idle-ish, not how fast it goes.
- **Multiple shards or replicas (D6/Phase 4).** One kcp server, one
  `APIExportEndpointSlice`, one manager process.
- **Idle-workspace eviction.** The sweep measures the cost that would justify
  building it. Building it is Phase 4.
- **Continuous regression tracking.** The report is written on every run and
  is comparable between runs, but nothing stores a baseline or fails on
  drift from one. Trigger: a measured cost that someone needs to hold to.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Someone can state what a workspace costs (Priority: P1)

An engineer sizing a deployment, or deciding whether idle eviction is worth
building, asks what one more active workspace costs this process. They get
numbers — goroutines, heap, watch streams, discovery requests — measured
against a real kcp server, with the conditions they were measured under.

**Why this priority**: this is the feature. Every other item here exists to
make this number trustworthy.

**Acceptance**: `task test:sweep` writes `bin/sweep-report.md` and
`bin/sweep-report.json` with a per-workspace figure for each measured
quantity, and the conditions of the run.

### User Story 2 - The O(types) claim is proved rather than asserted (Priority: P1)

A reviewer reading the conversion plan's claim that watches do not multiply
per workspace can see it demonstrated, against a real shard, rather than
argued from a library's source.

**Why this priority**: this is the claim the whole architecture rests on. If
it is false, the adoption decision (D4) was wrong, and no amount of
per-workspace tuning fixes it.

**Acceptance**: `task test:sweep` fails if watch streams grow with the
workspace count, or if any watch is addressed to a single workspace rather
than to the shared wildcard endpoint.

### User Story 3 - A leak shows up as a leak (Priority: P2)

An operator's process serves workspaces that come and go for weeks. What
those departed workspaces cost is released, rather than accumulating until
the process is restarted.

**Why this priority**: the per-workspace wiring specification asserts this
qualitatively ("stops costing anything", FR-004) and tests it for one
workspace with one runnable. The failure mode it is really about — slow
accumulation over many engagements — is only visible as a slope.

**Acceptance**: `task test:unit` for the wiring's own per-workspace cost
against a fake provider, and `task test:sweep` for the real one: after every
workspace has unbound, the process holds no more goroutines than it did while
serving one.

### Edge Cases

- The sweep is run on a machine that cannot start a kcp server: this reports
  as its own outcome, not as a pass (Principle IV).
- A sample is taken while the process is still reacting to the last step: the
  measurement waits for the goroutine count to hold still, and fails rather
  than sampling a process in motion.
- A watch is re-established by the shard mid-sweep: this must not read as a
  new watch stream. Distinct streams and total requests are counted
  separately for exactly this reason.
- A cached test result: a measurement that did not run must not report
  numbers.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The sweep MUST measure a process serving 1..W workspaces, one
  workspace at a time, sampling after each.
- **FR-002**: Each measured workspace MUST be active — holding objects that a
  real controller has reconciled through that workspace's own manager —
  before the sample that counts it is taken.
- **FR-003**: The measurement MUST distinguish requests that address every
  workspace at once (the shared wildcard endpoint) from requests addressed to
  a single workspace, and MUST distinguish distinct streams from repeats of
  the same stream.
- **FR-004**: The measurement MUST NOT count the harness's own fixture
  traffic as the manager's.
- **FR-005**: The sweep MUST fail if watch streams grow with the workspace
  count, and MUST fail if any watch is addressed to a single workspace.
- **FR-006**: The sweep MUST fail if, after every workspace has unbound, the
  process holds more goroutines than it held while serving one workspace.
- **FR-007**: The sweep MUST report every measured quantity, including those
  it does not assert on, together with the conditions of the run (workspace
  count, objects per workspace, reconciled types, Go version, GOMAXPROCS).
- **FR-008**: A sample MUST be taken from a settled process, and a sweep that
  cannot settle MUST fail rather than report.
- **FR-009**: The per-workspace cost of the wiring itself MUST be measurable
  without a kcp server, so that a change to this project's own code is
  attributable to this project's own code.
- **FR-010**: The sweep MUST be a step of the verification harness, reported
  by name, and MUST NOT be satisfiable from the test cache.

### Key Entities

- **Sample** — what the process cost at one point: goroutines, heap, and
  traffic to the shard, taken after settling.
- **Phase** — what a sample was taken after: baseline, bound-and-idle,
  active, or disengaged. Slopes are computed within a phase, never across
  one.
- **Per-workspace cost** — the slope of a measured quantity against the
  workspace count. The number this feature exists to produce.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: `task test:sweep` reports a per-workspace figure for
  goroutines, heap, watch streams and discovery requests, against a real kcp
  server, with the conditions of the run.
- **SC-002**: Watch streams per additional workspace are below 0.5 — that is,
  they do not grow with the workspace count — and no watch is addressed to a
  single workspace.
- **SC-003**: After every workspace unbinds, the process holds no more
  goroutines than it did while serving one.
- **SC-004**: The wiring's own per-workspace cost is measured with no kcp
  server, and is a flat two goroutines per workspace, fully reclaimed.
- **SC-005**: `task verify` names the sweep as a step and reports pass, fail
  or could-not-run for it.

## Findings

Recorded here because the measurement's result is part of its deliverable,
not a separate concern.

- **The O(types) claim holds.** Three watch streams served four active
  workspaces, none addressed to a tenant's logical cluster, with no
  per-workspace LISTs, discovery or requests of any kind.
- **A workspace costs 12 goroutines and ~106 KiB**, exactly linear in the
  workspace count, for one controller watching one type.
- **Two goroutines per departed workspace per watched type are not
  reclaimed.** controller-runtime's `Kind` source adds an event handler to
  the informer it watches through and never removes it; because that informer
  belongs to the shared wildcard cache rather than to the workspace, the
  handler outlives the workspace. This is the one part of user story 3 that
  does not hold, it accumulates with churn rather than with the workspace
  count, and it is now measured on every run and asserted not to grow. Fixing
  it means interposing on the cache handed to a `SetupFunc` the way `Add` is
  already interposed on — a change to this project's own seam, deferred with
  its trigger recorded in the design page.

## Known deviations

- **Heap is reported, not asserted.** The process the sweep measures contains
  the harness that measures it, including one client per workspace built by
  the test's own fixtures. Goroutine and traffic figures are unaffected by
  this (client-go shares one transport across configs that differ only in
  path); retained heap is not cleanly separable. Trigger for fixing it: a
  heap budget that someone needs to hold to, which would justify running the
  manager out of process.
- **The baseline is taken before the manager starts.** kcp populates an
  `APIExportEndpointSlice` only once a workspace has bound (ADR-0001, "lazy
  activation"), so "a running manager serving no workspaces" is not a state
  this sweep can hold still in. The baseline therefore sizes the fixed cost
  approximately; slopes, which are what the claims are about, are computed
  from the workspace samples alone and are unaffected.

## Assumptions

- One kcp server, started by the existing `test/integration/envtest` harness,
  is representative enough of a shard for the shape of a curve. Absolute
  numbers from a laptop or a CI runner are not a capacity model, and the
  report records the conditions so they are not mistaken for one.
