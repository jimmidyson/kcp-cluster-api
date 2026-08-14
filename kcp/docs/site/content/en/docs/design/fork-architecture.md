---
title: Fork Architecture
description: The read-only-upstream invariant and the extension-point-only integration model.
weight: 10
---

kcp-cluster-api is a fork of
[kubernetes-sigs/cluster-api](https://github.com/kubernetes-sigs/cluster-api)
whose entire purpose is to make Cluster API workspace-aware for
[KCP](https://github.com/kcp-dev/kcp) (logical clusters / workspaces), while
staying trivially rebaseable onto new upstream releases. The full, normative
rules live in `AGENTS.md` at the repository root; this page explains the
reasoning behind them.

## The prime directive: upstream code is read-only

Every file that existed in upstream cluster-api at the fork point
(`281e4e3`, upstream `main`, Cluster API v1.14 series, contract `v1beta2`)
must remain byte-for-byte identical to upstream forever, except when it
changes because of an explicit upstream merge/rebase.

This is not a style preference. A single unnecessary edit to an upstream
file turns every future rebase into a manual conflict to resolve — and that
cost compounds release after release. So the rule is absolute: no edits to
upstream files, not even a typo fix, a reformat, or a "harmless" comment.
A genuine upstream bug belongs in an upstream PR, not here.

This invariant applies to all top-level directories/files that existed at
the fork point: `api/`, `bootstrap/`, `cmd/`, `controllers/`,
`controlplane/`, `core/`, `docs/`, `exp/`, `feature/`, `internal/`, `test/`,
`util/`, `version/`, `Makefile`, `Dockerfile`, `Tiltfile`, `go.mod`,
`go.sum`, `README.md`, `CONTRIBUTING.md`, etc. — and to any new files that
land in those locations via future upstream rebases.

## Everything new lives under `kcp/`

Because upstream files can't change, all KCP-specific code, tests, and docs
(including this site) live under the top-level `kcp/` directory. Nothing
under `kcp/` exists upstream, so it can never conflict with an upstream
rebase. See [Repository layout](repository-layout.md) for what goes where.

KCP-specific files must not be added inside upstream directories either —
even a file like `controllers/cluster_kcp_controller.go` is off-limits,
because it risks colliding with a future upstream file at the same path and
blurs the mechanically-checkable `kcp/` vs. upstream boundary.

## Integration is extension-point-only

Since upstream files can't be edited, KCP-awareness has to be layered on top
using integration points upstream already exposes as public API:

- A separate `main`/manager entrypoint under `kcp/cmd/` that imports
  upstream's reconcilers/webhooks and wires them together, instead of
  running upstream's `main.go` directly.
- Wrapping/decorating the `client.Client` (and caches, REST config, etc.)
  passed into upstream constructors so it becomes workspace-aware — where
  upstream's constructors accept an injected client rather than hardcoding
  one.
- Using controller-runtime's own extension surfaces (manager options,
  webhook chains, custom `Source`/`Handler`/`Predicate` implementations,
  field indexers) instead of modifying upstream registration code.
- Running KCP-aware components (e.g. an APIExport/virtual-workspace proxy)
  as separate processes/binaries under `kcp/cmd/` that sit in front of or
  alongside unmodified upstream controllers.

If upstream doesn't expose the hook a feature needs (for example, a
constructor hardcodes `mgr.GetClient()` instead of accepting a client), the
answer is **not** to add the parameter upstream. That's a blocker to raise
(open an issue, ask a maintainer), not a workaround to reach for. Until
it's resolved, the options are: find a different integration point, or
accept the limitation.

## Manifest-style files: additive-only, and rare

A very small set of root files are mechanical manifests rather than logic,
and can be touched — additively only — when unavoidable to wire `kcp/` code
in:

- `go.mod` — new `require` lines for `kcp/`'s own dependencies.
- `.gitignore` — new ignore patterns for `kcp/` build artifacts.
- `.github/workflows/` — new workflow files for `kcp/` CI, never edits to
  existing upstream workflow files.

Only ever append; never reorder, reformat, or remove an existing line.
`Makefile`, `Dockerfile`, `Tiltfile`, and `README.md` are **not**
manifest-style despite looking mechanical — they contain real logic/prose
and fall under the strict read-only rule.

## Verifying the invariant

```sh
git diff --name-only <upstream-base>..HEAD -- . ':!kcp'
```

This should print nothing, or only manifest files deliberately and
additively touched. Anything else means undo it and re-implement inside
`kcp/`.
