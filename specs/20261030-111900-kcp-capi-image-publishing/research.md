# Research: KCP CAPI Image Publishing

**Feature**: `001-kcp-capi-image-publishing` | **Date**: 2026-10-30 | **Status**: Complete

**Decision**: Use `ko` (from `ko-build/ko`, installed via devbox rather than `go install`, so CI and local share the same tool without an extra `go install` step on the critical path). Each Go main package has a `kodata/`-less `ko` build. Image refs are `ko://github.com/jimmidyson/kcp-cluster-api/cmd/<name>` for root images and (after R6) the provider's module path.

Use `ko`'s default base image (currently Wolfi-based) — do not override it with `gcr.io/distroless/static:nonroot`. An explicit base is only set if a real need (CVE, policy, size regression) forces it later.
## R2 — Base image & build args

**Decision**: Use `ko`'s default Wolfi-based base image as-is. Go version is the one in `go.mod` (1.27.3) — `ko` reads it from the module's `go` directive rather than a Dockerfile `ARG builder_image`. Build metadata is passed via `KO_LD_FLAGS` or the project's existing `ldflags` that embed `gitCommitHash` and `version` (see Taskfile `ldflags` if one exists; otherwise `ko` defaults).

**Rationale**: `ko`'s default Wolfi base is minimal, regularly patched, and what `ko` itself tests against — overriding it to `gcr.io/distroless/static:nonroot` would add a divergence from both `ko`'s defaults and upstream CAPI for no concrete benefit today. If policy later requires distroless (e.g. image-scan policy at the target), override `defaultBaseImage` in `.ko.yaml` at that time.

**Alternatives considered**:
- `gcr.io/distroless/static:nonroot` — available as a `.ko.yaml:defaultBaseImage` override, but deferred (CAPI upstream used it for Dockerfiles, but the `ko` path here follows `ko`'s own Wolfi default; no security finding motivates keeping distroless today).
- `chainguard/static` — deferred; no existing pin.
- `alpine` — carries a shell, larger; contrary to practice.
## R3 — Versioning: release-please dual-package with `include-component-in-tag: false`

**Decision**: Keep `include-component-in-tag: false` (existing config) and declare both Go modules as packages:

```json
{
  "include-component-in-tag": false,
  "packages": {
    ".": { "package-name": "kcp-cluster-api" },
    "providers/nutanix-infrastructure": { "package-name": "nutanix-infrastructure" }
  }
}
```

Release-please then creates `vX.Y.Z` and `providers/nutanix-infrastructure/vX.Y.Z` at the same commit. The `.release-please-manifest.json` gains a second key.

**Rationale**:
- Verified against release-please's `release-type: go` behaviour: with `include-component-in-tag: false`, the tag for a sub-directory package is `path/vX.Y.Z` when the package key contains a `/`; with `true` it would be `component-vX.Y.Z`, which breaks `go get`'s `path@vX.Y.Z` resolution. The project relies on `go get .../providers/nutanix-infrastructure@vX.Y.Z`, so `false` is required.
- The existing `bootstrap-sha`, `initial-version`, and section mappings are unchanged.

**Alternatives considered**:
- Independent versioning per provider — rejected: FR-010 requires the two modules to stay in lockstep because the provider's `replace kcp-cluster-api => ../..` pins it to the root at the same commit; independent patch drift would let consumers resolve incompatible pairs.
- Single-package with manual second-tag creation — rejected: would split versioning logic between release-please and a shell script, duplicating state already tracked in the manifest.

## R4 — OCI tag scheme and mechanical derivation

**Decision**: Three aliases per image per release:
- `ghcr.io/jimmidyson/kcp-capi-<name>:vX.Y.Z` — repo version, the deployment-stable pin.
- `ghcr.io/jimmidyson/kcp-capi-<name>:vX.Y.Z-capi-<capi-tag>` — human-readable provenance. For provider images: `vX.Y.Z-capx-<capx-tag>-capi-<capi-tag>`. Built by replacing `+` with `-` in the semver (OCI forbids `+`).
- `ghcr.io/jimmidyson/kcp-capi-<name>:<fork-tag>` — upstream alias (`v1.15.0-kcp.13` for root images, `v1.11.0-kcp.3` for Nutanix). Lets CAPI trackers pin the fork version directly.

Plus on every `main` push: `:main` and `:sha-<short>` (first 7 of `GITHUB_SHA`).

Tag strings are mechanically derived at build time from `go list -m -json sigs.k8s.io/cluster-api` (root) and `go list -m -json github.com/nutanix-cloud-native/cluster-api-provider-nutanix` (provider module's go list invocation from `providers/nutanix-infrastructure`). No hard-coded version in `Taskfile.yaml` or workflow YAML.

**Implementation**: a small `scripts/image-tags.sh` invoked both locally and in GHA. It:
1. Reads `REPO_VERSION` from `git describe --tags --abbrev=0` or `GITHUB_REF_NAME` (release event).
2. Runs `go list -m -json` for each relevant module import path, extracting `.Version` via `jq` or `go`'s own `-f` (prefers `jq` fallback to `python3 -c json`, matching `.specify/scripts/bash/common.sh` conventions).
3. Validates each derived OCI tag against `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`, failing if too long (covers future unusually long provider fork tags).
4. Emits GHA `outputs` when `GITHUB_OUTPUT` is set; otherwise prints for local use.

Labels follow the same source: `io.kcp-capi.capi-version`, `io.kcp-capi.capi-base` (base commit from `DRIFT.md`'s `Base:` line), `io.kcp-capi.k8s-version` (`k8s.io/api`), plus provider-specific `io.kcp-capi.capx-version`. Also standard `org.opencontainers.image.version` (= `vX.Y.Z`) and `org.opencontainers.image.revision` (= full SHA).

**Alternatives considered**:
- Encoding k8s version in the tag as well (`-k8s-v0.37.1`) — rejected: tag would grow and k8s version is already in labels; adding it to the tag added no deployment value.
- Putting `+<build>` metadata in OCI tags — impossible: OCI tag charset forbids `+`.
- Deriving from `go.mod` text parse — rejected: fragile; `go list -m -json` is the canonical, replace-aware source.

## R5 — Local build vs CI publish with ko

**Decision**: Two distinct entry points sharing the same tag-derivation script:
- Local: `task image:build` runs `ko build` with `KO_DOCKER_REPO=ko.local` (ko's local convention, already used in CAPX), producing `ko.local/kcp-capi-*:<tag>` loadable via `kind load docker-image` or `docker tag` into kind.
- CI: the `publish-images.yaml` workflow on `release` (tag push) or `main` sets `KO_DOCKER_REPO=ghcr.io/jimmidyson`, logs in via `docker/login-action@v3`, and calls `ko build --bare --push` (or a `task image:publish` wrapper) with the full 3-alias tag matrix plus `:main`/`:sha-*` on `main`.

**Rationale**: ko's `KO_DOCKER_REPO` switch is exactly the local-vs-registry split the project already needs (FR-008). Contributors need no GHCR login to build.

**Alternatives considered**:
- Always pushing on PR for verification — rejected: expensive, leaks unreviewed images, and GHCR per-PR retention is unclear.

## R6 — Provider extensibility without tag-generation changes

**Decision**: The tag-generation script enumerates images from a static table keyed by binary name and module location, not by scanning `go.mod` for provider names. Adding a provider means:
1. `providers/<name>` Go module (with `replace kcp-cluster-api => ../..`, restated CAPI fork replaces, `cmd/<name>-manager`, `DRIFT.md`)
2. One entry in `Taskfile.yaml:PROVIDER_MODULES`
3. One entry in `release-please-config.json:packages`
4. One row in `scripts/image-tags.sh`'s image table (mapping `kcp-capi-<name>` → `go list -m` invocation for that provider's upstream import path)
5. A `ko` build for the new module: if ko's `ko://<importpath>` resolution from the root works for nested-module imports (to be verified during implementation — a spike builds a single provider image first), reuse the root `.ko.yaml` and add an entry to its `builds` list; if not, add a minimal `providers/<name>/.ko.yaml` and teach `Taskfile` to invoke `ko build` once per provider dir.

Step 4 is intentionally explicit — auto-discovery would mis-identify helpers vs providers. The spec's "no tag-generation change" means "no redesign", not "zero-line change".

**Alternatives considered**:
- Single-root Dockerfile that builds all provider images — rejected; ko avoids this entirely.
- One combined `KO_DOCKER_REPO` for all providers regardless of module — deferred pending spike.

## R7 — ko specifics that will be nailed during implementation

These follow from R1 and are deferred to the implementation tasks, not to research:
- Exact `ko` version pin in `devbox.json` (latest stable at implementation time, e.g. `ko@latest` then pinned to `ko@0.18` once verified).
- The `.ko.yaml` (or `ko` config block) that maps `ko://github.com/jimmidyson/kcp-cluster-api/cmd/<name>` → `ghcr.io/jimmidyson/kcp-capi-<name>` image names.
- Whether `ko` build flags need `--platform=linux/amd64,linux/arm64` (yes for release) and `--local` vs `--push` split between local and CI.
- Label injection: `ko` already sets `org.opencontainers.image.*` from `KO_DATA_PATH` plus custom labels via `--image-label`; `io.kcp-capi.*` labels are passed as `--image-label io.kcp-capi.capi-version=...` per image.
