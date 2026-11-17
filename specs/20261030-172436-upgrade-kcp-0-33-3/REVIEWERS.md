# Review Guide: Upgrade kcp to v0.33.3

**Generated**: 2026-10-30 | **Spec**: [spec.md](spec.md)

## Why This Change

The project pins kcp v0.32.3 as its CI/integration test server and as the source of several Go modules (API types, logical-cluster primitives). The kcp 0.33 line (latest v0.33.3, published 2026-10-29) ships fixes this project benefits from — cross-shard permission-claim reads, cache-server etcd-prefix handling, and LogicalClusterMigration stall fixes — and staying on 0.32.x accumulates drift between the server CI exercises and what real installations run. This change brings the project onto the 0.33 line so CI, demo, and local development all exercise the current server.

## What Changes

Two pins are bumped together in a single PR: the `kcp` server binary from `v0.32.3` → `v0.33.3` (`Taskfile.yaml: KCP_VERSION` and the `task tools:kcp` download URL) and `github.com/kcp-dev/sdk` from `v0.33.1` → `v0.33.3` (`go.mod` + refreshed `go.sum` via `go mod tidy`). `apimachinery/v2` stays at `v2.33.3` and `logicalcluster/v3` at `v3.0.5` (already on the 0.33 line, unchanged), and `multicluster-provider` is not bumped. `docs/conversion-plan.md` updates its "Measured on kcp v0.32.3" footnote to `v0.33.3`. The provider sub-module `providers/nutanix-infrastructure` is updated in lockstep only if `go mod tidy` would otherwise create a `k8s.io/*` minor-version split. The only noted breaking change in the 0.33 line — removal of `APIExport spec.resources[].storage.virtual.identityHash` (kcp PR #4326) — does not affect this repo.

## How It Works

The implementation is a pure dependency/binary bump, not a feature or schema change. The plan (14 tasks across 6 phases) proceeds as:

- **Setup/Foundational (T001–T005)**: Confirm artefact availability (`go list -m -json` for `sdk@v0.33.3`, `curl -I` on the templated release URL), then edit `Taskfile.yaml` and `go.mod` and run coherence checks (`go mod tidy`, `git diff --exit-code`, `go vet`, `go list -m all` for `k8s.io/*` minor consistency). Provider lockstep (T005) is conditional on a detected split.
- **Verification (T006–T008, P1 MVP)**: `task tools:kcp` → `bin/kcp --version` must report `v0.33.3`, then `task verify` (unit + `task test:integration:kcp` against the real kcp server started from `bin/kcp` by the test fixture, plus `task drift`) must pass within the existing 40-minute budget. `verify-result.json` three-outcome contract (`pass` / `fail` / `could not run`) is preserved.
- **Docs/Demo (T009–T011, P2)** and **Graph audit (T012, P3)**: `docs/conversion-plan.md` footnote + optional deferred mount-proxy note, and a final cross-module `go mod tidy` / `task lint` audit.
- **Polish (T013–T014)**: Sweep re-measurement note and quickstart re-run.

No new files, modules, or `replace` directives are introduced; `Taskfile.yaml: KCP_VERSION` remains the single authoritative binary pin.

## When It Applies

**Applies when**:
- A developer's branch (or `main` after merge) runs `task tools` / `task verify` / `task demo` — the new server binary is downloaded and exercised.
- CI's integration suites (`test/integration/...`) start an ephemeral kcp server from `bin/kcp` — they now run against v0.33.3.
- `go mod tidy` / `go build` / `go list -m all` is evaluated — the graph resolves `sdk@v0.33.3` together with `apimachinery/v2@v2.33.3`.

**Does not apply when**:
- Deploying or managing a long-lived kcp installation — test/demo servers are ephemeral (embedded etcd, torn down after each run); no persisted-data migration is needed or attempted.
- Exercising sharded kcp with mounted workspace access — the 0.33 front-proxy/mount-proxy TLS flags (GHSA-c8w2-fgvx-vhv4) are explicitly deferred; the demo and sweeps run single-shard and do not use mounts.
- Adopting `CachedAPIs` / `ClusterCachedResource` alpha APIs — renamed behind an alpha gate in 0.33 but not used here.
- Bumping Cluster API or the Nutanix provider fork — governed by `DRIFT.md` and a separate release line.

## Key Decisions

1. **Bump only `KCP_VERSION` + `sdk`; leave `apimachinery/v2` and `logicalcluster/v3` in place.** `sdk@v0.33.3`'s own `go.mod` requires `apimachinery/v2 v2.33.3`, which is already the project's pin; `logicalcluster/v3` is unchanged in the 0.33 line. Alternative of eagerly bumping `apimachinery` to a newer patch is accepted only if `go mod tidy` selects it — no anticipatory edit.
2. **Keep `multicluster-provider` at `v0.8.0` (ifdotpy fork).** It is not part of the kcp 0.33 release; bumping it is a separate decision with its own compatibility surface. Alternative of bundling it was rejected to keep blast radius small.
3. **Provider sub-module update is conditional, not mandatory.** `providers/nutanix-infrastructure/go.mod` is edited only if `go list -m all` would show a `k8s.io/*` minor-version split after tidy. Otherwise no commit is created for it. Alternative of always bumping the provider was rejected as unnecessary churn.
4. **No new `replace` directives.** `go mod tidy` is the arbiter; a new `kcp-dev/*` replace would be drift and requires justification (Constitution I/NFR-002). None is expected because the `sdk 0.33.3` ↔ `apimachinery v2.33.3` pairing is the upstream-intended one.
5. **Treat `status.identityHash` vs `spec.resources[].storage.virtual.identityHash` as distinct.** The removed field is a per-resource virtual-storage fingerprint used only by `CachedResource` (alpha behind `CacheAPIs`). This repo reads `APIExportStatus.IdentityHash` for permission-claim wiring (`internal/capiexports`, `internal/demo`). Grep confirms zero references to `virtual` in an APIExport context. Alternatives (migrating to the new fingerprint) were rejected as not applicable.
6. **Defer sharded mount-proxy TLS wiring.** The GHSA fix adds `--requestheader-client-ca-file` and `--mount-proxy-client-cert-file/key-file` for multi-shard mounts, but the project runs single-shard. Building plumbing without a deployment to exercise it is premature (YAGNI at seams, Principle VIII). A deferred note may be added to `docs/conversion-plan.md` next-steps.
7. **`task verify` + `task drift` remain the done condition; `CachedResource` rename ignored.** No new test suite, budget, or `contracts/` is added — the existing integration suites against a real kcp server are the test (Principle III). The `CachedResource` → `ClusterCachedResource` rename (alpha) is awareness-only.

## Areas Needing Attention

- **Binary/SDK lockstep is a single-PR invariant.** `KCP_VERSION` and `sdk` must move together; a partial upgrade (binary without SDK or vice versa) produces a binary/API-type mismatch. Reviewers should verify both pins land in the same commit and `bin/kcp --version` matches `go list -m github.com/kcp-dev/sdk`.
- **Transitive `k8s.io/*` coherence is the subtle failure mode.** `go mod tidy` can silently pull a split (e.g. `k8s.io/* v0.36` vs `v0.37`) if the new `sdk` transitively requires a different minor. Reviewers should check `go list -m all | grep k8s.io` in both modules and confirm a single minor is present; if a split exists, `spec NFR-002` governs whether a new `replace` is warranted.
- **Go toolchain floor.** kcp 0.33 requires Go 1.26; this project uses 1.27.3, so the build is fine, but if any `.github/workflows/*` pins Go separately it must be >= 1.26. A reviewer exercising CI should confirm the workflow image still satisfies the floor.
- **Server-flag stability is assumed, not proven in the spec.** The plan notes no removed top-level flags that the fixture uses, but the actual check is a manual diff of `kcp start --help` between binaries and the integration suite failing fast. Reviewers should look for an explicit note in the PR description that this diff was performed.
- **Docs single-source-of-truth.** `Taskfile.yaml: KCP_VERSION` must remain the only authoritative pin; reviewers should reject any second copy of the version in `README.md`/`docs/site` prose. Only the `docs/conversion-plan.md` footnote is in scope, and even it must track `KCP_VERSION`.
- **Small diff, large `go.sum` churn.** The meaningful change is ~<20 lines outside `go.sum`; `go.sum` will be noisy. Review the `go.mod` diff for intent and spot-check `go.sum` for unexpected downgrades rather than line-by-line review.

## Open Questions

No open questions identified. Deferred items are explicitly tracked: sharded mount-proxy TLS flags (spec Edge Cases, plan R5) and `ClusterCachedResource` alpha rename — both out of scope with rationale recorded.

## Review Checklist

- [ ] Key decisions are justified (R1–R6 in `research.md` each cite source/registry verification, not prose)
- [ ] Breaking changes are documented with migration guidance — `spec.resources[].storage.virtual.identityHash` removal is assessed and shown not to affect `status.identityHash`; no migration required
- [ ] Scope matches the stated boundaries — no Cluster API bump, no sharded-mount plumbing, no `CachedAPIs` adoption, no new `replace` without justification
- [ ] Success criteria are achievable — each SC-001..007 names a runnable command and exit status (`bin/kcp --version`, `task verify`, `go mod tidy` clean, `task drift` pass, `task demo` Available)
- [ ] No unstated assumptions — kcp artefact URL pattern, ephemeral-server/no-migration, Go 1.26 floor, `multicluster-provider` out of scope, and `status.identityHash` distinction are all stated in spec Assumptions
- [ ] `KCP_VERSION` and `sdk` updated together; `go mod tidy` leaves `go.mod`/`go.sum` clean and `k8s.io/*` on one minor
- [ ] `Taskfile.yaml: KCP_VERSION` remains the single source of truth for the binary pin (SC-007)
- [ ] `task verify` three-outcome contract preserved (`could not run` ≠ `pass`); `verify-result.json` attached to PR

---

<!-- Code phase sections are appended below this line by the phase-manager command -->
<!-- Each phase section follows the template below -->

<!--
## Phase N: [Phase Name] (YYYY-MM-DD)

### What Changed

[Summary of files and functionality added/modified]

### Spec Compliance

[Which requirements this phase addresses, compliance score]

### Focus Areas for Review

[Where the reviewer should concentrate]

### AI Assumptions

[Decisions made during implementation that were not in the spec]
-->
