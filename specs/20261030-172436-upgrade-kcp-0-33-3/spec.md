# Feature Specification: Upgrade kcp to v0.33.3

**Feature Branch**: `main` (dependency bump; see ADRs for design track)

**Created**: 2026-10-30

**Status**: Draft

**Input**: User description: "Upgrade to latest KCP release"

## Context

The project currently pins kcp v0.32.3 as its test/integration server and as the
source of several Go modules it depends on (the API types, client generation, and
logical-cluster primitives). The latest kcp release is v0.33.3 (published
2026-10-29; release family v0.33.x). Staying on 0.32.x blocks adoption of fixes
shipped in the 0.33 line (e.g. cross-shard permission-claim reads, cache-server
etcd-prefix handling, stall fixes around LogicalClusterMigration) and accumulates
drift between the server used in CI/demo and what an up-to-date kcp installation
serves. This feature brings the project onto the 0.33 line.

**Current pins (from `go.mod` / `Taskfile.yaml`):**

| Artefact | Current | Latest (v0.33 line) | How pinned |
|----------|---------|---------------------|------------|
| kcp server binary | `KCP_VERSION=v0.32.3` (`Taskfile.yaml`) | v0.33.3 | release archive downloaded into `bin/` by `task tools:kcp` |
| `github.com/kcp-dev/sdk` | v0.33.1 | v0.33.3 | `go.mod` `require` |
| `github.com/kcp-dev/apimachinery/v2` | v2.33.3 | v2.33.3 | `go.mod` `require` (already on v2.33.x) |
| `github.com/kcp-dev/logicalcluster/v3` | v3.0.5 | v3.0.5 (unchanged) | `go.mod` `require` |
| `github.com/kcp-dev/multicluster-provider` | v0.8.0 (replace → `ifdotpy/...`) | v0.8.0 | `go.mod` `require` + `replace` |

The kcp 0.33 line is Go 1.26-based (`go 1.26.0` in its `go.mod`, built with
1.26.4) while this project builds on 1.27.3; no Go-version conflict is expected.

### Known breaking change in the 0.33 line

**APIExport `spec.resources[].storage.virtual.identityHash` removed (kcp PR #4326).**
The field is dropped; the replacement is a "virtual resource fingerprint" of the
form `<APIExport identity hash>|<endpoint slice name>/<kind>.<group>`, used only
by the `CachedResource` machinery (alpha, behind `CacheAPIs` gate). This project's
usage of `APIExportStatus.IdentityHash` (capiexports permission-claim flow) is
unaffected — only the `spec.resources[].storage.virtual.identityHash` field is
removed, not `status.identityHash`. `APIExportEndpointSlice` unification (PR
#4304) is likewise noted but does not require code changes here.

### Other notable 0.33 changes (for awareness, not in-scope work)

- `ClusterCachedResource`/`ClusterCachedResourceEndpointSlice` rename behind the
  `CachedAPIs` alpha gate — not used by this project.
- LogicalCluster dump/migration pagination, Virtual Workspace hop limits,
  permission-claimed cross-shard read fix (GH #4215), front-proxy/mount-proxy
  TLS flags for mounted workspace access.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — CI runs on the current kcp server (Priority: P1)

A contributor pushes a branch or opens a PR. CI's integration suites spin up a
kcp server from the project's pinned binary and pass. The suites exercise the
same behaviours they did before (workspace onboarding, demo tenants, sweep
resource usage, claim flows) — now against the server version that real
installations run.

**Why this priority**: This is the only user-visible value of the upgrade. Every
other consumer (demo, sweep, local dev) just inherits the same binary. If CI is
not green on the new server, nothing else can be declared done.

**Independent Test**: `task verify` (which runs `task tools`, `task test:unit`,
`task test:integration:kcp`) passes in a clean checkout on a runner with the new
`KCP_VERSION`. No manual kcp installation required.

**Acceptance Scenarios**:

1. **Given** a clean checkout at this change's tip, **When** `task tools` runs, **Then** it downloads `kcp v0.33.3` into `bin/kcp` and the binary reports `v0.33.3`.
2. **Given** `task test:integration:kcp` is invoked, **When** the suites run to completion, **Then** they exit 0 and produce no new failures compared to the same suites on v0.32.3 (flakes retried once per the existing `verify-result.json` contract).
3. **Given** the new binary is in place, **When** `task drift` runs, **Then** it passes (drift record still accurate; new kcp does not introduce a fork divergence).

---

### User Story 2 — Local demo still reaches ready clusters (Priority: P2)

A developer runs `task demo` locally. The demo starts its own single-shard kcp
server from `bin/kcp`, creates the APIExport, workspaces, and clusters, and
reaches the same "2 workspaces, clusters available, tenants isolated" done
condition as before.

**Why this priority**: The demo is the project's smoke signal for
end-to-end correctness. It already has an integration-test twin
(`test/integration/demo`) that CI covers; this story is the local, observable
complement.

**Independent Test**: `task demo --duration=5m` reaches `Available` on both
clusters and exits 0 in a local checkout at the new kcp version. (Not part of
`task verify`; asserted by the demo integration suite.)

**Acceptance Scenarios**:

1. **Given** `bin/kcp` is v0.33.3, **When** `task demo` runs, **Then** the kcp
   server starts without flag errors and the APIExport becomes `Ready`.
2. **Given** the demo is running on v0.33.3, **When** workspaces and
   `APIBinding`s are created, **Then** bindings reach `Ready` and the demo's
   clusters reach `Available`.
3. **Given** `task demo` is run with `--kcp-kubeconfig` pointing at the new
   server, **When** the demo finishes, **Then** per-tenant `APIBinding` checks
   still read "own workspace and nothing else" (isolation unchanged).

---

### User Story 3 — Dependency graph stays coherent (Priority: P3)

A developer runs `go mod tidy && go vet ./...` and `go build ./...` after the
upgrade. No module, replace directive, or import that previously resolved now
fails, and no new `go.mod` inconsistency is introduced. Downstream modules that
restate this project's three `replace` directives (e.g. `providers/nutanix-infrastructure`)
resolve at the same versions as the root.

**Why this priority**: A kcp adoption is a dependency bump, not a merge (per
`AGENTS.md` § Adopting a newer Cluster API release — same discipline applies to
kcp). A broken graph blocks every other change.

**Independent Test**: `go mod tidy && git diff --exit-code -- go.mod go.sum` is
clean; `task lint` and `task build` pass.

**Acceptance Scenarios**:

1. **Given** `github.com/kcp-dev/sdk` is bumped to v0.33.3 and
   `KCP_VERSION` to v0.33.3, **When** `go mod tidy` runs, **Then** it completes
   without conflicts and does not downgrade any unrelated dependency.
2. **Given** the bump is applied, **When** `go list -m all` is inspected, **Then**
   every `k8s.io/*` transitive version is consistent with the project's current
   Kubernetes version (no split between 0.36 and 0.37 within one build).
3. **Given** `providers/nutanix-infrastructure/go.mod` exists, **When** its
   module is tidied, **Then** it still resolves the same `kcp` family at
   compatible versions (or is updated in lockstep with a noted follow-up).

---

### Edge Cases

- **Mounted workspace access in sharded deployments** — v0.33 carries the fix
  for GHSA-c8w2-fgvx-vhv4 that requires front-proxy `--requestheader-client-ca-file`
  plus per-shard `--mount-proxy-client-cert-file/key-file`. The project's demo
  and sweeps run single-shard and do not use mounts; no flag change is needed
  here. If a future sharded test is added, it must supply these flags. Document
  in `docs/conversion-plan.md` next-steps or a follow-up item if not done now.
- **Server flags renamed/removed between 0.32 and 0.33** — the kcp binary is
  invoked only via `task tools:kcp` (download) and the test fixture's bare
  `kcp start` with a small, stable flag set. Verify the fixture's flag set still
  parses on 0.33; if a flag was removed, the integration suite will fail fast.
- **Go version skew** — kcp 0.33 requires Go 1.26; this project already uses
  1.27.3 (newer). No toolchain downgrade needed. If CI's Go is pinned
  separately (e.g. in `.github/workflows/*`), that pin must be >= 1.26.
- **CachedResource alpha rename** — `CachedResource` → `ClusterCachedResource`
  behind `CachedAPIs`. Not used by this project; no migration needed. Future
  consumers of that API must use the new names.
- **Partial upgrade** — updating `KCP_VERSION` without updating `sdk` (or vice
  versa) can produce a binary/API-type mismatch. Both are updated together in
  one PR.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The pinned kcp server binary version (`KCP_VERSION` in
  `Taskfile.yaml` and the `tools:kcp` download URL) MUST be updated to
  `v0.33.3`.
- **FR-002**: `github.com/kcp-dev/sdk` in `go.mod` MUST be updated to `v0.33.3`
  (the tag matching the kcp release). `go.sum` MUST be updated via `go mod tidy`.
- **FR-003**: `github.com/kcp-dev/apimachinery/v2` MUST remain at or be moved
  to the latest `v2.33.x` compatible with the new `sdk` (currently v2.33.3 —
  already current). If `go mod tidy` selects a newer patch, it is accepted.
- **FR-004**: `github.com/kcp-dev/logicalcluster/v3` MUST remain compatible
  with the new `sdk` (currently v3.0.5, unchanged in the 0.33 line).
- **FR-005**: `go mod tidy` MUST complete without unresolved version conflicts,
  and `go vet ./...` / `task lint` MUST pass on the resulting graph.
- **FR-006**: `task test:integration:kcp` MUST pass on the new server binary in
  a clean checkout (same suite, same budget; flakes retried per the project's
  existing `verify` contract).
- **FR-007**: Any kcp server flag or kubeconfig shape change between 0.32 and
  0.33 that the test fixture or `cmd/demo` depends on MUST be adapted so that
  the fixture and demo start the server successfully on the new binary.
- **FR-008**: `docs/conversion-plan.md` MUST be updated to record the new kcp
  version and, if applicable, any follow-up deferred to a later change (e.g.
  sharded mount-proxy flags, `CachedAPIs` rename awareness).
- **FR-009**: If `providers/nutanix-infrastructure/go.mod` pins `kcp`-family
  modules at a version incompatible with the root's new `sdk`, it MUST be
  updated in the same PR and its `task drift` check MUST pass.

### Non-Functional Requirements

- **NFR-001**: The change MUST NOT alter per-workspace resource usage beyond
  measurement noise. If `task test:sweep` (if run) shows a material change in
  per-workspace cost, it MUST be reported as evidence rather than asserted
  away (Constitution Principle IX).
- **NFR-002**: No new `replace` directives MUST be introduced unless required
  to resolve the new `sdk`/kcp family; if added, they MUST be documented in
  `go.mod` with the same propagation warning that the Cluster API replaces
  carry.

### Key Entities

- **KCP server binary** — the `kcp` executable downloaded by `task tools:kcp`
  from `github.com/kcp-dev/kcp/releases/download/<version>/…`. Pinned by
  `KCP_VERSION` in `Taskfile.yaml`.
- **kcp Go modules** — `github.com/kcp-dev/sdk` (API types for `APIExport`,
  `APIBinding`, `APIResourceSchema`, `LogicalCluster`, etc.),
  `github.com/kcp-dev/apimachinery/v2` (shared kcp apimachinery), and
  `github.com/kcp-dev/logicalcluster/v3` (logical-cluster helpers). Each is a
  separate Go module with its own tag, but all are versioned in lockstep with
  the kcp release in the `v0.33.x` line.
- **Integration environment** — the ephemeral kcp server started by the test
  fixture (`test/integration` harness) and by `cmd/demo`. It is not a deployed
  cluster; it is a process started from `bin/kcp` for the duration of a test
  or demo run.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: `bin/kcp version` (or `kcp --version`) reports `v0.33.3` after
  `task tools:kcp` in a clean checkout at this change's tip.
- **SC-002**: `task test:integration:kcp` passes (exit 0) against the new
  server within its existing 40-minute budget, with no new persistent failures
  (retried flakes do not count against this).
- **SC-003**: `task verify`'s three-outcome contract is preserved: if the
  environment lacks a capability, that step reports "could not run" rather than
  "pass".
- **SC-004**: `go mod tidy` leaves `go.mod`/`go.sum` clean (`git diff --exit-code`
  passes) and `go vet ./...` reports zero errors on the new graph.
- **SC-005**: `task drift` passes at the new pin (no new divergence introduced
  by the kcp bump; existing records remain accurate).
- **SC-006**: `task demo` (local, manual) reaches its "available clusters in
  every workspace, tenants isolated" done-condition on the new server, matching
  the behaviour asserted by `test/integration/demo` in CI.
- **SC-007**: The project's documentation names the current kcp version
  unambiguously (either `Taskfile.yaml: KCP_VERSION` alone is authoritative, or
  any prose in `docs/conversion-plan.md`/`docs/site` that mentions it is
  updated to match).

## Assumptions

- The kcp `v0.33.3` release artefact URL pattern is unchanged from `v0.32.3`
  (`kcp_<version>_<os>_<arch>.tar.gz` under
  `github.com/kcp-dev/kcp/releases/download/<tag>/`). This has been stable
  across all recent releases and is used by `task tools:kcp`.
- No migration of persisted data is needed: the integration and demo servers are
  ephemeral (backed by embedded etcd, torn down after each run). There is no
  long-lived kcp installation whose stored objects must be migrated by this change.
- The `kcp-dev/logicalcluster` and `kcp-dev/apimachinery` major versions do not
  change in the 0.33 line (they remain `v3` and `v2` respectively) — confirmed
  by the tags at time of writing.
- The `multicluster-provider` dependency is not bumped by this change (still
  `v0.8.0` via `ifdotpy` fork). Bumping it is a separate decision; it is not
  part of the kcp 0.33 release.
- CI's Go toolchain is already >= 1.26 (the project uses 1.27.3), so no runner
  image change is needed. If a workflow pins Go separately, the plan will call
  it out.
- The `APIExport.status.identityHash` field that this project reads for
  permission-claim wiring is **not** the removed
  `spec.resources[].storage.virtual.identityHash` field. The two are distinct
  paths; only the latter is removed in 0.33.

## Out of Scope

- Bumping Cluster API (`sigs.k8s.io/cluster-api` fork) or the Nutanix provider
  fork — governed by `DRIFT.md` and a separate release line.
- Adding sharded kcp support or mount-related TLS wiring for multi-shard
  deployments (flagged as a deferred note under Edge Cases).
- Adopting the `CachedAPIs`/`ClusterCachedResource` alpha APIs.
- Any change to `internal/capiexports` permission-claim logic beyond what is
  required to stay compatible with the new `sdk` types (none expected).

## Dependencies

- Upstream kcp `v0.33.3` release must exist and its binary artefact must be
  downloadable (it does; published 2026-10-29).
- `github.com/kcp-dev/sdk@v0.33.3` Go module must be resolvable (it is;
  `go list -m` resolves it).
- No new fork patch is expected; this is a pure dependency/binary bump.
