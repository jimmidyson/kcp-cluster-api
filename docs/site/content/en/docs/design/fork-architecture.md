---
title: Dependency Architecture
description: Why upstream Cluster API is a pinned dependency, and what the patched fork is for.
weight: 10
---

kcp-cluster-api makes Cluster API workspace-aware for
[kcp](https://github.com/kcp-dev/kcp) while staying cheap to move onto new
upstream releases. The normative rules live in `AGENTS.md` and
`.specify/memory/constitution.md`; this page explains the reasoning.

## Upstream is a dependency, not a tree

Upstream Cluster API is consumed the way any other Go dependency is: pinned
by version in `go.mod`. This repository contains none of it.

That arrangement is deliberate, and it replaced an earlier one. The project
began as a full fork of the Cluster API tree, with its own work confined to a
`kcp/` subdirectory and a rule that nothing outside it could be edited. The
rule was sound — a single unnecessary edit to an upstream file turns every
future upgrade into a manual conflict, and that cost compounds release after
release — but it was enforced by asking people to remember it, over a tree
that was almost entirely off-limits.

It did not hold. Seven inherited CI workflow files accumulated local edits to
keep them working, and dependency bots rewrote upstream `go.mod` and `go.sum`
in four separate modules. None of that was anyone's decision; it was the
ordinary background activity of maintaining a repository.

Deleting the upstream tree converts the invariant from a rule people must
remember into a property of the repository: the files it forbade editing are
not here to edit. An upgrade becomes a change to a version string, reviewed
as a diff of pins.

## The patched fork

Some changes cannot be avoided. Cluster API does not expose every hook this
project needs, and until it does, those changes live in a patched fork:
[`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api).

The fork is deliberately minimal — patches and tags, nothing else. No
specifications, no tooling, no process of its own. That keeps
`git diff upstream..kcp/v1.15` meaningful without anything to mentally
subtract.

Its contract:

- One branch per upstream release line, cut from the exact upstream commit
  this project builds against.
- One commit per carried patch, each referencing the upstream proposal that
  will retire it.
- Three immutable tags per release — the root module plus `api/` and `test/`,
  which are separate Go modules resolved by tag prefix.

### The base is chosen, but not freely

The fork branch is cut from an upstream `main` commit rather than a release
tag, and that is forced rather than preferred. At `v1.14.0` the docker
provider's reconcilers and admission webhooks lived under `internal/`, which
an external module cannot import. They became public on `main` afterwards.
This project cannot build without them, so the base has to be a commit that
contains that change.

## Divergence is counted

Every carried patch is recorded in `DRIFT.md` with the base commit it applies
to and the upstream proposal that will make it unnecessary. `task drift`
compares that record against reality and fails on any path that diverges
without an entry.

A patch may be carried before its proposal is filed, but only with a filing
date, no more than 90 days out. After that it is a defect by the project's
own rules: file it, remove it, or amend the constitution — never quietly
extend the date.

The check runs on a schedule and on changes to the pin or the record, rather
than on every pull request. Gating unrelated work on the state of another
repository produces failures their authors cannot fix.

## Integrate through public extension points

KCP-awareness is layered onto upstream using what upstream already exposes:
own manager entrypoints, injected clients and caches, controller-runtime's
manager options, sources, handlers, predicates and webhook chains.

When something appears to need an upstream internal, or a hook that does not
exist, the response is to stop and raise it — find another integration point,
propose the hook upstream, or accept the limitation. Adding it to the fork is
the last resort, and costs a `DRIFT.md` entry and a deadline.

The one patch carried today is exactly that case, and its shape reflects it:
rather than copying upstream logic or re-exporting an internal package
wholesale, it exposes two symbols through a public seam that mirrors an
escape hatch upstream already provides elsewhere — the shape most likely to
be accepted when proposed.
