# Quickstart: Upgrade kcp to v0.33.3

**Feature**: `20261030-172436-upgrade-kcp-0-33-3` | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md) | **Research**: [research.md](./research.md) | **Data Model**: [data-model.md](./data-model.md)

All commands run from the worktree root (the directory `git worktree add` created for branch `20261030-172436-upgrade-kcp-0-33-3`). The main checkout must stay on `main`.

## Prerequisites

- Go 1.27 (already required; kcp 0.33 targets Go 1.26, so no toolchain change).
- `task` (go-task) installed via `devbox` / `Taskfile.yaml`.
- Network access to `github.com/kcp-dev/kcp/releases/download` for the binary.

## Validate the pins

```sh
task tools:kcp
bin/kcp --version          # must say v0.33.3 (SC-001)
go list -m github.com/kcp-dev/sdk github.com/kcp-dev/apimachinery/v2 github.com/kcp-dev/logicalcluster/v3
```

## Validate the graph

```sh
go mod tidy
git diff --exit-code -- go.mod go.sum   # SC-004; must be clean
go vet ./...
task lint                                # same gate as verify
```

## Validate tests against the real kcp server

```sh
task test:integration:kcp         # SC-002; must exit 0 within the existing budget
task drift                         # SC-005; must pass (no new fork divergence)
```

The done condition is `task verify` (`bin/verify-result.json` three-outcome contract: `pass` / `fail` / `could not run`). A single flake may be retried once and recorded as `could not run` — see SC-003.

## Optional — demo against the new server (manual, not part of verify)

```sh
task demo
```

Expect "available clusters in every workspace, tenants isolated" — same as `test/integration/demo` asserts in CI (SC-006).

## Optional — sweep evidence (only if you need to prove NFR-001)

```sh
task test:sweep          # measures per-workspace cost; only report if materially changed
```

## Check the doc was updated

```sh
grep -n "kcp v0.33.3" docs/conversion-plan.md   # SC-007
```

## Cleanup

No state to clean — the kcp server is ephemeral (embedded etcd, torn down after each suite/demo run).
