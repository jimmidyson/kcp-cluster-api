---
title: Adopting Upstream Releases
description: Moving onto a newer Cluster API release is a dependency bump, not a merge.
weight: 30
---

Upstream Cluster API is a pinned dependency, so adopting a newer release
changes version strings rather than merging a tree:

1. **Prepare the fork.** Cut a branch in
   [`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api)
   from the new upstream ref, replay the patches listed in `DRIFT.md`, and
   tag all three modules — the root, `api/` and `test/`. A partial tag set
   fails later at dependency resolution with a confusing "unknown revision"
   error rather than at build time.

2. **Move the pins.** Update the `replace` directives in `go.mod` and the
   base commit recorded in `DRIFT.md`.

3. **Check it.** `task verify` and `task drift`.

## When a patch stops applying

That is a signal, not an obstacle. Check whether the patch's upstream
proposal landed: if it did, delete the patch from the fork and its entry from
`DRIFT.md` rather than forward-porting it. The carried set is supposed to
shrink.

If it did not land and the patch still applies conceptually, replay it and
keep the entry — but the filing deadline in `DRIFT.md` does not reset just
because the base moved.

## Why this is cheap now

It was not always. The project previously carried the entire upstream tree,
and an upgrade was a tree-wide merge whose conflicts were proportional to
how much local editing had accumulated in upstream files. Since that editing
happened through routine activity — CI maintenance, dependency bots — the
cost grew on its own.

With upstream absent, there is nothing to conflict. The only files that can
disagree are the ones deliberately recorded in `DRIFT.md`, and there is a
check that fails when that set grows without a decision.
