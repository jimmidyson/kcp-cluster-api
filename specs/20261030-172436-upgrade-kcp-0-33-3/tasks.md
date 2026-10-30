# Tasks: Upgrade kcp to v0.33.3

**Input**: Design documents from `specs/20261030-172436-upgrade-kcp-0-33-3/` — [spec.md](./spec.md), [plan.md](./plan.md), [research.md](./research.md), [data-model.md](./data-model.md), [quickstart.md](./quickstart.md)

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, quickstart.md (all present; no contracts/)

**Tests**: Not separately requested — verification is via the existing suites (`task verify` = `go test ./...` + `task test:integration:kcp` + `task drift`) against a real kcp server. No new test files. See spec NFR-001 for optional sweep evidence.

**Organization**: Organized by user story (P1 → P2 → P3) per spec priorities; setup/foundational cover the two pins that every story depends on.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Record the pins that every user story depends on — no code behavior yet.

- [x] T001 Confirm kcp v0.33.3 artefact + sdk pins are resolvable before editing (research R1/R4) in worktree — run `go list -m -json github.com/kcp-dev/sdk@v0.33.3` and `curl -sI` on the `KCP_VERSION`-templated download URL; record the resolved hashes in the PR description

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Apply the two pins that every story's acceptance scenario requires. No story can be verified until both are green.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [x] T002 Bump `Taskfile.yaml` `KCP_VERSION` from `v0.32.3` to `v0.33.3` in `Taskfile.yaml`
- [x] T003 Bump `go.mod` `github.com/kcp-dev/sdk` from `v0.33.1` to `v0.33.3` in `go.mod` (edit the require line, then `go mod tidy`; commit `go.mod` + `go.sum` together — do not edit `go.sum` by hand)
- [x] T004 Run coherence checks in worktree — `go mod tidy` + `git diff --exit-code -- go.mod go.sum` + `go vet ./...` + `go list -m all` k8s.io/* minor-version check (spec SC-004, FR-005); fix any split before leaving this phase
- [x] T005 Handle provider lockstep in worktree if needed — if T004 shows a `k8s.io/*` minor-version split, update `providers/nutanix-infrastructure/go.mod` + `go.sum` in lockstep (spec FR-009, research R4); if no split, leave it untouched and do not create a commit for it

**Checkpoint**: `go vet ./...` passes, `go.mod`/`go.sum` are clean, `k8s.io/*` is on one minor, and both worktree modules resolve `github.com/kcp-dev/sdk` at `v0.33.3`. User story phases can now start.

---

## Phase 3: User Story 1 — CI runs on the current kcp server (Priority: P1) 🎯 MVP

**Goal**: `task verify` passes on the new server binary — contributor PRs stay green on the kcp version real installations run.

**Independent Test**: Clean checkout → `task tools:kcp` → `task verify` passes and `task test:integration:kcp` exits 0 within the existing budget; `bin/kcp version` reports `v0.33.3` (spec 1.1–1.2, SC-001/SC-002, quickstart § Validate tests).

- [x] T006 [US1] Fetch the new binary and confirm version in worktree — `task tools:kcp` then `bin/kcp version` must report `v0.33.3` (spec SC-001, quickstart § Validate the pins); remove `bin/kcp` afterwards so CI exercises the download, not a cached binary
- [x] T007 [US1] Run full verification in worktree — `task verify` must pass (or report `could not run` for a missing capability per the three-outcome contract, spec SC-003); capture `bin/verify-result.json` for the PR (spec SC-002)
- [x] T008 [US1] Run drift in worktree — `task drift` must pass for both the root module and `providers/nutanix-infrastructure` (spec SC-005, FR-009); any provider drift is fixed by T005, not by editing `DRIFT.md` here

**Checkpoint**: US1 is done when `task verify` + `task drift` are green (or correctly `could not run`) on `v0.33.3`. This is the MVP — the PR can merge on this alone if P2 requires manual/hardware steps.

---

## Phase 4: User Story 2 — Local demo still reaches ready clusters (Priority: P2)

**Goal**: `task demo` reaches the same "Available in every workspace, tenants isolated" done condition on the new server.

**Independent Test**: `task demo` (manual, not part of `task verify`) reaches `Available` and tenant-isolation checks read "own workspace and nothing else"; equivalently, `test/integration/demo` passing in Phase 3 covers this in CI (spec 2.1–2.3, SC-006, quickstart § Optional demo). Record the result in the PR if run.

- [x] T009 [US2] Update `docs/conversion-plan.md` measured-on line from `kcp v0.32.3` to `kcp v0.33.3` in `docs/conversion-plan.md` (spec FR-008, plan § Source Code; SC-007) — keep `Taskfile.yaml: KCP_VERSION` the single source of truth, no other prose copy
- [x] T010 [US2] Verify demo starts on v0.33.3 — either `task demo` locally reaches `Available` (quickstart § Optional demo) or confirm `test/integration/demo` passed inside T007; note which was used in the PR description (spec 2.1–2.3, SC-006); if server flags changed (spec FR-007, research R5), adapt the fixture/`cmd/demo` flags in this task
- [x] T011 [US2] Record deferred mount-proxy note if applicable — if sharded-mount work (spec Edge Cases / R5 GHSA-c8w2-fgvx-vhv4) is not done here, add a one-line deferred item to `docs/conversion-plan.md` next-steps referencing the flag names; if already covered, leave docs unchanged beyond T009 (spec Out of Scope, plan R5)

**Checkpoint**: US1 + US2 done — docs name `v0.33.3` unambiguously (SC-007) and the demo invariant holds on the new server.

---

## Phase 5: User Story 3 — Dependency graph stays coherent (Priority: P3)

**Goal**: No second, driftable copy of the pin is introduced; the graph is coherent and the provider module is consistent.

**Independent Test**: `go mod tidy && git diff --exit-code -- go.mod go.sum` is clean in both modules; `go list -m all` shows one `k8s.io/*` minor; `task lint`/`go vet` pass (spec 3.1–3.3, quickstart § Validate the graph).

- [x] T012 [US3] Final graph audit across both modules in worktree — `go mod tidy` + `git diff --exit-code -- go.mod go.sum` + `go list -m all | grep k8s.io` minor-version check in both `go.mod` roots, and `task lint`/`go vet ./...` (spec SC-004, FR-005); file any `k8s.io/*` split as a follow-up, do not add a new `replace` here unless required to make `tidy` resolve (spec NFR-002, research R4)

**Checkpoint**: All three stories green — the bump is shippable with no follow-up required beyond deferred mount-proxy work (P2 deferred note, if any).

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Review artifacts and verify the three-outcome contract one last time. No new behavior.

- [x] T013 Review re-measurement trigger in quickstart — if `task test:sweep` was run, confirm any material per-workspace delta was recorded as evidence per spec NFR-001 (Constitution IX); if sweep was not run, explicitly note "not measured — sweep not run" rather than claiming "unchanged" (plan Performance Goals)
- [x] T014 Final `quickstart.md` validation in worktree — re-run the quickstart steps end-to-end (pins → graph → verify + drift → doc check) from a clean `task tools:kcp` fetch and confirm `bin/verify-result.json` satisfies SC-003's three-outcome rule

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately (read-only probe).
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories. Both pins must be coherent before any `task verify`/`task demo` result is meaningful.
- **User Stories (Phase 3+)**: All depend on Foundational completion.
  - US1 (P1, CI) is the MVP and can merge alone.
  - US2 (P2, demo/docs) depends on US1's green verify only insofar as it reuses the same pins; doc edit (T009) can land in either order but is grouped with US2 for review clarity.
  - US3 (P3, graph audit) is technically already covered by Phase 2's coherence checks; it is the final sign-off that no second pin or `k8s.io/*` split was introduced.
- **Polish (Phase 6)**: Depends on all desired user stories being complete.

### User Story Dependencies

- **User Story 1 (P1)**: Can start after Foundational — no dependencies on other stories; independently testable via `task verify`.
- **User Story 2 (P2)**: Can start after Foundational — not blocked on US1, but its demo assertion is equivalently covered by US1's `test/integration/demo` suite; independently testable via `task demo`.
- **User Story 3 (P3)**: Can start after Foundational — audit step; no integration with US1/US2 beyond sharing the same pins.

### Within Each User Story

- Setup/foundational (pins + tidy) before any integration test.
- Binary fetch before integration suite.
- `task verify` before `task drift` (drift is the last gate).

### Parallel Opportunities

- T002 (Taskfile) and T003 (go.mod) could be one commit or two — not parallel in the file-contention sense (different files, but T005/T004 depend on both, so batch them).
- Once Foundational is done, US2's doc edit (T009) can proceed independently of US1's integration suite (T006–T008) if work is split — they touch disjoint files.
- Within Polish, T013 (doc review) and T014 (quickstart re-run) can proceed in either order.

---

## Parallel Example: User Story 1 (P1)

```bash
# Once Foundational is green, a second contributor can start docs while CI runs:
Task: "Update docs/conversion-plan.md measured-on line in docs/conversion-plan.md" (T009, US2)
# Meanwhile, primary contributor runs:
Task: "Fetch the new binary and confirm version" (T006, US1)
Task: "Run full verification" (T007, US1)
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Setup (T001) + Foundational (T002–T005) → pins coherent.
2. Complete US1 (T006–T008) → `task verify` + `task drift` green on `v0.33.3`.
3. **STOP and VALIDATE**: If green, the MVP is shippable — open the PR for review.

### Incremental Delivery

1. Foundational → `go.mod`/`go.sum` + `Taskfile.yaml` coherent.
2. Add US1 → `task verify` passes (MVP).
3. Add US2 → docs updated + demo invariant holds.
4. Add US3 → final graph audit, no drift.
5. Polish → sweep note (if any) + quickstart re-check.

### Parallel Team Strategy

With multiple developers after Foundational is done:

- Developer A: US1 (`task verify` loop, T006–T008).
- Developer B: US2 (doc edit T009 + demo check T010–T011).
- T012–T014 (graph audit + quickstart re-check) by whoever lands last.

---

## Notes

- [P] is omitted above — every remaining task touches the same few versioned files or is an ordered verification, so file-level parallelism is not meaningful for this bump. The only true parallel opportunity is US1 integration vs US2 doc edit (noted above).
- Each task includes its exact file path and the plan/research anchor so an implementer can work without re-reading the whole file set.
- `task verify` is the done condition per Constitution IV; `bin/verify-result.json` is the evidence to attach to the PR, not the exit code alone.
- Do not keep these generic template examples — all tasks above are specific to this bump.
