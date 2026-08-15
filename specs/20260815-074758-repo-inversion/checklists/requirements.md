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

**Third revision — Phase 0 research finding (R1).** Planning verified that
CRD manifests ship inside the published Go module, which the spec had assumed
was not the case. FR-005 and FR-006 required generating definitions into the
repository with a staleness check; they now require resolving them from the
pinned dependency, where they cannot disagree with the version the code is
built against. SC-006, the corresponding edge case, and the "Generated
resource definitions" entity (now "Resolved resource definitions") were
updated to match, and deployable manifest generation was added to Deferred
with the trigger being the first install from published artifacts.

This is Principle V working as intended: the spec's assumption was plausible
and wrong, and one command against a real published module settled it. The
correction was applied to the spec rather than carried as a justified plan
deviation, so the two do not disagree.

**Fourth revision — soundness gate re-run against the committed text.**
Requested explicitly because the third revision changed what a requirement
*meant* rather than only removing requirements. It found three Important
issues, two of which were contradictions the earlier revisions had
introduced:

1. **US2 acceptance scenario 5 still offered "only code generation"** as an
   example of a separately invocable portion, two revisions after generation
   was removed. A reader following it would have built the one target the
   contract says must not exist.
2. **US4's narrative still promised deferred behaviour** — that the drift
   check fails when a patch lacks an upstream proposal. FR-017 had been
   narrowed to comparing paths and the enforcement deferred, but the story
   was never updated to match.
3. **Nothing required the previous build entry point to be removed.** The
   title and Purpose said the task runner replaces it; no requirement did.
   Two definitions of how to build and test, able to disagree, is precisely
   the failure this feature exists to remove. Added as FR-021.

Minor: FR-008 now names network access explicitly and states that offline
operation is not a requirement; NFR-001 says where the budget is recorded and
that it must be measured rather than estimated; SC-007 now states openly that
it is human-verified because its check is deferred.

**Lesson recorded for the workflow, not just this spec**: the two
contradictions were both introduced by revisions that edited requirements
without re-reading the user stories those requirements serve. Prose above the
requirements section does not get re-read by default. Re-running the gate
against committed text — rather than trusting the reviewer who just made the
edits — is what caught it, and is worth making the standard.

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
