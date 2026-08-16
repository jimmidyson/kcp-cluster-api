# Review Guide: Workspace wiring that scales to a large fleet

**Generated**: 2026-08-15 | **Spec**: [spec.md](spec.md)

> **Read this first.** This feature contains a **measurement gate**. Eighteen of
> its seventy-nine tasks are marked `GATED` and are conditional on evidence
> produced part-way through. **A requirement closed with figures is a correct
> outcome, not an unfinished task.** A requirement closed *without* figures is a
> finding. See "The gate" below before reviewing any implementation PR.

> **Where this fits.** The target architecture is a **shard appliance** — a
> self-contained box of known capacity, where a region grows by adding another
> box and the system reports when scaling is needed
> ([ADR-0002](../../docs/adr-0002-shard-appliance-scaling.md)). This feature is
> that architecture's prerequisite and is deliberately scoped short of it: an
> appliance cannot declare a capacity nobody has measured. The ADR asks one
> thing of this feature — that capacity be machine-readable rather than merely
> published (FR-028, FR-032, FR-033, SC-016) — because that is cheap now and
> structural to retrofit. Provisioning, the regional scaling controller and
> appliance packaging are **not** in this PR.

## Why This Change

`core-manager` reconciles every workspace bound to the project's `APIExport`
from one process, and the cost of doing so grows with the number of workspaces
— not linearly, but worse. Each engaged workspace registers its own listeners
against the shared wildcard cache, and `client-go` delivers **every** event to
**every** listener, which then discards the ones that are not its own. Adding a
workspace also replays the entire object store into its new listeners while
holding a lock that stops event delivery process-wide.

None of this is visible today because the tests exercise two workspaces. The
conversion plan left "how many workspaces this needs to scale to in practice" as
an open question; it has now been answered — 100,000+, reached by composing
regional shards — which makes the cost structure a blocker rather than a
curiosity.

## What Changes

The feature turns an unknown cost into a **stated, measured, published
per-shard capacity**, and then removes only the costs that measurement shows
actually bind below that capacity. The primary deliverable is a number an
operator can plan a regional shard against, expressed per load profile, plus
runtime reporting so a process can be checked against it. Secondary deliverables
— cheaper event delivery, cheaper engagement, cheaper idle workspaces,
throughput-bearing replicas — are each conditional on evidence.

**No breaking changes.** No upstream Cluster API change, and no new `DRIFT.md`
entry: the whole feature is delivered through public extension points. One
behavioural default changes (`MaxConcurrentReconciles` moves off its
single-tenant upstream value of 10 and becomes configurable).

**What this does *not* change**: admission is still served for at most one
workspace. G4 is unbuilt, so the capacity figures describe *reconciliation
only*. Do not let a "scales to N workspaces" claim be read as a claim about the
whole system.

## How It Works

**The deployment model** is the frame for everything else: one process serves
one regional shard, replicas scale per shard, and the target is reached by
composing bounded shards. This makes the independent variable "workspaces in
this shard" — a number an operator sets — rather than fleet growth.

Two limits, deliberately not conflated, because conflating them sizes a
deployment wrong in both directions:

| Limit | Bounds | Does **not** bound |
|---|---|---|
| Workspaces per shard | Cached state, and every per-workspace cost | — |
| Replicas per shard | Reconcile throughput, availability | Cached state |

That second row is verified, not assumed: caches start in controller-runtime's
cache runnable group (`pkg/manager/internal.go:446`) *before* leader election
(`:477`), so even an idle standby replica holds the shard's entire cache.

**Implementation phases**, in dependency order:

1. **Observability** — per-workspace attribution with bounded cardinality.
   First because the harness cannot measure load the process does not expose,
   and today it exposes none (`SkipNameValidation` aggregates all workspaces'
   metrics under one controller name).
2. **Harness** — profiles, a sweep across geometrically spaced workspace counts,
   knee detection by a defined procedure, reporting through `internal/verify`'s
   *existing* three-outcome contract.
3. **The gate** — baseline, capacity derivation, eight build-or-close
   determinations.
4. **Conditional cost reduction** — if built, by interposing a cache at
   `workspaceManager.GetCache()`, replacing per-workspace listeners with one
   demultiplexing registration per type.
5. **Horizontal scale** — configuring `multicluster-runtime`'s existing sharded
   coordinator rather than building a new mechanism.

Unconditional robustness work (engagement retry, disengagement release,
backpressure) runs in parallel with 2 and 3, since none of it is gated.

## When It Applies

**Applies when**:

- One `core-manager` process serves many workspaces bound to one `APIExport`
- An operator needs to size a regional shard, or diagnose which workspace is hot
- Multiple replicas are run against one shard

**Does not apply when**:

- **Admission/webhook behaviour is at issue** — capped at one workspace by G4,
  untouched here
- **Workload-cluster cost is at issue** — `ClusterCache`'s per-workload-cluster
  connection scales with real infrastructure, not workspace count
- **Workspace placement onto shards is at issue** — kcp's concern; this feature
  states its capacity within a topology it does not schedule
- **Reads are at issue** — already cluster-scoped through the index and
  explicitly not a cost this feature addresses

## Key Decisions

1. **Capacity is an output of measurement, not an input to design.** No absolute
   figure is asserted anywhere in the spec. *Alternative*: pick a target
   (10k/replica, etc.) and engineer to it. *Rejected because* Principle V
   forbids asserting unverified figures, and the cited costs were established by
   reading source, not by running anything.

2. **The measurement gate (FR-031).** Eight cost-reduction requirements are
   conditional on measurement showing their cost binds below a plausible
   capacity. *Alternative*: build them all, since the source reading strongly
   suggests they bind. *Rejected because* Principle VIII prohibits building
   ahead of a **measured** constraint — and it applies to this feature as much
   as to any other.

3. **Interpose at `mgr.GetCache()` rather than rewrite reconcilers.**
   *Alternative*: adopt multicluster-runtime's native `mcreconcile.Request`
   model, which is structurally cleaner and would remove the problem entirely.
   *Rejected because* it means not running unmodified upstream reconcilers,
   which is the repository's whole premise.

4. **Regional shards with per-shard capacity, not one big process.**
   *Alternative*: scale a single process to the full target. *Rejected because*
   cached state cannot be divided by replicas (see the table above), so an
   unbounded per-process fleet has no memory ceiling.

5. **FR-008 is blocked, not worked around.** Sharing a REST mapper across
   workspaces has no clean seam — `Options.NewCluster` returns a concrete type
   and the cache reader is unexported. *Alternative*: reimplement the 323-line
   forked cache reader locally. *Rejected because* Principle II says raise it,
   and Principle I counts copied upstream code as divergence.

## Areas Needing Attention

**The gate is the thing to review most carefully.** Specifically:

- A `close` verdict **must** carry the figures that closed it. The constitution
  requires review to check that an acceptance condition actually ran and
  actually passed; a close with no evidence is a finding, not a decision.
- Equally, do not treat a well-evidenced `close` as incomplete work. Closing
  most of the list is a good outcome.

**Three properties are unconditional and must never be waived**, even inside
gated phases:

- **FR-002** — no workspace observes another's events, under any routing scheme
- **FR-012** — disengagement releases everything engagement acquired
- **FR-015** — no workspace actively reconciled by two replicas

These are Principle VIII's seam exception. The principle exists because this
project has already shipped wiring that served one tenant's admission requests
with another tenant's client, silently. Treat any PR that relaxes one of these
for performance as a blocking finding.

**Reject any PR that implements FR-008 by copying upstream code.** T049 is
deliberately a *file-an-upstream-proposal* task, not an implementation task. A
PR that vendors or reimplements `multicluster-provider`'s unexported forked
cache reader has worked around the finding rather than raising it.

**Check `research.md` tags before code is built on a claim.** Every dependency
claim is tagged `VERIFIED`, `ASSUMED` or `OPEN` with file and line. Two are
`ASSUMED` and each has a spike that must pass first:

- **R3** (sharded coordinator fencing) — spike is T055. Code built on R3 before
  T055 passes is a Principle V violation, and fencing correctness is exactly the
  kind of claim a type signature cannot establish.
- **R8** (shared rate limiter across copied `rest.Config`s) — gated by T065.

**Genuine trade-offs worth arguing about**:

- *Telemetry cardinality* (R7): bounded top-N loses the long tail; full
  labelling risks a new scale problem while fixing an observability one. The
  decision is T004's and should be argued on evidence.
- *Harness excluded from `task verify`*: deliberate — making the done-condition
  depend on a multi-workspace kcp would hold unrelated changes hostage, the same
  reasoning `DRIFT.md` records for the drift check. Reasonable people may
  disagree.
- *`MaxConcurrentReconciles` default*: lowering it trades per-workspace latency
  for fleet-wide footprint. The new default should be justified by measurement,
  not taste.

## Open Questions

- **R10 — can the environment host a meaningful sweep?** The largest open risk.
  If achievable workspace counts sit well below any interesting knee, the sweep
  cannot locate it, capacity figures become labelled extrapolations, and the
  gate's determinations rest on weaker evidence. T012/T013 resolve this and
  **must** run before the harness design is fixed. FR-022 requires this be
  reported as "could not run", never as a pass.
- **R6 — `HasSynced` semantics** for a per-cluster registration. Settled by the
  first TDD cycle of the demux work (T035), not on paper.
- **R7 — telemetry cardinality approach.** Three named candidates; decided in
  T004.

## Review Checklist

- [ ] Key decisions are justified
- [ ] Breaking changes are documented with migration guidance
- [ ] Scope matches the stated boundaries
- [ ] Success criteria are achievable
- [ ] No unstated assumptions

Feature-specific:

- [ ] Every `close` determination carries the figures that closed it
- [ ] No `GATED` task was implemented before its determination existed
- [ ] FR-002, FR-012 and FR-015 hold, and were not relaxed for performance
- [ ] FR-008 was not implemented by copying or vendoring upstream code
- [ ] No claim tagged `ASSUMED` in `research.md` was built on before its spike passed
- [ ] `task drift` is clean and `DRIFT.md` is unchanged — no new carried patch
- [ ] Numeric bounds come from the harness, with before/after on the same profile
- [ ] No assertion was weakened to get a green run

---

<!-- Code phase sections are appended below this line by the phase-manager command -->
