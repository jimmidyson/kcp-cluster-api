---
title: Adopting Upstream Releases
description: Moving onto a newer Cluster API release is a dependency bump, not a merge.
weight: 30
---

Upstream Cluster API is a vendored tree under `forks/cluster-api`, a snapshot
of upstream at a recorded base commit with this project's patches applied in
place. Adopting a newer release is a subtree merge into that directory, and
the merge's conflicts are the modified paths in `DRIFT.md` and nothing else:

1. **Fetch the new upstream ref** and merge it into the tree as a subtree:
   `git merge -X subtree=forks/cluster-api --allow-unrelated-histories <ref>`,
   or `git subtree merge --prefix=forks/cluster-api <ref>`. Resolve the
   conflicts, which fall on the recorded paths; a conflict anywhere else is a
   path the record is missing.

2. **Move the base.** Update the base commit recorded in `DRIFT.md`.

3. **Check it.** `task verify` and `task drift`. The drift check measures the
   directory against upstream at the new base and fails on any path the
   record does not list.

## When a patch stops applying

That is a signal, not an obstacle. Check whether the patch's upstream
proposal landed: if it did, delete the patch from the fork and its entry from
`DRIFT.md` rather than forward-porting it. The carried set is supposed to
shrink.

If it did not land and the patch still applies conceptually, replay it and
keep the entry — but the filing deadline in `DRIFT.md` does not reset just
because the base moved.

## Why the merge stays cheap

The project once carried the entire upstream tree at the repository root, and
an upgrade was a tree-wide merge whose conflicts were proportional to how
much local editing had accumulated in upstream files. That editing happened
through routine activity — CI maintenance, dependency bots — so the cost grew
on its own.

Vendored under a subdirectory, the same activity has nowhere to land: GitHub
runs workflows only from the root `.github/`, Dependabot is pointed only at
this project's own modules, and the drift check runs on every pull request
that touches the tree. The only files that can disagree with upstream are the
ones recorded in `DRIFT.md`, and the set cannot grow without a decision. See
[ADR-0005](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0005-one-repository.md).
