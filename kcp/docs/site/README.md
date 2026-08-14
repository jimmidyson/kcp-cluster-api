# kcp-cluster-api docs site

Hugo + [Docsy](https://www.docsy.dev/) site for kcp-cluster-api's user and
design documentation. Published from `content/en/docs/`:

- `docs/user/` — installation and usage, for people running kcp-cluster-api.
- `docs/design/` — architecture and deep dives, technical reference for
  developers and agents changing the code.

See [Documentation policy](content/en/docs/design/documentation-policy.md)
for what's expected of new pages, and the root
[`kcp/README.md`](../../README.md) for how this fits into the rest of the
fork.

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
