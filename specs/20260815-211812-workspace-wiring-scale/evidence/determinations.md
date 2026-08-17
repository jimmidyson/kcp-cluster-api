# Gate determinations (FR-031)

Eight gated requirements, each with a `build` or `close` verdict decided
**against measurement**, per the measurement gate and
`contracts/capacity-report.md`.

A `close` verdict is a successful outcome. It records that a cost was measured
and found not to bind below a capacity anyone would configure — Principle VIII
applied to this feature's own contents. Every `close` here records the figures
that closed it and the trigger that would reopen it (FR-025).

**Result: 4 close, 4 build.** All four builds are the *same engagement path*.

| Requirement | Verdict | Basis |
|---|---|---|
| FR-001 — delivery cost independent of fleet size | **close** | measured flat to 1,216 listeners |
| FR-003 — per-workspace watch cost bounded and stated | **close** | measured: 2 goroutines, ~21 KiB per registration |
| FR-004 — engagement cost independent of fleet and objects | **build** | source-verified O(total objects) per registration |
| FR-005 — engagement does not suspend delivery | **build** | source-verified process-wide lock |
| FR-006 — engagement proceeds concurrently | **build** | source-verified serialization on one goroutine |
| FR-008 — no repeated process-wide discovery | **build** (blocked) | source-verified, no public seam |
| FR-009 — idle workspace cost within a stated budget | **close** | measured and stated |
| FR-011 — idle workspace releases non-essential cost | **close** | measured: there is no non-essential cost to release |

Measurements are at the wired census — 5 controllers, 14 informer-backed
watches, 2 workers (`controller-census.md`) — unless stated. Load mode:
**synthetic** (FR-039).

---

## FR-001 — delivery cost MUST NOT grow with engaged workspaces

**verdict: close**

**evidence.** Delivery latency, write to reconcile, measured per workspace at 19
watches per workspace — 1,216 listeners on a single shared informer, harsher
than any real deployment spreads it:

| Workspaces | p50 | p99 | Missed |
|---|---|---|---|
| 8 | 5.54 ms | 6.02 ms | 0 |
| 16 | 5.64 ms | 6.70 ms | 0 |
| 32 | 5.57 ms | 6.53 ms | 0 |
| 64 | 6.01 ms | 8.03 ms | 0 |

Flat across an 8× fleet increase, nothing missed, reproduced across runs.

**The cost is not literally independent of fleet size, and the close says so.**
`client-go`'s `sharedProcessor.distribute` iterates every registered listener for
every event, so dispatch work is O(listeners) by construction. But the
per-listener work is a closure, a comparison and a channel send, against a
baseline of ~6 ms dominated by the round trip to kcp. At 1,216 listeners it is
not detectable.

So this closes on the strong form of the gate question — the cost does not bind
below any capacity anyone would configure — rather than on the literal wording.

**trigger to reopen (FR-025).** A measured p99 delivery latency above 25 ms
attributable to dispatch rather than to the API round trip; or any sweep showing
delivery latency rising with fleet size at any listener density; or a listener
count per informer above ~2,500, which is twice what has been measured.

---

## FR-003 — per-workspace fixed watch cost bounded, stated, and paid only for watched types

**verdict: close**

**evidence.** Measured exactly, by decomposition across six configurations and
confirmed out-of-sample twice (`goroutine-decomposition.md`):

- **2 goroutines per registration** — `processorListener`'s `run` and `pop`
  (`shared_informer.go:1063-1064`), matching the source exactly.
- **~21 KiB live heap per registration.**
- **Bounded**: the per-registration ring buffer is a fixed 1024 slots
  (`shared_informer.go:1279`).
- **Paid only for watched types**: registrations are created per `Watch` call. A
  workspace pays for the 14 its controllers register and for nothing else.

**This requirement asks for the cost to be bounded and stated, not reduced.**
Both clauses hold and the figures are now published. Reducing it is a real
opportunity — registrations are 37% of a workspace's 75 goroutines, and cache
interposition would remove them — but that is a design option
(`fleet-wide-controllers.md`), not a condition this requirement imposes.
Building against FR-003 would be building something the requirement does not
ask for.

**trigger to reopen.** A controller set that registers watches dynamically
without an upper bound; a per-registration cost that stops being constant in
fleet size; or a wiring where a workspace pays for types it does not watch.

---

## FR-004 — engagement cost MUST NOT grow with workspaces already engaged, nor with total object count

**verdict: build**

**evidence, two halves with different answers.**

*Workspace count — satisfied.* R10 measured engagement at a flat ~1.05 s per
workspace to at least 256, and the engagement-only configuration (no
controllers, no watches) measured a constant 2 goroutines and 464 KiB per
workspace with no drift.

*Object count — violated by construction.* `client-go@v0.36.3`
`shared_informer.go:918-934`, verified in R2: registering an event handler on an
already-started informer enqueues **every object in the store** into the new
listener. Engagement therefore costs O(total objects in the shard) per
registration, and a workspace makes 14 of them. At the candidate capacity of 800
active workspaces holding 10 objects each, engaging one more workspace replays
8,000 objects, fourteen times.

**fix.** Cache interposition at `mgr.GetCache()` (R1 VERIFIED seam, R2 VERIFIED
mechanism): serve a joining workspace's initial sync from the existing indexer
via `kcpcache.ClusterIndexName`, in O(objects in that workspace) rather than
O(shard). This is the already-designed work.

---

## FR-005 — engaging a workspace MUST NOT suspend delivery for already-engaged workspaces

**verdict: build**

**evidence.** `shared_informer.go:918` (R2, VERIFIED): the registration path
takes `s.blockDeltas.Lock()` — the same lock the informer holds while
distributing deltas. Every already-engaged workspace's delivery stalls for the
duration of the new registration's replay, which by FR-004 above is O(total
objects in the shard).

**Not measured, and that is stated rather than glossed.** The sweep engages all
workspaces before measuring, never during, so no number here quantifies the
stall. The mechanism is source-verified and the determination does not need the
number — but any *published* claim about stall duration would.

**fix.** The same interposed cache. A registration that becomes a map entry
never touches `blockDeltas`.

---

## FR-006 — engagement of distinct workspaces MUST proceed concurrently

**verdict: build**

**evidence — verified by source read, new in this pass.**
`multicluster-provider@v0.8.0` `pkg/provider/provider.go:348-384`: the APIBinding
informer's `AddFunc` and `UpdateFunc` call `we.update(ctx, t, aware)`
**synchronously**. Those handlers run on `processorListener.run` — one goroutine
per registration. Every engagement for an endpoint is therefore serialized on
that single goroutine.

And each serialized engagement does, inline:

1. `apiutil.NewDynamicRESTMapper` — a **discovery round trip** (R5, and see
   FR-008);
2. `Clusters.Add` → engage every controller → 14 registrations, each replaying
   the whole store under `blockDeltas` (FR-004, FR-005).

This explains R10's measured ~1.05 s per workspace being *flat*: it is flat
because it is serial. A 256-workspace shard takes about 4.5 minutes to engage
with nothing overlapping; the 800-workspace candidate would take about 14.

**fix, and an honest note on whose it is.** The serialization lives inside
`multicluster-provider` and there is no injection point for it — a Principle II
finding to raise, like R5. But the *impact* is dominated by what each engagement
does, not by the fact that they queue: remove the store replay (FR-004) and the
discovery round trip (FR-008) and serialized engagement becomes cheap enough
that concurrency stops mattering. **Fixing the payload is available to us;
fixing the serialization is not.**

---

## FR-008 — per-workspace setup MUST NOT repeat process-wide discovery

**verdict: build — blocked, and MUST NOT be worked around**

**evidence.** R5, VERIFIED: `multicluster-provider@v0.8.0`
`pkg/cache/cluster.go:66` — `NewScopedCluster` calls
`apiutil.NewDynamicRESTMapper(cfg, httpClient)` for every workspace. The
discovery result is identical for every workspace on one virtual workspace URL.

FR-006's source read **raises this from a memory cost to a latency cost**: the
mapper is constructed on the serialized engagement path, so every workspace's
discovery round trip delays every subsequent workspace's engagement.

**blocked twice.** `Options.NewCluster` is an injection point, but its type is
the concrete `*ScopedCluster` constructor signature rather than an interface, so
a custom implementation cannot substitute a shared mapper without reimplementing
`ScopedCluster`. Principle II requires this be raised upstream rather than
worked around, and it MUST NOT be delivered by copying upstream code.

**And it is worse than one round trip per workspace (R17).** Cluster API's
provider model requires core and infrastructure providers to run as separate
deployments, and each engages workspaces independently. A workspace using core
plus one provider therefore builds **two** dynamic REST mappers and pays **two**
discovery round trips, queued behind **two** separate single-goroutine
engagement loops. The cost is multiplied by an architectural requirement, which
raises this above the other blocked items.

---

## FR-009 — steady-state cost of an engaged workspace with no Cluster API objects MUST be within a stated budget

**verdict: close**

**evidence — the stated budget.** Measured at the wired census, `idle-heavy`
(zero objects, zero events):

| Quantity | Per idle workspace |
|---|---|
| Process footprint | **2.09 MiB** |
| Live heap | 1.16 MiB |
| Goroutines | **75** |

For comparison: engagement with no controllers or watches at all costs 2
goroutines and 464 KiB, and an *active* workspace costs 2.83 MiB — 1.35× the
idle figure.

**This requirement asks for a stated figure rather than "whatever it turns out
to be".** It is now stated, measured, reproducible from committed runs, and
fitted with held-out validation at under 0.4% error. That is what closes it.

**The budget is per deployment (R17).** Core and infrastructure providers run as
separate deployments and engage workspaces independently, so a workspace's total
idle cost is the sum over the deployments it is engaged by — today ~2.09 MiB in
core plus a comparable figure in each provider it uses, not 2.09 MiB outright.
Sizing guidance must state it per role.

**trigger to reopen.** The figure moving by more than 25% at a fixed census —
which the sweep will catch, since it is the same measurement — or the census
growing toward parity without the budget being restated. The latter is
near-certain at Phase 3, and lands almost entirely on **core**: every deferred
controller is a core one, taking it from 47 to ~206 goroutines per workspace
while providers stay near 32.

---

## FR-011 — an idle workspace MUST be able to release non-essential cost while remaining able to notice new objects promptly

**verdict: close**

**evidence — this is the determination the measurements changed most.**

An idle workspace costs 2.09 MiB and 75 goroutines. An active one costs 2.83 MiB
and **exactly the same 75 goroutines**. The entire difference is ~0.74 MiB of
object-related heap.

So the question "what could an idle workspace release?" has a measured answer:
**almost nothing**. Its cost is 5 controllers and 14 registrations, and both are
precisely what the requirement's own second clause — "remaining able to notice
new objects promptly" — requires it to keep. Releasing the registrations means
not noticing; releasing the controllers means not acting.

This closes not because the cost is small — 2.09 MiB and 75 goroutines per idle
workspace is the *dominant* cost of a real installation, since most workspaces
are idle — but because it is **essential under this requirement's own
constraint**. There is no non-essential portion to release.

**trigger to reopen — and it is a live one.** If a mechanism appears that can
notice new objects *without* a per-workspace registration, the essential cost
collapses and this requirement becomes both achievable and valuable. That
mechanism is exactly option D in `fleet-wide-controllers.md`: one fleet-wide
registration per type, cluster derived from the object. Under D an idle
workspace costs 2 goroutines and 464 KiB, and FR-011 is satisfied for free.

**So this `close` is conditional on the architecture staying as it is.** Anyone
adopting fleet-wide controllers must revisit it.

---

## What the eight determinations say together

**Delivery is fine. Engagement is not.**

The three unconditional builds — FR-004, FR-005, FR-006 — are not three
problems. They are one code path: a workspace joins, and for each of its 14
watches the informer takes a process-wide lock and replays the entire shard's
store into a new listener, having first done a discovery round trip, with every
other joining workspace queued behind it on a single goroutine.

Two of the three (FR-004, FR-005) are fixed by the interposed cache that R1 and
R2 already verified a seam for. The third (FR-006) has its mechanism upstream,
but its cost is mostly the other two plus FR-008.

**Four requirements closed, and two of them closed for a reason worth
noticing.** FR-003 and FR-009 ask for costs to be *bounded and stated*, not
reduced. They are now both. Reading them as mandates to reduce would have
produced work the spec never asked for — which is what the measurement gate
exists to prevent.

**One close is conditional.** FR-011 closes because the current architecture
makes idle cost essential. A design change would reopen it, and that is recorded
rather than left implicit.

**None of this is affected by the parity trajectory except FR-009**, whose
budget must be restated as controllers are added. The build verdicts get more
urgent at parity, not less: 45 registrations replaying the store instead of 14.

**And more urgent again under the provider split (R17).** Separate deployments
for core and each infrastructure provider are required for extensibility, not
optional, and every deployment pays the engagement path in full. That makes
these four the only work in this feature whose value is multiplied by an
architectural requirement — and makes **core**, which engages every workspace
and grows 4.4× toward parity, the place to spend it.
