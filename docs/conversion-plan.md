# Plan of attack: making Cluster API KCP-workspace-aware

> **Note on paths.** This document was written while the project was
> arranged as a fork of the Cluster API tree, with its own code under a
> `kcp/` subdirectory. That subdirectory is now the repository root, and
> upstream is a pinned dependency — so read `kcp/internal/...` as
> `internal/...`, `kcp/docs/...` as `docs/...`, and so on. The decisions
> recorded here stand; only their locations moved. See
> [`docs/site/content/en/docs/design/fork-architecture.md`](site/content/en/docs/design/fork-architecture.md).

Status: draft, for discussion. Owner: none yet — this is a coordination
document for splitting the work across contributors/agents.

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
cache/transport per workspace. What it does *not* obviously solve, and
needs verifying in the Phase 1 spike rather than assumed:

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
  irrelevant until a kcp install actually runs multiple shards. What's
  still unverified: whether the Provider picks up `.status.endpoints`
  changes live if shard membership changes underneath a running partition
  (workspace migration between shards) — confirm in the Phase 1/P8 spike.
- **Per-workspace controller overhead still scales with W.** Each
  workspace still gets its own real `controller.Controller`
  (workqueue, goroutines, rate limiter) once `SetupWithManager` runs
  against its `GetManager()` result — cheap relative to a duplicated
  cache, but not free; matters mainly at very high workspace counts.
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

| # | Decision | Notes |
|---|---|---|
| D1 | Target kcp version + client libs to pin (`kcp-dev/kcp`, `kcp-dev/client-go`, `kcp-dev/multicluster-provider`, `sigs.k8s.io/multicluster-runtime`) | Record in an ADR under `kcp/docs/`, including the library-maturity check from D4/"Scalability" above. |
| D2 | Go module layout for `kcp/` | AGENTS.md prefers a second module over editing root `go.mod`. Use `kcp/go.mod` with a `replace sigs.k8s.io/cluster-api => ../` so it can import `core/reconcilers/*` etc. without touching root `go.mod`. Confirm this doesn't break `go work`/CI tooling. |
| D3 | APIExport/APIBinding schema strategy | Which CRDs get published (core v1beta1+v1beta2, addons, ipam, bootstrap, controlplane), how permission claims for `Secret`/`ConfigMap` (kubeconfigs, CRS resources) are requested/accepted. Permission-claim mechanism for *provider* CRDs (the reciprocal problem — core reading/writing provider-owned objects) is decided in [ADR-0001](adr-0001-provider-api-permissions.md); still open here: the `Secret`/`ConfigMap` claim scope specifically. |
| D4 | Discovery + cache engine: adopt `kcp-dev/multicluster-provider`'s `Provider` + `multicluster-runtime`'s `mcmanager.Manager.GetManager()`, or hand-roll | Default to adopting (see "Chosen model"/"Scalability" above). Confirm write-path routing, leader-election behavior, and library maturity in the Phase 1 spike before locking this in; hand-rolling a `WildcardCache`-alike layer is the documented fallback, not the default. |
| D5 | Identity/RBAC model | How each per-workspace manager authenticates (one system identity via the APIExport virtual workspace vs. per-workspace impersonation). **Decided:** single system identity via the virtual workspace — see [ADR-0001](adr-0001-provider-api-permissions.md). |
| D6 | Partition topology for horizontal sharding: how many `Partition`/`APIExportEndpointSlice` pairs, how they map to replica-group deployments | Use kcp's own `PartitionSet`→`Partition`→`APIExportEndpointSlice` chain (see "Chosen model"/"Scalability" above) rather than app-level hashing. Only matters once a kcp install runs multiple shards — moot for single-shard dev/small deployments, so this can start as "one partition, one replica-group" and grow later. |

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

**Status: G1, G2 and G5 done; G3 deferred with its trigger recorded; G4
outstanding.** See
[Per-workspace wiring](site/content/en/docs/design/per-workspace-wiring.md)
for the contract and the reasoning, and
[`specs/20260815-185524-per-workspace-wiring`](../specs/20260815-185524-per-workspace-wiring/spec.md)
for the specification it was built against.

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

| Track | Scope | Depends on |
|---|---|---|
| P1 | `kcp/cmd/kubeadm-bootstrap-manager`: port `bootstrap/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P2 | `kcp/cmd/kubeadm-control-plane-manager`: port `controlplane/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P3 | `kcp/cmd/docker-infrastructure-manager`: port `test/infrastructure/docker/main.go` wiring onto G2 (needed for dev/e2e, not for production) | G2, G3 |
| P4 | Webhook wiring for all 4 providers through G4 | G4, P1–P3 (can stub against G4's interface early) |
| P5 | `clusterctl` workspace-awareness: teach `cmd/clusterctl` to target a `clusters/<path>` kubeconfig context (flag/env plumbing only — clusterctl is a client, not a controller, so this track has no dependency on P1–P4) | G3 |
| P6 | APIExport/APIBinding manifests + permission-claim wiring per D3, plus the default single-partition `APIExportEndpointSlice` (D6's starting point — no `Partition`/`PartitionSet` needed until multi-shard). Per [ADR-0001](adr-0001-provider-api-permissions.md): includes the self-maintaining permission-claim-list controller and the `Maintain`-lifecycle `WorkspaceType` tenants use to onboard to CAPI. | D3 (Phase 0 only) |
| P7 | RBAC/identity provisioning per D5 | D5 (Phase 0 only) |
| P8 | `kcp/test` e2e harness: multi-workspace kind+kcp suite exercising Cluster→Machine across ≥2 workspaces concurrently, proving tenant isolation | Phase 1 skeleton (can stub P1–P3 initially) |
| P9 | Observability: workspace label/attribute injection into controller-runtime metrics, logs, and `kubebuilder:rbac` marker aggregation across the 4 new binaries | G2 |
| P10 | User-facing docs (`kcp/docs/`): deployment guide, APIExport binding walkthrough | Can be written incrementally alongside every other track |

P1–P3 are mechanically identical to Phase 1's core-provider port, so
they're the safest to parallelize across multiple agents/contributors at
once — same recipe, different source `main.go`.

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
2. Whether `ClusterCache` (the *workload*-cluster remote-client cache,
   orthogonal to KCP workspaces) needs any workspace-awareness at all, or
   whether per-workspace-manager isolation already makes it a non-issue
   (current read: non-issue, since each workspace's Cluster objects and
   their kubeconfig Secrets are already workspace-scoped by virtue of
   which manager is reading them).
3. How many workspaces this needs to scale to in practice — determines how
   urgent D6 (horizontal sharding) and Phase 4's eviction work are
   relative to Phase 3.
4. Whether `multicluster-provider`'s `Provider` reacts live to
   `APIExportEndpointSlice.status.endpoints` changing (e.g. a shard
   joining/leaving a `Partition`, or a workspace migrating between
   shards), or requires a restart to pick up a new endpoint set — drives
   whether D6's partition topology can change without a rollout.

## Executing this plan: what's dispatchable now vs. what needs a human first

This doc is written to be handed to multiple contributors/agents, but not
uniformly — some of it is ready for direct autonomous dispatch today, and
some of it isn't, yet.

**Needs a human decision before any agent touches it:** D1 (kcp version to
pin), D3 (APIExport schema + permission-claim scope), and D5
(RBAC/identity model) are written as options to weigh, not answers. An
agent hand this without a resolved ADR either stalls or silently picks a
default — and for D3/D5 specifically, a wrong silent default is a
security decision made without review (permission-claim scope creep, an
identity model that's more permissive than intended), not just a rework
cost. Get these into the `kcp/docs/adr-0001-*.md` as actual decisions
before dispatching Phase 1, and don't let an agent write that ADR
unsupervised.

**Keeps a human review checkpoint regardless of who writes the code:**
G4 (webhook dispatch) and anything implementing D5. G4 is explicitly
flagged above as "least well-understood," and Phase 4 separately flags it
as the one component where a bug is a cross-tenant bleed, not an ordinary
bug — don't let an agent's plausible-looking implementation of it merge
without a human (or a dedicated security review pass) checking the
workspace-resolution logic specifically, independent of normal code
review.

**Ready for dispatch now:** Phase 1, as one closely-watched session (it's
the spike everything else depends on — don't parallelize it). Once Phase
0's ADR and Phase 1 land: P1–P3 and P5 are the best-shaped tasks in this
doc for parallel agent dispatch — "port `<provider>/main.go`'s wiring onto
G2, same recipe as Phase 1's core-provider port" is concrete and bounded,
and each track lives in its own `kcp/cmd/<name>/` directory, so agents
working them simultaneously won't collide on files.

**Before fanning Phase 2/3 out to multiple agents with no shared
context:** pin G1–G3's behavioral descriptions into actual Go interface
signatures first (a types-only skeleton, landed as its own small PR) —
right now they're prose ("turns a workspace path + config into a
`*rest.Config`"), which is fine for a human but leaves room for two
agents to independently build incompatible shapes for the same seam. Add
a one-line acceptance check per G/P item as each is concretized (a test
or a minimal manual verification command), so an agent has an unambiguous
done-condition instead of "compiles."
