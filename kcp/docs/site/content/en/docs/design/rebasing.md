---
title: Rebasing onto Upstream
description: How kcp/ stays disjoint from upstream so new Cluster API releases merge cleanly.
weight: 30
---

Because `kcp/` is disjoint from every upstream path, pulling in new upstream
Cluster API releases should almost always be a clean merge:

```sh
git fetch origin main   # or the upstream remote, if configured
git merge origin/main   # or rebase, per team preference
```

If this produces a conflict **outside `kcp/`**, that's a strong signal that
a past change violated the [read-only-upstream invariant](fork-architecture.md)
— the fix is to find and correct the offending commit, not to resolve the
conflict in place. Use the check in
[Fork architecture](fork-architecture.md#verifying-the-invariant) to
confirm the diff is confined to `kcp/` before and after a rebase.

## Why this matters for docs specifically

This documentation site is itself under `kcp/docs/site/`, with its own
`go.mod` (a separate Hugo module) so it can pull in Docsy theme updates
independently of the repository's root Go module. A rebase never needs to
touch this site's files, and this site's changes never need to touch
anything a rebase would conflict with.
