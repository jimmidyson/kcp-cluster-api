# Specification Quality Checklist: Workspace wiring that scales to a large fleet

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-08-15
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Validation notes

**Iteration 1 findings and resolutions:**

1. *"No implementation details"* — initially failed. The costs table names
   dependency packages, files and line numbers. Retained deliberately: it is
   evidence for the problem statement, required by Constitution Principle V
   ("a design claim about a dependency's behaviour MUST be verified against
   that dependency's source... design documents MUST distinguish what has been
   verified from what is assumed, and MUST record how a verified claim was
   checked"). It describes what *currently* costs what, not what will be built.
   No requirement or success criterion names a mechanism, a library, or a data
   structure. Where a solution shape was tempting — a routing scheme, a
   sharding mechanism — FR-001/FR-002 and FR-013/FR-015 state the property
   required and leave the mechanism to planning. Marked pass on that basis;
   flagged here so the decision is visible rather than silent.

2. *"Success criteria are measurable"* — several absolute figures are marked
   *(to be set)*. Marked pass: the criteria themselves are measurable
   statements with a named producer (SC-001's harness), and setting the
   absolute numbers now from source-reading estimates would assert unverified
   figures, which Principle V prohibits and FR-021 explicitly forbids. The
   relative criteria (SC-002, SC-003, SC-005) are fully specified and are the
   load-bearing ones.

3. *"Written for non-technical stakeholders"* — partial by nature. The subject
   is the internal cost structure of a controller process; there is no
   non-technical framing of "per-event delivery cost". User stories and success
   criteria are written for an operator rather than an implementer, which is
   the closest available audience. Marked pass with that qualification.

4. *"Scope is clearly bounded"* — pass. Four exclusions carry triggers, and two
   known deviations are recorded with their own triggers, per Principle VIII's
   requirement that deferral be a recorded decision.

**Iteration 2 — clarifications resolved, and the spec reshaped by the answer:**

Both open questions are closed, but not by being answered as posed. The
repository owner reframed the target: the unit of scale is a **regional shard
with replicas scaled per shard**, with explicit per-shard capacity limits,
reaching 100,000+ by composition rather than by one process serving that many.
The spec was restructured around this:

- A new "deployment model" section states what the reframing solves (the
  independent variable becomes one shard's workspaces; cached state is bounded
  by a number an operator sets) and what it does not (the costs are super-linear
  *within* a process, so a limit sets the constant without flattening the curve).
- Capacity became the primary deliverable (new User Story 5, P1; FR-026–FR-031;
  SC-013), derived from a measured sweep rather than asserted.
- FR-031 turns Principle VIII inward: a cost that does not bind below a usable
  capacity is to be **closed rather than optimised**. User Story 1 carries an
  explicit "conditional on measurement" clause on the same basis. This is the
  most consequential change — it makes measurement a gate on the feature's own
  contents, not just evidence for them.
- Q1 (active-to-idle ratio) is resolved *by construction*: capacity is stated
  per load profile, so the harness measures both and an operator matches their
  own shard against the nearer one. Better than the answer would have been,
  since the ratio varies per installation.
- Q2 (deployment envelope) is resolved by the deployment model, with one part
  promoted to a requirement: replicas divide reconciliation but **not** cached
  state, verified at `controller-runtime@v0.24.1` `pkg/manager/internal.go:446`
  vs `:477` — caches start before leader election, so a standby replica holds
  the full cache while reconciling nothing. FR-016 now requires this be
  documented so the two limits are not conflated.

Re-validated all items after restructuring: 31 functional requirements
(FR-001–FR-031, contiguous and monotonic in document order), 13 success
criteria (SC-001–SC-013), 7 user stories. No markers remain.

**Iteration 3 — spec review gate (`speckit.spex-gates.review-spec`), 5 Important
and 4 Minor findings, all fixed:**

| # | Finding | Resolution |
|---|---|---|
| I1 | FR-031 let requirements be "closed as unnecessary", but FR-001–FR-012 were unconditional `MUST`, and SC-002/SC-003 asserted flatness unconditionally. A MUST that may be closed is not a MUST | Added a **"The measurement gate"** section defining exactly one form of conditionality, and marked the eight requirements it governs `(gated)` (FR-001, 003, 004, 005, 006, 008, 009, 011) plus the five criteria that evaluate them (SC-002–SC-006). Correctness and robustness requirements stay unconditional, with each carrying a sentence saying why |
| I2 | FR-030's "departs from linear" was not a command — no metric, tolerance, point count or fitting rule, so two runs would yield two capacities | FR-030 now specifies the swept quantities, a minimum count of geometrically spaced points, a stated deviation tolerance, and the departure point rule (smallest count exceeding the linear projection from the two smallest points by more than tolerance), with tolerance and point count recorded per result |
| I3 | Deployment-model reframing was applied to the front half only; SC-002/003/004/005/009, Story 7, an Out of Scope trigger, a Known deviation and a Key Entity still said "fleet size" — making the "may not reach the target" deviation permanently unsatisfiable, since no single-process harness reaches 100k | Swept throughout to per-shard terms; the "fleet" convention is now stated once, up front. The deviation explicitly notes it bounds per-shard capacity only and that the target is reached by composing shards |
| I4 | Story 7/FR-019 (backpressure) and FR-007 (engagement retry) had acceptance scenarios but no Success Criterion, so no exit status | Added SC-014 (retry) and SC-015 (backpressure) |
| I5 | Missing blank line before `**Constraints**`, rendering the heading inside the FR-022 list item | Fixed |
| M1 | FR-013 prescribed active-active ("every replica actively reconciling rather than standing by") — a mechanism, not a property | Restated as the property: adding a replica MUST increase throughput. Story 5 scenario 5 follows suit |
| M2 | SC-013 was placed between SC-001 and SC-002 | Moved to sequence |
| M3 | Stories ran P1, P1, P2, P2, **P1**, P2, P3 | Reordered to P1, P1, P1, P2, P2, P2, P3. Capacity is now Story 1, which also matches its billing as the story that governs the rest. All cross-references updated |
| M4 | FR-016 was a pure documentation obligation sitting among functional requirements | Given a functional clause first (the design MUST NOT rely on replicas to reduce cached state), with the documentation duty following from it |

Verified after fixes: 7 user stories in priority order; FR-001–FR-031
contiguous and monotonic; SC-001–SC-015 contiguous and monotonic; 8 gated
requirements and 5 gated criteria; no occurrences of the stale "fleet size"
framing; no clarification markers.

## Notes

- All checklist items pass. Ready for `/speckit-plan`.
- `/speckit-clarify` is not required — its two inputs were resolved during
  specification.
- **Carry into planning**: the measurement gate is a sequencing constraint, not
  a caveat. The plan must build and run the harness **first**, record a
  determination for each of the eight gated requirements, and only then
  implement the ones found binding. A plan that schedules gated work alongside
  the measurement has not understood FR-031.
