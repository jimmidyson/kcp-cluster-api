# Feature Specification: Standalone repository, task runner, and CI

**Feature Branch**: `claude/review-approach-direction-4troqb`

**Created**: 2026-08-15

**Status**: Draft

**Input**: User description: "Invert the repository so kcp-cluster-api is a standalone Go module that consumes cluster-api as an external dependency, replace Make with go-task, and migrate GitHub Actions to this project."

## Purpose

This project exists to make Cluster API workspace-aware, and its value
depends on being able to adopt new Cluster API releases cheaply. That in turn
depends on carrying as few changes to Cluster API as possible, and on those
changes being visible and temporary.

Today the project is arranged as a full copy of Cluster API with its own work
confined to one subdirectory, and the rule that the copy must never be edited
is enforced only by asking people not to. That rule is already broken:
inherited automation files carry local edits and dependency bots have been
modifying upstream dependency manifests. Each such edit becomes a conflict on
every future upgrade.

This feature changes the arrangement so that upstream code is *absent* rather
than *forbidden* — consumed as a versioned dependency, with the few required
changes isolated in a separate, measured patch set. It also replaces the
project's build entry points and automated checks, both of which are
currently inherited from upstream and cannot survive its removal, with ones
this project owns. The result is a repository where an upgrade is a version
bump, where a single command decides whether the project is healthy, and
where that command is the same one automation runs.

## Out of Scope

- Any change to what the software does: reconciler behaviour, webhook
  routing, and the identified per-workspace manager design corrections are
  explicitly excluded. This feature moves and rebuilds; it does not redesign.
- Authoring or submitting the upstream proposal itself. The patched fork must
  merely exist and expose what is needed.
- Migrating the project to a different organisation or repository name.
- Introducing a system-level environment manager for development tooling.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Upstream code is unreachable, not merely forbidden (Priority: P1)

A maintainer or agent opens the repository and sees only this project's own
code. There is no upstream Cluster API tree to read, edit, or accidentally
modify. Upstream is consumed the way any other dependency is consumed: by
version. Bringing in a newer Cluster API release is a change to a dependency
pin, reviewed as a diff of version strings, rather than a tree-wide merge.

**Why this priority**: This is the foundation everything else rests on. The
project's central invariant — that upstream code is never modified — is
currently enforced by policy and is already being broken by routine
automation: seven inherited workflow files carry local edits, and dependency
bots have been mutating upstream dependency manifests in four separate
modules. Every future rebase inherits those conflicts. Making the upstream
tree absent converts the invariant from a rule people must remember into a
property of the repository. It also removes the largest tax on delegated
work: today an agent must be taught, at length, that most of the tree is
off-limits.

**Independent Test**: Clone the repository fresh and confirm it builds with
no Cluster API source tree present, and that no file in the repository
belongs to upstream Cluster API. Delivers value on its own even before the
task runner or CI work lands.

**Acceptance Scenarios**:

1. **Given** a fresh clone containing no upstream Cluster API tree, **When** the project is built, **Then** it compiles successfully against Cluster API resolved as an external dependency.
2. **Given** the repository at any commit after this change, **When** its tracked files are listed, **Then** none of them are files that exist in upstream Cluster API.
3. **Given** a newer Cluster API release is available, **When** a maintainer adopts it, **Then** the change consists only of updated dependency pins, with no tree-wide merge and no conflict outside this project's own files.
4. **Given** this project reads a piece of Cluster API behaviour that upstream does not expose publicly, **When** the project is built, **Then** it resolves that behaviour through a publicly reachable interface supplied by the patched fork, not through a package upstream marks internal.

---

### User Story 2 - One command decides whether work is done (Priority: P1)

A contributor — human or agent — starts from a clean machine, runs a single
command, and gets a definitive answer as to whether the project is in a good
state. That command installs the tooling it needs at known versions, prepares
anything the tests depend on, and runs the full test suite. Nothing else has
to be known, installed, or remembered first.

**Why this priority**: This is the condition that makes unattended delegation
possible. Without a single command that decides "done", work handed to an
agent is graded by the agent's own judgement, which historically produced
weakened assertions passed off as success. It also removes the current
situation where the project's own documentation references a development
setup that does not exist.

**Independent Test**: On a machine with nothing but a language toolchain and
a container runtime, clone the repository and run the single verification
command. It must install its own tooling and finish with a clear pass or
fail, without any prior setup steps.

**Acceptance Scenarios**:

1. **Given** a clean environment with no project tooling installed, **When** the verification command is run, **Then** it installs every tool it needs at pinned versions and completes without any manual preparation.
2. **Given** the verification command has been run once, **When** it is run again, **Then** it reuses already-installed tooling rather than reinstalling it.
3. **Given** the test suite passes, **When** the verification command finishes, **Then** it reports success with a non-error exit status suitable for use as an automated gate.
4. **Given** any part of the suite fails, **When** the verification command finishes, **Then** it reports failure with a non-zero exit status and identifies which part failed.
5. **Given** a contributor wants to run only one portion of the work (for example only the fast tests, or only code generation), **When** they invoke that portion by name, **Then** it runs on its own without running the whole suite.

---

### User Story 3 - Continuous integration runs exactly what contributors run (Priority: P2)

A pull request is opened. Automated checks run the same named operations a
contributor runs locally, so a green local run and a green automated run mean
the same thing. No checks inherited from upstream Cluster API remain, and no
check tests code this project does not own.

**Why this priority**: Depends on Story 2 existing, but is what makes the
gate trustworthy rather than merely available. It also removes the inherited
checks that are currently a source of drift, since keeping them working has
required editing files this project is not supposed to touch.

**Independent Test**: Open a pull request and confirm the checks that run are
this project's own, that they invoke the same named operations available
locally, and that no inherited upstream check remains configured.

**Acceptance Scenarios**:

1. **Given** a pull request is opened, **When** automated checks run, **Then** every check invokes a named operation that a contributor can also invoke locally by the same name.
2. **Given** the repository after this change, **When** its automated check configuration is inspected, **Then** no check inherited from upstream Cluster API is present.
3. **Given** a change that fails verification locally, **When** it is pushed as a pull request, **Then** the automated checks fail as well, for the same reason.

---

### User Story 4 - Divergence from upstream is measured, not trusted (Priority: P3)

The patches this project carries against Cluster API are visible, countable,
and each one is on a path to being removed. An automated check reports the
current patch set and fails if it grows beyond what has been agreed or if a
patch has no corresponding proposal filed upstream.

**Why this priority**: The value of this project depends on the carried patch
set trending toward zero. Experience so far shows drift appears through
routine activity rather than deliberate decisions, so it needs measuring
rather than trusting. Lower priority only because the first three stories are
prerequisites — the drift cannot be measured before it is isolated in one
place.

**Independent Test**: Add an extra unjustified patch to the fork and confirm
the check fails; remove it and confirm the check passes.

**Acceptance Scenarios**:

1. **Given** the fork carrying this project's patches, **When** the drift check runs, **Then** it reports the exact set of files that differ from the upstream release the fork is based on.
2. **Given** a patch is added that is not in the record, **When** the drift check runs, **Then** it fails and names the unexpected patch.
3. **Given** the record, **When** a reader opens it, **Then** every carried patch names the upstream proposal it corresponds to.

---

### Edge Cases

- **The publicly reachable interface does not exist yet.** Story 1 depends on the fork exposing behaviour that upstream currently keeps internal. If the fork has not been prepared, the project cannot build at all — a hard ordering dependency, not a degradation. The failure must occur when dependencies are resolved, with a message naming the missing interface and the fork version expected to provide it, rather than surfacing later as an unrelated compilation error.
- **Published schema definitions drift from the dependency.** The project publishes its own copies of resource definitions derived from Cluster API. When the Cluster API pin moves, those copies can silently fall out of step. Verification must detect this rather than leave it to be discovered at runtime.
- **The container runtime or its image source is unavailable.** Part of the suite provisions real containers. Where images cannot be pulled, verification must distinguish "environment cannot run this" from "the code is broken", because these have been conflated before and produced a false green.
- **A required external download fails.** Verification fetches a server binary. A transient failure must be reported as an environment problem, not a test failure.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The repository MUST contain only this project's own source, tests, documentation, and configuration. No file belonging to upstream Cluster API may be tracked.
- **FR-002**: The project MUST consume Cluster API and its companion modules as external, version-pinned dependencies.
- **FR-003**: Those dependencies MUST resolve to a fork that carries this project's patches, pinned to an immutable released version rather than a moving branch.
- **FR-004**: The project MUST NOT depend on any package upstream marks as internal. Behaviour currently obtained that way MUST be obtained through a publicly reachable interface provided by the fork.
- **FR-005**: The project MUST NOT rely on reading files from an upstream source tree at build or test time. Resource definitions it needs MUST be generated into the repository and consumed from there.
- **FR-006**: The project MUST provide a command that regenerates those definitions, and verification MUST fail if the generated output in the repository is stale relative to the pinned dependency.
- **FR-007**: A single named verification operation MUST exist that installs required tooling at pinned versions, prepares test prerequisites, and runs the full test suite.
- **FR-008**: That operation MUST succeed from a clean environment containing only a language toolchain and a container runtime, with no prior setup.
- **FR-009**: Tooling MUST be installed into a location local to the repository, at pinned versions, without requiring any package manager or environment manager beyond the language toolchain.
- **FR-010**: Individual portions of the work — building, fast tests, tests requiring a running server, generation, linting — MUST each be invocable on their own by name.
- **FR-011**: All operations MUST report failure with a non-zero exit status, and MUST NOT report success when a step was skipped.
- **FR-012**: Where a step cannot run because the environment lacks a required capability, verification MUST end in a third outcome that is neither pass nor fail: a distinct, machine-readable result naming the missing capability. It MUST NOT be reported as success, MUST NOT be silently omitted from the summary, and MUST be detectable by automation without reading logs.
- **FR-013**: Every capability a step depends on MUST be checked before that step runs, so an unmet capability is reported up front rather than surfacing as a mid-run failure.
- **FR-014**: Automated checks MUST invoke the same named operations available to contributors locally, rather than reimplementing them.
- **FR-015**: All automated checks inherited from upstream Cluster API MUST be removed.
- **FR-016**: The patches carried against upstream MUST be recorded in a checked-in file naming the upstream release the fork is based on, each differing path, and — in prose — the upstream proposal each corresponds to.
- **FR-017**: An automated check MUST report the fork's actual differences against that release and fail if they do not match the record.
- **FR-018**: Dependency automation MUST operate only on this project's own dependency manifests.
- **FR-019**: Project documentation MUST describe the new layout, how to run verification, and how the fork and its patch set are maintained. Documentation describing the previous layout MUST be removed or rewritten, not left to contradict it.
- **FR-020**: The project's governance documents MUST be rewritten as part of this change, not after it. The contributor and agent guidance is currently built around a prohibition on editing a tree that will no longer exist, and MUST be replaced with the rules that actually apply: how the fork is maintained, how patches are added and retired, and how the drift record is kept.

### Non-Functional Requirements

- **NFR-001**: Verification MUST complete within a documented time budget on a clean environment, short enough to sit inside the feedback loop of a change rather than be deferred.
- **NFR-002**: The portion of verification that does not require external services or a container runtime MUST be separately invocable and MUST complete substantially faster than the full run, so the common case is not gated on the slowest path.
- **NFR-003**: Repeated verification runs on the same machine MUST NOT re-download or rebuild tooling that is already present at the pinned version.

### Key Entities

- **This project's module**: The single unit of code this repository publishes. Owns its own dependency manifest, tooling definitions, checks, and documentation.
- **The patched fork**: A separate repository holding Cluster API plus this project's carried patches. Its contract is one branch per upstream release line, one commit per patch, each referencing an upstream proposal, and immutable released versions this project pins to. It carries no specifications, tooling, or process of its own.
- **Carried patch**: A single change to Cluster API that this project needs and intends to remove. Has an upstream proposal associated with it and is expected to be deleted once that proposal is accepted.
- **Drift record**: A checked-in file in this repository naming the upstream release the fork is based on, every path the fork is permitted to differ in, and — in prose — the upstream proposal each difference corresponds to. It is the reference the drift check compares reality against, and the place a new patch must be justified before it can be accepted. It begins with a single entry.
- **Generated resource definitions**: Copies of Cluster API resource definitions, produced from the pinned dependency and stored in this repository, replacing the previous practice of reading them out of a co-located source tree.
- **Named operation**: A single invocable unit of work — build, test, generate, lint, verify — available identically to contributors and to automated checks.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Zero tracked files in this repository belong to upstream Cluster API.
- **SC-002**: A contributor starting from a clean environment can go from clone to a definitive pass/fail answer with exactly one command and no prior setup steps.
- **SC-003**: Adopting a newer Cluster API release requires changing only dependency pins, and produces no conflict in any file this project owns.
- **SC-004**: Every automated check corresponds to a named operation a contributor can run locally under the same name, with no check logic existing only in automation.
- **SC-005**: The patches carried against upstream are reported automatically on every change, and each is traceable to a proposal filed upstream.
- **SC-006**: Generated definitions falling out of step with the pinned dependency is caught automatically rather than by review.
- **SC-007**: No contributor or governance document instructs a reader to do something the repository no longer supports.
- **SC-008**: Verification completes within its documented time budget on a clean environment, and the fast subset completes in a small fraction of that, so contributors are not pushed into skipping it.
- **SC-009**: A step that cannot run for lack of an environment capability is reported as its own outcome and is never counted as a pass — verifiable by running verification in an environment missing that capability and confirming the result is neither success nor an ordinary failure.

## Assumptions

- **Repository identity**: The project keeps its current repository and history rather than starting a new one; the upstream tree is removed by a commit, so the history of this project's own work is preserved. Its published module identity follows the existing repository name (`github.com/jimmidyson/kcp-cluster-api`). If the project later moves to a shared organisation, that is a rename handled separately and is not part of this work.
- **Fork base**: The patched fork's branch is based on the upstream release line this project currently sits on (the v1.14 series), not on the fork's own long-stale default branch, which is five years behind and unusable as a base.
- **Patch set at the start**: Exactly one patch is carried initially — exposing the contract-metadata resolver publicly, which is also the change intended for upstream. The agreed drift list therefore begins at one file.
- **Dependency direction**: Consumers of this project will need to mirror its dependency pinning, since pinning of this kind does not propagate to downstream consumers. This is acceptable because the project ships runnable programs rather than a library, and it is a further reason to drive the carried patch set to zero.
- **Environment management**: No system-level package or environment manager is assumed to be available. This follows from verified constraints in the automated environments this project targets, where the relevant installer is unreachable and containers are discarded between runs, making per-run environment materialisation a recurring cost.
- **Scope boundary**: The design corrections identified for the per-workspace manager and webhook routing, the upstream proposal itself, and any change to reconciler behaviour are out of scope. This work changes where code lives and how it is built, verified, and checked — not what it does.
- **Behavioural equivalence**: The existing test suite is the definition of correct behaviour for this change. It must pass afterwards with assertions no weaker than before; weakening a test to accommodate the move counts as a failure of this work.

## Deferred

Per Constitution Principle VIII, the following were specified in the first
draft and cut because they build machinery for a set of one. Each is
recorded with the trigger that would make it worth building, so deferral is
distinguishable from omission.

| Deferred | Trigger to build it |
|---|---|
| Machine-checkable format for upstream-proposal references in the drift record, and a check that every entry has one | The record reaches roughly five entries, or an entry is found without a proposal reference by reading it |
| Drift check failing on a *recorded* patch that has disappeared, as well as on an unexpected one | A patch is lost or silently dropped during a fork rebase |
| Automated check that a file belonging to upstream has been reintroduced into this repository | Any upstream file reappears; the inversion makes this a deliberate act rather than an accident, so the structural fix is the control until then |
| Automated check that documentation no longer describes the previous layout | Documentation grows past what a reviewer can hold, or a stale instruction reaches the default branch |
| Splitting named operations across multiple grouped definition files | A single definition file becomes hard to navigate — a real threshold, not an anticipated one |
| Guarantee that concurrent verification runs on one machine do not contend | Two runs are actually observed to collide |
| Recording the verification time budget in a form that makes regressions against it visible automatically | The budget is exceeded and nobody notices until it hurts |

Two things that look like candidates for this list and are deliberately
**not** deferred, because Principle VIII's seam exception covers them:

- **FR-012/FR-013 and SC-009**, the "step could not run" third outcome. Its
  failure mode is silent, and it is structural to retrofit once contributors
  have learned to trust a green result that was never earned.
- **FR-004**, resolving upstream behaviour through a publicly reachable
  interface rather than an internal package. It is not extra work to be
  added later; it is the difference between the repository being able to
  build at all and not.
