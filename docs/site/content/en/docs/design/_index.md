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
- **[Adopting upstream releases](rebasing.md)** — why an upgrade is a
  dependency bump rather than a merge.
- **[Documentation policy](documentation-policy.md)** — what documentation is
  required when the codebase changes, and where it goes.
