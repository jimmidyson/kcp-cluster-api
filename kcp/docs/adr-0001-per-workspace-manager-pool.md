# ADR-0001: Per-workspace manager pool — Phase 0 decisions

Status: accepted. Resolves the Phase 0 decision table (D1, D3, D5) from
[`conversion-plan.md`](conversion-plan.md); D2, D4, D6 are recorded here
too for completeness since the plan already resolved them to a default.

This document is the record the conversion plan requires before Phase 1
work is dispatched: "Get these into the `kcp/docs/adr-0001-*.md` as actual
decisions before dispatching Phase 1, and don't let an agent write that
ADR unsupervised." The choices below were made by the repository owner,
not defaulted silently.

## D1 — kcp version + client libraries

Pin, in `kcp/go.mod`:

| Module | Version | Notes |
|---|---|---|
| `kcp` server (test/dev binary, via `kcp/Makefile`'s `KCP_VERSION`) | `v0.32.3` | Already pinned; unchanged. |
| `github.com/kcp-dev/sdk` | `v0.32.3` | Already pinned; unchanged. |
| `github.com/kcp-dev/multicluster-provider` | `v0.8.0` (latest tagged) | Explicitly experimental/pre-1.0 upstream. Adopting anyway per D4 — see risk note there. |
| `sigs.k8s.io/multicluster-runtime` | `v0.24.1` (latest stable, non-alpha/beta tag) | |

Revisit this pin set opportunistically (dependabot already covers `kcp/`),
and explicitly re-check `multicluster-provider`'s maturity before Phase 4
hardening.

## D2 — Go module layout

Already established: `kcp/go.mod` as its own module
(`sigs.k8s.io/cluster-api/kcp`), separate from the root module. Not yet
wired: a `replace sigs.k8s.io/cluster-api => ../` line is needed once
Phase 1 code actually imports `core/...` reconciler packages — add it in
the same change that adds the first such import, not preemptively.

## D3 — APIExport schema strategy

**Scope:** the first APIExport publishes **core CRDs, plus the
`test/infrastructure/docker` provider's CRDs** (v1beta2) — `Cluster`,
`Machine`, `MachineSet`, `MachineDeployment`, `MachineHealthCheck` from
`core/`, and `DevCluster`/`DevMachine`/`DevClusterTemplate`/
`DevMachineTemplate` (this fork point's naming for the docker/in-memory
infra provider's CRDs, group `infrastructure.cluster.x-k8s.io`) from
`test/infrastructure/docker`. Bootstrap and control-plane CRDs (`kubeadm`
provider) are **not** needed: a test can set
`Machine.spec.bootstrap.dataSecretName` directly
(`api/core/v1beta2/machine_types.go:794`) instead of going through a
`KubeadmConfig`, so Phase 1 doesn't need the kubeadm bootstrap/
control-plane providers to prove a real reconcile loop. Addon and IPAM
CRDs are out of scope until later phases.

The docker/dev-infra CRDs are included specifically because Phase 1's
exit criterion is a *real* `Cluster` → `Machine` provisioning loop, not
just object creation: without `DevCluster`/`DevMachine` bound in the test
workspace, `Machine.spec.infrastructureRef` never resolves and the
reconciler stalls before validating anything. Bootstrap/control-plane
CRDs remain deferred to Phase 3 (P1/P2), since those providers aren't
needed to clear Phase 1's bar.

**Permission claims:** claim all `Secret` and `ConfigMap` objects in bound
workspaces (no label-selector scoping yet). This is the simplest option
for the Phase 1 walking skeleton; narrow it with a selector later once
real usage patterns (which kubeconfig/bootstrap-data secrets reconcilers
actually touch) are known.

**Test requirement (explicit, from the repository owner):** this scope is
approved on the condition that Phase 1 ships with real, working unit
*and* integration tests exercising the core-CRD APIExport/APIBinding
end-to-end (via `kcp/test/integration/envtest`, per
[`testing.md`](testing.md)) — not stubs, and not deferred to a later PR.
This is already required by AGENTS.md's TDD policy; recorded here because
it was a condition attached specifically to approving this narrower
scope.

## D4 — Discovery + cache engine

Confirmed: adopt `kcp-dev/multicluster-provider`'s `Provider` +
`multicluster-runtime`'s `mcmanager.Manager.GetManager()`, per the
conversion plan's "Chosen model" section. Hand-rolling a
`WildcardCache`-alike layer remains the documented fallback if Phase 1's
spike (write-path routing, leader election, live
`APIExportEndpointSlice` reactivity) turns up a blocker — not the default.

Risk accepted: the library is pre-1.0/experimental (D1). No mitigation
beyond re-checking maturity before Phase 4.

## D5 — Identity / RBAC model

**Single system identity via the APIExport virtual workspace.** One
service identity authenticates through the virtual workspace and gets
access to every bound workspace per the D3 permission claims, rather than
per-workspace impersonation. This matches how `multicluster-provider`'s
`Provider` is designed to authenticate and keeps Phase 1 simple; revisit
if a concrete multi-tenant isolation requirement emerges later.

## D6 — Partition topology

Unchanged from the plan's default: start with one `Partition` / one
`APIExportEndpointSlice` / one replica-group (P6's starting point). Only
build out `PartitionSet`-driven multi-shard topology once a real kcp
install runs multiple shards — not preemptively.

## Next step

Phase 0 is now complete. Phase 1 (walking skeleton: `kcp/cmd/core-manager`
against one hardcoded workspace, core + docker-infrastructure CRDs, real
unit + integration tests) is unblocked and ready for dispatch as one
closely-watched session, per the conversion plan's "Executing this plan"
section.
