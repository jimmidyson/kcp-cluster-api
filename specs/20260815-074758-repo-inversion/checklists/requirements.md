# Specification Quality Checklist: Standalone repository, task runner, and CI

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

## Notes

Validation passed after one revision. The soundness gate raised four
Important and three Minor findings against the first draft, all fixed in
place:

1. **No time budget on verification** — SC-002 promised a definitive answer
   from one command without bounding how long it takes. An unattended gate
   that takes 40 minutes is a different product from one that takes 5.
   Added NFR-001/002 and SC-008.
2. **FR-016 referenced an artifact that did not exist** — "an agreed,
   recorded list" of permitted patches, with no statement of where it lives
   or who maintains it. Now a checked-in drift record, added as a Key Entity,
   with the check failing in both directions.
3. **FR-013 was the vaguest requirement in the document**, despite covering
   the exact failure this specification exists to prevent — a step that
   cannot run being counted as a pass. Now a distinct machine-readable third
   outcome, with FR-013a requiring capabilities to be checked up front, and
   SC-009 giving it a verification method.
4. **Governance was invalidated but out of scope** — the contributor and
   agent guidance is built almost entirely around a prohibition on editing a
   tree this feature deletes. Added FR-021/022 so it is rewritten as part of
   the change rather than left contradicting the repository.

Minor: added Purpose and Out of Scope sections; replaced "fail loudly and
early" and the subjective SC-007 with objectively checkable statements; made
the upstream-proposal reference machine-readable (FR-017).

**Second revision — Constitution Principle VIII (YAGNI with a seam
exception), ratified after this spec was written.** Re-reviewed against it
and cut seven requirements that built machinery for a set of one: 22
functional requirements down to 20 numbered (five removed, renumbered), four
non-functional down to three, and one success criterion reworded to stop
claiming automation that is no longer specified. Every cut is recorded in
the spec's new Deferred section with the trigger that would justify building
it, per the principle's requirement that deferral be a decision rather than
an omission.

The most over-specified item was FR-017: a fixed machine-readable format for
upstream-proposal references, plus a check enforcing it, against a drift
record whose expected size is one entry. The seam exception was applied
twice, deliberately, to keep the "step could not run" outcome and the
public-interface requirement — both silent-failure or build-blocking, not
convenience.

Points carried forward to planning:

- **Requirements name no tools.** Named operations, tooling installation and
  automated checks are described by behaviour, not by the runner, language or
  CI product implementing them. The specific choices already made
  (task runner, installation mechanism, CI provider) belong in the plan.
  The Assumptions section names concrete versions and repositories only where
  they are decisions already taken, not requirements to be derived.

- **One hard ordering dependency, deliberately stated as an edge case rather
  than a requirement.** FR-004 cannot be satisfied until the patched fork
  exposes the contract-metadata resolver publicly. Planning must sequence
  the fork work first; there is no partial or degraded path, and the spec
  says so explicitly so it cannot be discovered mid-implementation.

- **"Non-technical stakeholders" interpreted as "not requiring knowledge of
  this codebase".** This is developer infrastructure, so the audience is
  engineers; the test applied was that a reader who has never seen this
  repository can follow what changes and why.

- **Assumption most likely to need overriding**: the published module
  identity follows the current repository name. If the project is destined
  for a shared organisation, deciding that before implementation avoids a
  second rename across every import path.

- **SC-002 and FR-013 are in tension by design.** A single command must give
  a definitive answer, and must also distinguish "this environment cannot run
  this step" from "the code is broken". Planning must make that distinction
  explicit rather than resolving it by letting an unrunnable step pass — the
  failure mode this specification exists to prevent.
