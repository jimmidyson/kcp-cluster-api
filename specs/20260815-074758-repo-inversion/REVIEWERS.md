# Review Guide: Standalone repository, task runner, and CI

**Generated**: 2026-08-15 | **Spec**: [spec.md](spec.md)

## Why This Change

This project is a fork of Cluster API whose entire value proposition is that
adopting new upstream releases stays cheap. That depends on never modifying
upstream code — a rule currently enforced by asking people not to, over a
tree that is 95% upstream files. The rule is already broken: seven inherited
workflow files carry local edits, and dependency bots have been rewriting
upstream dependency manifests in four separate modules. Every one of those
edits becomes a conflict on every future upgrade, compounding release after
release. Meanwhile there is no single command that says whether the project is
healthy, which makes handing work to anyone — human or agent — a matter of
trusting their judgement rather than checking a result.

## What Changes

The upstream tree is deleted and Cluster API becomes an ordinary versioned
dependency, resolved to a small patched fork. The project's own code moves
from the `kcp/` subdirectory to the repository root and the module is renamed,
so every import path in the project changes. The inherited Make entry points
and eleven workflows are replaced by a task runner and two workflows this
project owns. A new `DRIFT.md` records what the fork diverges by, and a check
fails if reality disagrees with it.

**Breaking, and worth knowing upfront**: the module path changes, so anything
importing this project must update. Consumers must also mirror the dependency
pinning, because that kind of pinning does not propagate downstream — an
accepted cost, since the project ships binaries rather than a library, and a
further reason to retire the carried patch.

## How It Works

The single blocking dependency is a public seam in the fork. This project
imports exactly one upstream internal package (`internal/contract`, at three
call sites); that import is legal today only because the module path sits
beneath `sigs.k8s.io/cluster-api/`. The fork adds
`controllers/external.SetGKMetadataGetter` and `external.GetAPIVersion`,
delegating to the existing internal implementation with behaviour unchanged.
Until that tag exists, nothing here compiles — so it is Phase 2, strictly
serial, in a different repository.

Everything else follows: move files, rewrite the module path and imports,
swap three relative `replace` directives for pins to `v1.15.0-kcp.1`, then
build the task surface, then point CI at it.

One research finding changed the design. CRD manifests ship *inside* the
published Go module — verified by downloading a released module and listing
`config/crd/bases`. So fixtures resolve them from the pinned dependency
rather than from generated, checked-in copies. They are then in step by
construction, which removed a generation step, a checked-in artifact and a
staleness check that the first draft of the spec required.

## When It Applies

**Applies when**:

- Building, testing or verifying this project, locally or in CI
- Adopting a newer Cluster API release
- Adding or retiring a patch against upstream

**Does not apply when**:

- Changing what the software does. Reconciler behaviour, webhook routing and
  the identified per-workspace manager corrections are explicitly excluded —
  this feature moves and rebuilds, it does not redesign
- Submitting the upstream proposal. The fork must merely exist and expose
  what is needed
- Moving to a different organisation or repository name

## Key Decisions

1. **Invert the repository rather than tighten the rule.** Alternatives were
   keeping the read-only policy with better enforcement, or starting a fresh
   repository. Enforcement fails because dependency bots and CI maintenance
   generate the drift automatically; a fresh repository loses the project's
   own history. Deleting the upstream tree in a commit makes the invariant a
   property of the repository rather than a rule to remember.

2. **A narrow public seam in the fork, not a wholesale re-export.** Only two
   symbols are needed. A narrow seam is also the shape most likely to be
   accepted upstream, since it mirrors `conversion.SetAPIVersionGetter`, which
   already exists for the same problem on a different call path. Rejected:
   keeping the module path beneath `sigs.k8s.io/cluster-api/` to preserve the
   internal import (needs a vanity domain or a permanent replace for every
   consumer, and preserves the exact coupling being removed); copying the
   resolver's logic locally (a silent fork with no drift entry and no upstream
   path).

3. **Fork branch cut from a commit, because a release tag is not possible.**
   Basing the branch on `v1.14.0` was proposed, to minimise divergence from a
   release, and tested: two packages this project imports —
   `test/infrastructure/docker/reconcilers` and
   `.../webhooks/admission` — do not exist at that tag. They were under
   `internal/` at v1.14.0 and became public on `main` afterwards, in a
   132-file reorganisation. An external module cannot import `internal/`, so
   the tag either fails to build or forces us to carry the whole
   reorganisation as drift. The fork point is load-bearing, not arbitrary.

   The fork is tagged `v1.15.0-kcp.1` because that base carries public API
   surface `v1.14.0` lacks — minor-level content by semver. A 1.14.x version
   would read as "v1.14.0 plus fixes" and deliver a public API
   reorganisation. A `v1.14.1-kcp.1` tag was chosen briefly on weaker
   evidence (dates and `metadata.yaml`, which cannot distinguish the cases)
   and reversed once the package contents were checked.

4. **Tooling via `go install`, no environment manager on the critical path.**
   Verified that the devbox installer host is unreachable from the target
   environments, and that agent containers are discarded between runs, so a
   Nix store would be materialised per delegated task. The reference project
   this approach draws from also pins all 24 of its packages to "latest", so
   it is not currently buying reproducibility for that cost.

5. **Resolve resource definitions from the dependency, do not generate
   copies.** See "How It Works". The staleness problem stops existing rather
   than being detected.

## Areas Needing Attention

**Phase 2 has a different reviewer focus from everything else.** It lands in
`github.com/jimmidyson/cluster-api`, a public repository, and produces a
**published, immutable tag** that this project then depends on. Reviewers
should check it as a distribution artifact, not just a diff: that the branch
is cut from `281e4e3` and not from the fork's 2021-era `master`; that the
public seam changes no existing behaviour and upstream's own tests still pass
unmodified; that the commit references the upstream proposal it will become;
and that the tag is immutable once pushed. A mistake here is republished to
everyone who pins it, and cannot be quietly amended.

**The module rename touches every import in the project.** Mechanical, but
large. Worth confirming nothing was missed rather than trusting the compiler
alone, since a stale path can hide behind a `replace`.

**Deleting `pr-verify.yaml` removes the PR-title check** that the project
constitution depends on. T038 re-creates it. If that task is dropped or
deferred, a constitutional rule silently stops being enforced.

**The three-outcome contract may look like over-engineering.** It is
deliberately exempt from the project's YAGNI principle under its seam
exception: a step that cannot run must never be counted as a pass. This is a
response to a specific past event — an integration test that asserted
reconciliation "got past" a failure rather than reaching its goal. Reviewers
inclined to simplify it to pass/fail should read spec FR-012/FR-013 and
SC-009 first.

**Two tasks cannot be completed in a development container** (T051's
container-runtime scenarios, T052's measured budget). This is stated rather
than worked around; a reviewer should confirm they were genuinely run on a
real runner and not marked done from a plausible estimate.

## Open Questions

1. **Fork branch and tag names** (T001) are a maintainer decision, unresolved
   at the time of writing. The plan proposes branch `kcp/v1.15` and tag
   `v1.15.0-kcp.1`. The repository is public and the tag is a published
   artifact, so this is not an implementation detail.
2. **SC-003 cannot be verified within this feature.** "Adopting a newer
   release requires changing only dependency pins" is only demonstrable the
   first time a bump is actually done. T046 documents the procedure; the
   criterion is confirmed later.
3. **Whether generated agent skills should be committed.** `.gitignore`
   excludes `.claude/`, so the 25 generated skills are untracked — meaning
   fresh containers do not get them without re-running setup. Not part of this
   feature, but it interacts with delegating this work.

## Review Checklist

- [ ] Key decisions are justified
- [ ] Breaking changes are documented with migration guidance
- [ ] Scope matches the stated boundaries
- [ ] Success criteria are achievable
- [ ] No unstated assumptions
- [ ] Fork tag is immutable, cut from `281e4e3`, and changes no upstream behaviour
- [ ] Upstream's own tests pass unmodified against the fork's seam
- [ ] `DRIFT.md` matches the fork's actual divergence, and each entry names an upstream proposal
- [ ] The PR-title check survives the deletion of the inherited workflow
- [ ] Tasks that require a real runner were run on one, not estimated
- [ ] No test assertion was weakened to accommodate the move

---

<!-- Code phase sections are appended below this line by the phase-manager command -->
