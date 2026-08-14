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

## Phase 1 results

Phase 1's walking skeleton (`kcp/cmd/core-manager`, `kcp/internal/coremanager`,
`kcp/internal/kcpfixtures`) is implemented and has a passing integration test
(`kcp/test/integration/coremanager`) against a real kcp server. It answers
Phase 1's exit-criteria question — "do upstream's extension points really
compose the way AGENTS.md assumes?" — with **yes for the mechanism, no for
core reconcile logic that resolves contract-versioned cross-references**. Both
halves matter equally; see "Known gaps" below for the second one.

**Confirmed working, empirically, against a real kcp server:**

- **Write-path routing** (D4's flagged open question): `mcmanager.Manager.GetManager(ctx, clusterName)`
  returns a `scopedManager` whose `GetClient`/`GetCache`/etc. are scoped to
  that one engaged workspace (backed by `cluster.Cluster`), while `Add`/
  `Start` are shared with the local host manager. A reconciler's writes
  provably land in that specific workspace, not a wildcard — proved via a
  client built independently of the manager's own cache.
- **Leader election / shared process model**: confirmed workable — a single
  `mcmanager.Manager` runs every engaged workspace's controllers through one
  shared `Start()`/workqueue loop; controllers can be `Add()`-ed after
  `Start()` has already been called (needed since workspaces engage
  asynchronously).
- **Admission webhooks**: work unmodified. `coreadmission`/`infrawebhooks`
  packages' `SetupWebhookWithManager` register against the engaged
  workspace's manager exactly as they would against a normal
  `ctrl.Manager`, and the generated `ValidatingWebhookConfiguration`/
  `MutatingWebhookConfiguration` manifests work in a kcp workspace with only
  their `clientConfig` patched from a `Service` ref to a direct URL
  (`sigs.k8s.io/controller-runtime/pkg/envtest.WebhookInstallOptions`
  already does exactly this patching).
- **Conversion webhooks**: work unmodified for types with no
  contract-versioned cross-references (e.g. `DevCluster`/`DevMachine`).
  `multicluster-runtime`'s `scopedManager` implements `GetConverterRegistry`
  specifically so the shared `/convert` endpoint
  (`sigs.k8s.io/controller-runtime/pkg/builder`) works. kcp's
  `APIResourceSchema.spec.conversion.webhook.clientConfig` requires a bare
  `URL`, not a `Service` ref (kcp rejects `Service`-based client configs
  outright).
- **`APIExportEndpointSlice` activation is lazy**: kcp's
  `apiexportendpointsliceurls` controller leaves `status.endpoints` empty
  until at least one `APIBinding` consumes the export — publish the export,
  bind a workspace, *then* wait for the endpoint slice, not before
  (`kcpfixtures.PublishAPIExport`/`WaitForAPIExportEndpointSlice` are split
  for exactly this reason).
- **CRD → `APIResourceSchema` conversion has real constraints beyond
  `CRDToAPIResourceSchema`'s own documented ones**: (a) this fork's
  generated CRD manifests only carry a `spec.conversion` webhook strategy
  via a kustomize patch applied at deploy time, not baked into
  `config/crd/bases/*.yaml` — `kcpfixtures.PublishAPIExport` replicates that
  patch at runtime; (b) `MachineDeployment`'s generated v1beta2 CRD declares
  the `additionalPrinterColumns` entry `Available` twice, which vanilla CRD
  admission tolerates but kcp's `APIResourceSchema` validation rejects
  outright — worked around generically in `kcpfixtures.dedupeAdditionalPrinterColumns`
  rather than upstream (per AGENTS.md, upstream CRD manifests stay
  untouched).
- **A reconciler's watches on types outside the bound API set aren't just
  ignored.** `cluster.Reconciler`/`machine.Reconciler` unconditionally watch
  `MachineSet`/`MachineDeployment` (always) and `MachinePool` (feature-gated,
  **on by default** upstream) as event sources. If those CRDs aren't bound,
  `controller-runtime`'s cache blocks that controller's startup for a long
  time (order of a minute+) waiting on an informer that can never sync,
  rather than degrading gracefully — so a workspace's bound API set has to
  include every type a wired-in reconciler watches, even types this
  skeleton doesn't itself reconcile, or disable the feature gate
  (`feature.MutableGates`) for ones it can.

### Known gaps

**Core reconcilers couldn't resolve contract-versioned cross-references
(`spec.infrastructureRef`, and by the same code path
`spec.bootstrap.configRef`/`controlPlaneRef`) against a type only available
via `APIBinding` — resolved via a declared upstream exception, not a
workaround.**

`controllers/external.GetObjectFromContractVersionedRef` and
`internal/contract.GetContractVersion`/`GetAPIVersion` — used directly by
`cluster.Reconciler`, `machine.Reconciler`, and most of `core/reconcilers`
— all funnel through `internal/contract.GetGKMetadata`, which did a direct
`CustomResourceDefinition` object lookup by name with no pluggable hook
(unlike `core/webhooks/conversion`'s `SetAPIVersionGetter`). A workspace
that only consumes `DevCluster`/`DevMachine` via `APIBinding` has no such
object — the CRD-shaped source of truth (the `APIResourceSchema`) lives in
the *exporting* workspace instead — so every `Cluster`/`Machine` reconcile
against a cross-referenced infrastructure type failed with `InternalError`.
This wasn't a narrow corner case: `infrastructureRef` is how *every*
infrastructure provider integrates with core Cluster API, so it blocked the
core reconcile path broadly, not just this one field — and by extension
P1–P3 (bootstrap/control-plane/docker-infra provider ports), which all
depend on the same mechanism.

**Resolution (repo owner decision, not an agent's unilateral call):**
rather than a local patch or a workaround that papers over the gap without
touching upstream, `GetGKMetadata` was minimally factored into an
overridable package var, `GetGKMetadataFunc` — the single, deliberate,
tracked exception to this fork's upstream-is-read-only invariant, recorded
in AGENTS.md itself (search "declared exception") so the exception is
visible, not silent. The diff is exactly this indirection; default
behavior for anyone not overriding it is unchanged and covered by
upstream's own existing tests (`controllers/external`, `internal/contract`
still pass unmodified). `kcp/internal/contractmetadata.Registry` supplies
the override: a static `GroupKind -> labels` map built from the same CRD
manifests `kcp/internal/kcpfixtures` already reads to publish
`APIResourceSchema`s — no live lookup, no cross-workspace client, nothing
G3-shaped needed. `kcp/internal/coremanager.SetupContractMetadata` wires it
in for the docker/dev infrastructure provider types this skeleton
reconciles, and a test (`TestDevInfraContractLabelsMatchKustomization`)
guards the hardcoded labels against drifting from the real kustomize
labels transformer they mirror.

**Verified end to end**, in `kcp/test/integration/coremanager`: after this
fix, `Cluster`/`DevCluster`/`Machine`/`DevMachine` reconciliation
demonstrably runs unmodified upstream docker/dev-provider logic all the way
to real Docker daemon calls (creating the load-balancer and machine
containers) — the `InternalError` failure mode is gone entirely. The test
stops short of asserting full `DevMachine` `Ready`, not because of
anything KCP-related, but because that also requires pulling
`kindest/node`/`kindest/haproxy` images from Docker Hub, which the
sandbox this was developed in blocks at the network level (confirmed
independently: even a bare `docker pull kindest/haproxy:...` there gets a
403). In an environment with normal internet access — e.g. this repo's own
`kcp-tests.yaml` CI runner — the same reconcile loop is expected to reach
`Ready`; that remaining gap is a property of the sandbox, not the
architecture.

**Still worth doing, not urgent:** file the same fix upstream against
`kubernetes-sigs/cluster-api` (a pluggable resolver, mirroring
`SetAPIVersionGetter`) so this fork's local exception becomes deletable on
a future rebase once it lands, rather than permanent drift. Until then,
re-check `GetGKMetadataFunc`'s diff against `internal/contract/version.go`
on every rebase — it's small, but it's the one file this invariant no
longer holds for automatically.
