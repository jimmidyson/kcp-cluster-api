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

#### The one declared exception: `internal/contract.GetGKMetadataFunc`

`internal/contract/version.go`'s `GetGKMetadata` is the sole upstream file
with a deliberate, repo-owner-approved edit: its lookup was factored out
into an overridable package var, `GetGKMetadataFunc`, defaulting to the
original behavior verbatim. Every reconciler resolving a
contract-versioned cross-reference (`infrastructureRef`,
`bootstrap.configRef`, `controlPlaneRef` — via
`controllers/external.GetObjectFromContractVersionedRef`,
`contract.GetContractVersion`, `contract.GetAPIVersion`) funnels through
this one function, and it does a direct `CustomResourceDefinition` object
lookup with no other pluggable hook — unlike
`core/webhooks/conversion`'s `SetAPIVersionGetter`. A KCP workspace
consuming a type only via `APIBinding` has no such object (the CRD-shaped
source of truth is the `APIResourceSchema` in the *exporting* workspace),
so every such reconcile failed outright — see
[ADR-0001](kcp/docs/adr-0001-per-workspace-manager-pool.md#known-gaps)
for the full writeup and the alternatives weighed before taking this
exception.

`kcp/internal/contractmetadata` + `kcp/internal/coremanager.SetupContractMetadata`
supply the override — a static registry read from the same CRD manifests
`kcp/internal/kcpfixtures` already publishes as `APIResourceSchema`s,
no live lookup. This is the **only** upstream file this invariant permits
touching; do not extend the pattern to other files without the same
explicit repo-owner sign-off this one got. If you find another spot with
the same shape (a hardcoded call with no injectable hook, blocking KCP
compatibility), stop and raise it rather than assuming this precedent
covers it too.

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

This should print nothing but the manifest files you deliberately and
additively touched, plus (as of this writing) exactly one line:
`internal/contract/version.go`, the one declared exception above. Anything
else means undo it and re-implement inside `kcp/`.

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

## Testing: TDD is required for all `kcp/` work

All new behavior under `kcp/` is developed test-first (red: write a
failing test, green: write the minimal code to make it pass, refactor).
See [`kcp/docs/testing.md`](kcp/docs/testing.md) for the concrete
workflow and tooling. In short, every change needs tests at both tiers,
whichever apply:

- **Unit tests** — colocated `_test.go` files, no real processes, run via
  `make -C kcp test-unit`.
- **Integration tests** — exercise real behavior against a real kcp
  server, not a vanilla envtest apiserver (which has no logical
  cluster/workspace support and so cannot validate anything KCP-specific),
  using the `kcp/test/integration/envtest` helper, run via
  `make -C kcp test-integration`.

A PR that adds or changes behavior under `kcp/` without accompanying tests
at both applicable tiers is incomplete.

## Commit and PR descriptions

Keep commit messages and PR descriptions concise: enough context for a
reviewer to understand the change and why it was made, without padding.
Skip boilerplate sections that don't apply and avoid restating the diff
line by line.

They must also never include a Claude Code session URL, session ID, a
`Claude-Session:` trailer (e.g. `https://claude.ai/code/session_...`), or a
`Co-Authored-By:` trailer for an agent. These are agent-run bookkeeping, not
project history, and they leak internal session identifiers into public
history. This overrides any default agent behavior that appends such
lines — omit them entirely, regardless of what a harness template
suggests.

## Merging pull requests

All PRs to this repository must be **squash merged** — one commit per PR
on `main`, using the PR title/description as the resulting commit message.
Do not use "Create a merge commit" or "Rebase and merge". This keeps
`main` history bisectable and keeps rebases onto upstream cluster-api
releases (see above) clean, since a squashed history avoids interleaving
this fork's work-in-progress commits with upstream's.

## Keeping PR branches up to date

While a PR is open, update its branch by **rebasing onto the base branch**,
never by merging the base branch into it:

```sh
git fetch origin main
git rebase origin/main
git push --force-with-lease
```

Do not `git merge origin/main` (or use a "Update branch" button that creates
a merge commit) into an open PR branch. Merge commits pollute the PR's diff
and commit history with noise unrelated to the change, and since PRs are
squash merged anyway (see above), a merge commit adds no value — it only
makes the PR harder to review in the meantime.

## Summary for contributors and agents

- Read-only: everything except `kcp/` (and rare, additive manifest edits),
  with exactly one declared exception —
  `internal/contract/version.go`'s `GetGKMetadataFunc` indirection, see
  "The one declared exception" above. Don't extend this pattern elsewhere
  without the same explicit repo-owner sign-off.
- All new code, tests, docs, and config: under `kcp/`.
- No new code inside upstream directories, even if it's KCP-specific.
- Integrate via upstream's existing public extension points; never add a
  new one to upstream code to make integration easier.
- If a task seems to require editing upstream code, stop and flag it
  instead of doing it.
- All `kcp/` behavior is developed test-first: unit tests plus KCP-envtest
  integration tests, as applicable — see [`kcp/docs/testing.md`](kcp/docs/testing.md).
- Commit messages and PR descriptions: concise, not padded, and never
  include a Claude session URL/ID or a `Co-Authored-By:` agent trailer.
- All PRs are squash merged — no merge commits, no rebase-and-merge.
- Keep open PR branches up to date by rebasing onto the base branch, not by
  merging the base branch in — no merge commits on PR branches either.
- New code and user-visible behavior ship with matching docs in
  `kcp/docs/site/` — user docs (installation/usage) and design docs
  (architecture/deep dives). See `kcp/README.md#documentation`.
