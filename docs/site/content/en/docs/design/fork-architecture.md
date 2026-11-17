---
title: Dependency Architecture
description: Why upstream Cluster API is a pinned dependency, and what the patched fork is for.
weight: 10
---

kcp-cluster-api makes Cluster API workspace-aware for
[kcp](https://github.com/kcp-dev/kcp) while staying cheap to move onto new
upstream releases. The normative rules live in `AGENTS.md` and
`.specify/memory/constitution.md`; this page explains the reasoning.

## Upstream is a vendored tree, not a dependency and not the root

Upstream Cluster API lives under `third_party/forked/cluster-api`, and each infrastructure
provider under its own `third_party/forked/` directory: a complete snapshot of upstream at
a recorded base commit, with this project's patches applied in place. Each
tree keeps its upstream module path, so every module here imports upstream
under upstream's name; consuming modules point that name at the tree with
relative `replace` directives, and a committed `go.work` makes the whole
repository one build. There is no version to pin and nothing to tag.

This is the project's third arrangement, and the reasoning for it is in
[ADR-0005](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0005-one-repository.md).

The project began as a full fork of the Cluster API tree, with its own work
confined to a `kcp/` subdirectory and a rule that nothing outside it could be
edited. The rule did not hold: seven inherited CI workflow files accumulated
local edits to keep them working, and dependency bots rewrote upstream
`go.mod` and `go.sum` in four separate modules. None of that was anyone's
decision; it was the ordinary background activity of maintaining a
repository in which upstream was the root.

The second arrangement deleted the upstream tree and consumed it as a
version-pinned dependency, resolved to a patched fork in a repository of its
own. That made the invariant a property of this repository — the files it
forbade editing were not here to edit — and it worked while the fork was a
small, shrinking set of patches with upstream proposals attached. It stopped
fitting once [ADR-0003](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0003-workspace-aware-cluster-api.md)
made the fork a permanent derivative: every reconciler became two halves in
two repositories, with a signed three-tag ceremony and a pin bump between
them, and no test could see a change until it had been tagged.

The third arrangement puts the trees back, but under a subdirectory rather
than at the root, and that difference is the whole point. The forces that
broke the first arrangement have nowhere to land: GitHub runs workflows only
from the root `.github/`, so upstream's are inert text; Dependabot is pointed
only at this project's own modules; and the rule people had to remember is
now a check that runs on every pull request touching a vendored tree.

## What a vendored tree carries

Some changes cannot be avoided. Cluster API does not expose every hook this
project needs, and
[ADR-0003](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0003-workspace-aware-cluster-api.md)
decided to carry the workspace-aware wiring rather than propose it upstream.
Those changes live in the tree, in place, and the tree's contract is:

- It is complete. Upstream's docs, scripts and workflows are vendored as they
  are; deleting them would turn every upstream adoption into conflicts on
  paths nobody cares about.
- It differs from upstream at its base commit only in the paths its drift
  record lists.
- Adopting a new upstream release is a subtree merge into the directory, and
  the merge's conflicts are the record's modified paths and nothing else.

### The base is chosen, but not freely

The Cluster API tree is cut from an upstream `main` commit rather than a
release tag, and that is forced rather than preferred. At `v1.14.0` the
docker provider's reconcilers and admission webhooks lived under `internal/`,
which an external module cannot import. They became public on `main`
afterwards. This project cannot build without them, so the base has to be a
commit that contains that change.

## Divergence is counted

Every carried patch is recorded in `DRIFT.md` with the base commit it applies
to and the upstream proposal that will make it unnecessary. `task drift`
compares that record against reality and fails on any path that diverges
without an entry.

A patch may be carried before its proposal is filed, but only with a filing
date, no more than 90 days out. After that it is a defect by the project's
own rules: file it, remove it, or amend the constitution — never quietly
extend the date.

The check runs on every pull request that touches a vendored tree, a record
or the checker, and on a schedule besides. While the trees lived in forks of
their own it could not run per pull request, because gating unrelated work on
another repository's state produced failures their authors could not fix; the
thing it measures is in the pull request now.

## Integrate through public extension points

KCP-awareness is layered onto upstream using what upstream already exposes:
own manager entrypoints, injected clients and caches, controller-runtime's
manager options, sources, handlers, predicates and webhook chains.

When something appears to need an upstream internal, or a hook that does not
exist, the response is to stop and raise it — find another integration point,
propose the hook upstream, or accept the limitation. Adding it to the vendored
tree is the last resort, and costs a `DRIFT.md` entry and a deadline.

The contract-metadata patch is exactly that case, and its shape reflects it:
rather than copying upstream logic or re-exporting an internal package
wholesale, it exposes two symbols through a public seam that mirrors an
escape hatch upstream already provides elsewhere — the shape most likely to
be accepted when proposed. It is also the only patch carried on those terms.

## What the tree actually carries now

The paragraphs above describe the arrangement; the record describes the
contents. [ADR-0003](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0003-workspace-aware-cluster-api.md)
decided to carry the workspace-aware wiring and **not** to propose it
upstream, so most of the record is permanent rather than pending, and most
of it modifies upstream files in place rather than adding new ones.

`DRIFT.md` is the count, and is deliberately not reproduced here: a figure
copied into prose is how an earlier version of this page came to describe a
single-patch fork long after that stopped being true.

[ADR-0004](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0004-scaling-to-many-provider-forks.md)
settled how this scales to several providers — one integration module per
provider, under `providers/`, each with its own dependency graph — and
[ADR-0005](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0005-one-repository.md)
superseded its assumption that each provider's upstream would be a fork in a
repository of its own.
