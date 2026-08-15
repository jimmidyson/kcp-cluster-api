# Implementation Plan: Standalone repository, task runner, and CI

**Branch**: `claude/review-approach-direction-4troqb` | **Date**: 2026-08-15 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/20260815-074758-repo-inversion/spec.md`

## Summary

Remove the upstream Cluster API tree from this repository, promote the
existing `kcp/` module to the root, and consume Cluster API as a
version-pinned dependency resolved to a patched fork. Replace the inherited
Make entry points with a go-task surface whose single verification target is
the project's machine-checkable done-condition, and replace eleven workflows
— nine of them inherited, seven of those carrying local edits — with two of
our own that invoke those same targets.

Research changed one significant element of the approach: CRD manifests ship
inside the published Go module, so fixtures can resolve them from the pinned
dependency instead of a generate-and-check-in pipeline. That removes a
generation step, a checked-in artifact and a staleness check the spec assumed
were necessary. See [research.md](./research.md) R1, and Complexity Tracking
below for the spec deviation this implies.

## Technical Context

**Language/Version**: Go 1.26.3 (from the existing `kcp/go.mod`)

**Primary Dependencies**: `sigs.k8s.io/cluster-api` (+ its `api` and `test`
submodules) resolved via `replace` to `github.com/jimmidyson/cluster-api`;
`github.com/kcp-dev/multicluster-provider` v0.8.0;
`sigs.k8s.io/multicluster-runtime` v0.24.1; `github.com/kcp-dev/sdk` v0.32.3

**Storage**: N/A

**Testing**: Go test. Unit tests colocated; integration tests against a real
kcp server binary (v0.32.3) with a container runtime, per Constitution III

**Target Platform**: Linux; developer machines and ephemeral CI/agent
containers

**Project Type**: Single Go module producing controller binaries

**Performance Goals**: Fast subset ≤ 60 s warm (measured basis: 1 s unit, 4 s
build). Full verification budget to be recorded from first CI measurement;
15 min inherited ceiling until then — see research R4

**Constraints**: No system-level package or environment manager on the
critical path. Tooling installed at pinned versions via `go install` into a
repo-local location. Ephemeral containers: setup cost is paid per delegated
task

**Scale/Scope**: 13 Go files, ~3.4k lines moving; 11 workflows replaced by 2;
1 carried upstream patch

## Constitution Check

*GATE: evaluated before Phase 0 and re-evaluated after Phase 1 design.*

| Principle | Assessment |
|---|---|
| I — Divergence counted and temporary | **Advances it.** Net drift falls from 8 files (1 code patch + 7 edited workflows) to 1, and the remaining one gains a record and a check. Pass. |
| II — Public extension points only | **Pass, and strengthens.** R3 replaces an internal-package import with a narrow public seam in the fork, shaped to be proposable upstream. |
| III — Test-first against real kcp | **Pass with a caveat.** This feature adds no product behaviour; its test is the existing suite continuing to pass with assertions no weaker than before (spec: Behavioural equivalence). The integration suite still runs against a real kcp server. The caveat is that this environment cannot run it — see Complexity Tracking. |
| IV — Done is a command | **Central to the feature.** The deliverable *is* the command. FR-012/013 and SC-009 keep the "could not run" outcome distinct. Pass. |
| V — Verify dependencies against source | **Pass.** Every research finding is backed by a command; the one unmeasurable item (full-run duration) is declared unmeasurable rather than estimated. |
| VI — Documentation ships with the change | **Pass.** FR-019/020 put documentation and governance rewrites inside this feature, not after it. |
| VII — History and review discipline | **Pass, with one carry-over.** Deleting `pr-verify.yaml` removes the PR-title check this principle relies on; re-creating it is a task, not an afterthought (research R6). |
| VIII — YAGNI, except at seams | **Pass after revision.** The spec was already revised against this principle; research R1 removes three further pieces of machinery. Seam exception applied deliberately twice (FR-004, FR-012/013). |

**Result**: no unjustified violations. One justified deviation and one
environmental limitation are recorded in Complexity Tracking.

## Project Structure

### Documentation (this feature)

```text
specs/20260815-074758-repo-inversion/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── task-surface.md  # Phase 1 output — the named-operation contract
├── checklists/
│   └── requirements.md  # Spec quality checklist
└── tasks.md             # Phase 2 output (/speckit-tasks — not created here)
```

### Source Code (repository root)

Target layout after inversion. Everything currently under `kcp/` moves up one
level; everything else in the repository is deleted.

```text
Taskfile.yaml            # single file to begin with; split deferred
go.mod                   # module github.com/jimmidyson/kcp-cluster-api
go.sum
DRIFT.md                 # drift record: fork base, carried patches, proposals
AGENTS.md                # rewritten: fork discipline replaces read-only rule
README.md

cmd/
└── core-manager/        # was kcp/cmd/core-manager

internal/
├── contractmetadata/    # was kcp/internal/contractmetadata
├── coremanager/         # was kcp/internal/coremanager
└── kcpfixtures/         # was kcp/internal/kcpfixtures

test/
└── integration/         # was kcp/test/integration
    ├── envtest/
    └── coremanager/

docs/                    # was kcp/docs — ADRs, conversion plan, Hugo site
specs/                   # spec-driven feature specifications
.specify/                # Spec Kit state and extensions
bin/                     # gitignored: pinned tooling installed here

.github/workflows/
├── pr.yaml              # build, lint, test, drift check, PR title
└── docs.yaml            # documentation site build
```

**Structure Decision**: promote `kcp/*` to the repository root rather than
introducing a new top-level directory. The module becomes
`github.com/jimmidyson/kcp-cluster-api`, so `sigs.k8s.io/cluster-api/kcp/...`
import paths within the project are rewritten once, mechanically. The three
relative `replace` directives (`../`, `../api`, `../test`) become version
pins on the fork. Repository history is preserved: the upstream tree is
removed by a commit rather than by starting a new repository.

## Implementation Sequencing

Derived from the two hard ordering constraints. Only the first is strictly
serial; the rest is mostly parallel once it lands.

1. **Fork preparation (blocking, serial, separate repository).** Cut a branch
   in `jimmidyson/cluster-api` from upstream `281e4e3`, add the public
   resolver seam (R3), tag `v1.15.0-kcp.1`. Nothing else can begin: until
   this tag exists, this repository cannot compile after the internal import
   is removed.
2. **Inversion.** Delete the upstream tree, move `kcp/*` to root, rewrite the
   module path and imports, replace relative replaces with fork pins, and
   repoint fixtures at the resolved module directory (R1).
3. **Task surface.** `Taskfile.yaml` implementing the contract in
   `contracts/task-surface.md`, with pinned tooling installation.
4. **CI.** Two workflows invoking task targets; re-create the PR-title check;
   add the drift check.
5. **Documentation and governance.** Rewrite `AGENTS.md`, `README.md`, and
   the design docs that describe the old layout; create `DRIFT.md`.
6. **Budget measurement.** Record the measured full-run duration from the
   first green CI run into the documented budget (R4).

Steps 3–5 can proceed in parallel once step 2 lands. Step 6 depends on step 4
and is the only task that cannot be completed in a development container.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| Deviation from spec FR-005/FR-006: fixtures resolve CRD manifests from the pinned module rather than from generated, checked-in copies | Research R1 verified the manifests ship inside the module. Reading them from the resolved dependency makes them in step by construction | The spec's generate-and-check-in approach creates the drift it then needs a staleness check to detect. Keeping it would add a generation step, a checked-in artifact and a check, to solve a problem that does not exist once R1 is known. **This requires a spec revision to FR-005/FR-006 and SC-006 before implementation begins** |
| Integration suite cannot run in the development environment | No container runtime, and image pulls are blocked by egress policy | Nothing to substitute. This is stated rather than worked around: the full acceptance condition is met on a CI runner, and per Constitution IV a step that cannot run here is reported as its own outcome, never as a pass |
