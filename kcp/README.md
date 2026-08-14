# kcp/

This directory contains everything specific to making Cluster API
workspace-aware for [KCP](https://github.com/kcp-dev/kcp). It is the **only**
place new code, tests, and docs for this fork should live — see the root
[`AGENTS.md`](../AGENTS.md) for the invariants this exists to protect
(short version: everything outside `kcp/` is unmodified upstream
cluster-api and must stay that way).

## Layout

Structure mirrors upstream's top-level layout where it makes sense, so it's
obvious what a piece of KCP-aware code corresponds to. Create subdirectories
as they're actually needed rather than pre-scaffolding empty ones; suggested
names:

- `kcp/cmd/` — our own manager/binary entrypoints (we do not run upstream's
  `main.go` directly, since wiring in workspace-awareness happens here).
- `kcp/controllers/` — KCP-aware controllers/reconcilers.
- `kcp/client/` — workspace-aware `client.Client` / REST config wrapping.
- `kcp/api/` — any KCP-specific API types (e.g. new CRDs), if needed.
- `kcp/internal/` — implementation details not meant for external import.
- `kcp/test/integration/` — integration tests specific to KCP behavior,
  run against a real kcp server via `kcp/test/integration/envtest` (see
  `kcp/docs/testing.md`).
- `kcp/docs/` — design notes and docs specific to this fork, including the
  documentation site described below.

## Documentation

Everything this fork adds must be documented for two audiences, in the
[Hugo](https://gohugo.io/) + [Docsy](https://www.docsy.dev/) site under
`kcp/docs/site/`:

- **User docs** (`content/en/docs/user/`) — installation and usage, for
  people running kcp-cluster-api.
- **Design docs** (`content/en/docs/design/`) — architecture and deep dives,
  technical reference for developers and agents changing the code.

A feature isn't done until both are updated (or a no-op is genuinely
correct, e.g. an internal change with no user-visible behavior still needs
a design write-up but not a user-docs change). See
[Documentation policy](docs/site/content/en/docs/design/documentation-policy.md)
for the full policy, and `kcp/docs/site/README.md` for how to build/preview
the site.

`kcp/` is its own Go module (`kcp/go.mod`), separate from the root
`sigs.k8s.io/cluster-api` module, so it can depend on things like
`github.com/kcp-dev/sdk` without touching root `go.mod` — see the
"Manifest-style files" section of `AGENTS.md`.

## Ground rules (see `AGENTS.md` for full detail)

1. Nothing in here edits a file outside `kcp/`.
2. Integrate with upstream via its existing public extension points only.
3. If that's not possible for something, stop and raise it rather than
   reaching into upstream code.
4. All new behavior is developed test-first, with both unit tests and
   KCP-envtest integration tests as applicable — see
   [`kcp/docs/testing.md`](docs/testing.md).
5. New code and user-visible behavior ship with matching user and design
   docs — see [Documentation](#documentation) above.
