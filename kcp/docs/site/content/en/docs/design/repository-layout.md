---
title: Repository Layout
description: What lives where, and conventions for adding new code under kcp/.
weight: 20
---

## Top level

Everything outside `kcp/` is unmodified upstream Cluster API — treat it as
read-only (see [Fork architecture](fork-architecture.md)). `kcp/` is the
only place new code, tests, and docs for this fork live.

## Inside `kcp/`

Structure mirrors upstream's top-level layout where it makes sense, so it's
obvious what a piece of KCP-aware code corresponds to. Subdirectories are
created as they're actually needed rather than pre-scaffolded empty —
today only `kcp/docs/` exists. Suggested names, as new code arrives:

| Directory | Purpose |
|---|---|
| `kcp/cmd/` | Our own manager/binary entrypoints — we do not run upstream's `main.go` directly, since wiring in workspace-awareness happens here. |
| `kcp/controllers/` | KCP-aware controllers/reconcilers. |
| `kcp/client/` | Workspace-aware `client.Client` / REST config wrapping. |
| `kcp/api/` | Any KCP-specific API types (e.g. new CRDs), if needed. |
| `kcp/internal/` | Implementation details not meant for external import. |
| `kcp/test/` | Integration/e2e tests specific to KCP behavior. |
| `kcp/docs/` | This documentation site, plus any design notes that don't belong in it. |

## The docs site

This Hugo + Docsy site lives at `kcp/docs/site/`. It's a self-contained Hugo
module (its own `go.mod`, not the repository's root `go.mod`) plus a small
npm toolchain for Docsy's CSS pipeline:

```sh
cd kcp/docs/site
npm install     # first time only, or after theme updates
npm run serve   # local preview with live reload
npm run build   # production build into public/
```

See [Documentation policy](documentation-policy.md) for what's expected of
new docs.

## Ground rules

1. Nothing in `kcp/` edits a file outside `kcp/`.
2. Integrate with upstream via its existing public extension points only.
3. If that's not possible for something, stop and raise it rather than
   reaching into upstream code.

See [`AGENTS.md`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/AGENTS.md)
for the full, normative rules.
