# Plan of attack: making Cluster API KCP-workspace-aware

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

## Why the "obvious" approaches don't fit here

- `sigs.k8s.io/multicluster-runtime` + `github.com/kcp-dev/multicluster-provider`
  is the standard way to make a controller-runtime controller
  workspace-aware today, but it requires controllers to be written against
  `mcreconcile.Request` / `mcbuilder` and to pull a per-workspace
  `cluster.Cluster` out of an `mcmanager.Manager`. Every reconciler in this
  repo (`core/reconcilers/*`, `bootstrap/kubeadm`, `controlplane/kubeadm`,
  `test/infrastructure/docker/reconcilers`) is plain controller-runtime:
  a struct with a `client.Client` field and `SetupWithManager(ctx,
  mgr ctrl.Manager, opts)`. We can't rewrite them (upstream is read-only),
  and we can't pass an `mcmanager.Manager` where a `ctrl.Manager` is
  expected — it's a different type.
- A single wildcard cache + a context-injecting client wrapper (the
  pre-multicluster-runtime kcp pattern) would let one process watch all
  workspaces at once, but every upstream `SetupWithManager` builds its own
  watches straight off `mgr.GetCache()`/`mgr.GetClient()`. We have no hook
  to make *that* cache workspace-partitioned without editing upstream
  controller registration code, which AGENTS.md rules out.

## Chosen model: one real, unmodified `ctrl.Manager` per workspace

Every upstream provider's `main.go` already reduces to a pure function of
a `*rest.Config`:

```
restConfig -> ctrl.NewManager -> setupReconcilers(ctx, mgr) -> setupWebhooks(ctx, mgr)
```

`setupReconcilers` does nothing but assign `mgr.GetClient()` into exported
reconciler structs (`cluster.Reconciler{Client: ...}`, etc.) and call their
exported `SetupWithManager`. That is precisely the "own manager
entrypoint" + "inject a client into upstream constructors" extension
point AGENTS.md calls out.

So: for each KCP workspace that has Cluster API bound (via APIBinding to
an APIExport we publish), `kcp/` spins up a **complete, ordinary,
unmodified-from-upstream's-perspective `ctrl.Manager`** whose `rest.Config`
just happens to point at that workspace's cluster path
(`<kcp-front-proxy>/clusters/<workspace-path>`), and wires it exactly like
`core/main.go` does today, calling the same exported
`core/reconcilers/*`/`core/webhooks/*` (and bootstrap/controlplane/docker
equivalents) constructors. No wrapped client, no context plumbing, no
upstream edits — just N managers instead of 1, supervised by our own
process.

This trades some memory/goroutine overhead (a cache + workqueues per
workspace) for zero upstream coupling. Mitigations for that cost are in
Phase 4 below; they're explicitly deferred so Phase 1–3 can prove
correctness first.

Webhooks are the one place a single shared process is cheaper than N: a
`ValidatingWebhookConfiguration`/`ConversionWebhook` request is stateless
per call, so a single webhook server can serve every workspace if it
resolves *which* workspace each `AdmissionReview` is for (from the request
path/host kcp routes it through) and looks up the right workspace-scoped
client on demand, rather than needing one webhook server per workspace.
This needs a spike (G6 below) to pin down exactly how the target kcp
version identifies the source workspace on an incoming webhook call.

## Phase 0 — decisions (groundwork, sequential, do first)

These block essentially everything else; they're cheap to get wrong
early and expensive to unwind later.

| # | Decision | Notes |
|---|---|---|
| D1 | Target kcp version + client libs to pin (`kcp-dev/kcp`, `kcp-dev/client-go`, `kcp-dev/multicluster-provider` for discovery only) | Record in an ADR under `kcp/docs/`. |
| D2 | Go module layout for `kcp/` | AGENTS.md prefers a second module over editing root `go.mod`. Use `kcp/go.mod` with a `replace sigs.k8s.io/cluster-api => ../` so it can import `core/reconcilers/*` etc. without touching root `go.mod`. Confirm this doesn't break `go work`/CI tooling. |
| D3 | APIExport/APIBinding schema strategy | Which CRDs get published (core v1beta1+v1beta2, addons, ipam, bootstrap, controlplane), how permission claims for `Secret`/`ConfigMap` (kubeconfigs, CRS resources) are requested/accepted. |
| D4 | Workspace discovery mechanism | Use `kcp-dev/multicluster-provider`'s `Provider` purely as a discovery/lifecycle engine (workspace bound/unbound callbacks) driving our own manager-pool supervisor — not as the reconciler-facing API. |
| D5 | Identity/RBAC model | How each per-workspace manager authenticates (one system identity via the APIExport virtual workspace vs. per-workspace impersonation). |

**Output of Phase 0:** a short ADR (`kcp/docs/adr-0001-per-workspace-manager-pool.md`)
that the rest of the plan links back to.

## Phase 1 — walking skeleton (groundwork, sequential, small team)

Prove the model end-to-end against a *single, hardcoded* workspace before
building the harder dynamic multi-workspace machinery. This is the
highest-risk unknown in the whole plan (do upstream's extension points
really compose the way AGENTS.md assumes?), so de-risk it first and alone.

1. `kcp/go.mod` skeleton per D2, plus a `kcp/hack/` dev-loop: a local kcp
   instance (kind or `kcp-dev/kcp`'s own quickstart), one APIExport for
   the core CRDs, one workspace bound to it.
2. `kcp/cmd/core-manager/main.go`: copy `core/main.go`'s
   `setupReconcilers`/`setupWebhooks` *wiring* (not the upstream file —
   new code under `kcp/` that imports the same exported packages), but
   build `restConfig` from a `--workspace-path` flag instead of
   `ctrl.GetConfigOrDie()`.
3. Verify a full `Cluster` → `Machine` reconcile loop works against the
   `test/infrastructure/docker` provider (also single-workspace, hardcoded)
   inside that one workspace, including at least one admission webhook and
   the conversion webhook.

**Exit criteria:** a Cluster gets provisioned end-to-end inside a KCP
workspace using entirely unmodified upstream reconciler/webhook code.
This is the thing to demo before greenlighting Phase 2's investment.

## Phase 2 — shared infrastructure (groundwork, sequential-ish)

Everything Phase 3's parallel tracks import. Small surface, high fan-out —
keep it minimal and get it merged before fanning out.

- G1. **Workspace discovery + lifecycle** (`kcp/internal/discovery`):
  watches APIBindings (or the APIExport virtual workspace) for
  workspaces gaining/losing our export, emits typed
  add/remove events.
- G2. **Manager-pool supervisor** (`kcp/internal/managerpool`): consumes
  G1's events, calls a pluggable `func(ctx, workspaceRESTConfig) (stop
  func(), error)` per workspace, handles crash-restart, and graceful
  shutdown. Each of the 4 provider binaries plugs in its own
  `setupReconcilers`/`setupWebhooks` as that callback.
- G3. **Workspace-scoped `rest.Config` builder** (`kcp/internal/kcpclient`):
  turns a workspace path + base kcp front-proxy config into a
  `*rest.Config`, shared by G2 and clusterctl (P6).
- G4. **Webhook dispatch layer** (`kcp/internal/webhookrouter`): the spike
  from Phase 0/D-notes, resolving incoming `AdmissionReview`s to a
  workspace and handing back the right workspace-scoped client to
  upstream's `SetupWebhookWithManager`-registered handlers. This is the
  least well-understood piece — budget spike time before committing to an
  interface shape.
- G5. **New CI workflow** (`.github/workflows/kcp-pr-verify.yaml`, new
  file only): build/vet/lint `kcp/go.mod`, run Phase 1's walking-skeleton
  test as an integration job (kind + kcp).

G1–G3 can be built in parallel once the Phase 0 ADR is settled (they only
share the discovery event type). G4 depends on nothing but the ADR and
can also start immediately. G5 can start as soon as `kcp/go.mod` exists
(end of Phase 1).

## Phase 3 — parallel fan-out

Once G2 (manager-pool supervisor) and G3 (rest.Config builder) exist,
these tracks are independent of each other and can be owned by different
people/agents simultaneously. Each one is "repeat Phase 1's port for a
different binary/concern."

| Track | Scope | Depends on |
|---|---|---|
| P1 | `kcp/cmd/kubeadm-bootstrap-manager`: port `bootstrap/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P2 | `kcp/cmd/kubeadm-control-plane-manager`: port `controlplane/kubeadm/main.go` wiring onto G2 | G2, G3 |
| P3 | `kcp/cmd/docker-infrastructure-manager`: port `test/infrastructure/docker/main.go` wiring onto G2 (needed for dev/e2e, not for production) | G2, G3 |
| P4 | Webhook wiring for all 4 providers through G4 | G4, P1–P3 (can stub against G4's interface early) |
| P5 | `clusterctl` workspace-awareness: teach `cmd/clusterctl` to target a `clusters/<path>` kubeconfig context (flag/env plumbing only — clusterctl is a client, not a controller, so this track has no dependency on P1–P4) | G3 |
| P6 | APIExport/APIBinding manifests + permission-claim wiring per D3 | D3 (Phase 0 only) |
| P7 | RBAC/identity provisioning per D5 | D5 (Phase 0 only) |
| P8 | `kcp/test` e2e harness: multi-workspace kind+kcp suite exercising Cluster→Machine across ≥2 workspaces concurrently, proving tenant isolation | Phase 1 skeleton (can stub P1–P3 initially) |
| P9 | Observability: workspace label/attribute injection into controller-runtime metrics, logs, and `kubebuilder:rbac` marker aggregation across the 4 new binaries | G2 |
| P10 | User-facing docs (`kcp/docs/`): deployment guide, APIExport binding walkthrough | Can be written incrementally alongside every other track |

P1–P3 are mechanically identical to Phase 1's core-provider port, so
they're the safest to parallelize across multiple agents/contributors at
once — same recipe, different source `main.go`.

## Phase 4 — hardening (after Phase 3 lands)

- Scale-out: shard workspaces across replicas (consistent hashing or
  leader-election-per-shard) so one pod isn't holding every workspace's
  manager.
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
Phase 2 (G1 discovery | G2 pool supervisor | G3 restconfig builder | G4 webhook router | G5 CI)  -- mostly parallel
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
3. How many workspaces this needs to scale to in practice — determines
   how urgent Phase 4's sharding/eviction work is relative to Phase 3.
