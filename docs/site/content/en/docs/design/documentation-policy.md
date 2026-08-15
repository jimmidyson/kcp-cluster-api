---
title: Documentation Policy
description: What documentation is required when the codebase changes, and where it goes.
weight: 40
---

Everything this fork adds must be documented from two distinct angles, for
two distinct audiences. This is a requirement, not a suggestion — treat it
the same way you'd treat a missing test.

## The two angles

1. **User docs** — under [`content/en/docs/user/`](../user/_index.md).
   Audience: someone installing and running kcp-cluster-api. Focus strictly
   on *installation* and *usage* — prerequisites, build/deploy steps,
   configuration, day-to-day operation, upgrade notes. Assume no interest in
   implementation details.

2. **Design & architecture docs** — under [`content/en/docs/design/`](_index.md).
   Audience: developers and AI coding agents changing the code. This is
   technical reference: why a component is shaped the way it is, the
   invariants it must preserve, the extension points it relies on, and
   anything a future change is likely to get wrong without this context.

A feature isn't done until both exist (or an existing page has been updated
to cover it). If a change has no user-visible behavior, the user-docs side
may be a no-op — but a design write-up is still expected whenever the
change introduces a new component, integration point, or non-obvious
decision.

## Where new pages go

- New user-facing capability → new or updated page under `docs/user/`.
- New component, integration point, or architectural decision → new or
  updated page under `docs/design/`.
- Don't put design content in user docs or vice versa — split it across two
  pages if a change touches both, and cross-link them.

## Front matter conventions

Follow the pattern already used by pages in this site:

```yaml
---
title: Short Title
description: One sentence, shown in the sidebar/search preview.
weight: 10   # controls ordering within the section; leave gaps (10, 20, 30...)
             # so pages can be inserted later without renumbering everything
---
```

Section landing pages (`_index.md`) additionally set `linkTitle` when the
nav label should differ from the page title.

## Building and previewing

```sh
cd kcp/docs/site
npm install     # first time only, or after theme updates
npm run serve   # local preview at http://localhost:1313 with live reload
npm run build   # production build into public/, used in CI
```

The site is a self-contained Hugo module under `kcp/docs/site/` (see
[Repository layout](repository-layout.md)) — it does not touch the
repository's root `go.mod` and is unaffected by upstream rebases.

## Where this is enforced

- This page is the canonical statement of the policy.
- `kcp/README.md` (the repository's own README, one level up from this site)
  carries a condensed pointer back here, so it's visible without opening the
  site.
- CI builds this site on every PR that touches `kcp/docs/site/`, so a
  content or config mistake fails the PR rather than shipping broken docs.
