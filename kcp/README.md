# kcp/

This directory contains everything specific to making Cluster API
workspace-aware for [KCP](https://github.com/kcp-dev/kcp). It is the **only**
place new code, tests, and docs for this fork should live — see the root
[`AGENTS.md`](../AGENTS.md) for the invariants this exists to protect
(short version: everything outside `kcp/` is unmodified upstream
cluster-api and must stay that way).

## Layout

Structure mirrors upstream's top-level layout where it makes sense, so it's
obvious what a piece of KCP-aware code corresponds to. Create subdirectories
as they're actually needed rather than pre-scaffolding empty ones; suggested
names:

- `kcp/cmd/` — our own manager/binary entrypoints (we do not run upstream's
  `main.go` directly, since wiring in workspace-awareness happens here).
- `kcp/controllers/` — KCP-aware controllers/reconcilers.
- `kcp/client/` — workspace-aware `client.Client` / REST config wrapping.
- `kcp/api/` — any KCP-specific API types (e.g. new CRDs), if needed.
- `kcp/internal/` — implementation details not meant for external import.
- `kcp/test/` — integration/e2e tests specific to KCP behavior.
- `kcp/docs/` — design notes and docs specific to this fork.

## Ground rules (see `AGENTS.md` for full detail)

1. Nothing in here edits a file outside `kcp/`.
2. Integrate with upstream via its existing public extension points only.
3. If that's not possible for something, stop and raise it rather than
   reaching into upstream code.
