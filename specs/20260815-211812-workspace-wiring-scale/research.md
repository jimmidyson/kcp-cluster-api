# Phase 0 Research: Workspace wiring that scales to a large fleet

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md)

Constitution Principle V requires that a design claim about a dependency be
verified against that dependency's source, or demonstrated by a test, before
code is built on it — and that documents distinguish what is **verified** from
what is **assumed**, recording how each verified claim was checked.

Every entry below is tagged accordingly:

- **VERIFIED** — read in the dependency's source at the pinned version, with
  file and line recorded. Safe to build on.
- **ASSUMED** — plausible, not yet checked. MUST NOT be built on without a
  spike; each carries the spike that would settle it.
- **OPEN** — a question with no answer yet, carrying its resolution path.

---

## R1 — The cache is substitutable at `mgr.GetCache()` — VERIFIED

**Decision**: satisfy the gated event-delivery requirements (FR-001, FR-004,
FR-005) by interposing a cache at the `GetCache()` seam, not by changing
upstream.

**How verified**: read at `controller-runtime@v0.24.1`:

- `pkg/builder/controller.go:323`, `:351`, `:368` — every watch the builder
  constructs takes `blder.mgr.GetCache()`. There is no other path from a
  reconciler's `SetupWithManager` to an informer.
- `pkg/internal/source/kind.go` — `Kind.Start` uses only
  `Cache.GetInformer(ctx, type)`, then `informer.AddEventHandlerWithOptions`,
  then polls the returned registration's `HasSynced()`, then
  `Cache.WaitForCacheSync(ctx)`. Every one of those is an interface method.
- `sigs.k8s.io/cluster-api@v1.15.0-kcp.1` `controllers/external/tracker.go:47-64`
  — the dynamic watches added at runtime for `infrastructureRef` /
  `bootstrap.configRef` go through `ObjectTracker.Cache`, which every wired
  reconciler assigns from `mgr.GetCache()` in its `SetupWithManager`.

**Rationale**: `internal/providerwiring`'s `workspaceManager` already interposes
a manager, overriding `Add` and `GetWebhookServer`. Overriding `GetCache` is the
same kind of move at the same seam, so this satisfies Principle II (public
extension points) and adds no `DRIFT.md` entry — FR-023 holds.

**Alternatives considered**:

- *Rewrite reconcilers against `mcreconcile.Request`/`mcbuilder`* — the
  multicluster-runtime-native model, one controller per type for all
  workspaces. Rejected: it means not using upstream reconcilers unmodified,
  which is the premise of the whole repository (AGENTS.md, Principle I).
- *Patch `client-go`'s `sharedProcessor` to index listeners by cluster* —
  would fix it for everyone, but is a drift entry against a vendored
  Kubernetes library, far outside this project's fork contract.
- *Propose the hook upstream* — worth doing regardless (see R9), but cannot
  gate this feature on an upstream release cycle.

---

## R2 — Per-cluster initial sync can avoid the fleet-wide replay — VERIFIED

**Decision**: an interposed cache serves a new workspace's initial sync from the
existing indexer, scoped to that cluster.

**How verified**:

- `client-go@v0.36.3` `tools/cache/shared_informer.go:918-934` — registering a
  handler on an already-started informer takes `s.blockDeltas.Lock()` and
  enqueues **every** object in the store into the new listener. This is both the
  fleet-wide cost and the process-wide stall.
- `multicluster-provider@v0.8.0` `pkg/cache/wildcard.go:81-88` — the wildcard
  cache adds `kcpcache.ClusterIndexName` and
  `kcpcache.ClusterAndNamespaceIndexName` to every informer it creates.
- `pkg/cache/forked_cache_reader.go:167-176` — reads already use
  `indexer.ByIndex(ClusterIndexName, ...)`, so the index is populated and
  correct.

So the objects a joining workspace needs are retrievable by cluster in
O(N_workspace), without touching `blockDeltas` and without the other
workspaces' objects.

**Consequence for FR-003**: registrations become map entries rather than
`processorListener`s, which is where the two goroutines
(`shared_informer.go:1063-1064`) and the 1024-slot ring buffer (`:1279`) per
registration go away.

**Open sub-question — see R6**: `HasSynced` semantics for a registration that
syncs per-cluster rather than per-informer.

---

## R3 — The sharded coordinator exists and is pinned — VERIFIED (behaviour ASSUMED)

**Decision**: attempt FR-013/FR-014/FR-015 as configuration of
`multicluster-runtime`'s sharded coordinator rather than as new mechanism.

**How verified (existence and shape)**: `multicluster-runtime@v0.24.1`
`pkg/manager/coordinator/sharded/` — `coordinator.go` (decision loop),
`sharder/hrw.go` (highest-random-weight assignment), `peers/registry.go`
(Lease-based membership), `leaseguard.go` (fencing), `options.go`
(`WithShardLease`, `WithPerClusterLease`, `WithPeerRegistry`,
`WithLeaseTimings`, `WithSynchronizationIntervals`). `pkg/manager/manager.go:158`
shows the coordinator is pluggable and defaults to `basic.New()`, which is what
`cmd/core-manager` gets today.

**ASSUMED, and requiring a spike before FR-015 is claimed**:

1. That per-cluster Lease fencing actually prevents two replicas reconciling one
   workspace during handover, including the partitioned-replica case named in
   the spec's edge cases. Fencing correctness is exactly the kind of claim
   Principle V exists for, and a type signature does not establish it.
2. That the sharded coordinator composes with the kcp `apiexport` provider.
   The provider drives engagement from its own endpoint watcher
   (`multicluster-provider` `pkg/provider/provider.go:441-460`); whether the
   coordinator's decision loop and the provider's `Engage` path interact
   correctly is unverified.
3. Whether leader election must be disabled when the sharded coordinator is
   used, and what happens to the webhook workspace (which is elected-leader
   dependent in `cmd/core-manager/main.go:217`) if it is.

**Spike**: `test/integration` case running two managers with the sharded
coordinator against one kcp, asserting disjoint engagement and no double
reconcile across a killed replica. This is the P6 entry criterion.

---

## R4 — Replicas do not divide cached state — VERIFIED

**Decision**: state this as a documented limit (FR-016) rather than trying to
engineer around it.

**How verified**: `controller-runtime@v0.24.1` `pkg/manager/runnable_group.go:75-78`
routes any runnable implementing `hasCache` into the `Caches` group.
`pkg/manager/internal.go:446` starts that group; leader-election runnables do
not start until `:477`, after election is won. So a standby replica has already
started and synced its caches.

Combined with the wildcard cache covering an endpoint slice rather than a
replica's share, every replica holds every workspace in its slice.

**Consequence**: "replicas per shard" is a throughput and availability knob
only. The memory limit is "workspaces per shard", and only endpoint-slice or
`Partition` splitting divides that. This is why the spec's deployment model
carries two limits, and why FR-016 requires them documented as distinct.

---

## R5 — Sharing a REST mapper has no clean seam — VERIFIED, and is a Principle II finding

**Decision**: do **not** work around this. Record it, raise it, and treat FR-008
as blocked on the response.

**How verified**: `multicluster-provider@v0.8.0`:

- `pkg/cache/cluster.go:66` — `NewScopedCluster` calls
  `apiutil.NewDynamicRESTMapper(cfg, httpClient)` per workspace.
- `pkg/provider/provider.go:121` — `Options.NewCluster` is an injection point,
  but its default is `mcpcache.NewScopedCluster` and the field's type is the
  concrete `*ScopedCluster` constructor signature, not an interface returning
  `cluster.Cluster`.
- `pkg/cache/cache.go:38` (`scopedCache`) and
  `pkg/cache/forked_cache_reader.go` (`cacheReader`) are unexported.

So substituting a cluster implementation that shares a mapper would require
reimplementing the 323-line forked cluster-aware cache reader in this
repository — copying upstream code, which Principle I treats as divergence and
Principle II tells us to raise rather than route around.

**Response per Principle II** (find another integration point / propose
upstream / accept the limitation):

- **Preferred**: propose upstream that `multicluster-provider` accept an
  optional `RESTMapper` in `Options` (or that `Options.NewCluster` return
  `cluster.Cluster`). Small, obviously correct, benefits every consumer.
- **Fallback**: accept the limitation and record it. Per-workspace discovery is
  a startup cost, not a steady-state one, and R10's measurement will say whether
  it binds at all.

**FR-008 is therefore gated twice**: by the measurement gate, and by this
finding. It MUST NOT be implemented by copying upstream code.

---

## R6 — `HasSynced` for a per-cluster registration — OPEN

**Question**: `source.Kind.Start` blocks on `handlerRegistration.HasSynced()`
(`pkg/internal/source/kind.go`) and then on `Cache.WaitForCacheSync(ctx)`. An
interposed registration must report synced only once that cluster's replay is
complete, or a controller starts reconciling against a partial view; and it must
not report unsynced forever, or the controller never starts.

Today `scopedCache.WaitForCacheSync` delegates to the whole wildcard cache
(`multicluster-provider` `pkg/cache/cache.go:49`), which is correct but coarse.

**Resolution path**: unit tests against a fake informer covering: replay
completes → synced; replay racing disengagement → returns rather than hanging;
registration created before the underlying informer syncs → waits for both. This
is the first TDD cycle of the demux work, not a design decision to make on
paper.

---

## R7 — Per-workspace telemetry without unbounded cardinality — OPEN

**Question**: FR-017 wants reconcile and queue telemetry attributable to a
workspace, with volume and cardinality bounded as workspace count grows. Those
pull against each other: a workspace label on controller-runtime's existing
metrics is exactly an unbounded label.

Note the current state is the opposite failure: `controllerOptions` sets
`SkipNameValidation` (`internal/coremanager/setup.go:297`), so every workspace's
controllers share a name and metrics aggregate across tenants with no
attribution at all.

**Candidates to evaluate in P1**:

1. Bounded top-N: track per-workspace counters internally, export only the
   heaviest N as labelled series plus an aggregate remainder. Bounded by
   construction; loses the long tail.
2. Exemplar/log attribution: keep metrics aggregate, attribute via structured
   logs and a queryable endpoint. Cheapest, weakest for alerting.
3. Full labelling with an operator-set cardinality cap and explicit shedding.

**This is a genuine design decision and belongs in P1's design task, not
here.** It is also a prerequisite for the harness: the harness cannot report
per-workspace load it cannot observe.

---

## R8 — Aggregate backpressure — ASSUMED

**Assumption**: a shared rate limiter can be injected across per-workspace
clients by setting `QPS`/`Burst` and a shared `flowcontrol.RateLimiter` on the
`rest.Config` the provider copies per workspace.

**Known**: `controller-runtime@v0.24.1` `pkg/client/config/config.go:101` sets
`QPS = -1`, disabling client-side limiting; `multicluster-provider`
`pkg/cache/cluster.go:43` does `rest.CopyConfig(cfg)` per workspace.

**Unverified**: whether `rest.CopyConfig` preserves a shared `RateLimiter`
pointer such that one limiter governs all workspaces, or whether copying
detaches it. This determines whether FR-019 is a configuration change or needs
another mechanism.

**Spike**: read `rest.CopyConfig` and `rest.RESTClientFor` at the pinned
client-go, then a unit test asserting two clients built from copies of one
config share a limiter's token bucket.

---

## R9 — Upstream proposals this feature should file

Principle I requires carried divergence to trend to zero, and Principle II
prefers proposing a hook to working around its absence. Nothing here is a
`DRIFT.md` entry (this feature adds no upstream patch), but two gaps are worth
filing regardless:

1. **`multicluster-provider`**: allow a caller-supplied `RESTMapper`, or make
   `Options.NewCluster` return `cluster.Cluster` — see R5.
2. **`client-go`**: `sharedProcessor.distribute` fans every event to every
   listener, and handler registration replays the whole store under
   `blockDeltas`. An informer that could index listeners by a caller-supplied
   key would remove the need for R1's interposition entirely. This is a large
   ask with a long horizon; filing it does not gate this feature.

**Neither blocks delivery.** Recorded so the workaround is a known temporary,
not a silent permanent.

---

## R10 — Can the environment host a meaningful sweep? — OPEN, and gates everything

**Question**: FR-030 needs a sweep across geometrically spaced workspace counts
on a real kcp server (Principle III forbids validating this on vanilla envtest,
which has no logical clusters). How many workspaces can the CI and development
environments actually create and bind, and how long does creating them take?

This is the single biggest risk in the plan. If the environment tops out at a
number well below any interesting knee, the sweep cannot locate it, and:

- FR-022 requires this be reported as **"could not run"**, not as a pass.
- The gated requirements cannot get a determination from measurement, and the
  spec's Known Deviations already anticipate bounds being stated at the largest
  size actually measured.

**Resolution path — first task in P2, before harness design is fixed**: measure
workspace creation and binding cost against a real kcp
(`internal/kcpfixtures` already has `PublishAPIExport`, `BindExport`,
`WaitForAPIExportEndpointSlice`). Report the achievable ceiling. If it is low,
the harness design must lean on synthetic load against fewer workspaces plus
explicit extrapolation, with the extrapolation labelled as such.

**Do not design the harness before this number exists.**

---

## R11 — Reuse the existing three-outcome contract — VERIFIED

**Decision**: the harness reports through `internal/verify`'s existing types,
not a new convention.

**How verified**: `internal/verify/verify.go:49-110` already defines
`ExitCouldNotRun = 2`, `Outcome` with `OutcomePass` / `OutcomeFail` /
`OutcomeCouldNotRun` (whose `String()` is `"could not run"`), and `Capability`,
`Step`, `Result`. `bin/verify-result.json` is the existing machine-readable
sink, and AGENTS.md instructs readers to take the outcome from there rather than
from an exit status.

FR-022 is therefore satisfied by reuse. Inventing a parallel reporting scheme
would violate Principle IV's intent and create two contracts where one exists.

---

## Summary: what is safe to build on

| Area | Status | Gate before building |
|---|---|---|
| Cache interposition at `GetCache()` (R1, R2) | VERIFIED | Measurement gate only |
| Three-outcome reporting reuse (R11) | VERIFIED | None |
| Replicas do not divide cache (R4) | VERIFIED | None — it is a documentation duty |
| Sharded coordinator (R3) | Exists; behaviour ASSUMED | Integration spike on fencing + provider composition |
| Backpressure via shared limiter (R8) | ASSUMED | Source read + unit test |
| `HasSynced` semantics (R6) | OPEN | First TDD cycle of demux work |
| Telemetry cardinality (R7) | OPEN | Design task in P1 |
| REST mapper sharing (R5) | VERIFIED as blocked | Principle II finding — raise, do not work around |
| Environment sweep ceiling (R10) | OPEN | **Blocks harness design and the whole gate** |
