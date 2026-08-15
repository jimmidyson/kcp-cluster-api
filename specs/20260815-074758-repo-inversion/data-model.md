# Phase 1 Data Model: Standalone repository, task runner, and CI

**Feature**: [spec.md](./spec.md) | **Date**: 2026-08-15

This feature introduces no runtime data. The entities below are repository
artifacts — files with defined contents, owners, and rules about when they
change. They are modelled here because getting their relationships wrong is
how the current arrangement drifted.

---

## Module

The single unit of code this repository publishes.

| Field | Value |
|---|---|
| Path | `github.com/jimmidyson/kcp-cluster-api` |
| Location | repository root |
| Dependencies | Cluster API and its `api` / `test` submodules, pinned and replaced to the fork |

**Rules**

- MUST NOT import any package upstream marks internal (FR-004).
- MUST NOT read from a co-located upstream source tree; the resolved module
  directory of a pinned dependency is not a source tree and is permitted
  (FR-005, amended by research R1).
- Contains no file that exists in upstream Cluster API (FR-001).

**Relationships**: depends on → Fork Release. Described by → Drift Record.

---

## Fork Release

An immutable tagged version of `github.com/jimmidyson/cluster-api` that this
module resolves Cluster API to.

| Field | Value |
|---|---|
| Base commit | `281e4e3` (upstream `main`, 2026-08-14, after `v1.14.0`) |
| Branch | one per upstream release line |
| Tag | `v1.15.0-kcp.1` initially |
| Contents | upstream at base commit, plus carried patches |

**Rules**

- MUST be an immutable tag. A branch is not a valid pin (FR-003).
- One commit per carried patch; each commit message references its upstream
  proposal.
- Carries no specifications, tooling, workflows or process of its own — it is
  a patch carrier, nothing else.
- MUST NOT be branched from the fork's own default branch, which is five
  years stale (research R2).

**Relationships**: contains → Carried Patch (0..n). Consumed by → Module.

---

## Carried Patch

A single change to Cluster API that this project needs and intends to remove.

| Field | Description |
|---|---|
| Path | the upstream file it modifies |
| Rationale | why the project cannot proceed without it |
| Upstream proposal | reference to the proposal that will make it unnecessary |

**State transitions**

```
proposed → carried → accepted upstream → deleted
                  ↘ rejected upstream → re-justified or removed
```

`carried` is not a terminal state. A patch with no open upstream proposal is
a defect (Constitution I), not a stable configuration.

**Initial population**: exactly one — the public contract-metadata resolver
seam (research R3).

---

## Drift Record

Checked-in file (`DRIFT.md`) that is the authoritative statement of permitted
divergence.

| Field | Description |
|---|---|
| Base | the upstream commit the fork branch is cut from |
| Entries | one per carried patch: path, rationale, upstream proposal (prose) |

**Rules**

- The drift check compares the fork's actual differences against this file
  and fails on any unexpected path (FR-016, FR-017).
- A new patch MUST be justified here before it is accepted.
- Begins with one entry. Machine-readable proposal references and
  bidirectional checking are deferred until the record grows — see the spec's
  Deferred section.

**Relationships**: describes → Fork Release, Carried Patch.

---

## Named Operation

A single invocable unit of work, defined once and used identically by
contributors and automation. Full contract:
[contracts/task-surface.md](./contracts/task-surface.md).

| Field | Description |
|---|---|
| Name | stable identifier invoked as `task <name>` |
| Capability requirements | what the environment must provide |
| Outcome | pass / fail / could-not-run |

**Rules**

- `verify` is the composition of the others, never a reimplementation.
- Every operation reports one of three outcomes; a skipped step never yields
  success (FR-011, FR-012).
- CI invokes these by name and holds no logic of its own (FR-014).

---

## Workflow

An automated check. Two survive this feature; nine are deleted.

**Rules**

- Body is an invocation of a Named Operation plus checkout, toolchain setup
  and reporting (FR-014).
- No workflow inherited from upstream may remain (FR-015).
- The PR-title check that Constitution VII depends on MUST be re-created as
  ours when the inherited workflow carrying it is deleted (research R6).

---

## Entity relationships

```
Drift Record ──describes──► Fork Release ──contains──► Carried Patch
                                 ▲                          │
                                 │ pinned by                 │ removed by
                                 │                           ▼
                              Module ◄──verified by── Named Operation
                                                            ▲
                                                            │ invokes
                                                        Workflow
```
