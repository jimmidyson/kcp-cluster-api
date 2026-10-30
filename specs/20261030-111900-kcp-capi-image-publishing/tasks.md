# Tasks: KCP CAPI Image Publishing

## Requirement Traceability

| Requirement | Tasks | Notes |
|-------------|-------|-------|
| FR-001 (6 images) | T002, T003, T007, T015 | Root + provider image definitions, build + publish |
| FR-002 (naming `ghcr.io/jimmidyson/kcp-capi-*` flat) | T002, T003 | `.ko.yaml` image names enforce prefix |
| FR-003 (3 OCI tag aliases + `main`/`sha-*`) | T004, T009, T013, T015, T017, T018 | Tag derivation + publish workflow |
| FR-004 (OCI labels) | T008, T014, T019 | Label injection + verification |
| FR-005 (mechanical derivation via `go list -m`) | T004, T009 | Tag script reads from go module graph, no literals |
| FR-006 (dual-package versioning) | T005, T006, T011, T027 | release-please config + manifest + dry-run check |
| FR-007 (PROVIDER_MODULES iteration + fail-outside) | T007, T010, T023, T024 | Build/lint/drift + image tasks all iterate |
| FR-008 (local without GHCR, CI push) | T007, T015, T029, T031 | `ko.local` local path vs `publish-images.yaml` CI path |
| FR-009 (add provider = 4 edits) | T023, T025, T026, T027 | Extensibility seam + fixture test T028 |
| FR-010 (preserve bootstrap-sha, 0.1.0, bump-minor-pre-major) | T005, T006, T036 | Dual-package without changing existing invariants |


## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add build tooling and config skeleton that all later work depends on

- [ ] T001 Add `ko` to `devbox.json` with pinned version per research R7 in `devbox.json`
- [ ] T002 Create root `.ko.yaml` with `builds` entries mapping 5 root images (`cmd/core-manager`, `cmd/kubeadm-bootstrap-manager`, `cmd/kubeadm-control-plane-manager`, `cmd/workspace-manager`, `cmd/dev-infrastructure-manager`) to `ghcr.io/jimmidyson/kcp-capi-*` in `.ko.yaml`
- [ ] T003 Create root `.ko.yaml` build entry for Nutanix provider image (`providers/nutanix-infrastructure/cmd/nutanix-infrastructure-manager` → `ghcr.io/jimmidyson/kcp-capi-nutanix-infrastructure`) in `.ko.yaml`, verifying ko can resolve nested-module importpaths from the root context (spike: `ko build --dry-run` or `--push=false`)
- [ ] T004 Create `scripts/image-tags.sh` with shebang, error handling (`set -euo pipefail`), and derivation logic reading `REPO_VERSION` from `git describe`, `CAPI_VERSION`/`CAPX_VERSION`/`K8S_VERSION` from `go list -m -json` (with `+`→`-` sanitization), and `CAPI_BASE` from `DRIFT.md`, plus OCI tag regex validation in `scripts/image-tags.sh`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Versioning and task surface that every user story requires

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [ ] T005 Update `release-please-config.json` to declare second package `providers/nutanix-infrastructure` with `include-component-in-tag: false` per research R3 in `release-please-config.json`
- [ ] T006 Update `.release-please-manifest.json` to add second key `"providers/nutanix-infrastructure": "0.1.0"` in `.release-please-manifest.json`
- [ ] T007 Add `image:build` task to `Taskfile.yaml` that sets `KO_DOCKER_REPO=ko.local` and runs `ko build --bare` for all 6 images with `--tags local` (or `sha-<short>`), iterating `PROVIDER_MODULES` for provider images in `Taskfile.yaml`
- [ ] T008 Add `image:verify` task to `Taskfile.yaml` that asserts: (a) every image's `io.kcp-capi.capi-version` label equals `go list -m sigs.k8s.io/cluster-api` version, (b) long-tag `-capi-` segment matches label, (c) no tag contains `+`, (d) `image-tags.sh --check` passes in `Taskfile.yaml`
- [ ] T009 Extend `scripts/image-tags.sh` with `--check` mode (validate all derived tags against OCI regex, no `+`, length < 128, exit non-zero on violation) and `--gha-output` mode (write `tags`, `capi_version`, `capx_version`, etc. to `GITHUB_OUTPUT`) in `scripts/image-tags.sh`
- [ ] T010 Add check that `PROVIDER_MODULES` covers all `providers/*/go.mod` directories — fail CI if a nested module exists outside the list in `Taskfile.yaml` (new task `image:check-modules` or extension to existing `check` task)
- [ ] T011 Verify `task release-please -- --dry-run` includes both packages and would create `vX.Y.Z` + `providers/nutanix-infrastructure/vX.Y.Z` at the same commit (manual or CI validation step)

**Checkpoint**: Foundation ready — `task image:build` and `task image:verify` are actionable; versioning is dual-package; user story work can begin

---

## Phase 3: User Story 1 — Deploy a KCP-aware CAPI stack on Kubernetes (Priority: P1) 🎯 MVP

**Goal**: All 6 images build, push to GHCR, and when deployed against a KCP host cluster produce ready Clusters in two isolated workspaces with no cross-workspace bleed

**Independent Test**: Pull 6 images from GHCR at a release tag, deploy via sample manifests against a kcp-backed cluster with APIExports published, create ClusterClass + Cluster in two workspaces, assert both reach `Available` and Machines provision (see quickstart.md §3)

- [ ] T012 [P] [US1] Verify `.ko.yaml` builds produce runnable entrypoints for all 6 managers — confirm each `cmd/*/main.go` `package main` builds via `KO_DOCKER_REPO=ko.local ko build --bare --push=false` without error in `.ko.yaml` and respective `cmd/*/main.go`
- [ ] T013 [US1] Implement `scripts/image-tags.sh` derivation for all 6 images — per-image tag sets: `repoTag` (`vX.Y.Z`), `longTag` (`vX.Y.Z-capi-…` for root, `vX.Y.Z-capx-…-capi-…` for Nutanix), `forkTag` (for root: CAPI fork, for Nutanix: CAPX fork) in `scripts/image-tags.sh`
- [ ] T014 [P] [US1] Add `--image-label` passthrough for `org.opencontainers.image.version`, `org.opencontainers.image.revision`, `org.opencontainers.image.source`, `io.kcp-capi.capi-version`, `io.kcp-capi.k8s-version`, `io.kcp-capi.capi-base` (all images) and `io.kcp-capi.capx-version` (Nutanix) to Taskfile image tasks in `Taskfile.yaml`
- [ ] T015 [US1] Create `.github/workflows/publish-images.yaml` — trigger on `push: tags: ['v*']` (release) and `push: branches: [main]`, login via `docker/login-action`, derive tags via `scripts/image-tags.sh --gha-output`, run `ko build --bare --push --platform=linux/amd64,linux/arm64` for each image with 3-alias tag matrix (release) or `:main`/`:sha-*` (main branch) in `.github/workflows/publish-images.yaml`
- [ ] T016 [US1] Validate published images are deployment-ready — smoke test that each image's entrypoint responds to `--help`/`--version` flags and that `org.opencontainers.image.*` labels are present via `docker inspect` in `scripts/image-tags.sh` or `Taskfile.yaml` (`image:verify` extension)

**Checkpoint**: User Story 1 complete — `task image:build` produces 6 GHCR-ready images that deploy as KCP-aware managers and together deliver ready clusters

---

## Phase 4: User Story 2 — Trace an image back to its upstream source (Priority: P1)

**Goal**: Every published image's OCI tags and labels are mechanically consistent with `go list -m` pins, letting an operator determine the upstream fork version from the tag alone

**Independent Test**: `docker pull ghcr.io/jimmidyson/kcp-capi-core:v0.2.0` and verify: 3 OCI tags share same digest, labels match `go list -m sigs.k8s.io/cluster-api`, long-tag suffix equals label with `+`→`-`

- [ ] T017 [P] [US2] Implement root-image long-tag format `vX.Y.Z-capi-<capiVersion>` derived from `go list -m sigs.k8s.io/cluster-api` with `+`→`-` sanitization in `scripts/image-tags.sh`
- [ ] T018 [P] [US2] Implement Nutanix-image tags: `vX.Y.Z-capx-<capxVersion>-capi-<capiVersion>` and fork-alias `v1.11.0-kcp.3`, derived from `go list -m` inside `providers/nutanix-infrastructure` for CAPX version in `scripts/image-tags.sh`
- [ ] T019 [US2] Ensure `io.kcp-capi.capi-version`, `io.kcp-capi.k8s-version`, `io.kcp-capi.capi-base`, `io.kcp-capi.capx-version` labels are injected via `ko --image-label` per image and match the tag-embedded versions in `Taskfile.yaml` and `.ko.yaml`
- [ ] T020 [US2] Add `scripts/image-tags.sh --check` validation that every tag matches `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`, contains no `+`, and long-tag length < 100 in `scripts/image-tags.sh`
- [ ] T021 [US2] Add `image:verify` assertions that `io.kcp-capi.capi-version` label equals the `-capi-` segment of the longTag for each built image (using `docker inspect` or `skopeo inspect` equivalent) in `Taskfile.yaml`
- [ ] T022 [US2] Handle no-fork-bump case — when CAPI/CAPX pins unchanged between releases, verify that `forkTag` alias still moves to newest digest and labels remain identical across consecutive releases (contract verification) in `scripts/image-tags.sh` or release notes documentation

**Checkpoint**: User Stories 1 AND 2 complete — images deploy AND are traceable to upstream via tags/labels

---

## Phase 5: User Story 3 — Add a new infrastructure provider without breaking the release (Priority: P2)

**Goal**: A contributor can add a new provider under `providers/<name>/` with ≤4 edits (directory, PROVIDER_MODULES, release-please package, DRIFT.md) and no redesign of tag generation

**Independent Test**: Add fixture provider `providers/example/` with trivial `cmd/example-manager`, register in `PROVIDER_MODULES` + `release-please-config.json:packages`, run `task build && task release-please -- --dry-run` — both include it; tag derivation covers it

- [ ] T023 [P] [US3] Document provider-addition contract in `Taskfile.yaml` — ensure `PROVIDER_MODULES` iteration pattern in `build`, `lint`, `test:unit`, `drift`, and `image:build`/`image:verify` is consistent (extract to shared var so all tasks iterate `PROVIDER_MODULES`) in `Taskfile.yaml`
- [ ] T024 [US3] Verify `task drift` checks per-provider `DRIFT.md` for each `PROVIDER_MODULES` entry — test with existing Nutanix entry and document expected `drift.yaml` behaviour for additional providers in `.github/workflows/drift.yaml` or `Taskfile.yaml`
- [ ] T025 [US3] Extend `scripts/image-tags.sh` image table to be extensible — confirm adding a new row (mapping `kcp-capi-<name>` → `go list -m <upstreamImport>`) requires only one table edit, not code restructure in `scripts/image-tags.sh`
- [ ] T026 [US3] Extend `.ko.yaml` extensibility — confirm adding a new provider means adding one `builds` entry for `./providers/<name>/cmd/<name>-manager` with its image name, or document when a per-provider `.ko.yaml` is needed (R6 spike result) in `.ko.yaml`
- [ ] T027 [P] [US3] Extend `release-please-config.json` / `.release-please-manifest.json` handling — document that each new `providers/<name>` entry is one `packages` key; verify `include-component-in-tag: false` still produces `providers/<name>/vX.Y.Z` for each in `release-please-config.json`
- [ ] T028 [US3] Integration test: add ephemeral fixture provider to validate extensibility end-to-end — scaffold `providers/fixture-test/` with minimal `go.mod`/`cmd/fixture-manager/main.go`/`DRIFT.md`, register it, run `task build && task image:build` to confirm the 7th image builds, then remove fixture (CI-only or local validation step)

**Checkpoint**: User Stories 1–3 complete — fleet publishes, is traceable, and is extensible to new providers with the 4-edit contract

---

## Phase 6: User Story 4 — Run locally-built images for development and CI (Priority: P2)

**Goal**: Contributors build all 6 images locally without GHCR credentials and load them into kind for dogfooding

**Independent Test**: `task image:build` with no GHCR login produces `ko.local/kcp-capi-*:local` images; `kind load docker-image` + sample manifests reconcile as the published path

- [ ] T029 [US4] Ensure `task image:build` works with `KO_DOCKER_REPO=ko.local` and no registry auth — verify all 6 images are listed by `docker images` as `ko.local/kcp-capi-*:local` or `sha-<short>` on a clean checkout in `Taskfile.yaml`
- [ ] T030 [P] [US4] Add `image:build` platform handling — default local build is single-platform (`linux/amd64` or host arch) for speed; document `KO_PLATFORMS=linux/amd64,linux/arm64` override for multi-arch parity testing in `Taskfile.yaml`
- [ ] T031 [US4] Add `image:load-kind` task (or document in `AGENTS.md`) that runs `kind load docker-image` for each built image into the configured kind cluster name in `Taskfile.yaml`
- [ ] T032 [US4] Validate local images run identically to published — document that manifests differ only by image reference and that local + published paths both satisfy `task verify` (when a kcp test harness is available) in `quickstart.md` or `Taskfile.yaml`

**Checkpoint**: All user stories complete — full publish lifecycle plus local development loop

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Docs, CI wiring, and final verification

- [ ] T033 [P] Update `AGENTS.md` to document new `task image:build`, `task image:verify`, `task image:load-kind` targets in the named-operations table in `AGENTS.md`
- [ ] T034 [P] Update `README.md` or design doc with GHCR image locations (`ghcr.io/jimmidyson/kcp-capi-*`), tag conventions, and link to `contracts/tag-schema.md` in `README.md`
- [ ] T035 [P] Add `packages: write` permission to the publish workflow's job and ensure `GITHUB_TOKEN` is passed correctly for GHCR push in `.github/workflows/publish-images.yaml`
- [ ] T036 Verify `task drift` still passes and `task verify` (or `task check`) is green with the new dual-package release-please config (requires that `v0.1.0` single-tag history remains compatible with the new manifest's two-key state) in `Taskfile.yaml` and `.github/workflows/pr.yaml`
- [ ] T037 Edge case: validate `providers/nutanix-infrastructure/go.mod`'s `replace kcp-cluster-api => ../..` requires both Go tags at same commit — document in `contracts/tag-schema.md` or task that building from a commit with only one tag should fail loudly, not resolve a stale dependency
- [ ] T038 Run `quickstart.md` validation end-to-end on a fresh checkout (all 5 quickstart sections) and record any gaps

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately
- **Foundational (Phase 2)**: Depends on Setup completion — BLOCKS all user stories
- **User Stories (Phase 3+)**: All depend on Foundational phase completion
  - US1 (P1) and US2 (P1) share implementation of tag/label derivation — implement together, US2 adds verification on top of US1
  - US3 (P2) and US4 (P2) can proceed in parallel after US1/US2 are done, or after Foundational if US1/US2 are not blockers
- **Polish (Final Phase)**: Depends on all desired user stories being complete

### User Story Dependencies

- **US1 (P1) — Deploy stack**: Can start after Foundational. No dependencies on other stories. Requires all 6 image builds + publish workflow.
- **US2 (P1) — Trace image**: Can start after Foundational. Overlaps heavily with US1 (same tag derivation + labels). Should be implemented alongside US1; its independent test is tag/label inspection, not deployment.
- **US3 (P2) — Add provider**: Depends on Foundational (PROVIDER_MODULES, drift, release-please) + US1/US2 patterns established. Independently testable via fixture provider.
- **US4 (P2) — Local build**: Depends on Foundational (`image:build` shape). Independent of US1–US3; can start immediately after Foundational.

### Within Each User Story

- Tag/label derivation script before ko config labelling
- ko config before workflow (workflow consumes ko + tags)
- Workflow before smoke/verification tests

### Parallel Opportunities

- T002 and T004 (different files: `.ko.yaml` vs `scripts/image-tags.sh`) can run in parallel
- T005, T006 are independent config edits — parallel
- T007, T008, T010 are independent Taskfile.yaml sections — parallel once file exists
- T017, T018 (root vs Nutanix tag formats) are parallel (same file, different table rows — coordinate or serialize)
- T023–T027 (provider extensibility) are all [P] — different files
- T029, T030, T031 (local build variants) can run in parallel
- T033, T034, T035 (polish docs/workflows) are parallel

---

## Parallel Example: User Story 1

```bash
# Setup: independent files
Task: "T001 Add ko to devbox.json"
Task: "T004 Create scripts/image-tags.sh"

# Foundational: independent configs
Task: "T005 Update release-please-config.json"
Task: "T006 Update .release-please-manifest.json"

# US1 implementation: different layers
Task: "T014 Add --image-label passthrough to Taskfile.yaml"
Task: "T015 Create .github/workflows/publish-images.yaml"
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2)

1. Complete Phase 1: Setup (devbox.json, .ko.yaml skeleton, image-tags.sh)
2. Complete Phase 2: Foundational (dual-package versioning, Taskfile image tasks, tag derivation modes)
3. Complete Phase 3 + Phase 4: US1 + US2 together (6 images build, push with 3-alias tags, labels)
4. **STOP and VALIDATE**: `task image:build` + `task image:verify` pass locally; `task verify` still green; `task release-please -- --dry-run` shows 2 packages
5. Deploy/demo if ready — the MVP is "6 images exist on GHCR with correct tags/labels"

### Incremental Delivery

1. Setup + Foundational → Foundation ready (ko + tags + versioning)
2. Add US1 + US2 → 6 images exist and are traceable → Deploy/Demo (MVP!)
3. Add US3 → Fixture provider proves extensibility without redesign → Deploy/Demo
4. Add US4 → Local loop closed → Deploy/Demo
5. Polish → Docs + CI permissions + quickstart validation

### Parallel Team Strategy

With multiple developers after Foundational:

- Developer A: US1 + US2 (tag/label matrix is shared work; combine them)
- Developer B: US3 (provider extensibility — needs US1/US2 patterns but can draft the contract)
- Developer C: US4 (local build — independent of publish workflow)

---

## Notes

- [P] tasks = different files, no dependencies — safe for parallel execution by agent teams
- [Story] label maps task to user story for traceability (US1–US4)
- Each user story has independent test criteria per spec.md — verify before moving on
- Commit after each task or logical group; do not batch unrelated stories
- The 4-edit provider-addition contract (FR-009) is the extensibility seam — do not automate it into a generator until a second real provider arrives (Principle VIII)
- Task IDs (T001–T038) are sequential execution order; parallel tasks share a phase but have distinct file targets
- Spec deviation: ko was the operator's explicit preference after the initial Dockerfile proposal; FRs remain satisfied (tool choice is not a spec constraint, only "image builder works for both modules" is)
