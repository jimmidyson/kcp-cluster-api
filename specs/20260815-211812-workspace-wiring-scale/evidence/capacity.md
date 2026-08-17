# Candidate per-shard capacity

Derived from the sweep runs committed in this directory, through the fitted
models in `model-accuracy.md`. **Candidate**, not published: FR-031's
determinations are not yet made, and the largest figures here reach far past
what was measured.

**Load mode: synthetic** (FR-039). Every figure below inherits that caveat.

## What sets capacity, and what does not

No departure from linear was found in any run, at any listener density, up to 64
workspaces. So capacity here is **not** set by a measured breaking point — there
isn't one in the swept range. It is set by a **resource budget**: choose a
memory limit for the shard's `core-manager`, and the fitted footprint says how
many workspaces fit inside it.

That is a weaker kind of capacity figure than a departure point would be, and
saying so matters. A departure point is a property of the system; a
budget-derived figure is a property of the system *and* a decision someone made
about provisioning. It is also the honest one available: the cost is linear as
far as anyone has looked.

> **Corrected 2026-08-16.** The figures below originally used a probe that
> spread 19 watches across **19** controllers. The wired set is **5
> controllers and 14–15 informer-backed watches** — established by reading every
> `SetupWithManager`, see `controller-census.md`. Re-measured there, a workspace
> costs **75 goroutines and 2.83 MiB**, not 211 and 3.54 MiB.
>
> **And these figures describe a walking skeleton.** `setup.go` defers
> ClusterClass/topology, MachineSet/MachineDeployment/MachinePool,
> MachineHealthCheck, ClusterResourceSet and RuntimeSDK to Phase 3. At core
> parity the set is about **16 controllers and 45 watches — roughly 236
> goroutines per workspace, 3.1× today**. A shard sized on the figures below
> and then brought to parity would be over its budget by a factor of three.
> This is the single largest caveat on the numbers in this file.

## The units capacity is actually consumed in (FR-027)

Per workspace, at **14 watches across 5 controllers** — the census of what
`setup.go` actually wires, and therefore the only shape these figures should be
read at:

| Unit | Wired today (5 ctl, 14 watch) | Projected at parity (16 ctl, 45 watch) | Source |
|---|---|---|---|
| Watched objects | 10 | 10 | profile, declared |
| Sustained event rate | 1 /s | 1 /s | profile, declared |
| Process footprint | **2.83 MiB** | not measured | measured |
| Live heap | 1.29 MiB | not measured | measured |
| Goroutines | **75** | ~236 | measured / projected by R16 |

The left column is the shape to size from **today**. The right is what Phase 3
would cost per workspace on goroutines; its memory has not been measured and is
not projected here, because the R16 formula covers goroutines only.

Measured at the wired census, `idle-heavy` costs **2.09 MiB and 75 goroutines**
per workspace — the same goroutine count as active, and 1.35× less memory. That
figure is FR-009's stated budget (`determinations.md`).

### The two profiles are much closer than the spec assumed

FR-026 requires capacity per profile because "a count of idle workspaces and a
count of active ones are not interchangeable units". They are not — but at the
same listener count they are only **1.3× apart** in footprint, not the order of
magnitude the framing implies.

The reason is visible in the measurements: goroutines are **identical** whether
the workspace holds anything or not — 211 at the 19-controller shape, and by the
same argument 75 at the wired census. Registration is what costs, and an idle
workspace registers exactly as many watches and controllers as a busy one. Ten
objects and one event per second add about 800 KiB on top.

This is a finding about where the cost lives, and it points the same way
everything else here does: **at the wiring, not at the workload.**

## Candidate capacity

Solving `footprint = base + per-workspace × W` for a container limit, then
taking **30% headroom** below it.

### active-heavy at the wired census — 14 watches, 5 controllers, 2.83 MiB/workspace

| Memory limit | Workspaces at the limit | Candidate capacity | Watched objects | Event rate | Goroutines | Extrapolation |
|---|---|---|---|---|---|---|
| 2 GiB | 716 | **500** | 5,000 | 500 /s | 38,000 | 7.8× |
| 4 GiB | 1,439 | **1,000** | 10,000 | 1,000 /s | 75,000 | 15.6× |
| 8 GiB | 2,886 | **2,020** | 20,200 | 2,020 /s | 152,000 | 31.6× |

### active-heavy at 19 controllers — retained as a bound, 3.54 MiB/workspace

The committed sweeps were taken at this shape. It is not the wiring, but it
bounds one that spreads its watches more thinly, and it is the measurement the
fitted models in `model-accuracy.md` come from.

| Memory limit | Workspaces at the limit | Candidate capacity | Watched objects | Event rate | Extrapolation |
|---|---|---|---|---|---|
| 2 GiB | 571 | **400** | 4,000 | 400 /s | 6.3× |
| 4 GiB | 1,150 | **800** | 8,000 | 800 /s | 12.5× |
| 8 GiB | 2,307 | **1,600** | 16,000 | 1,600 /s | 25.0× |

**Recommended candidate: 4 GiB, 800 workspaces.** The conservative row rather
than the corrected one — 800 rather than 1,000 — because the correction is
fresh, because the raw sources and `externalTracker`'s runtime watches are
unmeasured additions to the census, and above all because **parity would triple
the per-workspace cost**. A candidate capacity is not the place to spend a
newly-found 25% that Phase 3 will take back threefold. Not the 8 GiB
row either: it projects 25–31× beyond anything measured, and a linear model
checked only to 64 workspaces should not be trusted that far without a
confirming run at 256 (which R10 says the fixture can host).

Headroom of 30% is a decision, not a measurement. It covers the difference
between synthetic and real load (FR-039), a production reconciler doing work the
stub does not, and the fact that these are projections.

## The goroutine figure deserves its own line

At 800 workspaces this is **60,000 goroutines** in one process at the wired
census (75 per workspace) — and **189,000 at parity** (236 per workspace). The
count does not depend on the profile: it is the same whether the workspace is
busy or empty, because listener and controller registration is what costs.

Nothing measured says that fails — the runtime schedules them, delivery latency
stayed flat at 13,556 of them, and the memory they cost is already inside the
footprint figure. But it is far outside how a controller process is normally
operated, and no measurement here reaches it. It is named because it is the
figure most likely to bind first for a reason this sweep could not see:
scheduler behaviour, profiling and debugging tools, thread limits, and stack
growth under a real reconciler rather than a stub.

This is the strongest argument in the evidence for FR-003 (bounded per-workspace
watch cost) being **built** rather than closed. At the wired census, 37% of a
workspace's 75 goroutines are informer registrations — which is what FR-003's
planned cache interposition removes. It is one of two large terms, not the
larger one; `fleet-wide-controllers.md` scores both.

## What is not covered

1. **CPU is not modelled at all.** The harness records no CPU time, so a CPU
   request or limit cannot be derived from any of this. Sizing guidance must say
   so rather than fill the column.

2. **A stub reconciler.** The measured cost is registering watches and starting
   workers. A production reconciler adds its own footprint per workspace, and
   nothing here bounds it.

3. **64 workspaces measured; up to 2,020 quoted.** Every candidate above is an
   extrapolation, and the factor is in the table because it is the number to
   discount by. A confirming run at 256 would cut the largest factor by four.

5. **The census will grow.** Phase 3 parity is about 16 controllers against
   today's 5. The R16 formula projects 236 goroutines per workspace there, and
   the memory at that shape has not been measured at all. Re-running the sweep
   after each controller is added is cheap and is the only thing that keeps
   these figures honest.

4. **One shard, one deployment.** FR-037 requires a model per deployment where
   several serve a shard. Only `core-manager` exists today, so there is one
   model; when a second controller deployment arrives, its coefficients are its
   own and must not be blended into these.

## Held-out accuracy (FR-035, SC-017)

Worst prediction error across all four runs, each point excluded from its own
fit in turn: **0.39%** on live heap, **0.00%** on goroutines. Full table in
`model-accuracy.md`.

That error bound applies to predicting a point *within or just beyond* a sweep
of 8–64 workspaces. It is not evidence that a 12× extrapolation carries the same
error, and must not be quoted as if it were.
