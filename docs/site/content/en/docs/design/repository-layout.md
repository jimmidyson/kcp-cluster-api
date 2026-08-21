---
title: Repository Layout
description: What lives where.
weight: 20
---

Everything in this repository is this project's own code. Upstream Cluster
API is a pinned dependency, not a tree here — see
[Dependency architecture](fork-architecture.md).

| Path | Purpose |
|---|---|
| `Taskfile.yaml` | The named operations. `task --list` shows them. |
| `DRIFT.md` | What this project carries against upstream, and why. |
| `cmd/core-manager/` | The KCP-aware manager entrypoint. We do not run upstream's `main.go`: wiring in workspace-awareness happens here. One sibling per provider. |
| `cmd/workspace-manager/` | Onboards workspaces to Cluster API and keeps their permissions right. It reconciles no Cluster API object; everything it writes is a permission. See [Workspace onboarding](workspace-onboarding.md). |
| `cmd/verify/` | The verification harness behind `task verify`. |
| `cmd/drift/` | The drift check behind `task drift`. |
| `internal/` | Implementation packages, not for external import. |
| `test/integration/` | Integration tests against a real kcp server. |
| `docs/` | ADRs, design notes and this site. |
| `specs/` | Spec-driven feature specifications. |
| `.specify/` | Spec Kit state, the project constitution, and extensions. |

Directories are created as they are needed rather than pre-scaffolded.

## The docs site

This Hugo + Docsy site lives at `docs/site/`. It is a self-contained Hugo
module — its own `go.mod`, separate from the repository's — plus a small npm
toolchain for Docsy's CSS pipeline:

```sh
task docs:build   # production build into public/

cd docs/site
npm run serve     # local preview with live reload
```

Keeping it as its own module means theme updates move independently of the
project's Go dependencies.

See [Documentation policy](documentation-policy.md) for what is expected of
new docs.

## Ground rules

1. Integrate with upstream via its existing public extension points only.
2. If that is not possible, stop and raise it rather than patching around
   it. A patch in the fork is the last resort and costs a `DRIFT.md` entry
   with a deadline.
3. New behaviour is developed test-first, with integration tests against a
   real kcp server.

See `AGENTS.md` for the working rules and
`.specify/memory/constitution.md` for the governing principles.
