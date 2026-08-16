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

## The units capacity is actually consumed in (FR-027)

Per workspace, at **19 listeners** — the count the wired Cluster API set
registers, and therefore the only density these figures should be read at:

| Unit | idle-heavy | active-heavy | Source |
|---|---|---|---|
| Watched objects | 0 | 10 | profile, declared |
| Sustained event rate | 0 /s | 1 /s | profile, declared |
| Process footprint | **2.72 MiB** | **3.54 MiB** | fitted, R² 0.994 / 0.983 |
| Live heap | 1.40 MiB | 1.47 MiB | fitted, R² 1.000 |
| Goroutine stacks | 466 KiB | 733 KiB | fitted, R² 1.000 |
| Goroutines | 211 | 211 | fitted, R² 1.000 |

Fixed process cost: 19.6 MiB (idle) / 25.3 MiB (active) footprint, 52
goroutines.

### The two profiles are much closer than the spec assumed

FR-026 requires capacity per profile because "a count of idle workspaces and a
count of active ones are not interchangeable units". They are not — but at the
same listener count they are only **1.3× apart** in footprint, not the order of
magnitude the framing implies.

The reason is visible in the table: goroutines are **identical** at 211 per
workspace whether the workspace holds anything or not. Listener registration is
what costs, and an idle workspace registers exactly as many listeners as a busy
one. Ten objects and one event per second add about 800 KiB on top of a 2.72 MiB
fixed-per-workspace cost.

This is a finding about where the cost lives, and it points the same way
everything else here does: **at the wiring, not at the workload.**

## Candidate capacity

Solving `footprint = base + per-workspace × W` for a container limit, then
taking **30% headroom** below it.

### idle-heavy, 19 listeners — the profile that bounds a shard

| Memory limit | Workspaces at the limit | Candidate capacity | Watched objects | Event rate | Extrapolation |
|---|---|---|---|---|---|
| 2 GiB | 746 | **500** | 0 | 0 /s | 7.8× |
| 4 GiB | 1,499 | **1,000** | 0 | 0 /s | 15.6× |
| 8 GiB | 3,005 | **2,100** | 0 | 0 /s | 32.8× |

### active-heavy, 19 listeners

| Memory limit | Workspaces at the limit | Candidate capacity | Watched objects | Event rate | Extrapolation |
|---|---|---|---|---|---|
| 2 GiB | 571 | **400** | 4,000 | 400 /s | 6.3× |
| 4 GiB | 1,150 | **800** | 8,000 | 800 /s | 12.5× |
| 8 GiB | 2,307 | **1,600** | 16,000 | 1,600 /s | 25.0× |

**Recommended candidate: 4 GiB, 800 workspaces** — the active-heavy row, because
a shard sized on the idle figure fails when its tenants start using it. Not the
8 GiB row: it projects 25× beyond anything measured, and a linear model checked
only to 64 workspaces should not be trusted that far without a confirming run at
256 (which R10 says the fixture can host).

Headroom of 30% is a decision, not a measurement. It covers the difference
between synthetic and real load (FR-039), a production reconciler doing work the
stub does not, and the fact that these are projections.

## The goroutine figure deserves its own line

At 800 workspaces this is **169,000 goroutines** in one process, and at 1,600 it
is 338,000. The count does not depend on the profile: it is 211 per workspace
whether the workspace is busy or empty.

Nothing measured says that fails — the runtime schedules them, delivery latency
stayed flat at 13,556 of them, and the memory they cost is already inside the
footprint figure. But it is far outside how a controller process is normally
operated, and no measurement here reaches it. It is named because it is the
figure most likely to bind first for a reason this sweep could not see:
scheduler behaviour, profiling and debugging tools, thread limits, and stack
growth under a real reconciler rather than a stub.

This is the strongest argument in the evidence for FR-003 (bounded per-workspace
watch cost) being **built** rather than closed. 211 goroutines per workspace is
what 19 listeners costs; the requirement is about not paying it.

## What is not covered

1. **CPU is not modelled at all.** The harness records no CPU time, so a CPU
   request or limit cannot be derived from any of this. Sizing guidance must say
   so rather than fill the column.

2. **A stub reconciler.** The measured cost is registering watches and starting
   workers. A production reconciler adds its own footprint per workspace, and
   nothing here bounds it.

3. **64 workspaces measured; up to 2,100 quoted.** Every candidate above is an
   extrapolation, and the factor is in the table because it is the number to
   discount by. A confirming run at 256 would cut the largest factor by four.

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
