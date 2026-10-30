# Data Model: Upgrade kcp to v0.33.3

**Feature**: `20261030-172436-upgrade-kcp-0-33-3` | **Date**: 2026-10-30 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Research**: [research.md](./research.md)

No new entities, types, or storage are introduced. This artifact records the **version pins as the model's single row** — the contract that tasks must satisfy and that `task drift`/`go mod tidy` enforce.

## Pinned versions (post-change)

| Pin | File | Value | How verified |
|-----|------|-------|--------------|
| kcp server binary | `Taskfile.yaml` var `KCP_VERSION` | `v0.33.3` | `bin/kcp --version` after `task tools:kcp`; curl HEAD on release artefact URL |
| `github.com/kcp-dev/sdk` | `go.mod` | `v0.33.3` | `go list -m github.com/kcp-dev/sdk` |
| `github.com/kcp-dev/apimachinery/v2` | `go.mod` | `v2.33.3` (unchanged) | `go list -m` — matches `sdk 0.33.3`'s own `require` |
| `github.com/kcp-dev/logicalcluster/v3` | `go.mod` | `v3.0.5` (unchanged) | `go list -m` |
| `github.com/kcp-dev/multicluster-provider` | `go.mod` + `replace` | `v0.8.0` via `ifdotpy` fork (unchanged) | `go list -m` — not part of kcp 0.33 release |
| Provider sub-module `kcp-dev/*` | `providers/nutanix-infrastructure/go.mod` | match root (conditional — only if `go mod tidy` would split `k8s.io/*`) | `go list -m all` in both modules — `k8s.io/*` minors must agree |

## Invariants

1. **One binary pin**: `Taskfile.yaml: KCP_VERSION` is the sole source of truth for the server binary. Docs prose must not duplicate it.
2. **One SDK bump per kcp bump**: `KCP_VERSION` and `github.com/kcp-dev/sdk` are updated together in one commit; partial upgrade (binary without SDK or vice versa) is a defect.
3. **No drift**: `task drift` passes — no new `replace` for `kcp-dev/*`, no Cluster API fork change, no provider drift entry.
4. **Graph coherence**: `go mod tidy` leaves `go.mod`/`go.sum` clean (`git diff --exit-code` passes) and `go vet ./...` / `task lint` pass. Transitive `k8s.io/*` stays on one minor version.

## Lifecycle

Pins are `Created → Updated` (by this feature) → `Verified` (by `task verify`/`task drift`). No deletion or archival — the next kcp upgrade supersedes these values.
