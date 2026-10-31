# Research: Upgrade kcp to v0.33.3

**Feature**: `20261030-172436-upgrade-kcp-0-33-3` | **Date**: 2026-10-30 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

All findings verified against source/registry, not docs prose (Constitution V).

## R1 — kcp v0.33.3 release is available

- **Decision**: Pin `KCP_VERSION` to `v0.33.3` (Taskfile.yaml var `KCP_VERSION`).
- **Rationale**: Latest stable at spec time (published 2026-10-29). Supersedes `v0.32.3` without a Go-minor boundary (kcp 0.33 targets Go 1.26; this repo uses 1.27.3, newer → compatible).
- **Verification**: `go list -m -json github.com/kcp-dev/sdk@v0.33.3` resolves with `GoVersion: 1.26.0`; kcp binary URL `https://github.com/kcp-dev/kcp/releases/download/v0.33.3/kcp_0.33.3_darwin_arm64.tar.gz` returns 302 to `release-assets.githubusercontent.com` (artefact pattern unchanged from 0.32).
- **Alternatives**: Stay on 0.32.x (rejected — misses cross-shard permission-claim fix GH #4215, cache-server etcd-prefix fix, migration stall fixes).

## R2 — Test invocation and flake policy are stable

- **Decision**: `task verify` remains the done condition (build/vet/unit + `task test:integration:kcp` + `task drift`); three-outcome `verify-result.json` contract unchanged.
- **Rationale**: `Taskfile.yaml` already defines `verify` as `test:unit` + `test:integration:kcp` + `task drift` for the root module and iterates `PROVIDER_MODULES`. Integration suites are `go test ./test/integration/...` against a real kcp server from `bin/kcp` (fixture `test/integration/envtest/envtest.go`), not envtest.
- **Flake handling**: Per spec AC 1.2 and project history, transient integration flakes are retried once and recorded in `verify-result.json` as `could not run` vs `pass` (Constitution IV). No test-budget change needed — existing suite budget is generous for a single-shard kcp.
- **Alternatives**: Introduce a separate `task test:integration:kcp:33` gate (rejected — same suite, same contract; version is input, not a test).

## R3 — `status.identityHash` vs `virtual.identityHash` are distinct fields

- **Decision**: No code change in `internal/capiexports` — the removed field is not the one this repo reads.
- **Rationale**: kcp PR #4326 removes `spec.resources[].storage.virtual.identityHash` (per-resource virtual-storage fingerprint used only by `CachedResource`/`ClusterCachedResource` behind `CacheAPIs` alpha). This repo reads `status.identityHash` (`APIExportStatus.IdentityHash`) for permission-claim wiring (`internal/capiexports/capiexports.go:776`, `internal/capiexports/discovery.go:108`, `internal/demo/onboarding.go:220`). Grep of the repo shows no reference to `virtual` or `storage` in an APIExport context. The SDK's `apis.kcp.io/v1alpha1` types still carry `status.identityHash`.
- **Verification**: Searched repo for `virtual` and `VirtualResource` in Go/MD/YAML — zero hits outside this research doc; `IdentityHash` hits all go through `export.Status.IdentityHash`.
- **Alternatives**: Preemptively migrate to the new fingerprint (rejected — not applicable; would break claim wiring).

## R4 — `apimachinery`/`logicalcluster` stay put; provider lockstep is conditional

- **Decision**: Keep `github.com/kcp-dev/apimachinery/v2` at `v2.33.3` and `github.com/kcp-dev/logicalcluster/v3` at `v3.0.5`; do not add `replace` directives. Update `providers/nutanix-infrastructure/go.mod` only if `go mod tidy` would otherwise create a `k8s.io/*` minor-version split.
- **Rationale**: `go list -m -json` confirms `apimachinery/v2@v2.33.3` and `logicalcluster/v3@v3.0.5` are current and compatible with `sdk@v0.33.3`; `sdk 0.33.3`'s own `go.mod` requires `apimachinery/v2 v2.33.3`. `go mod tidy` is the arbiter — if it keeps `k8s.io/*` on one minor (currently `v0.37.1`), no provider edit is needed. If it would pull a split, the provider module is bumped in the same PR per spec FR-009.
- **Verification**: `go list -m github.com/kcp-dev/apimachinery/v2@v2.33.3` resolves; `go list -m github.com/kcp-dev/logicalcluster/v3@v3.0.5` resolves; `providers/nutanix-infrastructure/go.mod` already pins the same lines indirectly via the root replace (`kcp-cluster-api => ../..`) — run `go list -m all` in both modules after tidy and compare `k8s.io/*` minors.
- **Alternatives**: Bump apimachinery eagerly to `v2.33.4`+ if one publishes before merge (acceptable via `go get -u` at PR time, then re-tidy).

## R5 — Server flags and binary invocation are unchanged

- **Decision**: No change to how `bin/kcp` is launched — `task tools:kcp` downloads the same `kcp_<version>_<os>_<arch>.tar.gz` pattern; the fixture starts `kcp start` with the existing small flag set.
- **Rationale**: 0.32 → 0.33 release notes list no removed top-level flags that the fixture uses (new flags are additive: `--shard-labels`, `--logical-cluster-object-count-scan-interval`). 0.33's front-proxy/mount-proxy TLS flags for mounted workspace access (GHSA-c8w2-fgvx-vhv4) are only needed for multi-shard mounts — this repo runs single-shard, so they are deferred per spec Edge Cases, not adopted now.
- **Verification**: Manual diff of `kcp start --help` between 0.32.3 and 0.33.3 binaries during implementation; integration suite fails fast if a flag was removed.
- **Alternatives**: Adopt mount-proxy flags now (rejected — no sharded deployment to exercise them; premature per VIII).

## R6 — Documentation update is minimal and single-sourced

- **Decision**: Update only `docs/conversion-plan.md` line "Measured on kcp v0.32.3" → `v0.33.3`; optionally add a deferred-note on mount-proxy TLS for future sharded support. No install guide, no site doc.
- **Rationale**: `Taskfile.yaml: KCP_VERSION` is the single authoritative pin (spec SC-007); prose must not carry a second version that can drift. The conversion-plan footnote is the only place that names a measured kcp version outside code.
- **Alternatives**: Duplicate the version in `README.md`/`docs/site` (rejected — would be a drift source; SC-007 explicitly forbids it).
