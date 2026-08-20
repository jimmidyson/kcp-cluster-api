# Plan of attack: making Cluster API KCP-workspace-aware

> **Note on paths.** This document was written while the project was
> arranged as a fork of the Cluster API tree, with its own code under a
> `kcp/` subdirectory. That subdirectory is now the repository root, and
> upstream is a pinned dependency — so read `kcp/internal/...` as
> `internal/...`, `kcp/docs/...` as `docs/...`, and so on. The decisions
> recorded here stand; only their locations moved. See
> [`docs/site/content/en/docs/design/fork-architecture.md`](site/content/en/docs/design/fork-architecture.md).

This document is the record of what is done and what is next. Every item
below carries its own status, and a change that lands one updates that status
in the same pull request — see [`AGENTS.md`](../AGENTS.md#tracking-work). Read
it before starting work: it is meant to answer "what now?" without anyone
having to reconstruct the answer from the commit log.

## Next

- **Providers are separate deployments with separate APIExports.** One export
  per provider (`internal/capiexports`), one binary each, and the claims
  between them resolved at run time because an identity hash is per kcp
  instance. P3 landed with it. See
  [One APIExport per provider](site/content/en/docs/design/provider-exports.md).
- **The per-workspace cost is measured per deployment, and added up.** Every
  deployment costs **2 goroutines per active workspace**, flat to twenty, with
  no watch streams or LISTs added by a workspace and everything returned when
  one departs; an installation of all four pays 8, plus 17 discovery requests
  and 26 watch streams held on the shard. What is *not* uniform is reconciling:
  the control plane provider costs 72 requests per workspace against core's 7,
  which a single fleet-wide figure hid. `cmd/sweeptotals` does the addition and
  refuses to print a total when a deployment's report is missing. See
  [Workspace resource usage](site/content/en/docs/design/workspace-resource-usage.md).
- **P1 has landed: the kubeadm bootstrap provider serves the fleet.**
  `cmd/kubeadm-bootstrap-manager`, plus `--control-plane-machines` in the demo,
  and `test/integration/bootstrap` asserts per-workspace bootstrap data and
  per-workspace cluster certificates. It needed D3's open item settled — see
  below. The fork is tagged `v1.15.0-kcp.8` and `go.mod` pins that tag, so
  `task drift` runs against a real ref again (see [`DRIFT.md`](../DRIFT.md)).
- **P2 has landed: a control plane comes up in every workspace.**
  `cmd/kubeadm-control-plane-manager` and its own export; `task demo` brings a
  KubeadmControlPlane to ready in each workspace, and
  `test/integration/bootstrap` asserts it. The kubeconfig and the worker
  `MachineDeployment` that were the remaining gap to a usable cluster have
  landed too.
- **The demo reaches ready clusters, in workspaces two tenants cannot see each
  other's.** `task demo` builds a cluster with a control plane machine and a
  worker in each of several workspaces from one manager, starting its own
  single-shard kcp server, and waits for the `Cluster`'s `Available` condition,
  every control plane replica and every Machine. The workspaces belong to
  users — `alice` and `bob` by default — one home workspace each under an org
  workspace granting nobody anything, and the run finishes by asking kcp, as
  each user, what it will let them read of the other's. It exits non-zero if
  the answer is anything but "their own and nothing else".
  `test/integration/demo` asserts the same run under `task verify`, both halves
  of the tenancy isolation P8 existed to prove.

  Getting there took two defects that only a readiness done-condition could
  surface, because provisioned infrastructure was true throughout both. The
  fleet-wide `ClusterCache` did not register the Node-by-`providerID` index the
  Machine reconciler lists through, so no Machine ever got a `nodeRef`; and in
  the fork, a source declared with `WatchesRawSource` on a wildcard-mode
  controller was never started, so the ClusterCache's Cluster-event sends
  blocked until they timed out and no probe failure reached the control plane
  provider. Both are fixed, the second in `v1.15.0-kcp.8`. See
  [The demo](site/content/en/docs/design/demo.md).
- **Fixed: one workspace of two intermittently never reached ready.** Found by
  investigation rather than by another timeout, and the cause was not the one
  first guessed here.

  The in-memory backend's mux handed out workload cluster ports by counting
  upward from its minimum without checking any of them — upstream's own TODO,
  standing where the check should be. A caller that derives its range from one
  probed port gives the first workload cluster the port it probed and every
  later one an unprobed neighbour, so on a busy machine the *second* workspace
  gets a port something else already holds. The port is recorded on the
  listener and never revisited, so it is retried with the same port forever:
  the cluster's endpoint answers nothing, the remote connection probe never
  succeeds, no Node appears, the Machine stays `Provisioned` and the control
  plane never initialises.

  CI said so in as many words, once the failing run was read rather than
  retried:

  ```
  "WorkloadClusterListener successfully started" listenerName="…|default/demo-00" address="https://127.0.0.1:35267"
  "Reconciler error" error="failed to start WorkloadClusterListener …|default/demo-00, :35268: listen tcp :35268: bind: address already in use"
  ```

  The first workspace got the probed port and started; the second got its
  neighbour and never did. That is why it was always 1 of 2 and never 0 of 2,
  why it never reproduced locally, and why it looked like slowness — it is a
  permanent failure, and no budget increase was ever going to help. The 10-minute integration budgets
  raised while chasing it are headroom that should now be unnecessary; they are
  left because a slow runner is still a slow runner, not because anything is
  hiding behind them.

  Fixed in the fork by binding to check and skipping what is taken
  (`test/infrastructure/inmemory/pkg/server/mux.go`, recorded in
  [`DRIFT.md`](../DRIFT.md) with an upstream proposal pending), with a
  regression test for both halves.

- **G4, the webhook dispatch layer, is the gating item.** Phase 3's P4 waits
  on it, and until it lands webhook wiring serves one named workspace and
  refuses a second rather than silently serving it wrong. It keeps a human
  review checkpoint (see [Executing this
  plan](#executing-this-plan-what-needs-a-human-first)) — a
  workspace-resolution bug here is a cross-tenant bleed, not an ordinary bug.
- **P1–P3 are dispatchable in parallel now.** Each ports one provider's
  `main.go` wiring onto the per-workspace glue G2 already provides — same
  recipe as Phase 1's core-provider port, each in its own `cmd/<name>/`
  directory, so simultaneous work won't collide.
- **G3 has no caller and is not built.** Building it is the first step of P5
  (clusterctl), and nothing else needs it today.
- Phase 0, Phase 1 and the rest of Phase 2 are done. Phase 4 starts once
  Phase 3 lands, and Phase 4's idle eviction now has the measurements it
  needs (see [Scalability](#scalability)).

## Goal

Run the four existing Cluster API manager binaries (`core`,
`bootstrap/kubeadm`, `controlplane/kubeadm`, and, for dev/e2e,
`test/infrastructure/docker`) as **multi-tenant, KCP workspace-aware**
controllers: one deployment per provider that transparently reconciles
`Cluster`/`Machine`/etc. objects living in many KCP logical
clusters/workspaces, without editing a single upstream file (see
`AGENTS.md`).

## Chosen model: `multicluster-runtime` + `kcp-dev/multicluster-provider` as the discovery/cache engine, unmodified upstream managers on top

Every upstream provider's `main.go` already reduces to a pure function of
a `manager.Manager`:

```
mgr -> setupReconcilers(ctx, mgr) -> setupWebhooks(ctx, mgr)
```

`setupReconcilers` does nothing but assign `mgr.GetClient()` into exported
reconciler structs (`cluster.Reconciler{Client: ...}`, etc.) and call their
exported `SetupWithManager`. That is precisely the "own manager
entrypoint" + "inject a client into upstream constructors" extension
point AGENTS.md calls out — and it only needs *something implementing
`sigs.k8s.io/controller-runtime`'s `manager.Manager` interface*, not
literally a `ctrl.NewManager()`-constructed value.

`github.com/kcp-dev/multicluster-provider` (the `apiexport` or
`path-aware` provider) plus `sigs.k8s.io/multicluster-runtime` already
supply that, and do so efficiently:

- Its `pkg/cache.WildcardCache` watches `/clusters/*/...` **once per GVK
  across every engaged workspace** (a single `SharedIndexInformer` set,
  indexed by cluster and cluster+namespace) rather than once per
  workspace — this is the piece that avoids watch/LIST/QPS multiplication
  onto the shard (see "Scalability" below).
- Discovery is a single wildcard watch over `APIBinding` objects (small,
  low-volume) to learn when a workspace joins/leaves our export — we
  don't need to hand-write G1.
- Crucially, `multicluster-runtime`'s `mcmanager.Manager` interface
  exposes `GetManager(ctx, clusterName) (manager.Manager, error)` — a
  **plain `manager.Manager`** (exactly `ctrl.Manager`) scoped to one
  workspace but backed by the shared wildcard cache underneath. That's
  the seam: our Engage handler calls `GetManager(ctx, clusterName)` and
  wires the exact same `setupReconcilers`/`setupWebhooks` pattern onto
  the result, unmodified. No rewriting reconcilers against
  `mcreconcile.Request`/`mcbuilder` needed — that's the API this library
  is more commonly documented against, but it's not the only one it
  exposes, and the `GetManager` escape hatch is what makes it usable here
  without touching upstream.
- Its `Provider` constructor (`New(cfg *rest.Config, endpointSliceName
  string, options Options) (*Provider, error)`) takes a kcp
  `APIExportEndpointSlice` name directly, which is also kcp's own
  horizontal-sharding primitive (see D6/Phase 4) — one less thing we need
  to invent.

So the plan is: adopt the library as the discovery+cache engine (Phase 2,
G1/G2 below), and write only the thin per-workspace glue — Engage/Disengage
callbacks that call `GetManager` and run upstream's unmodified wiring —
ourselves. Confirm the remaining unknowns (write-path routing,
leader-election behavior, library maturity) in Phase 1's spike before
committing; fall back to hand-rolling a discovery+pool layer (see
"Scalability" below) only if those don't hold up.

Webhooks are handled separately either way: a `ValidatingWebhookConfiguration`/
`ConversionWebhook` request is stateless per call, so a single shared
webhook server can serve every workspace if it resolves *which* workspace
each `AdmissionReview` is for (from the request path/host kcp routes it
through) and looks up the right workspace-scoped client on demand. This
needs a spike (G4 below) to pin down exactly how the target kcp version
identifies the source workspace on an incoming webhook call.

## Scalability

Controller-runtime's `cache.Cache` is informer-based (one `Reflector`
doing LIST+WATCH per GVK) with no built-in per-tenant partitioning, and
KCP workspaces are logical — many commonly share one physical shard (one
etcd, one apiserver-like process). Naively running one fully independent
`ctrl.Manager`+cache per workspace would multiply watch connections,
startup LISTs, and QPS by workspace count *onto that same shard*, plus pay
a fixed cache/informer/transport cost per workspace regardless of how
idle it is — a real problem at KCP's expected scale (many, often-idle
workspaces).

Adopting `kcp-dev/multicluster-provider`'s `WildcardCache` (see above)
solves the biggest parts of this for free: watches and startup LISTs are
O(types), not O(types × workspaces), and there's no duplicated
cache/transport per workspace.

> **Measured, and it holds.** Both halves of that paragraph are now
> demonstrated rather than argued, against a real kcp server, by
> `task test:sweep` (`test/integration/sweep`), in six shapes: one controller on
> one type, one per provider deployment, and all four providers co-located on
> the dev provider's in-memory backend.
>
> A hundred active workspaces were served by the same three watch streams as
> one, each provider deployment's twenty by the same six or seven it opened for
> one, and the whole fleet's by the same twelve; none in any sweep was
> addressed to a tenant's logical cluster, and no shape paid a per-workspace
> LIST. Engaging the hundredth workspace took no longer than the first. See
> [Workspace resource usage](site/content/en/docs/design/workspace-resource-usage.md)
> for the numbers, the method, and the one thing that does *not* come back
> when a workspace leaves, and
> [`specs/20260817-183433-workspace-resource-sweeps`](../specs/20260817-183433-workspace-resource-sweeps/spec.md)
> for the specification the sweeps were built against.

What it does *not* obviously solve, and needs verifying in the Phase 1 spike
rather than assumed:

- **Leader election.** The library wraps a controller-runtime manager
  internally; the expectation is one election for the whole process
  (good — avoids a per-workspace Lease-renewal QPS floor), but this needs
  confirming from behavior, not just the type signatures.
- **Write-path routing.** Reads are clearly wildcard-shared; each engaged
  workspace's `GetManager()` result must still route *writes* to that
  specific workspace, not the wildcard endpoint. Verify this explicitly.
- **Horizontal sharding across replicas — solved by kcp's own topology
  API, not something to build.** kcp's `APIExportEndpointSlice` is
  documented as "consumed by managers to start controllers and informers
  for the respective APIExport services," filtered by an optional
  `Partition` (itself generated from a `PartitionSet` selecting `Shard`
  objects by label). `multicluster-provider`'s `Provider` constructor
  takes an endpoint-slice name directly, so pointing different
  replica-groups at different endpoint slices already gives each one a
  disjoint set of shards/workspaces to engage, with kcp itself owning
  shard-membership and rebalancing. See D6/Phase 4 — this is now mostly
  manifest work (Partitions + APIExportEndpointSlices + one
  `--endpoint-slice-name` flag per replica-group), not new code, and it's
  irrelevant until a kcp install actually runs multiple shards. **Resolved:**
  the Provider *does* pick up `.status.endpoints` changes live —
  `endpointSliceUpdate` (`multicluster-provider@v0.8.0`
  `pkg/provider/provider.go:259-293`) reconciles the watched endpoint set on
  every slice change, starting watches for new URLs and cancelling those no
  longer listed. So topology changes underneath a running partition are handled.
  What remains unverified is kcp's half — whether a logical cluster can move
  between shards at all. See
  [ADR-0002](adr-0002-shard-appliance-scaling.md) A1, which defers rebalancing
  on that basis.
- **Per-workspace controller overhead still scales with W.** Each
  workspace still gets its own real `controller.Controller`
  (workqueue, goroutines, rate limiter) once `SetupWithManager` runs
  against its `GetManager()` result — cheap relative to a duplicated
  cache, but not free; matters mainly at very high workspace counts.
  **Now quantified, and largely designed away.** Per-workspace controllers cost
  12 goroutines per active workspace for one controller watching one type,
  exactly linear to W=100. A provider deployment no longer wires them: its
  controllers are fleet-wide, so every one of the four costs **2 goroutines per
  active workspace**, exactly linear to W=20, for the engagement — plus the
  `RESTMapper` the provider builds per engaged workspace, which is 3 discovery
  requests for core and the dev provider, 4 for bootstrap and 7 for the control
  plane. That is the number that decides how many workspaces one replica should
  serve, measured for each deployment rather than for the four together.

  The same sweeps quantified the one cost that is not reclaimed on
  disengagement, and showed that the same change removes it: **two goroutines
  per event-handler registration**, retained by a handler controller-runtime's
  `Kind` source adds to the shared wildcard cache's informer and never removes.
  Fleet-wide controllers register once for the process rather than once per
  workspace, so a deployment retains **zero** per departed workspace; the
  per-workspace seam still retains 2, and accumulates with workspace *churn*
  rather than with W.
- **Library maturity.** `kcp-dev/multicluster-provider` is explicitly
  documented as experimental and pre-1.0 (currently v0.8.x). Pinning
  production multi-tenancy on it is an adoption risk to weigh
  independently of whether it technically solves the caching problem —
  worth a maturity/support-model check in Phase 0 (D1).

If the Phase 1 spike turns up a blocker here (e.g. write-path routing
doesn't work as expected, or the library's maturity is judged too risky),
the fallback is hand-rolling a discovery watcher + a `WildcardCache`-alike
shared informer layer ourselves — same shape, more work, no external
dependency. Treat that as the documented fallback, not the default.

## Phase 0 — decisions (groundwork, sequential, do first)

These block essentially everything else; they're cheap to get wrong
early and expensive to unwind later.

| # | Decision | Status | Notes |
|---|---|---|---|
| D1 | Target kcp version + client libs to pin (`kcp-dev/kcp`, `kcp-dev/client-go`, `kcp-dev/multicluster-provider`, `sigs.k8s.io/multicluster-runtime`) | done — [ADR-0001](adr-0001-per-workspace-manager-pool.md) | Record in an ADR under `kcp/docs/`, including the library-maturity check from D4/"Scalability" above. |
| D2 | Go module layout for `kcp/` | done — [ADR-0001](adr-0001-per-workspace-manager-pool.md); later superseded by the repository inversion (#22), which made `kcp/` the root module | AGENTS.md prefers a second module over editing root `go.mod`. Use `kcp/go.mod` with a `replace sigs.k8s.io/cluster-api => ../` so it can import `core/reconcilers/*` etc. without touching root `go.mod`. Confirm this doesn't break `go work`/CI tooling. |
| D3 | APIExport/APIBinding schema strategy | done — [ADR-0001](adr-0001-per-workspace-manager-pool.md); `Secret`/`ConfigMap` claims settled by P1 (claimed `all: true`, accepted at bind; narrowing them is open) | Which CRDs get published (core v1beta1+v1beta2, addons, ipam, bootstrap, controlplane), how permission claims for `Secret`/`ConfigMap` (kubeconfigs, CRS resources) are requested/accepted. Permission-claim mechanism for *provider* CRDs (the reciprocal problem — core reading/writing provider-owned objects) is decided in [ADR-0001](adr-0001-provider-api-permissions.md). The `Secret`/`ConfigMap` half was settled by P1: both are claimed `all: true` and accepted when a workspace binds, which is what makes the bootstrap provider work at all. Narrowing those claims with kcp's resource selectors is open, and is the thing to do before a deployment holds real tenant credentials. |
| D4 | Discovery + cache engine: adopt `kcp-dev/multicluster-provider`'s `Provider` + `multicluster-runtime`'s `mcmanager.Manager.GetManager()`, or hand-roll | done — adopted, confirmed empirically in Phase 1 ([ADR-0001](adr-0001-per-workspace-manager-pool.md)) | Default to adopting (see "Chosen model"/"Scalability" above). Confirm write-path routing, leader-election behavior, and library maturity in the Phase 1 spike before locking this in; hand-rolling a `WildcardCache`-alike layer is the documented fallback, not the default. |
| D5 | Identity/RBAC model | done — [ADR-0001](adr-0001-per-workspace-manager-pool.md) | How each per-workspace manager authenticates (one system identity via the APIExport virtual workspace vs. per-workspace impersonation). **Decided:** single system identity via the virtual workspace — see [ADR-0001](adr-0001-provider-api-permissions.md). |
| D6 | Partition topology for horizontal sharding: how many `Partition`/`APIExportEndpointSlice` pairs, how they map to replica-group deployments | deferred — one partition, one replica-group until an install runs multiple shards | Use kcp's own `PartitionSet`→`Partition`→`APIExportEndpointSlice` chain (see "Chosen model"/"Scalability" above) rather than app-level hashing. Only matters once a kcp install runs multiple shards — moot for single-shard dev/small deployments, so this can start as "one partition, one replica-group" and grow later. |

**Output of Phase 0:** a short ADR (`kcp/docs/adr-0001-per-workspace-manager-pool.md`)
that the rest of the plan links back to.

**Status: done.** See
[ADR-0001](adr-0001-per-workspace-manager-pool.md) for the recorded
decisions (D1, D3, D5 resolved by the repository owner; D2, D4, D6
confirmed at their stated defaults). Phase 1 is unblocked.

## Phase 1 — walking skeleton (groundwork, sequential, small team)

Prove the model end-to-end against a *single, hardcoded* workspace before
building the harder dynamic multi-workspace machinery. This is the
highest-risk unknown in the whole plan (do upstream's extension points
really compose the way AGENTS.md assumes?), so de-risk it first and alone.

1. `kcp/go.mod` skeleton per D2, plus a `kcp/hack/` dev-loop: a local kcp
   instance (kind or `kcp-dev/kcp`'s own quickstart), one APIExport for
   the core CRDs, one workspace bound to it.
2. `kcp/cmd/core-manager/main.go`: pull in `kcp-dev/multicluster-provider`
   (apiexport provider) + `multicluster-runtime`, engage the one test
   workspace, call `mcmgr.GetManager(ctx, clusterName)` to get a plain
   `manager.Manager`, and wire `core/main.go`'s `setupReconcilers`/
   `setupWebhooks` *logic* onto it (new code under `kcp/` importing the
   same exported packages — not the upstream file itself). This is also
   where D4's open questions (write-path routing, leader election) get
   answered empirically rather than assumed.
3. Verify a full `Cluster` → `Machine` reconcile loop works against the
   `test/infrastructure/docker` provider (also single-workspace) inside
   that one workspace, including at least one admission webhook and the
   conversion webhook, and confirm a write from the reconciler actually
   lands in the right workspace (not a wildcard/no-op).

**Exit criteria:** a Cluster gets provisioned end-to-end inside a KCP
workspace using entirely unmodified upstream reconciler/webhook code, and
D4's open questions about the library are answered (adopt as-is, adopt
with workarounds, or fall back to hand-rolling). This is the thing to
demo before greenlighting Phase 2's investment.

**Status: complete — see
[ADR-0001's "Phase 1 results" section](adr-0001-per-workspace-manager-pool.md#phase-1-results)
for the full writeup.** D4's open questions are answered (write-path
routing, leader election/shared-process model, and conversion/admission
webhooks all work as hoped). The exit criterion — a Cluster reconciling
through unmodified upstream code into real docker/dev-provider Docker
daemon calls — is met. Getting there required one deliberate, tracked,
repo-owner-approved exception to the upstream-is-read-only invariant (see
AGENTS.md's "declared exception" section and ADR-0001's "Known gaps"):
`controllers/external.GetObjectFromContractVersionedRef` and friends
funnel through `internal/contract.GetGKMetadata`, which did a hardcoded
`CustomResourceDefinition` lookup with no pluggable hook — blocking every
reconciler that resolves `infrastructureRef`/`bootstrap.configRef`/
`controlPlaneRef`, not a corner case. `GetGKMetadata` is now a minimal,
overridable indirection (`GetGKMetadataFunc`), backed in `kcp/` by a
static registry built from the same CRD manifests already used to publish
`APIResourceSchema`s — no cross-workspace client, no G3 work needed. Full
`DevMachine` readiness in the integration test is gated only by this
sandbox's network policy blocking Docker Hub image pulls, not by anything
KCP-related; a normal CI runner is expected to reach it.

## Phase 2 — shared infrastructure (groundwork, sequential-ish)

Everything Phase 3's parallel tracks import. Small surface, high fan-out —
keep it minimal and get it merged before fanning out.

- G1. **Discovery + cache engine wiring** (`kcp/internal/discovery`): the
  `kcp-dev/multicluster-provider` (apiexport/path-aware) `Provider` +
  `multicluster-runtime` `mcmanager.Manager` setup validated in Phase 1,
  generalized to run continuously rather than against one hardcoded
  workspace. If Phase 1's spike instead forced the hand-rolled fallback,
  this is where that discovery watcher + shared `WildcardCache`-alike
  layer gets built.
- G2. **Per-workspace glue** (`kcp/internal/providerwiring`): the
  Engage/Disengage handlers that call `GetManager(ctx, clusterName)` and
  run a pluggable `func(ctx, mgr manager.Manager) error` per workspace
  (crash-restart, graceful shutdown on Disengage). Each of the 4 provider
  binaries plugs in its own `setupReconcilers`/`setupWebhooks` as that
  callback — this is the only piece that's genuinely bespoke to this repo.
- G3. **Workspace-scoped `rest.Config` builder** (`kcp/internal/kcpclient`):
  turns a workspace path + base kcp front-proxy config into a
  `*rest.Config`, needed for clusterctl (P6) and anything that talks to a
  specific workspace outside the engaged-manager pool.
- G4. **Webhook dispatch layer** (`kcp/internal/webhookrouter`): the spike
  from Phase 0/D-notes, resolving incoming `AdmissionReview`s to a
  workspace and handing back the right workspace-scoped client to
  upstream's `SetupWebhookWithManager`-registered handlers. This is the
  least well-understood piece — budget spike time before committing to an
  interface shape.
- G5. **New CI workflow** (`.github/workflows/kcp-pr-verify.yaml`, new
  file only): build/vet/lint `kcp/go.mod`, run Phase 1's walking-skeleton
  test as an integration job (kind + kcp).

**Status**

| # | Status | Evidence |
|---|---|---|
| G1 | done | #25 — no code of its own beyond `cmd/core-manager`'s provider construction |
| G2 | done | #25 — `internal/providerwiring` |
| G3 | deferred, no caller | trigger: P5, or anything else reaching a specific workspace from outside the engaged pool |
| G4 | not started — **gates P4** | keeps a human review checkpoint |
| G5 | done | #22 — `.github/workflows/pr.yaml` |

See
[Per-workspace wiring](site/content/en/docs/design/per-workspace-wiring.md)
for the contract and the reasoning, and
[`specs/20260815-185524-per-workspace-wiring`](../specs/20260815-185524-per-workspace-wiring/spec.md)
for the specification G1 and G2 were built against.

- **G1** needed no code of its own beyond `cmd/core-manager`'s existing
  provider construction, and deliberately gets no project-owned interface:
  `multicluster.Provider` and `mcmanager.Manager` are already interfaces
  owned by their implementers.
- **G2** is `internal/providerwiring`. The generalization from one hardcoded
  workspace turned out to be less about discovery than about three
  process-global mechanisms in the dependencies that are silently
  single-tenant — the webhook builder's skip-if-already-registered, the
  never-emptied controller-name registry, and a per-workspace manager whose
  `Add` delegates to the host — all documented at the package.
- **G3** has no caller and is not built. Trigger: P5, or anything else that
  must reach a specific workspace from outside the engaged pool.
- **G4** remains the gating item for Phase 3's P4, and keeps its human review
  checkpoint. Until it lands, webhook wiring is constrained to one named
  workspace and refuses a second rather than silently serving it wrong.

G1 and G2 are largely proven by Phase 1's skeleton already (this is
mostly generalizing that code from one hardcoded workspace to the real
discovery loop); G3 is independent and can be built in parallel with them
once the Phase 0 ADR is settled. G4 depends on nothing but the ADR and can
also start immediately. G5 can start as soon as `kcp/go.mod` exists (end
of Phase 1).

## Phase 3 — parallel fan-out

Once G2 (per-workspace glue) and G3 (rest.Config builder) exist, these
tracks are independent of each other and can be owned by different
people/agents simultaneously. Each one is "repeat Phase 1's port for a
different binary/concern."

| Track | Status | Scope | Depends on |
|---|---|---|---|
| P1 | done — `cmd/kubeadm-bootstrap-manager`, fleet-wide rather than per workspace ([design](site/content/en/docs/design/bootstrap-provider.md)) | `kcp/cmd/kubeadm-bootstrap-manager`: port `bootstrap/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P2 | done — `cmd/kubeadm-control-plane-manager`, on its own APIExport ([design](site/content/en/docs/design/provider-exports.md)) | `kcp/cmd/kubeadm-control-plane-manager`: port `controlplane/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P3 | done — `cmd/dev-infrastructure-manager`, on its own APIExport ([design](site/content/en/docs/design/provider-exports.md)) | `kcp/cmd/docker-infrastructure-manager`: port `test/infrastructure/docker/main.go` wiring onto G2 (needed for dev/e2e, not for production) | G2, G3 |
| P4 | blocked on G4 | Webhook wiring for all 4 providers through G4 | G4, P1–P3 (can stub against G4's interface early) |
| P5 | not started — needs G3, which is unbuilt | `clusterctl` workspace-awareness: teach `cmd/clusterctl` to target a `clusters/<path>` kubeconfig context (flag/env plumbing only — clusterctl is a client, not a controller, so this track has no dependency on P1–P4) | G3 |
| P6 | partly — the exports, their endpoint slices and the claim list between them are built and maintained (`internal/capiexports`); the `WorkspaceType` tenants onboard with is not | APIExport/APIBinding manifests + permission-claim wiring per D3, plus the default single-partition `APIExportEndpointSlice` (D6's starting point — no `Partition`/`PartitionSet` needed until multi-shard). Per [ADR-0001](adr-0001-provider-api-permissions.md): includes the self-maintaining permission-claim-list controller and the `Maintain`-lifecycle `WorkspaceType` tenants use to onboard to CAPI. | D3 (Phase 0 only) |
| P7 | not started | RBAC/identity provisioning per D5 | D5 (Phase 0 only) |
| P8 | done on the in-memory backend — `test/integration/demo` takes two workspaces' Cluster→Machine to ready concurrently and asserts isolation, both between the workspaces' objects and between the two tenants who own them. On a real container runtime the same shape reaches ready Clusters, initialized control planes and bootstrapped Machines, and stops at Node readiness for want of a CNI nothing here installs (see below) | `kcp/test` e2e harness: multi-workspace kind+kcp suite exercising Cluster→Machine across ≥2 workspaces concurrently, proving tenant isolation | Phase 1 skeleton (can stub P1–P3 initially) |
| P9 | not started | Observability: workspace label/attribute injection into controller-runtime metrics, logs, and `kubebuilder:rbac` marker aggregation across the 4 new binaries | G2 |
| P10 | in progress — user docs exist, plus a runnable demo (`task demo`) and its design write-up | User-facing docs (`kcp/docs/`): deployment guide, APIExport binding walkthrough | Can be written incrementally alongside every other track |

P1–P3 are mechanically identical to Phase 1's core-provider port, so
they're the safest to parallelize across multiple agents/contributors at
once — same recipe, different source `main.go`.

**P2** is the first provider that talks to the clusters it creates, and the
first that authors another provider's types: a KubeadmControlPlane creates
Machines, KubeadmConfigs and DevMachines. Three of its claims are therefore
writes, and each missing one was found by running the thing rather than by
reading it — a claim that is absent fails at the first reconcile that needs it,
in the provider that needs it.

It also turned up a real cross-tenant fault in the dev infrastructure provider,
now fixed in the fork: the in-memory backend named a cluster's resource group
and listener by namespace and name, so two workspaces holding a `default/demo-00`
shared one API server. Infrastructure provisioning did not notice; the second
workspace's control plane could not initialise.

**P3** is the split's first citizen: the dev infrastructure provider publishes
its own export and runs as its own deployment, so an installation that will
never run upstream's *test* infrastructure provider is not offered its types.
What made it possible was P1's finding about claims, applied in the other
direction — core claims the provider's types to resolve `infrastructureRef`,
and the provider claims core's to watch `Cluster` and `Machine`.

**P1** turned out not to be mechanical, and what it established is reusable by
P2 and P3. The bootstrap provider's output is Secrets and its init lock is a
ConfigMap, none of which an `APIExport` publishes — so it is the first provider
that needed **permission claims**, and the first evidence that a claimed
resource is readable *and writable* through the export's virtual workspace by
the fleet's own client (`test/integration/claims`). It also needed the core
Machine reconciler to resolve `spec.bootstrap.configRef`, which meant
registering the bootstrap types in the contract-metadata registry whether or not
this process wires that provider. See
[The bootstrap provider](site/content/en/docs/design/bootstrap-provider.md).

**P8**: `test/integration/demo` now does both at once. Two workspaces reconcile
a Cluster→DevCluster to ready concurrently, under one manager, and the test
asserts what nothing did before — each workspace sees exactly its own Cluster,
and each DevCluster is owned by the Cluster in its own workspace. It runs in
`task verify`, and `task demo` is the same code with a person watching.

Tenancy is now asserted from the tenant's side as well as the object's. The
workspaces belong to two users, granted their own home and their own workspaces
and nothing else; the run asks kcp, impersonating each of them, what it will
serve. Each reads their own and is refused the other's home, the other's
Clusters, and the org workspace holding both homes. Both directions are
asserted: "no user read another's" is satisfied completely by an RBAC bug that
refuses everybody everything, so an allowed read is required as well as a
refused one. See [The demo](site/content/en/docs/design/demo.md).

The shape of the tree is load-bearing and was not obvious. kcp's `root`
binds `system:kcp:tenancy:reader` to `system:authenticated`, and a `Workspace`
list is neither recursive nor filtered by what the caller can enter — so homes
placed directly under `root` would have their names listable by every
authenticated user on the shard. The org workspace in between is what makes the
refusal complete.

The Machine half of P8 is done too. It waited on P1 and P2, and with both wired
the demo's done-condition is readiness rather than provisioned infrastructure:
the Cluster's `Available` condition, every control plane replica it was asked
for, and every Machine Ready, control plane and worker alike.

What is left of P8 is the backend rather than the shape. That proof runs on the
dev provider's in-memory backend, where the workload cluster is a process, its
API server is a fake and its Node objects are written rather than joined.
`test/integration/dockerbackend` is the one that uses a real container runtime.

### What two container-backed workspaces reach, and where they stop

Measured, on a branch that takes two workspaces to ready on the docker backend
concurrently. It is not merged, and the paragraph below is why.

The wiring works against a real API server. Both Clusters reach ready, both
control planes initialize and report `Available`, and both Machines bootstrap
and reach `Running` with their own data secret, in two workspaces whose objects
share names and stay separate. That is the whole chain P8 exists to prove, and
it took a defect to get there: `NewDevCluster` gave a docker-backed cluster a
control plane endpoint host and no port, so nothing could dial the workload
cluster. The in-memory backend assigns its own port and never exercised that
path, which is the argument for this suite in one sentence.

**It stops at Node readiness, and the reason is not a defect.** Both Machines
sit at:

```
Node.Ready: container runtime network not ready: NetworkReady=false
reason:NetworkPluginNotReady message:Network plugin returns error:
cni plugin not initialized
```

A kubeadm cluster's Node stays `NotReady` until a CNI is applied. Nothing in
Cluster API applies one — in a deployment that is an add-on provider's job, and
in Cluster API's own e2e suites it is a manifest the test applies
(`test/framework/clusterctl`, `CNIManifestPath`). The in-memory backend never
raised the question, because it writes its Nodes rather than joining them.

**The test now installs one, and it costs no image pull and no vendored
manifest.** kind's node image already contains both halves: its build writes the
manifest for its own CNI to `/kind/manifests/default-cni.yaml` and preloads the
`kindnetd` image that manifest names into the node's containerd store
(`pkg/build/nodeimage/`, `buildcontext.go` and `const_cni.go`). So the test
reads the manifest back out of a control plane container and applies it through
the workload kubeconfig — which is exactly how kind installs it
(`pkg/cluster/internal/create/actions/installcni`). A runner that can start the
cluster can install this CNI with no registry reachable, and the manifest cannot
drift from the Kubernetes version it is for, because it ships inside the image
built for that version.

Two details this pinned down. The manifest is a Go template whose one variable
is the pod subnet, so demo `Cluster`s now **state** `spec.clusterNetwork.pods`
rather than leaving it to a default: the kubeadm bootstrap provider copies that
field into the `ClusterConfiguration` it renders, which is what makes kubeadm
and the CNI the same value rather than two defaults that happen to agree. And
the templating is marked in kind's own source as "intentionally undocumented …
not intended for external usage and is unstable", so the test checks for the
marker string rather than assuming, and applies the manifest verbatim if a
future node image stops carrying it.

The install runs *while* the clusters come up, through `demo.Options`'
`WhileProvisioning` hook, because it cannot run before — the kubeconfig it needs
does not exist until the control plane is up — or after, since waiting for ready
would be waiting for the thing it unblocks.

**Explained: the TLS failures were the two workspaces sharing containers.**
Throughout that run the KubeadmControlPlane's etcd health check failed to
connect, for twelve minutes rather than transiently:

```
grpc: addrConn.createTransport failed to connect to {Addr: "etcd-demo-00-cp-…"}
… error upgrading connection: tls: failed to verify certificate:
x509: certificate signed by unknown authority (… candidate authority
certificate "kubernetes")
```

This was first recorded here as unexplained, with two clients resolving the
workload CA differently as the shape of it. That was the wrong reading. Nothing
is confused about the CA: one client is reaching **the wrong cluster**.

Every container lookup in the docker backend selected on the
`io.x-k8s.kind.cluster` label, whose value is the Cluster's *name*, with nothing
naming the workspace. That is sufficient where Cluster API normally runs — one
management cluster's daemon, one set of names — and insufficient here, because
both demo workspaces hold a Cluster called `demo-00`. Two consequences, and the
second is the one that bites:

- The load balancer's container is `<name>-lb`, and container names are unique
  per daemon, so the second cluster adopted the first's rather than getting one.
- `LoadBalancer.UpdateConfiguration` collects its backend servers by that label,
  so **one workspace's load balancer was configured with the other workspace's
  control plane** and forwarded to it.

kubeadm names every cluster's CA `kubernetes`. Reaching the wrong cluster
therefore reports a certificate signed by an unknown authority whose name is
right and whose key is not — which reads as a certificate bug rather than the
routing one it is.

This is the same defect the in-memory backend had and had already fixed:
`workspace_keys.go` names that backend's per-cluster state by management cluster
as well as namespace and name, because two `default/demo-00` clusters otherwise
shared a resource group and a listener. The docker backend was never given the
same treatment. It now is — containers carry the logical cluster their Cluster
was read from and every lookup filters on it, carried in the fork and recorded
in [`DRIFT.md`](../DRIFT.md).

**What that means for the CNI finding above: it was necessary but not
sufficient.** Installing a CNI alone would not have made this run green, because
the two clusters would still have been sharing a load balancer.

**Settled: G3 is not a dependency of P1–P3.** This table listed it as one,
while Phase 2 recorded G3's trigger as P5 alone — i.e. that a ported provider
binary needs nothing outside the engaged manager pool. All three providers have
now shipped, and G3 still has no caller and is not built, so the Phase 2
reading is the correct one and the dependency column above is wrong for P1–P3.
It is left as written rather than quietly corrected, because the evidence is
what settled it: three ports, none of which reached for it.

## Phase 4 — hardening (after Phase 3 lands)

- Scale-out (D6): define the `PartitionSet`/`Partition`/`APIExportEndpointSlice`
  topology (P6-adjacent manifest work) and templated replica-group
  deployments, each Provider pointed at its own endpoint slice via
  `--endpoint-slice-name`. Only build this out once a kcp install actually
  runs multiple shards and a single leader-elected process's capacity is
  a measured constraint — not preemptively.
- Idle eviction: stop (and later restart) managers for workspaces with no
  recent reconcile activity, to bound steady-state memory.
- Upgrade/rebase drill: pull the next upstream cluster-api release through
  `git merge origin/main` and confirm the invariant check
  (`git diff --name-only <upstream-base>..HEAD -- . ':!kcp'`) stays clean,
  proving the model survives a real rebase.
- Security review of the webhook dispatch layer (G4) specifically — it's
  the one component that fans a single network listener out across tenant
  boundaries, so a workspace-resolution bug there is a cross-tenant
  bleed, not just a bug.

**Status: not started**, and deliberately so — this phase waits on Phase 3.
One piece of its groundwork does exist: idle eviction needs a per-workspace
cost to bound, and #26 measured it (see [Scalability](#scalability) and
[Workspace resource usage](site/content/en/docs/design/workspace-resource-usage.md)),
including the one cost that is not reclaimed when a workspace disengages.

## Dependency summary

```
Phase 0 (ADR, sequential)
   |
Phase 1 (walking skeleton: core provider, 1 hardcoded workspace)
   |
Phase 2 (G1 discovery+cache engine | G2 per-workspace glue | G3 restconfig builder | G4 webhook router | G5 CI)  -- mostly parallel
   |
Phase 3 (P1 bootstrap | P2 controlplane | P3 docker-infra | P4 webhooks | P5 clusterctl |
         P6 APIExport manifests | P7 RBAC | P8 e2e | P9 observability | P10 docs)  -- fully parallel
   |
Phase 4 (sharding, idle eviction, rebase drill, security review)
```

## Open questions to resolve during Phase 0/spikes

1. Exact mechanism the target kcp version uses to identify the source
   workspace on an inbound webhook call (path prefix vs. header) — drives
   G4's design.
2. ~~Whether `ClusterCache` (the *workload*-cluster remote-client cache,
   orthogonal to KCP workspaces) needs any workspace-awareness at all.~~
   **Answered: it does, and the "non-issue" reading was wrong.** That reading
   held while each workspace had its own manager doing the reading. With
   fleet-wide controllers there is one `ClusterCache` for every workspace, so
   its accessors are keyed by workspace *and* cluster, and the kubeconfig
   Secret it needs is not served by the export's virtual workspace at all —
   it is read through a separate shard-scoped client
   (`internal/coremanager/secretreader.go`). See
   `controllers/clustercache/cluster_cache_workspace.go` in the fork.
3. ~~How many workspaces this needs to scale to in practice.~~ **Answered:
   100,000+, reached by composition across regional shards rather than by one
   process** — with replicas scaled per shard and a stated capacity limit per
   shard ([ADR-0002](adr-0002-shard-appliance-scaling.md)). What one workspace
   costs a shard is now measured per deployment rather than argued; see
   [Workspace resource usage](site/content/en/docs/design/workspace-resource-usage.md)
   and the feature that produced it,
   [`specs/20260815-211812-workspace-wiring-scale`](../specs/20260815-211812-workspace-wiring-scale/spec.md),
   whose own status records which of its work shipped and which was superseded.
4. Whether `multicluster-provider`'s `Provider` reacts live to
   `APIExportEndpointSlice.status.endpoints` changing (e.g. a shard
   joining/leaving a `Partition`, or a workspace migrating between
   shards), or requires a restart to pick up a new endpoint set — drives
   whether D6's partition topology can change without a rollout.

## Executing this plan: what needs a human first

This doc is written to be handed to multiple contributors/agents, but not
uniformly. What is dispatchable right now is in [Next](#next) at the top of
this file; what follows here is the standing rule about which work cannot be
handed over unsupervised, whatever its status says.

**Needed a human decision, and got one:** D1 (kcp version to pin), D3
(APIExport schema + permission-claim scope) and D5 (RBAC/identity model) were
written as options to weigh, not answers, and were resolved by the repository
owner in the ADRs before Phase 1 was dispatched. The rule they set stands for
anything of the same shape: an agent handed such a question without a resolved
ADR either stalls or silently picks a default — and for D3/D5-shaped
questions a wrong silent default is a security decision made without review
(permission-claim scope creep, an identity model more permissive than
intended), not just a rework cost. Record the decision as an ADR first, and
don't let an agent write that ADR unsupervised.

**Keeps a human review checkpoint regardless of who writes the code:**
G4 (webhook dispatch) and anything implementing D5. G4 is explicitly
flagged above as "least well-understood," and Phase 4 separately flags it
as the one component where a bug is a cross-tenant bleed, not an ordinary
bug — don't let an agent's plausible-looking implementation of it merge
without a human (or a dedicated security review pass) checking the
workspace-resolution logic specifically, independent of normal code
review.

**Before fanning Phase 3 out to multiple agents with no shared context:**
pin G3's behavioural description into an actual Go interface signature first
(a types-only skeleton, landed as its own small PR) — it is still prose
("turns a workspace path + config into a `*rest.Config`"), which is fine for
a human but leaves room for two agents to independently build incompatible
shapes for the same seam. G1 and G2 no longer have this problem: they are
built, and their contract is written down in
[Per-workspace wiring](site/content/en/docs/design/per-workspace-wiring.md).
Give each P item a one-line acceptance check as it is picked up (a test, or a
minimal verification command), so whoever takes it has an unambiguous
done-condition instead of "compiles" — `task verify` is the project's
done-condition, but it does not know what a given track was supposed to add.
