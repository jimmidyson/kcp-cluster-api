# Feature Specification: KCP CAPI Image Publishing

**Feature Branch**: `001-kcp-capi-image-publishing`

**Created**: 2026-10-30

**Status**: Draft

**Input**: User description: "Publish 6 kcp-capi container images with upstream-aware tags and extensible provider support for Kubernetes deployment"

## Purpose

This repository builds six manager binaries — core, kubeadm bootstrap, kubeadm control-plane, Nutanix infrastructure, workspace onboarding, and dev infrastructure — but publishes none of them as runnable container images. Users who want to "run everything on Kubernetes" cannot: the binaries exist only as local `go build` outputs, and the only published images are the upstream CAPI/CAPX ones, which lack the KCP workspace-aware wiring (2 goroutines/workspace, fleet-wide caching, credential isolation).

The project also needs a versioning story that makes the relationship between its own releases and the upstream forks legible: a reader seeing `kcp-capi-core:v0.x` should be able to tell which CAPI fork version (`v1.15.0-kcp.13`) and which k8s version it was built from, and a future Nutanix or new-provider image should be able to carry its own upstream version without diverging.

## Out of Scope

- **Fork image publishing**: The patched forks (`jimmidyson/cluster-api`, `jimmidyson/cluster-api-provider-nutanix`) are patch-carriers only (`AGENTS.md#patched-fork` contract: `git diff upstream..kcp/*` means something). They do not publish images themselves; all KCP images are built from this repository's manager entrypoints, which import the forks via `replace` pins. Building fleet lifecycle code into every fork would duplicate `internal/providerwiring`.
- **Helm charts / kustomize overlays for deployment**: How the images are installed on a kcp host cluster (clusterctl is P5/G3, still gated). This feature makes the images exist and taggable; rendering manifests to deploy them is a separate concern.
- **GHCR namespace other than `ghcr.io/jimmidyson`**: The registry location is fixed; moving to an org namespace is a follow-up.
- **Sigstore / cosign attestation**: Signing images is follow-up.
- **Multi-arch strategy beyond what ko already does**: ko handles `linux/amd64,linux/arm64` by default; adding `linux/arm` is out of scope unless the current build opts demand it.

## User Scenarios & Testing

### User Story 1 — Deploy a KCP-aware CAPI stack on Kubernetes (Priority: P1)

A platform operator wants to run the full Cluster API stack on a Kubernetes-hosted kcp installation (not locally via `task demo` with an embedded kcp). They pull and deploy the published images.

**Why this priority**: This is the stated goal — "run everything now on Kubernetes." Without it, nothing else ships. Each image must be a drop-in for its upstream equivalent (same flags plus `--endpoint-slice-name` for the KCP provider), and the six together must produce ready clusters in multiple workspaces as `task demo` does locally.

**Independent Test**: Can be fully tested by pulling the 6 images from GHCR, applying their sample manifests against a kcp-backed cluster with the matching APIExports published (via `capiexports.Publish`), creating a ClusterClass and a Cluster in two workspaces, and asserting both reach `Available`.

**Acceptance Scenarios**:

1. **Given** a kcp host cluster with APIExports published for all 6 providers, **When** the operator deploys the 6 `kcp-capi-*` images (with `--kubeconfig` pointing at the shard), **Then** each Deployment becomes `Ready` and starts reconciling.
2. **Given** the stack is running, **When** two tenants each create a ClusterClass-based Cluster in their own workspace, **Then** both Clusters reach `Available` and their Machines are provisioned, with no cross-workspace bleed (different CAPI categories, no shared Prism caches or fallback credentials for the Nutanix case).
3. **Given** an image tag `vX.Y.Z`, **When** the operator inspects `docker inspect --format '{{index .Config.Labels "io.kcp-capi.capi-version"}}'`, **Then** they see the pinned CAPI fork version (e.g. `v1.15.0-kcp.13`) and can correlate it with `go.mod`'s `replace sigs.k8s.io/cluster-api`.

---

### User Story 2 — Trace an image back to its upstream source (Priority: P1)

An operator or contributor needs to know "which upstream CAPI is this KCP image a fork of?" from the image tag alone (for filing bugs, for deciding whether an upstream fix is already in).

**Why this priority**: The project carries ~49 drift paths against CAPI (`DRIFT.md`), and the fork itself carries `vX.Y.Z-kcp.N` tags. Without a visible link, triage ("is this upstream's bug or ours?") requires digging through `go.mod` pins.

**Independent Test**: `docker pull ghcr.io/jimmidyson/kcp-capi-core:v0.2.0` and check OCI tags and OCI labels; all three must be consistent with `go list -m sigs.k8s.io/cluster-api`.

**Acceptance Scenarios**:

1. **Given** a release `v0.2.0` built while `go.mod` pins `sigs.k8s.io/cluster-api => jimmidyson/cluster-api v1.15.0-kcp.13`, **When** CI publishes `kcp-capi-core`, **Then** it pushes three aliases for the same digest: `:v0.2.0`, `:v0.2.0-capi-v1.15.0-kcp.13`, and `:v1.15.0-kcp.13`, with OCI tag length &lt;128 and no `+`.
2. **Given** a Nutanix image built from `providers/nutanix-infrastructure` pinning CAPX `v1.11.0-kcp.3`, **When** published, **Then** its OCI tags include `:v1.11.0-kcp.3` and `:v0.2.0-capx-v1.11.0-kcp.3-capi-v1.15.0-kcp.13`, and its labels carry `io.kcp-capi.capx-version`, `io.kcp-capi.capi-version`, `io.kcp-capi.k8s-version`, and `io.kcp-capi.capi-base`.
3. **Given** no fork bump between two repo releases, **When** comparing OCI labels across them, **Then** `io.kcp-capi.capi-version` is identical and `v1.15.0-kcp.13` still aliases the newest digest (so CAPI trackers can pin the fork version directly).

---

### User Story 3 — Add a new infrastructure provider without breaking the release (Priority: P2)

A contributor adds a second infrastructure provider (e.g. CAPA) alongside the existing Nutanix one, following ADR-0004's "one directory per provider" model.

**Why this priority**: The whole point of the nested-module split (`providers/<name>/`, `Taskfile:PROVIDER_MODULES`, per-provider `DRIFT.md`) is that the second provider is a known upcoming cost. If adding it requires hand-editing tag generation or release-please in divergent ways, the model has failed.

**Independent Test**: Add a fixture provider under `providers/example/` with a trivial `cmd/example-manager`, register it in `PROVIDER_MODULES` and `release-please-config.json:packages`, and run `task build && task release-please -- --dry-run` — both must include it without other changes.

**Acceptance Scenarios**:

1. **Given** a new `providers/aws/` module (with `go.mod` `module …/providers/aws`, `replace kcp-cluster-api => ../..`, and pins restating the CAPI fork), **When** the contributor adds `providers/aws` to `PROVIDER_MODULES` and `release-please-config.json:packages`, **Then** `task build`/`task lint`/`task test:unit` all iterate it, `task drift` checks `providers/aws/DRIFT.md`, and GHCR publish pushes `ghcr.io/jimmidyson/kcp-capi-aws` with the per-provider tag shape.
2. **Given** the new provider pins a different upstream fork version (e.g. CAPA `v2.1.0-kcp.1`), **When** published, **Then** its image's tag derivation is driven mechanically from its own `go.mod` pins (not hand-typed), and the root images' tags are unchanged.

---

### User Story 4 — Run locally-built images for development and CI (Priority: P2)

A contributor needs to build and run the same images locally (kind, `task demo`-like flows, `test/e2e` with the docker backend) without pushing to GHCR.

**Why this priority**: Images are not CI-only artifacts — they are how the project dogfoods its own workspace wiring.

**Independent Test**: `task image:build` produces locally loadable images; `kind load docker-image` + sample manifests produce the same ready-cluster result as the published path.

**Acceptance Scenarios**:

1. **Given** no GHCR credentials, **When** the contributor runs `task image:build`, **Then** all 6 images are built locally (ko or docker buildx) and listed by `docker images` under a `kcp-capi-*` name with a `local` or `sha-<short>` tag.
2. **Given** a local build, **When** loaded into a kind cluster that hosts kcp, **Then** the manifests reference the local image digest/name and reconcile as the published ones do.

---

### Edge Cases

- A release where only a nested provider's fork moved (e.g. CAPX `kcp.3` → `kcp.4`) but the root's CAPI pin did not: the release still bumps `v0.Y.Z` and publishes all 6 images (so installers can pin one version), with per-provider long tags differing while the root images' long tags are identical to prior.
- `providers/nutanix-infrastructure/go.mod`'s `replace kcp-cluster-api => ../..` means its Go tag `providers/nutanix-infrastructure/vX.Y.Z` and the root tag `vX.Y.Z` must be created at the same commit; building from a commit that has one but not the other must fail loudly, not resolve a stale dependency.
- OCI tag character set forbids `+` (Go build metadata) — long tags use `-` (`v0.2.0-capi-v1.15.0-kcp.13`), labels keep full semver with `+` where needed.
- `go build ./...` deliberately skips nested modules; any task that omits `for: PROVIDER_MODULES` regresses (a provider left out never compiles in CI) — captured by a check.
- ko vs Dockerfile: the project currently uses `ko` for publishing but has `ko.local` conventions from CAPX; this spec does not prescribe which, but whatever is chosen must work for both root and nested modules (ko's default `KO_DOCKER_REPO`/`importpath` resolution must be verified for `providers/`).

## Requirements

### Functional Requirements

- **FR-001**: The project MUST publish six container images, one per manager binary: `kcp-capi-core`, `kcp-capi-kubeadm-bootstrap`, `kcp-capi-kubeadm-control-plane`, `kcp-capi-nutanix-infrastructure`, `kcp-capi-workspace`, and `kcp-capi-dev-infrastructure`.
- **FR-002**: Image names MUST use the `ghcr.io/jimmidyson/kcp-capi-` prefix, flat (hyphen-separated, no path nesting under the prefix).
- **FR-003**: Each release-published image MUST carry three OCI tag aliases for the same digest: (a) the repo version `vX.Y.Z`, (b) a human-readable long tag encoding the upstream fork version(s) (e.g. `vX.Y.Z-capi-v1.15.0-kcp.13`; for provider images including their own upstream, e.g. `vX.Y.Z-capx-v1.11.0-kcp.3-capi-v1.15.0-kcp.13`), and (c) the fork version itself (`v1.15.0-kcp.13` for root images, `v1.11.0-kcp.3` for the Nutanix image). The `main` and per-commit `sha-<short>` tags MUST also be published on `main` branch pushes.
- **FR-004**: Every image MUST include OCI labels exposing `org.opencontainers.image.version`, `org.opencontainers.image.revision`, `io.kcp-capi.capi-version`, `io.kcp-capi.k8s-version`, `io.kcp-capi.capi-base`, and for provider images additionally `io.kcp-capi.capx-version` (or equivalent per provider).
- **FR-005**: Image tag derivation MUST be mechanical, reading fork versions from the `go list -m` output of the relevant `go.mod` at build time, never hand-typed in workflow YAML or shell.
- **FR-006**: The two Go modules in this repository (root and `providers/nutanix-infrastructure`) MUST be versioned together via `release-please`, producing two Git tags at the same commit: `vX.Y.Z` (root) and `providers/nutanix-infrastructure/vX.Y.Z` (nested). The release-please config MUST declare both packages with `include-component-in-tag: false`.
- **FR-007**: The build and verify tasks (`task build`, `task lint`, `task test:unit`, `task drift`) MUST continue to iterate `PROVIDER_MODULES`; any new provider under `providers/` MUST be added to that list to become CI-visible, and the CI check MUST fail if a nested module exists outside the list.
- **FR-008**: A local image build (`task image:build`) MUST work without GHCR credentials, producing images loadable into a kind cluster; a CI publish (`task image:publish` / GH workflow on `release` event) MUST push to `ghcr.io` with the three-alias tag set.
- **FR-009**: Adding a new provider MUST require only: (i) a new `providers/<name>/` directory with its own `go.mod` (`replace kcp-cluster-api => ../..`, restated CAPI fork pins), (ii) one `PROVIDER_MODULES` entry, (iii) one `release-please-config.json:packages` entry, and (iv) a new `DRIFT.md`. No other code or tag-generation changes.
- **FR-010**: Existing versioning semantics MUST be preserved: `bootstrap-sha` stays `281e4e3ed2af1d6852651d69e1207a3073b478c2`, `initial-version` stays `0.1.0`, `bump-minor-pre-major` stays true, and the first `v0.1.0` release flow is unchanged; the existing single-component release history (`v0.1.0` tag) MUST remain resolvable (nested module consumers at `providers/nutanix-infrastructure/v0.1.0` if the tag exists, or via `replace` during the transition).

### Key Entities

- **Image**: A publishable OCI artifact for one manager binary; attributes: `name` (`ghcr.io/jimmidyson/kcp-capi-*`), `gitTag` (`vX.Y.Z` / `providers/*/vX.Y.Z`), `ociTags[]` (repo, long, fork-alias, branch, sha), `labels{}` (fork/k8s versions, base commit), `digest`.
- **Release**: A synchronized publish of all images at one commit; attributes: `repoVersion`, `providerVersions{}`, `commit`, `ociTagMatrix` (per-image alias sets), `changelog`.
- **Provider module**: A nested Go module under `providers/<name>/` with its own `go.mod`/`DRIFT.md`/`cmd/<name>-manager`; relationship: imported by the root only via `replace => ../..` for wiring, built independently for its image.

## Success Criteria

### Measurable Outcomes

- **SC-001**: Pulling and deploying the six `ghcr.io/jimmidyson/kcp-capi-*` images at a released `v0.x` produces ready Clusters in two isolated workspaces (as `task demo` asserts locally) — verified by a smoke run against a kcp-hosted cluster on each release.
- **SC-002**: Every published image's OCI tags and labels are consistent: `go list -m sigs.k8s.io/cluster-api` in the release commit equals the `io.kcp-capi.capi-version` label, and the long-tag suffix (`-capi-v1.15.0-kcp.13`) is exactly that label with `+` replaced by `-`.
- **SC-003**: Adding a new provider requires ≤4 documented edits (directory, PROVIDER_MODULES, release-please package, DRIFT.md) and no change to tag-generation logic — demonstrated by a fixture provider build in CI.
- **SC-004**: `task image:build` completes without credentials and the resulting local images run correctly in a kind cluster (same manifests as the published path, differing only by image reference).
- **SC-005**: Existing Git tags (`v0.1.0`, `bootstrap-sha`) remain fetchable and `go get` for both modules continues to resolve; `task drift` passes before and after the change.

## Assumptions

- Registry is `ghcr.io/jimmidyson` (GHCR, personal fork namespace); moving to an org namespace is future work.
- Image builder is `ko` (existing upstream pattern) with `KO_DOCKER_REPO=ghcr.io/jimmidyson`; if a `Dockerfile` per manager proves simpler for the nested module, either is acceptable as long as local and CI builds use the same path.
- The dev-infrastructure image is intentionally included; the user explicitly wants it regardless of prior "dev/e2e only" categorization.
- OCI tag length limit is 128; the long-tag shape (`v0.2.0-capi-v1.15.0-kcp.13`, longest per-provider case `v0.2.0-capx-v1.11.0-kcp.3-capi-v1.15.0-kcp.13`) stays well under it; any new provider with an unusually long fork tag will need its short name kept compact.
- `include-component-in-tag: false` is intentional: it produces the `path/vX.Y.Z` Go-semver tags (`providers/nutanix-infrastructure/v0.2.0`) required by `go get` while keeping the manifest key readable.
- No image signing, SBOM, or Helm chart work is in scope; they follow separately after images exist.
