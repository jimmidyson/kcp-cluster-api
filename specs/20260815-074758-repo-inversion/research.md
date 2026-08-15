# Phase 0 Research: Standalone repository, task runner, and CI

**Feature**: [spec.md](./spec.md) | **Date**: 2026-08-15

Every finding below was verified against a command or source, per
Constitution Principle V. Where something could not be verified in the
development environment, that is stated rather than estimated.

---

## R1 — Do the CRD manifests ship inside the published Go module?

**Decision**: Yes. Read them from the resolved module directory rather than
building a generate-and-check-in pipeline.

**Verification**: Downloaded a published Cluster API module into a throwaway
module and listed it:

```
/root/go/pkg/mod/sigs.k8s.io/cluster-api@v1.11.0/config/crd/bases/
  cluster.x-k8s.io_clusters.yaml
  cluster.x-k8s.io_machinedeployments.yaml
  ... (all CRD bases present)
```

Go module zips include every file in the module tree except nested modules
and VCS directories, so `config/`, `hack/` and other non-Go assets travel
with the dependency. `go list -m -f '{{.Dir}}' sigs.k8s.io/cluster-api`
resolves that path at build or test time.

**Consequence — this contradicts a spec assumption.** FR-005 and FR-006
assume the manifests must be generated into this repository and kept in step
with a staleness check. If fixtures read them from the resolved module
directory instead, they are in step *by construction*: the files come from
the same pinned version the code compiles against, so there is nothing to
drift and nothing to check. That removes a generation step, a checked-in
artifact, and a staleness check — three things Principle VIII says not to
build without a need.

**What this does not remove**: a deployment needs real `APIResourceSchema`
and `APIExport` YAML to apply to a kcp installation. That is a genuine need,
but it is not this feature's need — nothing in the four user stories requires
deployable manifests — so it is deferred, with the trigger being the first
attempt to install this project into a cluster from published artifacts.

**Alternatives considered**: generate-and-check-in (rejected: builds a
staleness problem in order to solve it); vendor the CRDs by copying
(rejected: same drift problem, plus copies of upstream files in a repository
whose whole purpose is not to contain any).

**Caveat carried into planning**: the manifest path is version-dependent —
`config/crd/bases` in v1.11, `core/config/crd/bases` at this fork point. The
resolution must not hardcode a path that a future dependency bump silently
invalidates; failing loudly when the expected path is absent is required.

---

## R2 — What upstream ref is the fork branch based on?

**Decision**: Base the fork branch on commit `281e4e3`, not on a release
tag. Tag the fork `v1.15.0-kcp.1`.

**Verification**:

- Upstream `v1.14.0` exists: tag `4cfef8c`, dereferencing to `560d4ac`.
- This repository's fork point `281e4e3` is a merge commit on upstream
  `main` dated **2026-08-14** — i.e. *after* v1.14.0, not on it.
- The existing fork `jimmidyson/cluster-api` has a default branch at
  `d0c3bf8`, dated **2021-03-10**. It is five years stale and unusable as a
  base; the branch must be cut from a freshly fetched upstream ref.

**Rationale for the tag choice**: the code in this repository was written
against `281e4e3`, so basing the patch branch on `v1.14.0` would mean
building against a different tree than the one the code was developed and
tested on. A replace directive uses the replacement's version verbatim, so
ordering does not affect resolution — but a version below `v1.14.0` would
misrepresent content that is ahead of it. `v1.15.0-kcp.1` reads honestly as
"ahead of v1.14.0, not yet v1.15.0". The exact base commit goes in the drift
record, which is the authoritative statement.

**Alternatives considered**: a Go pseudo-version of the base commit
(rejected: unreadable in a manifest and gives the drift record nothing
human-checkable); `v1.14.0-kcp.1` (rejected: sorts as a v1.14.0 pre-release,
which is the opposite of what it contains).

---

## R3 — How should the fork expose the contract-metadata resolver?

**Decision**: Add a small public package in the fork that exposes a setter
for the resolver, delegating to the existing internal implementation. Do not
move or re-export the internal package wholesale.

**Context**: This repository imports exactly one upstream internal package —
`sigs.k8s.io/cluster-api/internal/contract` — at three sites: assigning
`GetGKMetadataFunc`, and calling `GetAPIVersion`. That import is legal today
only because this module's path sits beneath `sigs.k8s.io/cluster-api/`.
Every other upstream import already resolves to a public package.

**Rationale**: the surface actually needed is two symbols. A narrow public
seam is also the shape most likely to be accepted upstream, since it mirrors
`core/webhooks/conversion.SetAPIVersionGetter`, which already exists and
solves the same problem for a different call path. Re-exporting the whole
internal package would be a larger diff, a larger drift-record entry, and a
worse upstream proposal.

**Alternatives considered**: keeping the module path beneath
`sigs.k8s.io/cluster-api/` in a standalone repository so the internal import
stays legal (rejected: requires either a vanity import domain this project
does not control, or a permanent replace directive for every consumer, and
it preserves exactly the coupling this feature exists to remove); copying the
resolver's logic into this repository (rejected: a silent fork of upstream
behaviour with no drift record entry and no upstream path).

**Ordering consequence**: this is a hard prerequisite. Until the fork is
tagged with this package present, nothing in this repository compiles. It is
the first task, and it is not parallelisable with anything else.

---

## R4 — What is the verification time budget?

**Decision**: Fast subset budget **60 seconds warm**. Full verification
budget **to be set from the first CI measurement**, with 15 minutes as the
inherited ceiling until measured.

**Measured here** (this development container, Go 1.26, no Docker):

| Operation | Cold (empty module cache) | Warm |
|---|---|---|
| `go build ./...` | included below | **4 s** |
| `go test ./...` (unit) | **138 s** including downloads | **1 s** |

**Not measurable here**: the integration suite requires a container runtime,
and this environment has no Docker socket (`/var/run/docker.sock` absent). It
also requires pulling node images, which a previously-confirmed egress
restriction blocks. So the integration portion's duration cannot be
established in this environment and **must not be guessed**.

**Inherited ceiling**: the existing `kcp/Makefile` passes `-timeout=15m` to
the integration run. That is a timeout, not a measurement — it says the suite
is expected to take less than 15 minutes, nothing more.

**Consequence for planning**: NFR-001 requires a documented budget. The
budget for the full run must therefore be recorded from the first successful
CI run on a runner with Docker, not from this environment. Planning treats
"record the measured full-run duration" as an explicit task with a real
output, not as a documentation afterthought.

The fast-subset figure needs no such caveat: 1 s warm for unit tests, 4 s for
a build. A 60-second budget for the whole fast path leaves generous headroom
for linting and still sits inside an edit-run loop, satisfying NFR-002.

---

## R5 — Task runner feasibility and structure

**Decision**: go-task, installed via `go install`, single `Taskfile.yaml` to
begin with.

**Verification**: `go install github.com/go-task/task/v3/cmd/task@latest`
succeeds in this environment and yields **v3.52.0**. No system package
manager involved, satisfying the constitution's tooling constraint.

**Structure**: the reference project this approach is modelled on splits its
definitions across six included files. This project has 13 Go files and
perhaps a dozen operations. Principle VIII says the split happens when one
file becomes hard to navigate, not in anticipation — so a single file, with
the split recorded as deferred.

**Rejected — environment manager on the critical path**: verified that the
devbox installer host is unreachable from this environment (connection
fails), while the Nix binary caches are reachable. The devbox binary is
obtainable by `go install`, so the approach is not impossible — but the
containers this project's work is delegated to are discarded between runs, so
a Nix store would be materialised per task. That cost is paid on every
delegated unit of work. The reference project's own configuration also pins
all 24 of its packages to "latest", so it is not currently buying
reproducibility in exchange for that cost. Optional local convenience only,
never the critical path.

---

## R6 — Which existing workflows are inherited, and which are ours?

**Decision**: delete nine, keep and rewrite two.

**Verification**: enumerated `.github/workflows/` and cross-referenced
against the fork-point diff.

| Workflow | Origin | Disposition |
|---|---|---|
| `kcp-tests.yaml` | this project | rewrite to invoke task targets |
| `pr-kcp-docs.yaml` | this project | rewrite to invoke task targets |
| `pr-verify.yaml` | upstream, locally edited | delete; re-create PR-title check as our own |
| `pr-golangci-lint.yaml` | upstream, locally edited | delete; lint becomes a task target |
| `pr-dependabot.yaml` | upstream, locally edited | delete |
| `pr-md-link-check.yaml` | upstream, locally edited | delete |
| `weekly-md-link-check.yaml` | upstream, locally edited | delete |
| `weekly-security-scan.yaml` | upstream, locally edited | delete |
| `weekly-test-release.yaml` | upstream, locally edited | delete |
| `pr-gh-workflow-approve.yaml` | upstream, unmodified | delete |
| `release.yaml` | upstream, unmodified | delete |

Seven of these carry local edits against upstream — the drift this feature
exists to eliminate. Deleting them removes seven drift entries at a stroke,
which is a larger immediate reduction than the one carried code patch.

**Note**: `pr-verify.yaml` runs the PR-title check that Constitution
Principle VII depends on. That behaviour must survive the deletion, as our
own workflow, or the constitution's title rule becomes unenforced.

---

## Unresolved

Nothing blocking. One item is deliberately deferred to first CI run rather
than resolved here: the measured full-run verification duration (R4), because
this environment cannot produce it and inventing a number would violate the
principle the spec was written to enforce.
