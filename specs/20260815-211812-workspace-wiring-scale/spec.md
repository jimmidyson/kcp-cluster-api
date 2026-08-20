# Feature Specification: Workspace wiring that scales to a large fleet

**Feature Branch**: `claude/controller-wrapper-scalability-99fkd2`

**Created**: 2026-08-15

**Status**: Partly shipped, and superseded in its central mechanism — see [Where this stands](#where-this-stands)

**Input**: The scalability question left open by
[`docs/conversion-plan.md`](../../docs/conversion-plan.md) — "How many
workspaces this needs to scale to in practice" — answered by the repository
owner as **100,000+ workspaces reached by composition across regional shards**,
with replicas scaled per shard and explicit capacity limits per shard. Follows
the per-workspace wiring feature
([`specs/20260815-185524-per-workspace-wiring`](../20260815-185524-per-workspace-wiring/spec.md)),
which made wiring dynamic but not cheap.

## Where this stands

*Added 2026-08-20, after the work below had been overtaken by the route the
project actually took. The specification is left as it was written; this
section says what happened to it.*

**Not abandoned, and not obsolete. Its P1 goal was reached — by a mechanism
this document rejected before measuring.**

What this feature set out to fix was real and is fixed. A workspace no longer
taxes every event: measured across six doublings to a hundred active
workspaces, each costs **2 goroutines and no additional watch stream**, and a
departing one leaves nothing behind. Every provider deployment pays the same
2, flat to twenty. Those figures are published in
[Workspace resource usage](../../docs/site/content/en/docs/design/workspace-resource-usage.md)
and reproduced by `task test:sweep`.

**The mechanism is not the one below.** Phases 4–6 of
[tasks.md](tasks.md) build a `clusterdemux` package: an interposing cache
holding one upstream registration per GVK and a `map[clusterName][]handler`
underneath, so that many per-workspace handlers cost one real watch. What
shipped instead removes the per-workspace handler: controllers are **fleet-wide**,
built once against a cache spanning every workspace, and the cluster travels
on the request rather than being fixed at registration. There is one
registration per type because there is one controller, not because a demux
folds many into one. `cmd/core-manager` now engages workspaces solely to count
them — its `SetupFunc` is empty, and says so.

That is a stronger result than the demux would have produced, and it costs
something this specification explicitly forbade.

**FR-023 and SC-011 no longer hold, by decision rather than by drift.** Both
require this feature to add no entry to the drift record. The fleet-wide
wiring is carried in the fork —
`util/multicluster/lift.go`, `util/multicluster/recorder.go`,
`util/controller/builder_workspace.go` and the rest, all recorded in
[`DRIFT.md`](../../DRIFT.md) as *carried deliberately*. That was decided in
[ADR-0003](../../docs/adr-0003-workspace-aware-cluster-api.md), which accepted
option B and reopened [research R1](research.md) — the alternative this feature
rejected at the outset, and which its own measurements made cheaper to reverse
than assumed. FR-023's stated response to a requirement that cannot be met
through a public extension point is *raise it as a finding*; that is what the
ADR is.

**The gate still stands.** All eight determinations in
[evidence/determinations.md](evidence/determinations.md) were recorded before
any of this, and the four `close` verdicts are unaffected — they closed on
measurements, and the measurements have only improved. Of the four `build`
verdicts:

| Gated requirement | What happened |
|---|---|
| FR-004 — engagement cost independent of fleet and objects | **Superseded.** The cost was one store replay per watch per joining workspace. There are no per-workspace watch registrations left to pay it. |
| FR-005 — engagement does not suspend delivery | **Superseded**, for the same reason: the process-wide lock was taken by that replay. |
| FR-006 — engagement proceeds concurrently | **Superseded in importance.** Engagement is still serialized on the provider's goroutine, but what it now does per workspace is build a cluster, not register 14 watches. Reopen it if that stops being true. |
| FR-008 — no repeated process-wide discovery | **Still open, and still measured.** A workspace costs 3 discovery requests in core and 7 in the control plane provider. The blocker recorded at the gate — no public seam, and Principle II forbidding the workaround — is unchanged. |

**What is genuinely still open** is everything that was never gated on the
demux: the runtime capacity surface (US1's T026–T028 — `internal/scaleharness`
measures capacity offline, but a running manager does not report where it sits
between its limits), engagement retry with bounded backoff (US3's T044–T046),
replica sharding (US5, whose R3 entry criterion is still ASSUMED), the
remaining telemetry quantities (US6's T064), aggregate rate limiting and
backpressure (US7), and the capacity-planning documentation (US5–US7 and Phase
10). Those are not superseded by anything; they were simply not reached.

## Purpose

`core-manager` reconciles every workspace bound to the project's `APIExport`
from one process. The wiring that does this is correct — it isolates tenants,
binds runnables to workspace lifetime, and refuses the webhook configuration
that would serve one tenant with another's client. What it is not is cheap.

Each engaged workspace registers its own set of watches against the shared
wildcard cache, and each of those registrations is a separate listener that
sees **every other workspace's events** and discards them. The result is a
process whose per-event cost grows with the number of workspaces it serves,
and whose per-workspace onboarding cost grows with their total contents.
Neither is visible in the current tests, because the current tests exercise two
workspaces.

This feature makes that cost a stated, measured, bounded quantity rather than
an unknown one. The numbers below were established by reading dependency
source; this feature is not done until they are established by a command.

## The deployment model, and what it does and does not solve

The target is not reached by one process serving 100,000 workspaces. It is
reached by **regional shards, each bounded, with replicas scaled per shard**.
That framing is load-bearing for everything below, so its consequences are
stated rather than left implicit.

**Where this is going.** The shard is intended to become an *appliance* — a
self-contained unit of known capacity, where a region grows by adding another
box rather than by enlarging one, and where the system reports that it needs
scaling rather than waiting to be asked. That target architecture is recorded in
[ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md).

**This feature is that architecture's prerequisite**, and is scoped deliberately
short of it: an appliance cannot declare a capacity nobody has measured. What
the ADR requires of *this* feature is one thing only — that capacity be
machine-readable rather than merely published (FR-028, FR-032, FR-033), because
that is cheap now and structural to retrofit. Provisioning, the regional scaling
controller, appliance packaging and G4's admission routing are all out of scope
here and tracked in the ADR.

**What it solves.** The independent variable for a single process becomes
"workspaces in this shard", not "workspaces in the fleet". Cached state is
bounded by shard capacity, which is a number an operator sets, rather than by
fleet growth, which is not. Capacity planning replaces open-ended scaling.

**What it does not solve.** The costs in the table below are super-linear
*within a single process*. A per-shard limit sets the constant; it does not
flatten the curve. Where that limit lands therefore decides how much of this
feature needs to exist:

- At a limit in the low hundreds, the present design is plausibly adequate, and
  this feature reduces to stating the limit, measuring it, and tuning
  concurrency away from its single-tenant default.
- At a limit in the thousands, the super-linear costs still dominate inside one
  shard, and the event-delivery and engagement requirements below are load
  bearing.

The limit is therefore an **output of measurement, not an input to design**:
the harness finds where cost departs from linear, and the limit is set below
that departure point with headroom. This is why no absolute figure is asserted here, and
why the requirements it decides are explicitly gated on it — see "The
measurement gate".

**Two limits, two constraints.** These are separate and must not be conflated:

| Limit | Bounds | Why it does not substitute for the other |
|---|---|---|
| Workspaces per shard | Cached state, and every cost that scales with workspace count inside one process | Adding replicas does not reduce it |
| Replicas per shard | Reconcile throughput and availability | Adding replicas does not reduce cached state — see below |

Verified, and the reason the second row carries a caveat: the wildcard cache
covers an endpoint slice, so **every replica caches every workspace in that
slice**, and it does so whether or not it is doing any work — caches start in
the manager's cache runnable group (`controller-runtime@v0.24.1`
`pkg/manager/internal.go:446`) *before* leader election starts anything
(`:477`). Under plain leader election a standby replica therefore pays full
cache memory to do nothing. Replicas become capacity only when they are
concurrently active over disjoint subsets of the shard's workspaces.

## The costs, and how each was established

Per Constitution Principle V, each row was verified by reading the
dependency's source rather than its documentation. Nothing in this table is
inferred from a type signature or an example.

Throughout this specification, **"the fleet" means the workspaces one process
serves — one shard's worth**, per the deployment model above. Every cost here
is paid inside a single process and is bounded by the per-shard limit, not by
the total across shards.

| Behaviour | Where | Cost as workspaces per shard grows |
|---|---|---|
| A workspace's watch registration becomes a **new listener on the shared informer**, wrapping the caller's handler in a `logicalcluster.From(obj) == clusterName` filter | `multicluster-provider@v0.8.0` `pkg/cache/cache.go:134`, `scopedInformer.AddEventHandler` | Listener count grows with workspace count, per watched type |
| Every event is delivered to **every** listener, which then filters | `client-go@v0.36.3` `tools/cache/shared_informer.go:1094`, `sharedProcessor.distribute` | Per-event work grows linearly with workspaces per shard. One object change runs one filter per engaged workspace |
| Registering a watch on an already-started informer **replays the entire store** into the new listener while holding `blockDeltas` | `client-go` `tools/cache/shared_informer.go:918-934` | Onboarding cost grows with the shard's total contents, and stalls event delivery for that type process-wide while it runs. Onboarding a whole shard is quadratic |
| Each listener costs two goroutines and a 1024-slot ring buffer, allocated on registration | `client-go` `tools/cache/shared_informer.go:1063-1064`, `:1279` | Fixed memory and goroutine cost per workspace per watched type, paid whether or not the workspace has any objects |
| Engagement runs **synchronously on a single goroutine** — the endpoint watcher's informer handler | `multicluster-runtime@v0.24.1` `pkg/clusters/clusters.go:141`, calling `aware.Engage` | Workspaces onboard strictly one at a time, each slower than the last |
| Each engaged workspace builds **its own** discovery-backed REST mapper | `multicluster-provider` `pkg/cache/cluster.go:66`, `NewScopedCluster` | One discovery sweep and one cached API surface per workspace |
| A failed engagement is **logged and forgotten** | `multicluster-provider` `pkg/provider/provider.go:365` | No retry. Recovery waits for the next binding update or the cache resync — a default of roughly ten hours |
| Client-side rate limiting is disabled, and each workspace holds its own client configuration | `controller-runtime@v0.24.1` `pkg/client/config/config.go:101` | No aggregate ceiling on what one process can send at its shard |

Two things are **not** costs, and are called out so this feature does not
"fix" them:

- **Reads already scale.** Cached reads resolve through a cluster-scoped
  index (`multicluster-provider` `pkg/cache/forked_cache_reader.go:145-176`),
  so a list in one workspace never scans another's objects.
- **Watches and startup lists already scale.** The wildcard cache watches each
  type once for the whole shard, which is exactly what it was adopted for.

The problem is confined to **event delivery and engagement**, not to caching.

## The measurement gate

Constitution Principle VIII prohibits building scale work ahead of a measured
constraint. That prohibition applies to this feature's own contents, not just
to features in general: a cost that does not bind below any capacity an
operator would actually set must be **closed, not optimised**.

So the cost-reduction requirements below are **gated**, and marked `(gated)`.
A gated requirement is conditional in exactly one way:

> A gated requirement is binding **if and only if** the measurement of SC-013
> shows its cost departing from linear at or below a workspace count an
> operator would plausibly configure as per-shard capacity. If the measurement
> shows otherwise, the requirement is **closed** — recorded as satisfied by
> measurement, with the figures, rather than implemented.

Requirements not marked `(gated)` are unconditional. Correctness properties
(isolation, lifecycle, disengagement), robustness properties that are wrong at
any scale (engagement retry), and the measurement obligations themselves are
never gated: a silent permanent failure is a defect at two workspaces, not only
at two thousand.

This gate is a sequencing constraint on planning, not a licence to defer: the
measurement must be built and run *first*, and each gated requirement must
carry a recorded determination before implementation begins.

## What must remain true

This feature changes how much wiring costs. It must not change what wiring
guarantees. The tenancy and lifecycle properties established by the
per-workspace wiring feature are preconditions here, not goals to re-litigate:
no workspace may observe another's objects or events, everything registered
for a workspace must stop when that workspace goes away, and no
workspace-scoped value may reach process-global state.

## Out of Scope

- **Webhook serving across workspaces (G4).** Unbuilt, carries a required
  human review checkpoint, and remains a hard ceiling of one workspace for
  admission regardless of how far reconciliation scales. This feature does not
  raise that ceiling and does not depend on it being raised. **Trigger**: G4's
  own design work, which this feature's results make more urgent but no more
  tractable.
- **Parity with upstream `core/main.go`'s reconciler set.** The wired set stays
  as it is. This feature changes how many workspaces that set runs against and
  what each costs, not which reconcilers run. **Trigger**: Phase 3's P1–P3.
- **Reducing the cost of a workspace's *workload* clusters.** `ClusterCache`
  holds a connection and cache per workload cluster; that cost is proportional
  to real infrastructure, not to workspace count, and is upstream's design.
  **Trigger**: a measurement showing workload-cluster cost dominating at stated
  per-shard capacity.
- **kcp shard topology and workspace placement.** How many shards exist, which
  region each serves, and which workspaces land on which shard are kcp's
  concerns. This feature consumes that topology and states its own capacity
  within it; it does not schedule workspaces onto shards and cannot enforce a
  workspaces-per-shard limit itself. **Trigger**: none — this stays kcp's.
- **Admission-time enforcement of any capacity limit.** Rejecting a workspace's
  objects for exceeding a per-workspace limit would require admission across
  workspaces, which G4 does not provide. This feature therefore *states and
  reports* capacity rather than enforcing it. **Trigger**: G4.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - An operator knows a shard's capacity before exceeding it (Priority: P1)

An operator planning a regional deployment needs to know how many workspaces
one shard's `core-manager` can carry, and how many replicas that needs. They
get a stated figure derived from measurement, expressed in terms they can
check against their own fleet. When a shard exceeds it in production, they are
told — the process does not quietly degrade.

**Why this priority**: P1, and first because it governs the rest. With regional
shards and per-shard limits, capacity *is* the deliverable — every other story
exists to determine how high the figure can be set, and the measurement gate
means none of them can be judged necessary until this one has run. A limit
nobody can measure against is not a limit, and a process that degrades silently
past it converts a capacity-planning problem into an outage.

**Independent Test**: run the harness across increasing workspace counts on a
fixed profile, identify where cost departs from linear, and confirm the process
reports its own position against the stated capacity at runtime.

**Acceptance Scenarios**:

1. **Given** a fixed load profile, **When** the harness sweeps workspace count,
   **Then** it identifies the point at which cost departs from linear, and that
   point is reported as a number with the profile it belongs to.
2. **Given** a stated capacity, **When** a running process approaches or exceeds
   it, **Then** the operator can see that from what the process reports.
3. **Given** two different load profiles, **When** capacity is stated, **Then**
   it is stated per profile — a count of idle workspaces and a count of active
   ones are not interchangeable.

---

### User Story 2 - Workspace count stops being a tax on every event (Priority: P1)

An operator runs `core-manager` against a shard whose workspace count grows
from a handful to its stated capacity. A tenant updates one `Machine` in one
workspace. The work the process does to deliver that one change to the one
controller that wants it does not depend on how many other workspaces exist.

**Why this priority**: this is the cost that sets the per-shard limit. Every
other cost in this feature is additive and bounded; this one multiplies, so it
is what makes the departure point appear. Sharding does not relieve it — each process pays
the multiplication over its own shard's workspaces — so a low limit is the only
alternative to fixing it, and a low limit means more shards for the same fleet.

**Gated**: if Story 1's measurement shows the departure point falls well above any capacity
an operator would set, this story is closed on the evidence rather than built.

**Independent Test**: run the harness at two workspace counts an order of
magnitude apart, apply an identical event workload to a fixed number of
workspaces, and compare per-event cost.

**Acceptance Scenarios**:

1. **Given** N engaged workspaces, **When** an object changes in one workspace,
   **Then** the work performed to route that change does not grow as N grows.
2. **Given** N engaged workspaces, **When** a fixed event workload is applied,
   **Then** the process's steady-state event-handling cost is within a stated
   bound that holds across an order of magnitude change in N.
3. **Given** an event in workspace A, **When** it is delivered, **Then** no
   controller belonging to workspace B observes it — the isolation property is
   unchanged by any change in how delivery is routed.

---

### User Story 3 - A workspace joins a busy shard without disturbing it (Priority: P1)

A tenant binds a new workspace to the `APIExport` while the process is already
serving many. That workspace starts reconciling promptly. The workspaces
already running do not pause, slow, or miss events while it does.

**Why this priority**: equal-first with Story 2 because it is the other
super-linear cost, and because it is the one an operator meets first — it
governs cold start, which is when every workspace on the shard engages at once.
A process that cannot restart inside a maintenance window cannot be operated at
capacity, however well it runs once warm.

**Gated**, on the same basis as Story 2 — except for scenario 4, which covers
engagement retry and is unconditional.

**Independent Test**: measure time-to-reconciling for a workspace joining a
shard at increasing occupancy, and measure event delivery latency for
already-engaged workspaces during that join.

**Acceptance Scenarios**:

1. **Given** N engaged workspaces, **When** one more binds, **Then** its time to
   first reconcile does not grow as N grows.
2. **Given** N engaged workspaces, **When** one more binds, **Then**
   already-engaged workspaces continue receiving events throughout, with no
   process-wide pause attributable to the join.
3. **Given** an empty process and N bound workspaces, **When** the process
   starts, **Then** all of them reach reconciling within a stated bound that
   does not grow quadratically with N.
4. **Given** a workspace whose engagement fails transiently, **When** the failure
   clears, **Then** the workspace is engaged without operator intervention and
   without waiting for the cache resync interval.

---

### User Story 4 - An idle workspace costs almost nothing (Priority: P2)

Most workspaces in a large kcp installation are bound but empty, or bound and
long since quiescent. Such a workspace consumes a bounded, small share of the
process — enough to notice its first object promptly, and no more.

**Why this priority**: P2 because it raises the per-shard limit rather than
changing its shape. An idle workspace that costs almost nothing means a shard
holds far more of them for the same memory, which directly reduces how many
shards a given fleet needs. Its value is proportional to how idle the shard
actually is, which is why capacity is stated per profile — see FR-026.

**Gated**: binding only if measurement shows idle cost is what limits capacity
on the idle-heavy profile.

**Independent Test**: engage many workspaces containing no Cluster API objects
and measure the process's resident footprint and goroutine count per workspace
against a stated budget.

**Acceptance Scenarios**:

1. **Given** a bound workspace with no Cluster API objects, **When** it is
   engaged, **Then** its steady-state cost to the process is within a stated
   per-workspace budget.
2. **Given** an idle workspace, **When** its first Cluster API object is
   created, **Then** it begins reconciling within a stated bound.
3. **Given** a workspace that becomes idle again, **When** it has been quiescent
   for a stated period, **Then** its cost returns to the idle budget without
   losing the ability to notice a subsequent object.

---

### User Story 5 - Replicas add throughput, not just standby (Priority: P2)

An operator runs several `core-manager` replicas against one shard. The shard's
workspaces are divided among them, and adding a replica increases the work the
deployment gets through. Each workspace is reconciled by exactly one replica at
a time. When a replica is lost, its share is picked up by the others without an
operator touching anything, and without any workspace being reconciled by two
replicas at once during the handover.

**Why this priority**: P2 because it is what makes "replicas scaled per shard"
mean added capacity rather than added cost. Under plain leader election a
second replica reconciles nothing while still holding the shard's entire cache
— it buys availability at full memory price. This story converts that spend
into throughput. It is sequenced after Stories 2 and 3 deliberately: dividing a
super-linear cost still leaves each replica paying it over its own share.

**Independent Test**: run multiple replicas against one shard, verify the
partition covers every workspace exactly once and that throughput rises with
replica count, kill a replica, and verify coverage is restored without overlap.

**Acceptance Scenarios**:

1. **Given** several replicas, **When** all are running, **Then** every bound
   workspace is reconciled by exactly one of them.
2. **Given** several replicas, **When** one is lost, **Then** its workspaces are
   taken over by the remaining replicas within a stated bound.
3. **Given** a workspace being handed from one replica to another, **When** the
   handover happens, **Then** it is never actively reconciled by both.
4. **Given** a replica joining, **When** it starts, **Then** the shard's
   workspaces redistribute without disengaging those that did not change owner.
5. **Given** a fixed workload, **When** replica count increases, **Then**
   reconcile throughput increases — replicas are capacity, not standby.

---

### User Story 6 - An operator can tell which workspace is hot (Priority: P2)

Something on the shard is misbehaving — a reconcile loop, a workspace with
pathological object counts, a tenant generating churn. The operator can find
which workspace it is from what the process already reports, without attaching
a debugger or restarting with different flags.

**Why this priority**: P2, and promoted from its previous status as a deferred
nice-to-have (P9) because the scale target changes what it is for. Aggregated
metrics are a reporting limitation at two workspaces and an operational
dead-end at capacity: an operator who cannot attribute load cannot act on it.
Story 1's capacity figures and Story 5's sizing both depend on it.

**Independent Test**: generate disproportionate load in one workspace of a busy
shard and confirm it is identifiable from reported telemetry alone.

**Acceptance Scenarios**:

1. **Given** a busy shard, **When** one workspace generates disproportionate
   reconcile load, **Then** that workspace is identifiable from reported
   telemetry.
2. **Given** a busy shard, **When** telemetry is collected, **Then** its volume
   and cardinality remain bounded as workspace count grows.

---

### User Story 7 - A process at capacity does not overwhelm its shard (Priority: P3)

The process serves its workspaces without presenting the kcp shard with a load
that scales unchecked. When the shard pushes back, the process slows down
rather than amplifying.

**Why this priority**: P3 because it is a failure mode at the boundary rather
than a cost inside the process, and because kcp's own admission control is the
primary defence. It is in scope because this feature is the first thing to make
the unbounded case reachable.

**Independent Test**: apply a workload at stated per-shard capacity against a
constrained shard and confirm the process's aggregate request rate respects a
stated ceiling and degrades without collapsing.

**Acceptance Scenarios**:

1. **Given** a process at capacity under load, **When** requests are issued to
   the shard, **Then** the process's aggregate rate respects a configured
   ceiling.
2. **Given** a shard rejecting or delaying requests, **When** the process
   observes this, **Then** it backs off rather than retrying at full rate.

---

### Edge Cases

- **Cold start of a full shard.** Every bound workspace engages at once. This is
  the worst case for Story 3 and the case an operator meets on every rollout.
- **Churn.** Workspaces bind and unbind continuously. Engagement and
  disengagement must not leak listeners, goroutines, workqueues, or telemetry
  series, and must be safe when they race each other.
- **Re-engagement.** A workspace that left and came back must wire cleanly.
- **The noisy neighbour.** One workspace holds orders of magnitude more objects
  than the rest, or generates continuous churn. Other workspaces' event delivery
  must not be starved by it.
- **The empty shard and the shard of one.** Nothing about the scaled design may
  make the small case slower or more complex to operate than it is today.
- **Partial failure.** Engagement fails for one workspace — repeatedly. It must
  not consume unbounded resources retrying, must not stall other workspaces'
  engagement, and must be visible.
- **A workspace moving between shards** underneath a running process, and an
  endpoint slice's membership changing while workspaces are engaged.
- **Replica loss during a join**, and a replica partitioned from its peers but
  still able to reach the shard — the case where two replicas could each
  believe they own a workspace.

## Requirements *(mandatory)*

### Functional Requirements

Requirements marked `(gated)` are conditional on the measurement gate defined
above. All others are unconditional.

**Event delivery**

- **FR-001** `(gated)`: The work performed to deliver one object change to the
  controllers that want it MUST NOT grow with the number of engaged
  workspaces.
- **FR-002**: The process MUST NOT deliver a workspace's events to any other
  workspace's controllers, under any routing scheme adopted to satisfy FR-001.
  Unconditional: this is the isolation property, and it constrains whatever is
  built rather than being something built.
- **FR-003** `(gated)`: The per-workspace fixed cost of watching a type —
  memory, goroutines, and any allocated buffers — MUST be bounded and stated,
  and MUST be paid only for types that workspace actually watches.

**Engagement**

- **FR-004** `(gated)`: The cost of engaging one workspace MUST NOT grow with
  the number of workspaces already engaged, nor with the shard's total object
  count.
- **FR-005** `(gated)`: Engaging a workspace MUST NOT suspend event delivery for
  already-engaged workspaces.
- **FR-006** `(gated)`: Engagement of distinct workspaces MUST be able to
  proceed concurrently, and MUST NOT be serialized behind a single worker.
- **FR-007**: A workspace whose engagement fails MUST be retried with a bounded
  backoff, without operator intervention and without waiting for a cache
  resync. Repeated failure MUST be visible and MUST NOT consume unbounded
  resources. Unconditional: a silently permanent failure is a defect at any
  workspace count.
- **FR-008** `(gated)`: Per-workspace setup MUST NOT repeat process-wide
  discovery work that is identical for every workspace.

**Steady state**

- **FR-009** `(gated)`: The steady-state cost of an engaged workspace with no
  Cluster API objects MUST be within a stated budget, and that budget MUST be a
  stated figure rather than "whatever it turns out to be".
- **FR-010**: Concurrency limits for per-workspace controllers MUST be
  configurable and MUST default to a value chosen for many-tenant operation
  rather than inherited from single-tenant upstream defaults. Unconditional
  because it is configuration, not construction.
- **FR-011** `(gated)`: A workspace that becomes idle MUST be able to release
  its non-essential cost while remaining able to notice new objects promptly.
- **FR-012**: Disengagement MUST release everything engagement acquired —
  including event-routing registrations, telemetry series, and per-workspace
  clients — such that sustained churn does not grow the process's footprint.
  Unconditional: this is the lifecycle property.

**Horizontal scale**

- **FR-013**: Adding a replica to a shard MUST increase the deployment's
  reconcile throughput, and one shard's workspaces MUST be divisible across
  replicas such that every bound workspace is reconciled by exactly one
  replica.
- **FR-014**: Loss of a replica MUST result in its workspaces being taken over
  by surviving replicas within a stated bound, without operator action.
- **FR-015**: The design MUST prevent two replicas from actively reconciling one
  workspace simultaneously, including during handover and including when a
  replica is partitioned from its peers but can still reach the shard.
- **FR-016**: The design MUST NOT rely on added replicas to reduce cached state,
  since every replica caches every workspace in its endpoint slice. The two
  limits MUST be documented as distinct — workspaces per shard bounds cached
  state, replicas per shard bounds reconcile throughput — so that a deployment
  is not sized on the assumption that one substitutes for the other.

**Operability**

- **FR-017**: Reconcile and queue telemetry MUST be attributable to the
  workspace that produced it, with volume and cardinality that remain bounded as
  workspace count grows.
- **FR-018**: The process MUST expose enough about its own state — engaged
  count, engagement progress and failures, per-workspace load — for an operator
  to size replicas and diagnose a hot workspace.
- **FR-019**: Aggregate request rate to the shard MUST be bounded by
  configuration, and the process MUST back off when the shard signals pressure.

**Evidence**

- **FR-020**: A scale measurement harness MUST exist as a named operation,
  reporting the quantities every numeric requirement above is stated in, at a
  configurable workspace count and load profile.
- **FR-021**: Every numeric bound this feature claims MUST be produced by that
  harness, with before-and-after evidence recorded for each cost this feature
  set out to remove.
- **FR-022**: The harness MUST distinguish "ran and passed", "ran and failed",
  and "could not run" — a workspace count the environment cannot host is not a
  pass.

**Constraints**

- **FR-023**: This feature MUST be delivered through upstream's public
  extension points, adding no new entry to the drift record. If any requirement
  here cannot be met that way, that MUST be raised as a finding rather than
  worked around — the available responses being another integration point, an
  upstream proposal, or accepting the limitation.
- **FR-024**: The tenancy and lifecycle guarantees established by the
  per-workspace wiring feature MUST hold unchanged, and MUST be demonstrated to
  still hold at stated capacity rather than assumed to survive.
- **FR-025**: Deferrals MUST be recorded as decisions naming what would trigger
  the work.

**Capacity**

Stated last because every requirement above feeds it: capacity is what this
feature ultimately delivers, and the rest is what determines how high it can be
set.

- **FR-026**: The capacity of one shard's `core-manager` MUST be a stated
  figure, derived from measurement, and MUST be expressed per load profile
  rather than as a single workspace count — a count of idle workspaces and a
  count of active ones are not interchangeable units of capacity.
- **FR-027**: Capacity MUST be stated in terms of what actually consumes it —
  at minimum watched object count and event rate — with any workspace-count
  guidance derived from those, so that an operator can check the figure against
  their own shard rather than against an assumed shape.
- **FR-028**: A running process MUST report its own position against its stated
  capacity, so exceeding it is observable rather than inferred from degradation.
  That report MUST be **machine-readable**: the configured limit, the observed
  load in FR-027's units, and the position between them, consumable by a process
  other than the one being measured. Publishing the figure for humans remains
  necessary and is no longer sufficient — see FR-032.
- **FR-032**: The capacity surface MUST indicate **which** limit is being
  approached — reconcile throughput, or workspace count and cached state —
  because the two have different remedies: more replicas within a shard, or
  another shard. A single undifferentiated "utilisation" figure cannot be acted
  on, since it does not say which lever to pull (FR-016's two limits).
- **FR-033**: The capacity surface MUST NOT be designed in a way that precludes
  workspaces later moving between shards. Rebalancing is deferred, not rejected
  ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A1), and a surface
  that assumes a workspace's shard is permanent would have to be redesigned to
  allow it.
- **FR-029**: Exceeding stated capacity MUST degrade observably rather than
  silently. This feature MUST NOT claim to enforce a limit it cannot enforce:
  workspace placement onto shards is kcp's, and admission-time enforcement
  requires G4 — see Out of Scope.
- **FR-030**: The harness of FR-020 MUST derive the departure point by a defined,
  repeatable procedure, not by inspection. That procedure MUST specify: which
  reported quantities are swept; a minimum number of geometrically spaced
  workspace counts per sweep; a stated deviation tolerance; and the rule for
  identifying the departure point — the smallest swept workspace count at which a measured
  quantity exceeds the linear projection from the sweep's two smallest points by
  more than that tolerance. The tolerance and the point count MUST be recorded
  with each result, so two runs of the same profile yield the same figure.
- **FR-031**: Each gated requirement MUST carry a recorded determination against
  FR-030's measurement before implementation begins: built, because the cost
  binds at or below a plausible capacity; or closed, because it does not. A
  closed requirement MUST record the figures that closed it.
- **FR-034**: Capacity MUST be expressed as a **fitted resource model** —
  coefficients over the known cost structure — and not only as a table of
  measured points, so that a fleet shape which was not measured can still be
  sized. The model MUST state the regime in which it is valid and MUST decline
  to project across a discontinuity it has not observed
  ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A4).
- **FR-035**: The model's accuracy MUST be established by **held-out
  validation** — coefficients fitted on a subset of measured points, then used
  to predict a point deliberately excluded from the fit, with the prediction
  error recorded. Every published figure MUST carry that error and, where it is
  a projection rather than a measurement, its extrapolation factor.
- **FR-036**: Memory MUST be modelled in terms of live heap with a stated
  derivation to resident size, under stated garbage-collector settings, because
  resident size is not a clean function of allocation. Per-workspace event rate
  MUST be a declared parameter of a load profile and MUST NOT be inferred, since
  the event-dispatch term is highly sensitive to it.
- **FR-037**: Where more than one controller deployment serves a shard, the
  model MUST be produced **per deployment** rather than blended, since their cost
  drivers are unrelated. Costs proportional to real infrastructure rather than to
  workspace count — a per-workload-cluster connection and cache — belong to the
  models of the deployments that hold them and MUST be absent from those that do
  not.
- **FR-038**: The harness's **service-specific** parts — how a profile's objects
  are constructed, and how a controller's watch set and engaged count are
  obtained — MUST sit behind a narrow interface, separate from the
  service-agnostic sweep, fit and departure-detection machinery. This feature does
  **not** build a general-purpose characterisation utility: there is one
  controller today, and Principle VIII prohibits the abstraction before a second
  caller. **Trigger to generalise: the conversion plan's P1**
  ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md) A5).
- **FR-039**: Every measured or fitted figure MUST record how its load was
  produced — **synthetic** (objects generated for the sweep) or **observed**
  (a running deployment's real load). Synthetic load can under-measure when
  generated objects fail validation or take cheap error paths rather than real
  reconcile paths, so a figure that does not state its mode MUST NOT be used for
  sizing.

### Key Entities

- **Shard's fleet**: the set of workspaces bound to the project's `APIExport`
  that one process is responsible for — one shard's worth, per the deployment
  model. Its size is the independent variable, and the quantity a per-shard
  capacity limit bounds.
- **Capacity**: the stated load one shard's process can carry, per profile.
  Derived from where measured cost departs from linear, set below that departure point with
  headroom. The primary deliverable, since the deployment model reaches its
  target by composing bounded shards rather than by unbounded scaling.
- **Engagement**: the act of making one workspace reconciled, and the state that
  act creates. Has a cost, a latency, a failure mode, and a lifetime.
- **Watch registration**: one workspace's interest in one type. The unit that
  currently multiplies, and whose cost FR-001 and FR-003 bound.
- **Replica share**: the subset of a shard's workspaces one replica reconciles.
  Distinct from the subset it caches, which FR-016 requires not be conflated
  with it.
- **Scale profile**: a named, reproducible shard shape — workspace count, object
  counts, event rate, active-to-idle ratio — that the harness can construct and
  measure, so that a claim is attached to a scenario rather than a number.

## Success Criteria *(mandatory)*

Per Constitution Principle IV, each criterion below is answered by running a
command and reading its exit status or its recorded result — not by judgement.
Absolute figures marked *(to be set)* are deliberately unset until the harness
of SC-001 produces a baseline; setting them from source-reading estimates would
be exactly the unverified claim Principle V prohibits.

Criteria marked `(gated)` are evaluated only for requirements the measurement
gate found binding; for a closed requirement, the recorded determination is the
result.

### Measurable Outcomes

- **SC-001**: A scale measurement run is a named operation that reports
  engagement latency, per-event delivery cost, per-workspace footprint, and
  throughput at a configurable workspace count and load profile, and reports
  "could not run" distinctly from "failed" when the environment cannot host the
  requested size.
- **SC-002** `(gated)`: Per-event delivery cost is flat across an
  order-of-magnitude increase in workspace count, within a stated tolerance.
- **SC-003** `(gated)`: Time to first reconcile for a newly bound workspace is
  flat across an order-of-magnitude increase in workspace count, within a stated
  tolerance.
- **SC-004** `(gated)`: Event delivery to already-engaged workspaces shows no
  pause attributable to another workspace's engagement, at the largest measured
  workspace count.
- **SC-005** `(gated)`: Cold start of a full shard completes within a stated
  bound that grows no worse than linearly with workspace count.
- **SC-006** `(gated)`: Steady-state footprint per idle engaged workspace is
  within a stated budget *(to be set from SC-001's baseline)*.
- **SC-007**: A shard's workspaces spread across replicas are reconciled exactly
  once each; throughput rises with replica count; losing a replica restores full
  coverage within a stated bound; no workspace is actively reconciled by two
  replicas during handover.
- **SC-008**: A workspace generating disproportionate load is identifiable from
  reported telemetry alone, and telemetry volume stays bounded as workspace
  count grows.
- **SC-009**: Sustained bind/unbind churn over a stated duration leaves the
  process's footprint flat — no growth in goroutines, memory, or telemetry
  series.
- **SC-010**: Before-and-after evidence exists for each cost this feature set
  out to remove, produced by the same harness on the same profile.
- **SC-011**: The project's existing done-condition passes unchanged, including
  the tenancy and lifecycle assertions of the per-workspace wiring feature, with
  no new drift-record entry.
- **SC-012**: The small case does not regress: a single-workspace and a
  two-workspace deployment behave as they do today, on the same measurements.
- **SC-013**: A capacity figure exists per load profile, derived by FR-030's
  procedure, and is published where an operator planning a shard will find it.
  Every gated requirement has a recorded determination against it.
- **SC-014**: A workspace whose engagement fails transiently is engaged
  automatically once the failure clears, without operator action and without
  waiting for the cache resync interval; repeated failure is visible and does not
  grow the process's footprint.
- **SC-015**: Under load against a constrained shard, the process's aggregate
  request rate respects its configured ceiling and backs off when the shard
  signals pressure, rather than amplifying.
- **SC-016**: A process other than the one being measured can read a running
  `core-manager`'s configured capacity, its observed load in FR-027's units, and
  which of the two limits it is approaching — without parsing logs or scraping
  documentation.
- **SC-017**: A fitted resource model predicts a held-out measurement point that
  was excluded from its fit, and the prediction error is recorded and within a
  stated bound. Published sizing figures carry that error and, where projected,
  the extrapolation factor.
- **SC-018**: Sizing guidance for an operator and the runtime capacity signal of
  SC-016 are derived from the **same** fitted model, so a deployment sized from
  the published tables reports its position against the arithmetic it was sized
  with.
- **SC-019**: The sweep, fit and departure-detection machinery runs against a
  service-specific implementation supplied through an interface, demonstrated by
  a second implementation in tests that constructs different objects — evidence
  that characterising another controller later is a matter of supplying that
  implementation rather than rewriting the harness.
- **SC-020**: Every figure the harness reports, and every figure published from
  it, states whether its load was synthetic or observed.

## Clarifications

Two scope questions were raised during specification. Both are resolved; the
resolutions are recorded because each changed the shape of the feature.

- **Q1 — fleet composition** *(affects Story 4's value, FR-009, FR-011,
  SC-006)*. Asked: what proportion of workspaces are actively running Cluster
  API objects versus bound but empty? **Resolved by construction rather than by
  answer.** Because capacity is now stated per load profile (FR-026, FR-027),
  the harness measures both an idle-heavy and an active-heavy profile and states
  a figure for each. The ratio does not need to be known in advance; an operator
  checks their own shard against whichever profile it resembles. This is
  stronger than the answer would have been, since the ratio varies by
  installation and would have been a guess in any case.

- **Q2 — deployment envelope** *(affects Story 5, FR-013, FR-016, SC-007)*.
  Asked: how many workspaces per replica, within what memory budget, and is a
  multi-shard kcp installation available? **Resolved by the deployment model**:
  the unit of scale is a regional shard with replicas scaled per shard, so the
  question becomes per-shard capacity, which SC-013 derives from measurement
  rather than fixing up front. The multi-shard question is answered by the same
  move — regional shards *are* the topology, so cached state is bounded by shard
  capacity by construction rather than by splitting endpoint slices after the
  fact.

  What survives from Q2 and is now stated as a requirement rather than a
  question: replicas divide reconciliation but not cached state (FR-016), so
  "replicas per shard" is not a substitute for a workspaces-per-shard limit.

## Known deviations

- **The admission ceiling is unchanged by this feature.** Reconciliation will
  scale to stated per-shard capacity; admission is still served for at most one
  workspace, or none, until G4 exists. A deployment needing Cluster API's
  defaulting and validation is therefore still limited to one workspace, and
  this feature does not change that. Recorded here so the scale claim is not
  read as a claim about the whole system. **Trigger for removal**: G4 — whose
  feasibility spike has now run and found the work contained rather than
  structural, see [R12](research.md#r12--g4-spike-can-an-admission-request-be-resolved-to-its-workspace--verified-answer-is-yes).
  Note the ceiling is narrower than previously stated: version conversion is
  already multi-tenant-safe, so only defaulting and validation are affected.
- **The measurement environment may not reach stated capacity.** The harness is
  required to report honestly when it cannot host a requested workspace count
  (FR-022). Bounds this feature claims are therefore bounds at the largest count
  actually measured, with any extrapolation to a higher capacity stated as an
  extrapolation. Note this bounds *per-shard* capacity only: the 100,000+ target
  is reached by composing shards and is not something a single-process harness
  is expected to reproduce. **Trigger for closing the gap**: an environment that
  can host a full shard at stated capacity.

## Assumptions

- **The stated target is a requirement, not an anticipation.** Constitution
  Principle VIII forbids building scale work ahead of a concrete need; the
  trigger it names is "a stated requirement", which the repository owner has
  given. This assumption is recorded explicitly because without it this entire
  feature is prohibited by Principle VIII. The measurement gate applies that
  same principle *within* the feature: a cost that does not bind below a usable
  capacity is closed rather than optimised.
- **The unit of scale is a regional shard**, with replicas scaled per shard and
  the target reached by composing bounded shards. Reaching the target within one
  process is explicitly not a requirement, and no requirement here should be
  read as implying it.
- **Per-shard capacity is an output, not an input.** No absolute figure is
  asserted anywhere in this specification. Every one is to be produced by the
  harness and then written down.
- **The existing discovery and cache engine is kept.** This feature changes what
  is layered on the wildcard cache, not the decision to adopt it (ADR-0001, D4).
  The conversion plan's hand-rolled fallback remains the fallback, and would be
  triggered by a requirement here that cannot be met at a public extension
  point — see FR-023.
- **The pinned dependency set is a constraint, not a variable.** Where a pinned
  library already offers a capability this feature needs, using it is preferred
  to building one. Where it does not, that is a finding for FR-023.
- **Workload-cluster cost is proportional to real infrastructure**, not to
  workspace count, and is therefore out of scope — see Out of Scope. If
  measurement contradicts this, the assumption becomes a finding.
- **"Could not run" is a first-class outcome** of the harness, matching the
  project's existing done-condition contract rather than inventing a second
  reporting convention.
