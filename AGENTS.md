# kcp-cluster-api

This repository is a **fork of [kubernetes-sigs/cluster-api](https://github.com/kubernetes-sigs/cluster-api)**
whose purpose is to make Cluster API workspace-aware for
[KCP](https://github.com/kcp-dev/kcp) (logical clusters / workspaces).

Fork point: `281e4e3` on upstream `main` (cluster-api v1.14 series, contract `v1beta2`).

## The prime directive: upstream code is read-only

**Every file that exists in upstream cluster-api must remain byte-for-byte
identical to upstream, forever**, except when it changes because of an
explicit upstream merge/rebase. This is not a style preference — it is the
entire point of this fork. The easier it is to rebase onto new cluster-api
releases, the more valuable this project is. A single unnecessary edit to an
upstream file turns every future rebase into a manual conflict to resolve,
compounding release after release.

This applies to **all existing top-level directories and files** as of the
fork point: `api/`, `bootstrap/`, `cmd/`, `controllers/`, `controlplane/`,
`core/`, `docs/`, `exp/`, `feature/`, `internal/`, `test/`, `util/`,
`version/`, `Makefile`, `Dockerfile`, `Tiltfile`, `go.mod`, `go.sum`,
`README.md`, `CONTRIBUTING.md`, etc. — and to any new files/directories that
arrive in those locations via future upstream rebases.

**Do not touch these files. Not to fix a typo, not to reformat, not to add a
"harmless" comment, not to reorder an import, not for a "small" bugfix you
noticed in passing. If it lives outside `kcp/`, treat it as read-only.**

If upstream code genuinely needs a bugfix, that fix belongs upstream
(open an issue/PR against `kubernetes-sigs/cluster-api`), not here.

### No exceptions, no patch mechanism

There is deliberately no sanctioned way to modify upstream files in this
repo — no patch files, no `// KCP-PATCH` markers, no build-tag overlays on
existing files. If a feature seems to require editing upstream code, that is
a signal to find a different integration point (see below), not to make the
edit. If no such point exists yet, stop and raise it (open an issue, ask a
maintainer) rather than compromising the invariant.

## All new work lives under `kcp/`

Every line of code, config, docs, and tests this project adds goes under the
top-level `kcp/` directory. Nothing under `kcp/` exists in upstream, so it
can never conflict with an upstream rebase. See `kcp/README.md` for the
internal layout.

Corollary: never add KCP-specific files inside upstream directories either
(no `controllers/cluster_kcp_controller.go`, no `internal/kcp/...`). Even
though such a file is technically "new," putting it inside an upstream
directory risks future upstream files landing at the same path, invites
accidental edits to neighboring upstream files in the same PR, and blurs the
`kcp/` vs. upstream boundary that makes rebases mechanically checkable.

### Integration is extension-point-only

Because upstream files can't be edited, KCP-awareness must be layered on
top using integration points upstream already exposes as public API —
things like:

- Building our own `main`/manager entrypoint under `kcp/cmd/` that imports
  upstream's reconcilers/webhooks and wires them together ourselves, rather
  than running upstream's `main.go`.
- Wrapping/decorating the `client.Client` (and caches, REST config, etc.)
  passed into upstream constructors so it becomes workspace-aware, if
  upstream's constructors accept an injected client rather than hardcoding
  one.
- Using controller-runtime's own extension surfaces (manager options,
  webhook chains, custom `Source`/`Handler`/`Predicate` implementations,
  field indexers) rather than modifying upstream registration code.
- Running KCP-aware components (e.g. an APIExport/virtual-workspace proxy)
  as separate processes/binaries under `kcp/cmd/` that sit in front of or
  alongside unmodified upstream controllers.

If, while implementing something, you find upstream doesn't expose the hook
you need (e.g. a constructor hardcodes `mgr.GetClient()` instead of taking
a client), do not "just" add the parameter upstream — treat that as a
blocker to raise (open an issue, ask a maintainer) rather than work around.
Options at that point are: find a different approach (e.g. operate on a
wrapped `Manager`), or accept the limitation until it's solved upstream.

### Manifest-style files: additive-only, and rare

A very small set of root files are mechanical manifests rather than logic,
and changing them is sometimes unavoidable to build/wire `kcp/` code in:

- `go.mod` — adding new `require` lines for `kcp/`'s own dependencies.
- `.gitignore` — appending new ignore patterns for `kcp/` build artifacts.
- `.github/workflows/` — adding **new** workflow files for `kcp/` CI, never
  editing existing upstream workflow files.

Rules for these:
1. Only ever **append**. Never reorder, reformat, or edit an existing line.
2. Never remove or modify an existing upstream entry.
3. Prefer a new file over touching an existing one wherever the tooling
   allows it (e.g. a second `go.work`/separate go module under `kcp/` is
   preferable to editing the root `go.mod`, if feasible).
4. When in doubt, ask before touching a manifest file — it's the one place
   the "never touch it" rule has any give, so treat every instance as
   worth double-checking rather than assumed-fine.

Do **not** treat `Makefile`, `Dockerfile`, `Tiltfile`, or `README.md` as
manifest-style — they contain real logic/prose and fall under the strict
read-only rule. Give `kcp/` its own `Makefile`/docs instead, included from
the root only via an additive one-line `include` if absolutely necessary.

## Verifying the invariant

Before committing, changes should be confined to `kcp/` (plus, rarely, the
additive manifest lines above). A quick sanity check:

```sh
git diff --name-only <upstream-base>..HEAD -- . ':!kcp'
```

This should print nothing (or only the manifest files you deliberately and
additively touched). If it prints anything else, undo it and re-implement
inside `kcp/`.

## Rebasing onto newer cluster-api releases

Because `kcp/` is disjoint from every upstream path, pulling in new
upstream releases should almost always be a clean merge:

```sh
git fetch origin main                     # or the upstream remote, if configured
git merge origin/main                     # or rebase, per team preference
```

If this ever produces a conflict **outside `kcp/`**, that is a strong
signal that a past change violated the invariant above — find and fix the
offending commit rather than resolving the conflict in place.

## Summary for contributors and agents

- Read-only: everything except `kcp/` (and rare, additive manifest edits).
- All new code, tests, docs, and config: under `kcp/`.
- No new code inside upstream directories, even if it's KCP-specific.
- Integrate via upstream's existing public extension points; never add a
  new one to upstream code to make integration easier.
- If a task seems to require editing upstream code, stop and flag it
  instead of doing it.
