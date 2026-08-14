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

- **[Fork architecture](fork-architecture.md)** — the read-only-upstream
  invariant and the extension-point-only integration model.
- **[Repository layout](repository-layout.md)** — what lives where, and the
  conventions for adding new code/docs under `kcp/`.
- **[Rebasing onto upstream](rebasing.md)** — how `kcp/` stays disjoint from
  upstream so pulling in new Cluster API releases stays a clean merge.
- **[Documentation policy](documentation-policy.md)** — what documentation is
  required when the codebase changes, and where it goes.
