---
title: Design & Architecture
linkTitle: Design & Architecture
weight: 20
---

Technical reference for developers — and AI coding agents — working on
kcp-cluster-api. This section explains *how* the project is built and *why*,
so a change can be made correctly without re-deriving context from scratch.

If you're looking for how to run the project rather than build it, see
[User docs](../user/_index.md).

- **[Dependency architecture](fork-architecture.md)** — why upstream is a
  pinned dependency rather than a tree here, what the patched fork is for,
  and how divergence is counted.
- **[Repository layout](repository-layout.md)** — what lives where.
- **[Per-workspace wiring](per-workspace-wiring.md)** — the seam between
  workspace discovery and unmodified upstream reconcilers.
- **[Workspace resource usage](workspace-resource-usage.md)** — what one
  manager process costs per active workspace, measured against a real kcp
  server.
- **[Adopting upstream releases](rebasing.md)** — why an upgrade is a
  dependency bump rather than a merge.
- **[Documentation policy](documentation-policy.md)** — what documentation is
  required when the codebase changes, and where it goes.
