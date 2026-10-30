# Implementation Plan: KCP CAPI Image Publishing

**Branch**: `20261030-111900-kcp-capi-image-publishing` | **Date**: 2026-10-30 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `specs/20261030-111900-kcp-capi-image-publishing/spec.md`

## Summary

Publish the six existing manager binaries as runnable OCI images under `ghcr.io/jimmidyson/kcp-capi-*` so a KCP-hosted installation can be deployed on Kubernetes. Each image must also be traceable to its upstream fork (CAPI `v1.15.0-kcp.13`, CAPX `v1.11.0-kcp.3`) via mechanical OCI tags/labels derived from `go list -m`. The two Go modules (root + `providers/nutanix-infrastructure`) are versioned together via `release-please`, and any future provider is added by editing a named provider registry, not by changing tag generation code. Image builds use `ko` (installed via devbox) with its default Wolfi base image.

## Technical Context

**Language/Version**: Go 1.27.3 (from `go.mod`; `providers/nutanix-infrastructure/go.mod` same). No new language.

**Primary Dependencies**: `sigs.k8s.io/cluster-api` (pinned to `jimmidyson/cluster-api v1.15.0-kcp.13` via replace), `github.com/nutanix-cloud-native/cluster-api-provider-nutanix` (pinned to `jimmidyson/cluster-api-provider-nutanix v1.11.0-kcp.3` in the provider module), `k8s.io/api` v0.37.1, `sigs.k8s.io/controller-runtime` v0.25.2, `github.com/kcp-dev/sdk` v0.33.1, `go-task/task` v3.52.0 for the command surface, `release-please` v17.11.1 (via `release-please-action` v5.0) for versioning.

**New Dependencies**: `ko` (ko-build/ko, installed via `devbox.json`; version pinned at implementation time). No new runtime deps. No `Dockerfile` is added — `ko` is the builder, using its default Wolfi base image (not overridden to distroless).

**Storage**: N/A (images are build artifacts; registry is GHCR, not a data store).

**Testing**: Go `go test ./...` for unit tests; `task verify` (build + vet + unit + integration against real kcp) remains the done condition. New image-building tasks are verified by `task image:build` succeeding (ko builds locally) and by label/tag assertions (a `task image:verify` that inspects built images without pushing).

**Target Platform**: `linux/amd64` and `linux/arm64` via `ko --platform=linux/amd64,linux/arm64`. Local dev on `linux/amd64` or `darwin/arm64` via cross-build / `ko --local`.

**Project Type**: Multi-module Go project producing controller container images (ko-built); new provider modules follow the existing `providers/<name>` pattern.

**Performance Goals**: `task image:build` for all 6 images completes in <10 min on a warm GH runner. Each image <150 MB compressed (ko Wolfi base).

**Constraints**:
- Tooling installed via devbox at pinned versions (Constitution: Environment constraints). `ko` is added to `devbox.json`; Docker and buildx are provided by GH runners but not needed as a separate install path.
- OCI tag character set: `[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}` — no `+` from Go semver metadata. Long tags replace `+` with `-`.
- `include-component-in-tag: false` with `path/vX.Y.Z` Go tags so `go get` continues to resolve both modules.
- `bootstrap-sha`, `initial-version: 0.1.0`, `bump-minor-pre-major: true` are invariant (FR-010).

**Scale/Scope**: 6 images (5 root-module, 1 provider-module). 2 Go modules versioned together. 1 GH publish workflow + `ko` config + task additions + release-please config change. Future providers add 1 image each with no code change to tag generation.

## Constitution Check

*GATE: evaluated before Phase 0, re-evaluated after Phase 1.*

| Principle | Assessment |
|-----------|------------|
| I — Divergence counted & temporary | No upstream code is changed. `.ko.yaml`/devbox addition are project build config, not fork patches. `DRIFT.md` is untouched. No new divergence is introduced. |
| II — Public extension points | Not applicable — no integration with upstream internals. Image publishing is project-owned build infra. |
| III — Test-first against real kcp | New behaviour is build/release infra, not reconciler logic. Tests for this feature are: `task image:build` + label/tag assertions (verifiable without kcp), and the existing integration suite continuing to pass (`task verify`). No weakening of existing kcp-backed tests. |
| IV — Done is a command | Each new task has a command form: `task image:build`, `task image:verify`, `task drift` (unchanged), `task verify`. No prose-only acceptance. |
| V — Verify dependencies against source | `ko` + devbox installation, `.ko.yaml` `builds` mapping, and `KO_DOCKER_REPO`/`--platform` flags must be verified against `ko`'s actual behaviour during implementation (see research R1/R7). Release-please dual-package with `include-component-in-tag: false` was checked against the release-please schema and the project's existing single-package config (research R3). |
| VI — Documentation ships with change | Docs PR requirement is deferred to implementation — tasks will include updating `README.md`/design doc to describe the new `task image:*` commands and GHCR locations. Spec itself satisfies the "what and why" requirement. |
| VII — History & review discipline | No violation. New PR(s) will follow Conventional Commits and squash merge. |
| VIII — YAGNI, except at seams | The tagging scheme explicitly covers *exactly* the providers that exist today (1 root fork + 1 provider fork). The "add a provider = 4 edits" contract is documented as a seam (FR-009), not built ahead. No speculative providers, signing, or SBOM work is included. |
| IX — Scalability claims | No per-workspace cost claim is altered. Image addition is orthogonal. |

**Post-Design Re-check** (after research.md, data-model.md, quickstart.md, contracts/): no gates changed. `ko` strengthens Principle V (importpath→image mapping verified in implementation spike, R7). Seam note in Principle VIII now names the `.ko.yaml` entry as part of the 4-edit provider-addition contract. All nine principles still pass.

**Result**: no unjustified violations. Proceed.

## Project Structure

### Documentation (this feature)

```text
specs/20261030-111900-kcp-capi-image-publishing/
├── plan.md              # This file
├── research.md          # Phase 0 — decisions & alternatives
├── data-model.md        # Phase 1 — entities, fields, relationships
├── quickstart.md        # Phase 1 — runnable validation guide
├── contracts/           # Phase 1 — image contract & tag schema
│   ├── image-contract.md
│   └── tag-schema.md
├── checklists/
│   └── requirements.md  # Spec quality checklist (already present)
└── tasks.md             # Phase 2 output (/speckit-tasks)
```

### Source Code (repository root)

Concrete layout changes. No new directories beyond `ko` config; no deletion.

```text
.ko.yaml                           # NEW — ko build config (maps ko://… import paths → ghcr.io/jimmidyson/kcp-capi-*)

providers/nutanix-infrastructure/
├── go.mod / go.sum                # existing (with replace kcp-cluster-api => ../..)
├── DRIFT.md                        # existing
├── ko.yaml  or .ko.yaml           # NEW — only if a separate ko config is needed for the nested module (R6); otherwise omitted
└── cmd/nutanix-infrastructure-manager/

cmd/
├── core-manager/                  # existing — ko://…/cmd/core-manager → ghcr.io/jimmidyson/kcp-capi-core
├── kubeadm-bootstrap-manager/     # existing — kcp-capi-kubeadm-bootstrap
├── kubeadm-control-plane-manager/ # existing — kcp-capi-kubeadm-control-plane
├── workspace-manager/             # existing — kcp-capi-workspace
├── dev-infrastructure-manager/    # existing — kcp-capi-dev-infrastructure
└── ...

devbox.json                        # MODIFIED — add ko (e.g. "ko@latest" → pinned, see R7)
Taskfile.yaml                      # MODIFIED — new `image:build`, `image:verify` tasks, PROVIDER_MODULES reused

release-please-config.json         # MODIFIED — second package: providers/nutanix-infrastructure
.release-please-manifest.json      # MODIFIED — second entry: "providers/nutanix-infrastructure": "0.1.0"

.github/workflows/
├── pr.yaml                         # UNCHANGED (task verify)
├── release-please.yaml             # UNCHANGED (still just opens/updates the release PR)
└── publish-images.yaml             # NEW — ko builds & pushes kcp-capi-* on release tag / main

scripts/
└── image-tags.sh                  # NEW — derives OCI tags from `go list -m` (FR-005), emits GHA outputs

# PROVIDER REGISTRY (convention, not code):
# Future providers are added under providers/<name>/ with their own go.mod, DRIFT.md,
# then registered in Taskfile.yaml:PROVIDER_MODULES, release-please-config.json:packages,
# and scripts/image-tags.sh's image table (+ .ko.yaml entry if a separate ko config is needed).
# No other file needs editing.

AGENTS.md                          # MODIFIED — document new task targets in the named-operations table
```

**Structure Decision**: keep the existing single-root + nested-provider-modules layout. ko is configured at the root `.ko.yaml` where possible; a per-provider ko config is added only if ko's importpath resolution requires it (R6 spike decides). Task iteration reuses `PROVIDER_MODULES` exactly as `build`/`lint`/`test:unit`/`drift` already do.

## Complexity Tracking

> No Constitution Check violations to justify. Leave empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| — | — | — |
