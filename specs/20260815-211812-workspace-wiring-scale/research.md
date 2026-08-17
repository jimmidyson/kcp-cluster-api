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

## R10 — Can the environment host a meaningful sweep? — RESOLVED (T012/T013)

**Measured**, by `test/integration/scale/ceiling_test.go` against a real kcp
v0.32.3:

| Workspaces | Create p50 / p99 | Bind p50 / p99 | Total p50 | Bind drift, first quarter → last |
|---|---|---|---|---|
| 32 | 533 ms / 737 ms | 516 ms / 519 ms | 1.049 s | 1.141 s → 517 ms (0.45×) |
| 256 | 532 ms / 935 ms | 516 ms / 528 ms | 1.048 s | 594 ms → 517 ms (0.87×) |

**Answer: at least 256 bound workspaces, with per-workspace cost flat.** No
failure was reached at either size; both runs stopped because they hit the
requested target. The p50 is identical at 32 and 256, so nothing in workspace
creation or binding degrades across an order of magnitude.

The drift ratios are **below 1.0** — onboarding got *faster* through the run,
not slower. That is warm-up: the first binds wait for export machinery to
settle. It is worth stating plainly because a naive reading of "cost grows with
fleet size" would predict the opposite, and this is evidence that **kcp's side
of onboarding is not where the quadratic lives**.

### What this does and does not bound

**Does**: the fixture ceiling. A sweep may use geometrically spaced points up to
256 without leaving measured ground.

**Does not**: the controller ceiling. This test runs no manager — it creates and
binds workspaces and stops. The costs this feature exists to find (listener
fan-out, store replay under `blockDeltas`, engagement serialisation) are all in
`core-manager`'s engagement path and are untouched here. **The departure point this feature
is looking for is not in these numbers, and their flatness is not evidence that
it does not exist.**

### Consequences for sweep design

- **Usable points**: 8, 16, 32, 64, 128, 256. Six geometrically spaced points,
  comfortably more than FR-030 needs to project a trend.
- **Cost**: about 1.05 s of setup per workspace, so a full sweep over those
  points is roughly nine minutes of workspace creation before any measurement,
  plus about 25 s of kcp startup per run.
- **Above 256 is unmeasured.** A thousand workspaces would need roughly 17.5
  minutes of setup alone. Reachable, but any capacity figure derived above 256
  is an extrapolation and FR-035 requires it be labelled one.
- Wall clock, not resource exhaustion, is the binding constraint at this scale —
  which is a much better problem to have than the alternative, and means the
  sweep can be designed against the controller's limits rather than the
  fixture's.

---

## R10 (original statement) — Can the environment host a meaningful sweep?

**Question**: FR-030 needs a sweep across geometrically spaced workspace counts
on a real kcp server (Principle III forbids validating this on vanilla envtest,
which has no logical clusters). How many workspaces can the CI and development
environments actually create and bind, and how long does creating them take?

This is the single biggest risk in the plan. If the environment tops out at a
number well below any interesting departure point, the sweep cannot locate it, and:

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

## R14 — First sweep results, and what the harness still cannot see — MEASURED

**Measured** by `test/integration/scale/sweep_test.go` against a real kcp with a
multicluster manager engaged on every workspace. One probe controller per
workspace, one watch on `Cluster`, `MaxConcurrentReconciles: 2`.

| Profile | Workspaces | Heap | Goroutines | Load | Events |
|---|---|---|---|---|---|
| idle-heavy | 8 / 16 / 32 / 64 | 10.2 / 17.0 / 30.5 / 57.6 MiB | 156 / 260 / 468 / 884 | — | 0 |
| active-heavy | 8 / 16 / 32 / 64 | 10.5 / 17.6 / 31.6 / 59.7 MiB | 156 / 260 / 468 / 884 | 46 / 104 / 213 / 393 ms | 8 / 16 / 32 / 64 |

**Both profiles are linear across the swept range, and strikingly so.**

- **Goroutines: exactly 13.0 per workspace** at every step. Deltas are 104/8,
  208/16, 416/32 — the same figure three times, with no drift.
- **Heap: ~867 KiB per workspace** idle, ~900 KiB active. Step deltas of
  850/864/867 KiB show no curvature. The ~33 KiB difference is the profile's ten
  objects.
- **No departure point on either profile** up to 64 workspaces.
- Per-event issue cost is flat: 5.8 / 6.5 / 6.7 / 6.1 ms.
- Telemetry held at **20 labelled series across 64 workspaces**, so the
  cardinality bound holds in practice rather than only in unit tests.

### What these numbers do not establish

Three limits, stated because the flatness is easy to over-read. **The first two
have since been closed by R15**; they are left here rather than edited away,
because what a measurement could not see at the time it was taken is part of
its record.

1. **Event *delivery* cost is not measured.** `LoadDuration` times how long it
   takes to *issue* mutations, and a write returns once the apiserver has
   accepted it. Dispatch to listeners happens afterwards, on the informer's own
   goroutine. The O(W) fan-out this feature is chiefly about therefore does not
   appear in any column above. **This is a gap in the harness, not evidence
   about the system**, and closing it needs delivery latency — write to
   reconcile — rather than write duration. FR-001's determination cannot be made
   from this run. *(Closed by R15.)*
2. **One watch, not nineteen.** The probe registers a single watch; the wired
   Cluster API set registers roughly nineteen across five controllers. The
   listener-driven terms should be expected to scale with that, so 13
   goroutines per workspace is a floor for the wiring, not a figure for
   `core-manager`. *(Closed by R15.)*
3. **64 workspaces, not 256.** The fixture reaches at least 256 (R10), so the
   swept range was limited by measurement time rather than by capability. Any
   figure quoted above 64 is an extrapolation. *(Still open.)*

### A methodological finding about the departure point procedure

The first sweep ran 1, 2, 4, 8 and reported no departure point — but its heap went
6.3 → 9.5 MiB from one workspace to two, then only 9.5 → 10.1 MiB from two to
eight. The first point was dominated by **fixed process cost**, which inflated
the slope projected from the two smallest points and would have hidden a real
departure.

FR-030 specifies projecting from the sweep's two smallest points, so this is a
property of the stated procedure rather than a bug in it. The practical
consequence: **a sweep must start above the warm-up region**, or its projection
encodes one-time cost as if it were marginal. Both reported runs start at 8 for
that reason. This belongs in the harness's guidance, and is a candidate spec
refinement if a later sweep is forced to start small.

---

## R13 — Per-reconcile attribution has no public seam — VERIFIED, Principle II finding

**Question** (from T007): FR-018 wants per-workspace *load*. Engagement counts
and failures are reachable, but attributing individual reconciles to the
workspace that caused them needs a hook into the reconcile path of reconcilers
this project does not own.

**Answer: there is no public seam for it, and the obvious one is explicitly
forbidden.**

`controller.Options` does carry a `Reconciler` field, which looks like exactly
the injection point — set it to a counting wrapper and every reconcile is
attributed. It is not usable. `controller-runtime@v0.24.1`
`pkg/builder/controller.go:394-399`:

```go
if ctrlOptions.Reconciler != nil && r != nil {
    return errors.New("reconciler was set via WithOptions() and via Build() or Complete()")
}
```

Every Cluster API `SetupWithManager` passes its reconciler to `Build` or
`Complete`, so setting `Options.Reconciler` in `controllerOptions()` does not
wrap anything — it makes setup fail outright.

**Responses considered**, per Principle II (another integration point, propose
upstream, or accept the limitation):

- *Wrap the reconciler before Cluster API sees it* — impossible without
  changing upstream, since each `SetupWithManager` constructs its own.
- *Attribute at the cache instead of the reconciler* — reachable, and it is the
  same `GetCache()` interposition R1 describes. **Deliberately not done now**:
  building that machinery under an unconditional observability label would
  smuggle gated work past the measurement gate, which is the one thing FR-031
  exists to prevent. If the gate says build, attribution comes free with it.
- *Propose upstream* — a reconciler middleware in `controller.Options`, or
  relaxing the check so an options-supplied wrapper composes with `Build`'s
  argument. Worth filing alongside R9's other two.
- **Accepted for now**: attribute what engagement gives us, and let the harness
  attribute its own synthetic load.

**Consequence, and it is asymmetric between the two load modes:**

- **Synthetic** mode attributes fully. The harness generates the load, so it
  knows which workspace it touched and records it directly.
- **Observed** mode cannot attribute reconciles at all today. It sees engagement
  counts, failures and aggregate totals, but not which workspace produced the
  reconciles.

That difference is a real limitation of `ModeObserved` and belongs in the mode's
documentation rather than being discovered by whoever first tries to
characterise a running deployment.

---

## R15 — Delivery is flat; footprint is what binds — MEASURED

Closes the first two limits R14 recorded against itself. Full run in
`evidence/baseline-2026-08-16.md`.

### The instrument

`internal/scaleharness/delivery.go`. The clock starts when the harness issues a
mutation and stops when a controller is invoked for it, attributed per
workspace. This is deliberately not the write duration R14 reported: a write
returns once kcp has accepted it, and the dispatch through every registered
listener — the O(W) term this feature exists to bound — happens entirely after
the writer has stopped looking.

An event that never arrives is a finding, so the timeout returns the shortfall
alongside the latencies already collected rather than failing the run or
waiting indefinitely.

### The listener density

The wired Cluster API set registers roughly nineteen watches across five
controllers, so the sweep was re-run at 19 watches per workspace. Dispatch cost
is per listener, which makes 64 workspaces × 19 watches the listener count of
1,216 workspaces at one watch — and concentrated on a **single** informer, which
is harsher than a real deployment spreads it.

| Workspaces | Live heap | Footprint | Goroutines | Deliver p50 | Deliver p99 | Missed |
|---|---|---|---|---|---|---|
| 8 | 15.5 MiB | 46.2 MiB | 1,740 | 5.54 ms | 6.02 ms | 0 |
| 16 | 27.3 MiB | 79.1 MiB | 3,428 | 5.64 ms | 6.70 ms | 0 |
| 32 | 50.9 MiB | 155.8 MiB | 6,804 | 5.57 ms | 6.53 ms | 0 |
| 64 | 97.9 MiB | 244.9 MiB | 13,556 | 6.01 ms | 8.03 ms | 0 |

The same sweep was run at `idle-heavy`, which turned out to matter more than
expected — see below.

### What it found

**Delivery latency does not grow with fleet size.** p50 stays within 5.5–6.0 ms
across an 8× increase, with nothing missed, and moves without trending. Against
a baseline dominated by the round trip to kcp, a per-listener filter — a
closure, a comparison, a channel send — is not detectable at these scales.

**Footprint is exactly linear, and large.** 3.54 MiB of process footprint and
**211 goroutines** per workspace, with goroutine deltas of exactly 211 at every
step. Extrapolating: ~211,000 goroutines and ~3.5 GiB at 1,000 workspaces.

**Live heap is not the sizing figure, and using it would under-provision.** It
excludes goroutine stacks, and stacks grow with the fleet — 733 KiB per
workspace against 1.47 MiB of heap. Process footprint (`MemStats.Sys`) runs
1.9× to 2.4× live heap. The sweep now records both.

**An idle workspace costs nearly what a busy one does.** 2.72 against 3.54 MiB
of footprint at the same listener count, and *identical* goroutine counts.
Listener registration is the cost; ten objects and one event per second add
about 800 KiB on top. FR-026 is right that the profiles differ, but by 1.3×
rather than the order of magnitude the requirement's framing implies — and that
makes the idle case, not the active one, the figure a shard is bounded by.

**The coefficients reproduce, once sampled correctly.** Repeated runs agree to
0.1% at every point and goroutine counts come back identical — but only after
the sampling defect below was fixed. An earlier claim in this file that they
agreed "to under one percent" was made from two runs that happened to agree; a
third did not.

**This inverts the feature's working assumption.** The suspected super-linear
dispatch cost did not appear. What binds a shard is the linear-but-large
per-workspace footprint — which is what FR-003 (bounded per-workspace watch
cost) and FR-009/FR-011 (idle cost) address, and is now the requirement set with
measured support behind it. No departure from linear was found in any run.

### A third defect, caught by held-out validation rather than by a test

The first 19-listener run fitted badly — R²=0.957 on live heap, and holding out
the smallest point mispredicted it by **54%** — while 1-listener runs from the
same build fitted at R²=1.0000.

`sample()` ran a single `runtime.GC()` before reading `MemStats`. One cycle
queues finalizers but does not run them, so objects awaiting one are still
reachable and get counted as live; how much that inflates the reading depends on
what happened to be in flight, which at 1,216 listeners is a great deal.
Sampling after two cycles fixed it, and two consecutive repeats then agreed to
0.1% at every point.

Two things make this worth recording beyond the fix. The bad numbers were **not
implausible** — 78 MiB where 98 MiB belonged looks like an ordinary
measurement, and nothing but the held-out error flagged it. And the error ran in
the direction that under-reports memory, which is the direction that produces an
under-provisioned limit. Held-out validation earned its place in FR-035 on its
first real use.

### Two defects the instrument exposed

Both were invisible without it, and both would have silently corrupted a
published figure:

1. **`Touch` wrote a non-monotonic value.** It set an annotation to the object's
   `Generation`, which does not increment on a metadata-only change — so repeat
   touches were no-ops the server discarded. Exactly half of all events at each
   accumulating point were never generated at all, while `LoadDuration` happily
   reported the time taken to issue them.
2. **Percentiles were truncating.** `int((n-1)*p)` returns the third of four
   samples for p99. It understates worst precisely at the tail, which is where a
   fan-out cost would first appear. Now nearest-rank.

### What it still does not establish

64 workspaces, not the 256 the fixture reaches (R10) — anything above 64 is an
extrapolation. The reconciler is a stub: what is measured is the cost of
registering watches and starting workers, to which a production reconciler adds
its own.

And the **tail percentile rests on very few samples**. At one event per
workspace a point yields 8 to 64 latencies, so its p99 is the slowest of a
handful; the single 8.40 ms at 32 workspaces is one slow round trip rather than
a shape. The p99 column is worth keeping — the tail is where a fan-out cost
appears first — but establishing a trend in it needs many more events per point
than the profiles currently drive.

---

## R16 — The wildcard cache is not the cost; per-workspace controllers are — MEASURED

**Question**: the `apiexport` provider already uses a wildcard cache — one
informer per type for the whole fleet. So why does a workspace cost 211
goroutines, and is that duplication better sharing would remove?

**Answer: no.** Five configurations against a real kcp, varying only how the
same 19 watches are distributed across controllers and how many workers each
runs, decompose it exactly. Full method and tables in
`evidence/goroutine-decomposition.md`.

```
goroutines/workspace = 2  (engagement)
                     + 7  × controllers
                     + 1  × workers × controllers
                     + 2  × watches
```

All four coefficients close against all five measured configurations, and the
engagement term was measured directly at zero controllers rather than inferred.
The per-watch coefficient of 2 is exactly `client-go`'s `processorListener`
starting a `run` and a `pop` goroutine per registration
(`shared_informer.go:1063-1064`, R2) — the measurement and the source agree.

### The finding that matters for the gate

At the real shape — 19 watches across 19 controllers, 2 workers each:

| Cost | Goroutines | Share | Removable by cache interposition? |
|---|---|---|---|
| Informer registrations | 38 | **18%** | **Yes** (R1, R2) |
| Controller machinery | 133 | 63% | No |
| Workers | 38 | 18% | No |
| Engagement | 2 | 1% | No |

The wildcard cache is doing its job: engagement costs 2 goroutines and 464 KiB
per workspace, and there is no duplicated informer to remove.

**This corrects an assumption running through the plan.** FR-003's mechanism —
replacing per-workspace registrations with map entries in an interposed cache —
targets the 38. It leaves 171 that are controller-runtime instantiating a full
controller per workspace, which a cache cannot reach. The listener fan-out was
taken to be the dominant per-workspace cost; measured, it is the **smallest** of
the three controller-side terms.

### What does move the rest

1. **Fewer controllers per workspace** — 19 watches on one controller costs 49
   goroutines instead of 211, a 77% cut with the listener count unchanged. But
   the topology is upstream Cluster API's, and changing it is the divergence
   Principle I counts.
2. **Fewer workers** — 2→1 saves 9%. Already configurable; trades throughput.
3. **One controller set for the whole fleet** — goroutines become O(1) in
   workspace count. This is exactly the alternative R1 recorded and rejected,
   because it means not running upstream reconcilers unmodified.

Ordered by saving and by cost in divergence, and those orders are the same. The
feature's central tension is now quantified rather than argued.

### The census, read rather than estimated

R16's early write-ups estimated "roughly 4–5 controllers, 19 watches". Reading
every `SetupWithManager` gives **5 controllers and 14–15 informer-backed
watches**, plus 4 channel-backed raw sources and dynamic watches that
`external.ObjectTracker` adds at runtime. Measured there: **75.0 goroutines per
workspace**, which the formula again predicted exactly before the run.

Two consequences, both recorded in `evidence/controller-census.md`:

- **The B-vs-C ordering in the options analysis flipped.** At the estimated
  shape, cache interposition beat fleet-wide controllers; at the real census it
  is the other way round, narrowly. The ordering is genuinely sensitive to the
  census, which is why estimating it was not good enough.
- **These are walking-skeleton figures.** `setup.go` defers most of the core
  set to Phase 3. Upstream `core/main.go` wires 15 controllers against our 5;
  at parity the projection is **~16 controllers, 45 watches, 236 goroutines per
  workspace — 3.1× today**. Every capacity figure in this feature carries that
  caveat.

### An unexplained fixed cost, flagged not resolved

The first controller-and-watch in a workspace costs about **415 KiB more** than
each subsequent one, on top of the 464 KiB of engagement. The marginal terms
account for only ~33 KiB of the gap. The plausible candidate is per-workspace
scoped informer and cache-reader instantiation, but that is a hypothesis this
measurement does not test — **ASSUMED**, and worth a source read before any
figure is built on it.

---

## R17 — The provider split is mandatory; engagement is what it multiplies — MEASURED

**Constraint, not a question.** Cluster API's provider model puts infrastructure
providers in separate deployments, authored and versioned by third parties. The
split is required for extensibility; a cost analysis does not outrank it. An
earlier version of this entry treated it as a trade to defer, which was wrong.

**Engagement is sparse per export — VERIFIED.** `multicluster-provider@v0.8.0`
`pkg/provider/provider.go:259-300`: a provider watches the virtual-workspace
URLs of *its own* `APIExportEndpointSlice`, with `ObjectToWatch` defaulting to
`APIBinding`. A deployment therefore engages only workspaces that bound its
export. Two roles follow:

| Role | Engages | Capacity binds on |
|---|---|---|
| Core | every workspace using Cluster API | total workspaces in the shard |
| Provider | only workspaces that bound it | that provider's adoption |

A workspace using one provider is engaged twice, not once per deployed provider.
Adding a provider costs nothing for workspaces that do not use it — which is the
independent scaling ADR-0002's appliance model wanted.

**Measured, at the mandatory boundary:**

| Deployment | Controllers | Watches | Goroutines/ws | Footprint/ws |
|---|---|---|---|---|
| Core | 3 | 9 | **47.0** | 2.38 MiB |
| Infrastructure | 2 | 6 | **32.0** | 2.16 MiB |

Both predicted by the R16 formula before being run and measured exactly — the
fourth and fifth out-of-sample confirmations.

**The cache is not what gets duplicated.** `wildcardCache` resolves
`GetSharedInformer` per GVK and creates informers lazily, so a deployment caches
only the types it watches; and cached objects are cheap — tripling objects per
workspace from 10 to 30 moved live heap by less than the measurement noise.

What gets duplicated is the fixed per-workspace cost: ~464 KiB of engagement
plus ~415 KiB on first watch, **paid in full by every deployment a workspace
uses**. For a workspace using core plus one provider that is ~1.76 MiB of its
~4.54 MiB — 39% of its cost, and structural.

### Three consequences

**Engagement becomes the primary scaling term.** All four `build` determinations
are the engagement path, and it is the only term multiplied by an architectural
requirement. FR-008 is worse than recorded: each deployment builds its own
dynamic REST mapper per workspace on its own serialized engagement loop, so a
workspace pays two discovery round trips behind two separate single-goroutine
queues.

**Core is where it matters.** Every deferred parity controller — topology,
MachineSet, MachineDeployment, MachinePool, MachineHealthCheck,
ClusterResourceSet, RuntimeSDK — is core. Core grows 47 → ~206 goroutines per
workspace at parity while providers stay near 32. Core also engages every
workspace, so a shard's capacity is core's.

**FR-037 is unconditional.** "Where more than one controller deployment serves a
shard" is now always. Capacity must be stated per deployment role, and FR-009's
budget is per deployment rather than per workspace outright. Full analysis in
`evidence/split-deployments.md`.

---

## R18 — Throughput is linear in the worker count; the partition is the problem — MEASURED

**Concern**: does `MaxConcurrentReconciles = 2` throttle throughput too much for
this to be scalable?

**Measured** by `test/integration/scale/throughput_test.go`: 8 workspaces, 40
objects each mutated at once to build a backlog, a probe reconciler taking a
stated 250 ms, one controller per workspace.

| Workers | Elapsed | Per workspace | Fraction of linear |
|---|---|---|---|
| 1 | 10.03 s | 4.0/s | — |
| 2 | 5.01 s | 8.0/s | **100%** |
| 4 | 2.64 s | 15.2/s | 95% |
| 8 | 1.38 s | 28.9/s | 91% |
| 16 | 1.02 s | 39.2/s | 61% — load generator bound, not a ceiling |

The 1-worker point matches theory exactly (40 × 250 ms = 10.0 s, measured
10.03). So `per-workspace throughput = workers / reconcile duration`, and at a
realistic 1–5 s Cluster API reconcile, two workers give **0.4–2 reconciles per
second per workspace**.

**The concern is correct, and the number is the wrong knob.** A shard at 800
workspaces already has 8,000 worker goroutines across five controllers — the
aggregate is enormous. They are **statically partitioned 2 per workspace per
controller**, so a tenant with a 100-Machine burst gets 2 while thousands sit
idle elsewhere. Tenant load is bursty and uncorrelated, which is precisely when
a shared pool beats a static partition.

| | Static partition (today) | Shared pool (fleet-wide) |
|---|---|---|
| Goroutines at 800 workspaces | 8,000 | ~50 |
| Available to one bursting workspace | 2 | ~50 |

**Action taken.** `DefaultMaxConcurrentReconciles` raised 2 → 4: a 2× gain in
the worst case one tenant can hit, for 13% more goroutines (75 → 85 per
workspace at the wired census), with the return measured rather than assumed.
Not upstream's 10, which costs 53% more and still cannot lend idle capacity
across workspaces.

**This is the third independent argument for fleet-wide controllers**, after
goroutine footprint (R16) and the engagement multiplier under mandatory
deployment splitting (R17) — and the first that is about performance rather than
footprint. Full analysis in `evidence/reconcile-throughput.md`.

### Two instrument defects worth recording

The first run had the **load generator as the constraint**: 320 sequential
get-then-update round trips put a ~3.8 s floor under every worker count, making
16 workers look like 1.7× over one. Fixed with concurrent merge patches, and the
test now *reports* issue time so a generator-bound point announces itself.

The second had **managers and workspaces accumulating across points** —
`t.Cleanup` deferred each manager's stop to the end of the test, so point three
ran three managers over twenty-four workspaces, all counting into one
measurement. It produced rates above linear and completion counts above the
run's own target, which is what exposed it.

Both would have answered the question confidently and wrongly.

---

## R19 — Per-workspace structure has one cause, and it is one function — VERIFIED

**Question**: why is anything per workspace? Why not one controller set across
the shard using `mcreconcile.Request` semantics?

**Nothing about event delivery requires it.** Read in
`cluster-api@v1.15.0-kcp.1`:

1. **`Reconcile` depends on unexported state.** `cluster.Reconciler` has
   `recorder` and `externalTracker` unexported; `machine.Reconciler` has six
   such fields. They are used far outside setup —
   `cluster_controller_phases.go:60` calls `r.externalTracker.Watch(...)`,
   `:243`/`:345` and `cluster_controller_status.go:48` call `r.recorder.Eventf`.
   So an external package **cannot** construct a working reconciler; it must
   call `SetupWithManager`.
2. **`SetupWithManager` hardcodes a single-cluster controller.** All 15 core
   reconcilers build through `capicontrollerutil.NewControllerManagedBy`
   (`util/controller/builder.go:65`), which wraps
   `builder.ControllerManagedBy(m)` — one controller, one manager, plain
   `reconcile.Request`.
3. **`reconcile.Request` is a `NamespacedName`.** A fleet-wide controller keyed
   on it could not tell workspace A's `default/my-cluster` from workspace B's;
   they would collide in the workqueue. A correctness failure, not a cost one.

Per-workspace managers are the adapter that satisfies (1) and (2) while keeping
tenants apart. **The 75 goroutines per workspace are the arithmetic of that
adapter, not a chosen design.**

### Almost everything else already exists

| Need | Status |
|---|---|
| Request carrying the cluster | **exists** — `mcreconcile.Request` |
| Adapting an unmodified reconciler | **exists** — `mccontext.ReconcilerWithClusterInContext` |
| Reusing existing map functions | **exists** — `TypedEnqueueRequestsFromMapFuncWithClusterPreservation` |
| One fleet-wide registration | **exists** — `WildcardCache.GetSharedInformer` + `kcp.io/cluster` (R12) |
| Context-scoped client | **missing** — ~100 lines |

`Reconcile` itself needs **no changes**, and neither do the map functions.

### The blocker is one function signature

`capicontrollerutil.Builder` is a thin wrapper over controller-runtime's,
written throughout against the **Typed** APIs —
`controller.TypedOptions[reconcile.Request]`,
`handler.TypedEventHandler[client.Object, reconcile.Request]`,
`source.TypedSource[reconcile.Request]`,
`reconcile.TypedReconciler[reconcile.Request]`. Every one is `reconcile.Request`
where it could be a type parameter, and `mcbuilder.TypedBuilder[request]`
already exposes the same surface generically.

**The upstream ask is therefore "make CAPI's builder wrapper generic over the
request type", not "make Cluster API multi-tenant"** — and because all 15
reconcilers build through it, one change reaches all of them unedited. That is
the Principle II proposal to raise, alongside R5's REST mapper and R17's
engagement serialization.

Beyond it, two things remain: `external.ObjectTracker`'s dynamic watches enqueue
plain requests, and `clustercache` keys accessors by namespace/name only
(`cluster_cache.go:362`) — the latter a cross-tenant correctness bug fleet-wide,
not an adaptation. Full analysis in `evidence/why-per-workspace.md`.

**Counted**: `SetupWithManager` is **617 lines of the 23,683** non-test lines in
the core reconciler packages — **2.6%**, and it is wiring. `Reconcile` does not
change at all. That number is what makes the premise question answerable rather
than rhetorical, and
[ADR-0003](../../docs/adr-0003-workspace-aware-cluster-api.md) takes it up: the
choice is between "unmodified controllers" and "unmodified *reconcile logic*",
not between Cluster API and a reimplementation.

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
| Telemetry cardinality (R7) | RESOLVED — bounded top-N + remainder | None; implemented and tested |
| REST mapper sharing (R5) | VERIFIED as blocked | Principle II finding — raise, do not work around |
| Environment sweep ceiling (R10) | RESOLVED — ≥256 workspaces, cost flat | None; sweep points 8..256 are on measured ground |
| G4 workspace resolution (R12) | VERIFIED — identity is present | Live test of a real `AdmissionReview` as G4's first TDD cycle |
| Per-event delivery cost (R14, R15) | MEASURED — flat to 1,216 listeners | None for FR-001's determination; the measurement exists |
| Per-workspace footprint (R15) | MEASURED — 2.72–3.54 MiB, 211 goroutines, linear | None; this is the binding constraint the gate now weighs |
| Idle vs active cost (R15) | MEASURED — 1.3× apart, identical goroutines | None; it reframes FR-026's premise rather than contradicting it |
| Fitted model and held-out accuracy | MEASURED — worst error 0.39% | None; `cmd/scalemodel` re-derives it from committed runs |
| Goroutine decomposition (R16) | MEASURED — 75/workspace; cache interposition reaches 37% | None; it reframes FR-003's determination |
| Controller census (R16) | VERIFIED — 5 controllers today, ~16 at parity | None; parity projection is a capacity caveat, not a blocker |
| Split deployments (R17) | MEASURED — mandatory; multiplies engagement cost | None; it raises the engagement repairs' priority |
| Reconcile throughput (R18) | MEASURED — linear in workers; default raised 2→4 | None; pooling is the structural fix |
| Per-workspace cause (R19) | VERIFIED — one builder function, plus tracker and clustercache | Principle II proposal, as with R5 |
| First-watch fixed cost (R16) | ASSUMED — ~415 KiB, mechanism unverified | Source read of the scoped informer path before building on it |
