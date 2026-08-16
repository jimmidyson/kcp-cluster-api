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

## R7 — Per-workspace telemetry without unbounded cardinality — RESOLVED (T004)

**Decision: bounded top-N with an aggregate remainder.** Track counters
internally for every engaged workspace; export aggregates unconditionally, plus
a labelled series for the busiest N workspaces and one remainder series
aggregating everything else. N is configurable with a small default.

**Rationale — the two consumers want different things.** This is what makes the
asymmetry principled rather than a compromise:

- Capacity and scaling decisions (FR-028, FR-032) want *totals*. They need no
  per-workspace breakdown at all, so they are served by cardinality-free
  aggregates.
- Diagnosis (SC-008) wants to know *which workspace is hot*, which in practice
  means the outliers. Nobody diagnoses a fleet by reading one series per tenant;
  the long tail has no diagnostic value and unbounded cost.

Bounded top-N is the only one of the three candidates that satisfies "cardinality
bounded as workspace count grows" **by construction** rather than by an operator
remembering to configure it.

**Alternatives considered and rejected:**

- *Full labelling with an operator-set cap and shedding.* Rejected: which series
  get shed is arbitrary, so the hot workspace — the one series that matters —
  can be the one dropped. Making shedding load-aware turns it into top-N, so
  this is either worse than top-N or identical to it.
- *Aggregate metrics plus log/exemplar attribution.* Rejected as the primary
  mechanism: logs cannot be alerted on cheaply and answering "which workspace is
  hot" requires log-aggregation infrastructure this project cannot assume.
  Retained as a *complement* — structured logs still carry the workspace, which
  is how the long tail stays diagnosable when it matters.

**Consequence that must be handled, not assumed away:** top-N membership
changes, and a metrics client will happily keep exporting a series for a
workspace that has dropped out — so displaced series must be actively deleted on
each refresh, and deleted on disengagement. Otherwise "bounded" becomes "bounded
in the top N, unbounded in the residue", which is the original problem wearing a
hat. This is asserted by test rather than left to review.

**Refresh is periodic, not per-event.** Recomputing a ranking on every reconcile
would put the cost of attribution into the hot path this feature exists to make
cheaper.

---

## R7 (original statement) — Per-workspace telemetry without unbounded cardinality

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

## R12 — G4 spike: can an admission request be resolved to its workspace? — VERIFIED, answer is yes

**Question** ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A3, task
T078a): does an incoming request carry enough identity to resolve it to its
source workspace? This gates the appliance roadmap, because an appliance that
cannot serve admission for its own tenants is not an appliance.

**Answer: yes, and G4 is contained work rather than a redesign.** Verified
against kcp v0.32.3 and its forked apiserver at the pinned revision.

### The fan-in is kcp's design, not an accident

For a type provided through an `APIBinding`, kcp looks up the
`ValidatingWebhookConfiguration` / `MutatingWebhookConfiguration` in **the
`APIExport`'s workspace, not the consumer's** —
`getSourceClusterForGroupResource` returns
`apiBinding.Status.APIExportClusterName` for any group-resource that came from a
binding (kcp `pkg/admission/validatingwebhook/plugin.go:166-181`, and the same
in `mutatingwebhook/plugin.go:166`).

So **one** webhook configuration in this project's provider workspace serves
**every** consuming workspace. That is exactly the fan-in G4 must handle, and it
is intended behaviour rather than something to work around.

### The logical cluster is on the object

Two complementary mechanisms, which together cover every operation:

1. **From storage, always.** `annotateDecodedObjectWith` sets
   `kcp.io/cluster` (and the shard annotation) on every object decoded from
   etcd — `k8s.io/apiserver` fork `pkg/storage/etcd3/store_kcp.go:169-193`,
   called from `store.go:1182`, `store.go:1205`, `watcher.go:738` and
   `watcher.go:760`. The comment states the design outright: *"we don't store
   the cluster name and the shard name in the objects in storage. Instead, they
   are derived from the storage key, and then applied after retrieving the
   object from storage."*
2. **On create, explicitly.** A create has no stored object, so kcp sets the
   annotation on the incoming object before dispatching to the webhook and undoes
   it afterwards — `SetClusterAnnotation`, called at
   `validatingwebhook/plugin.go:132-141` and `mutatingwebhook/plugin.go:133-138`.

Consequence: `oldObject` always carries the annotation (it came from storage),
and `object` carries it on create. **The resolution rule is: read
`kcp.io/cluster` from `request.object`, falling back to `request.oldObject`.**

### Conversion also carries it — and needs it less than expected

`ConversionReview` objects carry the same annotation, because kcp delegates
every non-`apis.kcp.io` group to the stock converter
(`pkg/server/conversion_webhook.go:60-68`), which passes the raw objects through
untouched (`apiextensions-apiserver` fork
`pkg/apiserver/conversion/webhook_converter.go:132-142` — no cluster handling at
all), and the objects were annotated at storage decode.

Two further findings about conversion:

- **It is mandatory, not optional.** A schema with multiple versions and no
  conversion strategy is a hard error — `apibinding_reconcile.go:792`. Cluster
  API serves `v1beta1` and `v1beta2`, so this is forced.
- **The client config accepts a bare URL only** —
  `apibinding_reconcile.go:801-808` sets `ClientConfig.URL` with no service
  reference, confirming ADR-0001's note.

### G4's real surface is one handler

The blanket refusal in `SetupWebhooks` is conservative, and correctly so given
that controller-runtime silently skips an already-registered path. But the
actual tenancy hazard is confined to handlers holding workspace-scoped state,
and in the currently wired set that is **exactly one**:

| Handler | Workspace state | Safe to serve all workspaces from one registration? |
|---|---|---|
| `coreadmission.Cluster` | `Client client.Reader`, `ClusterCacheReader` (`core/webhooks/admission/cluster.go:75-80`) | **No** — this is G4's actual case |
| `coreadmission.Machine` | `struct{}` (`machine.go:52`) | Yes — stateless |
| `infrawebhooks.DevCluster` | `struct{}` (`devcluster.go:34`) | Yes — stateless |
| `infrawebhooks.DevMachine` | `struct{}` (`devmachine.go:31`) | Yes — stateless |
| conversion (`/convert`) | scheme + converter registry only (`controller-runtime` `pkg/builder/webhook.go:317`, `conversion.NewWebhookHandler`) | Yes — pure function of the object |

**This corrects a claim made in ADR-0002 before this spike ran**: that an
appliance cannot serve the `v1beta1`↔`v1beta2` conversion webhook. It can.
controller-runtime's conversion is Hub/Spoke scheme-based and holds no client,
so it is already multi-tenant-safe. The ADR has been amended.

### What G4 therefore is

Not new kcp capability, and not a redesign. It is: register the webhook paths
once for the process rather than per workspace, and give the one stateful
handler a client resolved **per request** from the object's `kcp.io/cluster`
annotation, looked up in the pool of engaged workspaces. The current obstacle is
that controller-runtime's builder binds `mgr.GetClient()` at registration time —
a wrapper that resolves per request replaces that binding.

### Remaining unknowns — these belong to G4's own design, not to this spike

1. **A request for a workspace that is not engaged.** Needs an explicit policy;
   it must fail closed, because the alternative is serving a tenant with another
   tenant's client — the exact failure Principle VIII was written about.
2. **Requests arriving during engagement.** The webhook server is process-wide
   and starts before any workspace engages.
3. **A blind `PUT` whose `object` lacks the annotation.** The `oldObject`
   fallback covers it, but the rule must be written down rather than assumed.
4. **Not yet demonstrated by a live test.** Principle V accepts source
   verification, and this is source verification. A test asserting the
   annotation's presence on a real `AdmissionReview` and `ConversionReview`
   should still be G4's first TDD cycle, since the cost of being wrong here is
   cross-tenant bleed.

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
| G4 workspace resolution (R12) | VERIFIED — identity is present | Live test of a real `AdmissionReview` as G4's first TDD cycle |
