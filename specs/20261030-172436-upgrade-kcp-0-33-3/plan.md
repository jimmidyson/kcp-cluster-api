# Implementation Plan: Upgrade kcp to v0.33.3

**Branch**: `20261030-172436-upgrade-kcp-0-33-3` | **Date**: 2026-10-30 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/20261030-172436-upgrade-kcp-0-33-3/spec.md`

## Summary

Bump the pinned kcp server binary from `v0.32.3` → `v0.33.3` and the matching Go module `github.com/kcp-dev/sdk` from `v0.33.1` → `v0.33.3`. `apimachinery/v2` stays at `v2.33.3` (already on the 0.33 line) and `logicalcluster/v3` stays at `v3.0.5` (unchanged in this line); the Nutanix provider sub-module is updated in lockstep if needed. Verification is `task verify` against a real kcp server plus `task drift`; no new behaviour, schema, or API surface is introduced.

## Technical Context

**Language/Version**: Go 1.27.3 (from `go.mod`; `providers/nutanix-infrastructure/go.mod` same). No language or toolchain change — kcp 0.33 targets Go 1.26, so 1.27.3 is newer and compatible.

**Primary Dependencies**: `github.com/kcp-dev/sdk` (pinned to `v0.33.1` → `v0.33.3`), `github.com/kcp-dev/apimachinery/v2` `v2.33.3`, `github.com/kcp-dev/logicalcluster/v3` `v3.0.5`, `github.com/kcp-dev/multicluster-provider` `v0.8.0` (via `ifdotpy` fork; not bumped here), `k8s.io/*` `v0.37.1` transitively, `go-task/task` for the command surface. All resolved via `go list -m -json` (see research.md R1).

**New Dependencies**: None. No new runtime deps; no new `replace` directives expected (`go mod tidy` to confirm — see R3).

**Storage**: N/A for this bump. kcp's test/demo servers are ephemeral (embedded etcd, torn down after each run); no persisted data migration. If a future persistent kcp install is introduced, its migration is out of scope (spec Assumptions).

**Testing**: `go test ./...` (unit) + `task test:integration:kcp` against a real kcp server started by the test fixture from `bin/kcp` (Constitution III). `task drift` unchanged. `task verify` is the done condition (Constitution IV, three-outcome contract). See research R2 for flake policy and R5 for flag-change detection.

**Target Platform**: Linux (CI `ubuntu-latest`) + macOS arm64 (local darwin/arm64). kcp release artefacts are `kcp_<version>_<GOOS>_<GOARCH>.tar.gz` fetched by `task tools:kcp`; pattern verified live at `v0.33.3` (302 to `release-assets.githubusercontent.com`). No platform-specific code.

**Project Type**: Multi-module Go project (root + `providers/nutanix-infrastructure`); this change is a dependency bump, not a service/library boundary change.

**Performance Goals**: None for this bump. Reconcile-throughput / per-workspace cost claims (Constitution IX) are not re-measured unless `task test:sweep` shows a material change — then reported as evidence, not asserted (spec NFR-001).

**Constraints**:
- Tooling installed at pinned versions via `devbox` / Go toolchain only (Constitution: Environment constraints). No new system tooling.
- `Taskfile.yaml: KCP_VERSION` is the single authoritative pin for the binary; `go.mod` is authoritative for Go modules. Docs prose must not carry a second, driftable copy (spec SC-007).
- `providers/nutanix-infrastructure` restates `replace` directives independently; it must be updated in lockstep if its transitive `k8s.io/*` would otherwise split (research R4).
- Breaking change `spec.resources[].storage.virtual.identityHash` removal (kcp PR #4326) does not affect this repo's `status.identityHash` usage — verified by field-path distinction (research R3).

**Scale/Scope**: 2 pins bumped (Taskfile + go.mod), 1 `go.sum` refresh, 1 doc footnote update (`docs/conversion-plan.md` "Measured on kcp v0.32.3" line, optional), plus optional provider-module lockstep and `go.mod` propagation fix. One PR, ~<20 lines changed outside `go.sum`.

## Constitution Check

*GATE: evaluated before Phase 0, re-evaluated after Phase 1.*

| Principle | Assessment |
|-----------|------------|
| I — Divergence counted & temporary | No upstream Cluster API code is changed; `DRIFT.md` untouched. kcp is not forked in this repo, so no drift entry. No new divergence introduced. A new kcp-family `replace` would be drift only if it pointed at a fork — not expected here. |
| II — Public extension points | Not applicable — pure dependency bump, no integration wiring. |
| III — Test-first against real kcp | No new reconciler logic; existing integration suites are the test. They run against a real kcp server (fixture starts `bin/kcp`). No envtest-only path. |
| IV — Done is a command | `task verify` (which runs `task tools` → build/vet/unit → `task test:integration:kcp`) + `task drift`. Three-outcome contract preserved (`could not run` ≠ pass). Spec SC-001..007 each name a runnable command and exit status. |
| V — Verify dependencies against source | `kcp v0.33.3` binary URL liveness, `sdk v0.33.3`/`apimachinery v2.33.3` compatibility, and the `status.identityHash` vs `virtual.identityHash` distinction all verified against source/registry (R1–R4). |
| VI — Documentation ships with change | Single docs update: `docs/conversion-plan.md` measured-on line (or follow-up note for sharded mount-proxy flags). No user-facing install doc to update; `Taskfile.yaml: KCP_VERSION` remains the single source of truth per spec SC-007. |
| VII — History & review discipline | One PR, Conventional Commits title (`build: bump kcp to v0.33.3`), squash merge, rebased onto `main` not merged. No session URLs / Co-Authored-By agent trailers. |
| VIII — YAGNI, except at seams | No new abstractions, flags, or sharded-mount plumbing. Future mount-proxy TLS work is explicitly deferred with a trigger (spec Edge Cases / Out of Scope) rather than built ahead. |
| IX — Scalability claims | No per-workspace cost figure is published or changed by this bump. If `task test:sweep` is run and shows a material delta, it is reported as evidence (spec NFR-001), not claimed as unchanged. |

**Post-Design Re-check** (after research.md, data-model.md, quickstart.md): no gates changed. Research confirmed `sdk v0.33.3 → apimachinery v2.33.3` is the intended pairing, so no new `replace` is needed; Principle I stays green. Provider lockstep remains conditional on `go mod tidy` — no anticipatory edit. All nine principles still pass.

**Result**: no unjustified violations. Proceed.

## Project Structure

### Documentation (this feature)

```text
specs/20261030-172436-upgrade-kcp-0-33-3/
├── plan.md              # This file
├── research.md          # Phase 0 — decisions & alternatives
├── data-model.md        # Phase 1 — pinned versions as the "model"
├── quickstart.md        # Phase 1 — runnable validation guide (verify/drift/demo/sweep)
└── checklists/
    └── requirements.md  # Spec quality checklist (already present)
```

No `contracts/` — no API/CLI/schema contract is added or changed by a version bump; verification is via `task verify`/`task drift`.

### Source Code (repository root)

Only these paths are touched. No new files, no directory changes.

```text
Taskfile.yaml                          # MODIFIED — KCP_VERSION v0.32.3 → v0.33.3
go.mod                                 # MODIFIED — github.com/kcp-dev/sdk v0.33.1 → v0.33.3
go.sum                                 # MODIFIED — refreshed by go mod tidy
docs/conversion-plan.md               # MODIFIED — 1 line: "Measured on kcp v0.32.3" → v0.33.3 + optional deferred note on mount-proxy flags (spec Edge Cases)
providers/nutanix-infrastructure/
├── go.mod                             # MODIFIED only if lockstep needed (k8s.io/* split) — otherwise untouched
└── go.sum                             # MODIFIED only with the above
```

**Structure Decision**: keep the existing single-root + nested-provider-module layout. No new modules, binaries, or tooling. `Taskfile.yaml: KCP_VERSION` stays the single source of truth for the binary; `go.mod` for Go modules. Provider module inherits the bump only if `go list -m all` would otherwise show a `k8s.io/*` minor-version split (R4).

## Complexity Tracking

> No Constitution Check violations to justify. Leave empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
