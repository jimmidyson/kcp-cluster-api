# Drift record

The complete set of changes this project carries against upstream Cluster
API. Everything here is a liability: each entry makes the next upstream
adoption more expensive, and each is expected to be deleted once its
upstream proposal is accepted.

`task drift` checks this file against reality and fails on any path that
diverges without an entry here.

## Where the check runs, and why not on every pull request

The check runs on a daily schedule, on demand, and on pull requests that
touch the pin, this record, or the checker itself — not on every pull
request, and not in the fork.

**Not on every pull request here**, because the thing being measured lives
in another repository. Gating every change on it means an unrelated pull
request goes red because somebody pushed to the fork: a failure its author
can neither fix nor merge past. The scheduled run surfaces the same problem
within a day, without holding anyone's work hostage. The path filter keeps
it blocking exactly where a pull request in this repository *is* the right
place to fix it — when the pinned version or this record changes.

**Not in the fork**, though that is where drift is introduced, because the
checker would become drift. The fork's contract is "upstream at base commit
plus recorded patches, nothing else", which is what makes
`git diff upstream..kcp/v1.15` mean something without mental subtraction.
Adding a workflow and a copy of this record there would add two more
differing paths and require the check to exempt its own infrastructure. It
would also need a second implementation: the checker is Go in this module,
and the fork is the Cluster API module, which cannot import it without a
cycle.

If push-time rejection on the fork is wanted, branch protection on `kcp/*`
is the honest mechanism, not a workflow the fork has to carry.

Fork: [`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api), branch `kcp/v1.15`, tag `v1.15.0-kcp.1`

Base: `281e4e3ed2af1d6852651d69e1207a3073b478c2`

The base is an upstream `main` commit, not a release tag, and that is
forced rather than chosen: at `v1.14.0` the docker provider's reconcilers
and admission webhooks were under `internal/`, which an external module
cannot import. They became public on `main` afterwards, and this project
cannot build without them. See the feature's research notes (R2).

## Carried patches

| Path | Rationale | Upstream proposal |
|---|---|---|
| `internal/contract/version.go` | Factors the contract-metadata resolver into an overridable package variable. Every contract-version lookup funnels through it, so one seam covers `GetObjectFromContractVersionedRef`, `GetContractVersion` and `GetAPIVersion` uniformly. Default behaviour is unchanged. | **Pending**, due **2026-11-13** |
| `controllers/external/metadata.go` | Exposes `SetGKMetadataGetter` and `GetAPIVersion` publicly, so a module outside `sigs.k8s.io/cluster-api/` can supply its own resolver. Mirrors the existing `conversion.SetAPIVersionGetter` escape hatch. | **Pending**, due **2026-11-13** |

Both paths belong to a single patch, carried as one commit in the fork.

### Why this patch exists

Resolving a contract-versioned reference — `spec.infrastructureRef`,
`spec.bootstrap.configRef`, `spec.controlPlaneRef` — reads contract-version
labels off the referenced type's `CustomResourceDefinition` object. That
assumes a CRD object is the source of truth for every type a cluster serves.

Under kcp it is not: a workspace consuming a type through an `APIBinding`
has no such object, because the CRD-shaped source of truth is an
`APIResourceSchema` in the *exporting* workspace. Every reconcile that
resolves a cross-reference fails with an internal error — which is not a
corner case, since `infrastructureRef` is how every infrastructure provider
integrates with core Cluster API.

## Pending proposals

Constitution Principle I permits carrying a patch before its proposal is
filed, but only as an explicitly pending state with a filing date no more
than 90 days out. Once that date passes without a filed proposal, the
carried patch is a defect in this project — the response is to file, remove
the patch, or amend the constitution, never to extend the date quietly.

| Patch | Landed | Proposal due |
|---|---|---|
| Pluggable contract-metadata resolution | 2026-08-15 | 2026-11-13 |
