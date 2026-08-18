# Constitution amendment history

Amendment entries for versions before the one recorded at the top of
[`constitution.md`](constitution.md). They are kept here rather than in the
constitution itself so that reading the governing rules does not mean reading
every past decision about them; that file carries the current entry, this one
carries the rest, newest first.

Moving an entry here changes no rule. The constitution remains the authority.

---- Previous entry ----
Version change: 2.1.0 → 2.1.1
Rationale: PATCH — no principle is added, removed or redefined. This change
moves superseded amendment entries out of this file and states where they
went. Every rule reads exactly as it did at 2.1.0.

Modified principles: none.

Added sections: none. Removed sections: none.

The stacked entries for 2.1.0 and earlier now live in
`.specify/memory/constitution-history.md`. They had grown to 108 lines ahead
of the first principle — a third of the file — which every reader and every
agent that opens the constitution paid for before reaching a rule. Git already
holds that history; this keeps one entry here, as the workflow requires, and
the rest one link away.

Follow-up TODOs: `/speckit-constitution` prepends the report for each
amendment and does not roll older entries out. Whoever runs the next amendment
moves the entry it supersedes into the history file, or teaches the command to.

---- Previous entry ----
Version change: 1.2.0 → 2.0.0
Rationale: MAJOR — Principle VII's pull-request title rule is redefined,
not extended. The emoji-prefix convention inherited from upstream Cluster
API is replaced by Conventional Commits.

Modified principles:
  VII. History And Review Discipline — titles now follow Conventional
  Commits so release automation can derive versions and changelog entries
  from them. The no-issue-number rule survives; the emoji requirement does
  not.

Reason: the project is adopting release-please, which reads Conventional
Commit titles. Because pull requests are squash merged, the title is the
commit that lands on the default branch, so it is what release automation
parses. The two conventions cannot both hold.

Note: the previous rule's enforcement had already been removed. The check
lived in pr-verify.yaml and hack/scripts/verify/verify-pr-title.sh, both
deleted with the upstream tree, so no automation is being broken here — but
the replacement check is now specified rather than assumed (tasks T038).

Follow-up TODOs: AGENTS.md's "PR title format" section still documents the
emoji table and must be rewritten in the same change.

---- Previous entry ----
Version change: 1.1.0 → 1.2.0
Rationale: MINOR — Principle I materially expanded with a bounded
"proposal pending" state. No principle removed or redefined.

Modified principles:
  I. Divergence From Upstream Is Counted And Temporary — a carried change
  may now precede its upstream proposal, but only with a filing date
  recorded in the drift record, no more than 90 days out. An expired or
  absent date is a defect, exactly as a missing proposal was before.

Reason: implementing the first carried patch surfaced a conflict between
this principle and the repo-inversion specification, which puts filing the
upstream proposal out of scope. Without this amendment the drift record
would land in a self-declared defective state on day one. Amended rather
than left violated, per the governance rule that deviations are stated
explicitly rather than tolerated silently.

Follow-up TODOs: none.

---- Previous entry ----
Version change: 1.0.0 → 1.1.0
Rationale: MINOR — new principle added (VIII. Build What Is Needed Now —
Except At Seams). No existing principle removed or redefined.

Added principles:
  (added) → VIII. Build What Is Needed Now — Except At Seams

Follow-up TODOs: the repo-inversion specification predates this principle
and over-specifies against it — notably a machine-readable upstream-proposal
reference format for a drift record whose expected size is one entry. Revise
the specification against Principle VIII before planning.

---- Previous entry ----
Version change: (unversioned template) → 1.0.0
Rationale: Initial ratification. The scaffold shipped by Spec Kit was
placeholder text; this is the first populated constitution, derived from
AGENTS.md plus principles established by this project's Phase 1 results.

Modified principles:
  [PRINCIPLE_1_NAME] → I. Divergence From Upstream Is Counted And Temporary
  [PRINCIPLE_2_NAME] → II. Integrate Through Public Extension Points
  [PRINCIPLE_3_NAME] → III. Test-First, Against Real kcp (NON-NEGOTIABLE)
  [PRINCIPLE_4_NAME] → IV. Done Is A Command, Not A Judgement (NON-NEGOTIABLE)
  [PRINCIPLE_5_NAME] → V. Verify Dependencies Against Source
  (added)           → VI. Documentation Ships With The Change
  (added)           → VII. History And Review Discipline

Added sections:
  [SECTION_2_NAME]  → Environment And Tooling Constraints
  [SECTION_3_NAME]  → Development Workflow

Removed sections: none.

Follow-up TODOs: none. RATIFICATION_DATE is set to the date this
constitution was first populated; prior governance lived in AGENTS.md and
was never separately ratified.
