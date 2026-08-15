---

description: "Task list for the standalone repository, task runner, and CI feature"
---

# Tasks: Standalone repository, task runner, and CI

**Input**: Design documents from `/specs/20260815-074758-repo-inversion/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/task-surface.md](./contracts/task-surface.md), [quickstart.md](./quickstart.md)

**Tests**: This feature changes where code lives and how it is built, not what
it does, so it generates no TDD tasks for existing behaviour — the existing
suite is the definition of correct behaviour and must pass unchanged (spec:
Behavioural equivalence). It does introduce two pieces of genuinely new
behaviour — the three-outcome verification contract and the drift check —
and those are developed test-first per Constitution Principle III.

**Organization**: Tasks are grouped by user story. Phase 2 is a hard,
serial blocker: it lives in a different repository and nothing compiles until
it lands.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)
- Paths are repository-root-relative **after** the inversion unless a task
  says otherwise

---

## Phase 1: Setup

**Purpose**: Decisions and scaffolding that must exist before work starts

- [ ] T001 Confirm the branch name and tag to use in the fork repository `github.com/jimmidyson/cluster-api` (plan proposes branch `kcp/v1.14` and tag `v1.14.1-kcp.1`) — a maintainer decision, since the repository is public and the tag is a published artifact
- [ ] T002 [P] Record the confirmed module path `github.com/jimmidyson/kcp-cluster-api` in `specs/20260815-074758-repo-inversion/plan.md` if it differs from the spec's Repository identity assumption

**Checkpoint**: The fork's branch/tag names are agreed; nothing has changed yet

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The public seam in the fork. **Nothing in this repository
compiles until this phase completes and is tagged.**

**⚠️ CRITICAL**: This phase is in a *different repository*
(`/workspace/cluster-api`), is strictly serial, and cannot be parallelised
with any other phase. See [research.md](./research.md) R2 and R3.

- [ ] T003 In `/workspace/cluster-api`, add upstream as a remote and fetch commit `281e4e3`; do not branch from the fork's own `master`, which is at a 2021 commit (research R2)
- [ ] T004 Create branch `kcp/v1.14` in `/workspace/cluster-api` from `281e4e3`
- [ ] T005 Add `controllers/external/metadata.go` in `/workspace/cluster-api` exposing the two symbols below, both delegating to the existing `internal/contract` implementation with behaviour unchanged (research R3)

  **Interfaces** — T013 and T014 consume these; the names and signatures are fixed here and must not be chosen independently:

  ```go
  package external

  // SetGKMetadataGetter overrides how contract-version metadata is resolved
  // for a GroupKind. Passing nil restores the default behaviour.
  func SetGKMetadataGetter(f func(ctx context.Context, c client.Reader, gk schema.GroupKind) (*metav1.PartialObjectMetadata, error))

  // GetAPIVersion resolves the current apiVersion for a GroupKind using the
  // configured metadata getter.
  func GetAPIVersion(ctx context.Context, c client.Reader, gk schema.GroupKind) (string, error)
  ```
- [ ] T006 Confirm upstream's own tests still pass in `/workspace/cluster-api` for `internal/contract` and `controllers/external`, unmodified — the seam must not change existing behaviour
- [ ] T007 Write the commit message for T005 referencing the upstream proposal this change will become, per Constitution Principle I
- [ ] T008 Tag `/workspace/cluster-api` as `v1.14.1-kcp.1` and push branch and tag

**Checkpoint**: An immutable fork tag exists carrying exactly one patch. User
story work can begin.

---

## Phase 3: User Story 1 — Upstream code is unreachable, not merely forbidden (Priority: P1) 🎯 MVP

**Goal**: The repository contains only this project's code and builds against
Cluster API as a version-pinned dependency.

**Independent Test**: Fresh clone builds with no Cluster API tree present;
`git ls-files` shows no upstream file; `go list -deps` shows no upstream
internal package. Quickstart Scenario 1.

- [ ] T009 [US1] Move every file under `kcp/` to the repository root, preserving history, so `kcp/cmd/`, `kcp/internal/`, `kcp/test/`, `kcp/docs/` become `cmd/`, `internal/`, `test/`, `docs/`
- [ ] T010 [US1] Delete the upstream tree: `api/`, `bootstrap/`, `cmd/clusterctl/`, `controllers/`, `controlplane/`, `core/`, `exp/`, `feature/`, `internal/contract/`, `internal/controllers/` and every other upstream directory, plus root `Makefile`, `Dockerfile`, `Tiltfile`, `metadata.yaml`, `netlify.toml`, `OWNERS`, `OWNERS_ALIASES`, `SECURITY_CONTACTS`, `CHANGELOG/`, `hack/`, `util/`, `version/`, `test/e2e/` and the upstream `go.mod`/`go.sum` (FR-001)
- [ ] T011 [US1] Rewrite `go.mod`: module path `github.com/jimmidyson/kcp-cluster-api`, replacing the three relative `replace` directives with pins to `github.com/jimmidyson/cluster-api v1.14.1-kcp.1` for `sigs.k8s.io/cluster-api`, `/api` and `/test` (FR-002, FR-003)
- [ ] T012 [US1] Rewrite every `sigs.k8s.io/cluster-api/kcp/...` import to the new module path across `cmd/`, `internal/` and `test/`
- [ ] T013 [US1] Replace the `sigs.k8s.io/cluster-api/internal/contract` import in `internal/coremanager/contractmetadata.go` with `external.SetGKMetadataGetter` from T005 (FR-004)
- [ ] T014 [US1] Replace the same internal import in `internal/coremanager/setup.go` with `external.GetAPIVersion` from T005, where the conversion webhook's API-version getter is wired (FR-004)
- [ ] T015 [US1] Replace the relative CRD manifest paths in `test/integration/coremanager/coremanager_test.go` with resolution from the pinned dependency's module directory (FR-005, research R1)
- [ ] T016 [US1] Replace the relative webhook manifest paths in the same file with resolution from the pinned dependency's module directory (FR-005)
- [ ] T017 [US1] Implement the manifest resolution helper in `internal/kcpfixtures/`, failing with an identifiable error naming the expected location when the definitions are absent, with no fallback search (FR-006)
- [ ] T018 [P] [US1] Unit-test the resolution helper's failure path in `internal/kcpfixtures/`: an unexpected layout must produce the named error, not an empty result
- [ ] T019 [US1] Verify `go build ./...` succeeds and `go list -deps ./... | grep sigs.k8s.io/cluster-api/internal` is empty (FR-004, Quickstart Scenario 1)
- [ ] T020 [US1] Verify no tracked file belongs to upstream by running the `git ls-files` check from Quickstart Scenario 1 and confirming it returns nothing — US1's own independent test, not deferred to Phase 7 (SC-001, FR-001)
- [ ] T021 [US1] Verify the dependency resolves to an immutable fork tag, not a branch or filesystem path, using the `go list -m` check from Quickstart Scenario 1 (FR-002, FR-003, SC-003 in part — full verification of SC-003 requires an actual upstream version bump and happens the first time one is done, not within this feature)
- [ ] T022 [US1] Verify the existing unit tests pass unchanged, with no assertion weakened to accommodate the move (spec: Behavioural equivalence)

**Checkpoint**: The repository is standalone and builds. This is the MVP.

---

## Phase 4: User Story 2 — One command decides whether work is done (Priority: P1)

**Goal**: A single command installs its own tooling and returns a definitive
answer, with a third outcome for steps the environment cannot run.

**Independent Test**: On a clean machine, `task check` and `task verify` run
with no prior setup; on a machine without a container runtime, `task verify`
returns neither success nor an ordinary failure. Quickstart Scenarios 2 and 3.

### Tests for User Story 2 (new behaviour — write first)

- [ ] T023 [P] [US2] Write a failing test for the three-outcome contract: a step whose capability is unavailable yields an exit status that is non-zero and distinct from a test failure, names the missing capability, and appears in the summary as not-run (FR-012, SC-009)
- [ ] T024 [P] [US2] Write a failing test that capability checks run *before* their dependent steps, so an unmet capability is reported before work begins (FR-013)

### Implementation for User Story 2

- [ ] T025 [US2] Create `Taskfile.yaml` at the repository root implementing the targets in [contracts/task-surface.md](./contracts/task-surface.md), as a single file — the split into grouped files is deferred (spec: Deferred)
- [ ] T026 [US2] Implement the `tools` target installing pinned tooling via `go install` into a repository-local `bin/`, with no package or environment manager (FR-009)
- [ ] T027 [US2] Implement `build`, `test:unit`, `lint` and `check`, where `check` is the composition of the targets needing no container runtime (FR-010, FR-011, NFR-002)
- [ ] T028 [US2] Implement `test:integration`, including download of the pinned kcp server binary into `bin/` and creation of the container network the dev provider attaches to
- [ ] T029 [US2] Implement the capability checks and three-outcome reporting that T023 and T024 test, covering container runtime, image-source reachability and kcp binary availability (FR-011, FR-012, FR-013)
- [ ] T030 [US2] Implement `verify` as the composition of the other targets, never a reimplementation of them (FR-007, contract: task surface)
- [ ] T031 [US2] Add `bin/` to `.gitignore`
- [ ] T032 [US2] Delete `kcp/Makefile` — no second build entry point may survive this change (FR-021)
- [ ] T033 [US2] Verify tooling reuse: two consecutive `task check` runs, the second performing no downloads or rebuilds (NFR-003, Quickstart 2d)
- [ ] T034 [US2] Verify the clean-environment start: with `bin/` removed and no project tooling present, `task check` succeeds with no manual preparation (FR-007, FR-008, SC-002, Quickstart 2c)
- [ ] T035 [US2] Verify the fast subset completes within its 60 s warm budget and is a small fraction of the full run, recording both figures (NFR-002, SC-008)

**Checkpoint**: One command decides done, and says so honestly when it cannot.

---

## Phase 5: User Story 3 — CI runs exactly what contributors run (Priority: P2)

**Goal**: Automated checks invoke the same named operations, and nothing
inherited from upstream remains.

**Independent Test**: Every `run:` step in `.github/workflows/` invokes a
target from the contract; no inherited workflow file remains. Quickstart
Scenario 4.

- [ ] T036 [US3] Delete the nine inherited workflows in `.github/workflows/`: `pr-verify.yaml`, `pr-golangci-lint.yaml`, `pr-dependabot.yaml`, `pr-md-link-check.yaml`, `weekly-md-link-check.yaml`, `weekly-security-scan.yaml`, `weekly-test-release.yaml`, `pr-gh-workflow-approve.yaml`, `release.yaml` (FR-015, research R6)
- [ ] T037 [US3] Create `.github/workflows/pr.yaml` invoking `task check` and `task verify`, with no logic beyond checkout, toolchain setup and reporting (FR-014, SC-004)
- [ ] T038 [US3] Re-create the PR-title check as our own within `pr.yaml`, preserving the emoji-prefix and no-issue-number rules that Constitution Principle VII depends on and that died with `pr-verify.yaml` (research R6)
- [ ] T039 [P] [US3] Rewrite `.github/workflows/pr-kcp-docs.yaml` as `.github/workflows/docs.yaml`, invoking a task target rather than inline commands (FR-014)
- [ ] T040 [P] [US3] Update `.github/dependabot.yaml` so it covers only this project's own manifests at their new root location (FR-018)
- [ ] T041 [US3] Delete the old `kcp-tests.yaml`, whose behaviour is now covered by `pr.yaml` (FR-015)

**Checkpoint**: CI and local runs are the same operations.

---

## Phase 6: User Story 4 — Divergence from upstream is measured (Priority: P3)

**Goal**: The carried patch set is recorded and checked.

**Independent Test**: `task drift` passes against the recorded set and fails
when an unrecorded change is added to the fork. Quickstart Scenario 5.

### Tests for User Story 4 (new behaviour — write first)

- [ ] T042 [P] [US4] Write a failing test for the drift check: an unrecorded differing path causes failure naming that path; the recorded set alone passes

### Implementation for User Story 4

- [ ] T043 [US4] Create `DRIFT.md` recording the fork base commit `281e4e3`, the single carried patch from T005, and in prose the upstream proposal it corresponds to (FR-016)
- [ ] T044 [US4] Implement the `drift` target comparing the fork's actual differences against `DRIFT.md` and failing on any unrecorded path (FR-017)
- [ ] T045 [US4] Add `task drift` to `pr.yaml` so divergence is reported on every change (SC-005)

**Checkpoint**: Drift is measured rather than trusted.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [ ] T046 Rewrite `AGENTS.md`: replace the read-only-tree prohibition with the rules that now apply — how the fork is maintained, how patches are added and retired, how `DRIFT.md` is kept, and the upgrade procedure for adopting a newer Cluster API release, which is the documented form of SC-003 (FR-020)
- [ ] T047 [P] Rewrite `README.md` for the new layout and the `task` entry points, including the recorded verification budget (FR-019, NFR-001)
- [ ] T048 [P] Update `docs/site/content/en/docs/design/` pages describing the fork architecture, repository layout and rebasing, all of which describe the arrangement this feature removes (FR-019)
- [ ] T049 [P] Update `docs/conversion-plan.md` and the two ADRs where they reference the `kcp/` subdirectory layout or the read-only invariant (FR-019)
- [ ] T050 Update `CLAUDE.md` if it still points at guidance that has moved (FR-019)
- [ ] T051 Run the full quickstart: all six scenarios on a machine with a container runtime, and Scenario 3 on one without (SC-006, SC-009, Quickstart: What "done" means)
- [ ] T052 Record the measured full-run duration from the first green CI run into the project documentation as the verification budget — this cannot be done in a development container and must come from a real run (NFR-001, SC-008, research R4)
- [ ] T053 Confirm no contributor or governance document instructs a reader to do something the repository no longer supports — human review, since the automated check is deferred (SC-007)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies; T001 is a maintainer decision and gates Phase 2
- **Phase 2 (Foundational)**: depends on T001. **Blocks every other phase.** Serial, and in a different repository
- **Phase 3 (US1)**: depends on Phase 2's tag existing
- **Phase 4 (US2)**: depends on Phase 3 — the task surface is built against the inverted layout
- **Phase 5 (US3)**: depends on Phase 4 — CI invokes targets that must exist
- **Phase 6 (US4)**: depends on Phase 2 (a fork to measure) and Phase 4 (a target to add). Independent of Phases 3 and 5 otherwise
- **Phase 7 (Polish)**: depends on Phases 3–6; T048 additionally depends on a green CI run

### Critical path

```
T001 → T003…T008 (fork, serial) → T009…T022 (inversion) → T023…T035 (task surface) → T036…T041 (CI) → T052 (measured budget)
```

### Parallel Opportunities

- T039, T040 in Phase 5 touch different files and can run together
- T047, T048, T049 in Phase 7 are independent documentation files
- T018, T023, T024 and T042 are test tasks in different files
- Phase 6 can run alongside Phase 5 once Phase 4 lands
- **Nothing in Phase 2 parallelises.** It is one commit in one repository, and everything waits for it

## Implementation Strategy

### MVP

Phases 1–3. That delivers User Story 1 on its own: a standalone repository
that builds against a pinned fork, with the upstream tree gone. It is
demonstrable and valuable before any task runner or CI work exists, and the
existing Makefile still works until Phase 4 replaces it.

### Incremental delivery

1. Phases 1–2 → the fork exists and is tagged
2. Phase 3 → repository is standalone (**MVP**)
3. Phase 4 → one command decides done
4. Phase 5 → CI runs the same command
5. Phase 6 → drift is measured
6. Phase 7 → documentation matches reality, budget is recorded from measurement

### Notes

- T030 (deleting the Makefile) belongs to Phase 4, not Phase 3, deliberately:
  between the inversion and the task surface, the repository needs a working
  build entry point. FR-021 requires only that the two never coexist once the
  replacement exists.
- T048 is the one task that cannot be completed in a development container.
  Per Constitution Principle IV that is stated, not worked around: the budget
  is measured on a real runner or it is not recorded.
