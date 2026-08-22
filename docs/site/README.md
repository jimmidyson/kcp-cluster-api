# kcp-cluster-api docs site

Hugo + [Docsy](https://www.docsy.dev/) site for kcp-cluster-api's user and
design documentation. Published from `content/en/docs/`:

- `docs/user/` — installation and usage, for people running kcp-cluster-api.
- `docs/design/` — architecture and deep dives, technical reference for
  developers and agents changing the code.

The site has no landing page: `layouts/index.html` redirects `/` straight to
`docs/user/`, so the published root is the user docs.

See [Documentation policy](content/en/docs/design/documentation-policy.md)
for what's expected of new pages, and the root
[`kcp/README.md`](../../README.md) for how this fits into the rest of the
fork.

## Publishing

The site is published at <https://jimmidyson.github.io/kcp-cluster-api/> by
the `docs` workflow. It builds on every pull request touching `docs/site/`,
and on a merge to `main` deploys that same build to GitHub Pages — there is
no `gh-pages` branch and no published artefact to update by hand.

Two settings this depends on, neither of which the workflow can set for
itself:

- **Settings -> Pages -> Source** must be **GitHub Actions**. Enabling Pages
  over the API needs `administration: write`, which `GITHUB_TOKEN` cannot be
  granted, so this is a one-off a repository admin does.
- `baseURL` in `hugo.toml` is the project-site URL, so a fork or a rename
  needs it changed to match, or every link on the published site points at
  this repository.

## Search

The site's search box — in the navbar and at the top of the sidebar — is
Docsy's offline search. The build writes a [Lunr](https://lunrjs.com/) index
of every page to `public/offline-search-index.*.json`, and the browser
queries that file directly: no crawler, no search service, nothing to
re-index after a deploy and nothing to configure beyond `hugo.toml`.

Two things follow from that:

- A page becomes searchable when it is published, not when it is written —
  the index is built from the rendered site.
- Docsy pulls Lunr from `unpkg.com`, next to the jQuery it already loads for
  the rest of the theme. A reader who cannot reach that CDN gets the whole
  site minus the search box's results.

To keep a page out of the index, set `exclude_search: true` in its front
matter — `content/en/_index.md` does, being a redirect rather than a page
anyone should land on from a search result.

## Local development

Requires Node.js 18+ and internet access to fetch the Hugo module (Docsy
theme) and npm packages.

```sh
npm install     # first time only, or after theme updates
npm run serve   # local preview at http://localhost:1313/ with live reload
npm run build   # production build into public/
```

This directory is a self-contained Hugo module — it has its own `go.mod`,
separate from the repository's root `go.mod`, and is unaffected by upstream
Cluster API rebases.

## Updating the Docsy theme

```sh
hugo mod get -u github.com/google/docsy/theme
hugo mod npm pack   # refresh package.json's Docsy-supplied dependencies
npm install
```
