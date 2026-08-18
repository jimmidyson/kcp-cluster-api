# Reconcile throughput and the worker count

The concern: does `MaxConcurrentReconciles = 2` throttle throughput enough to
make this unscalable?

**Yes, for a single workspace's burst — and the measurement says so exactly.**
Throughput is linear in the worker count, so two workers means a workspace
retires two reconciles at a time and no more, however idle the rest of the shard
is.

But the number is the wrong knob. The problem is that workers are a **static
per-workspace partition** of a resource that should be pooled, and raising the
partition size is the expensive way to fix it.

## Measured

`test/integration/scale/throughput_test.go`, against real kcp. 8 workspaces, 40
objects each, all mutated at once to build a backlog; a probe reconciler that
takes a stated 250 ms; one controller per workspace at the worker count under
test.

| Workers | Elapsed | Reconciles/s | Per workspace | Fraction of linear |
|---|---|---|---|---|
| 1 | 10.03 s | 31.9 | 4.0/s | — |
| 2 | 5.01 s | 63.9 | 8.0/s | **100%** |
| 4 | 2.64 s | 121.3 | 15.2/s | 95% |
| 8 | 1.38 s | 231.2 | 28.9/s | 91% |
| 16 | 1.02 s | 313.4 | 39.2/s | 61% — generator-bound, see below |

The 1-worker point matches theory exactly: 40 objects × 250 ms = 10.0 s
measured 10.03. Every point to 8 workers is within 9% of linear.

The 16-worker point is **not** a scaling limit. Issuing the backlog took 666 ms
of the 1.02 s elapsed, so the load generator, not the worker pool, bound that
point — the test says so in its own output rather than leaving it to be
inferred.

### The relationship, stated

```
per-workspace throughput  =  workers / reconcile duration
```

At 2 workers and a 250 ms reconcile, 8 reconciles per second per workspace. A
real Cluster API reconcile that waits on infrastructure — 1 to 5 seconds — gives
**0.4 to 2 reconciles per second per workspace**. A workspace with 100 Machines
to work through would take 50 to 250 seconds at that rate.

## Two defects found while building this, both instructive

**The load generator was the constraint.** The first run issued 320 sequential
get-then-update round trips at ~6 ms each, putting a ~3.8 s floor under every
worker count and making 16 workers look like a 1.7× improvement over one. Fixed
by issuing merge patches (one round trip, not two) concurrently across
workspaces — and by *reporting the issue time*, so a point where the generator
still dominates announces itself instead of being read as a scaling ceiling.

**Managers and workspaces accumulated across points.** A fresh fleet and a fresh
manager per worker count, with `t.Cleanup` deferring the stop to the end of the
test, meant point two ran two managers over sixteen workspaces and point three
ran three over twenty-four — all counting completions into the same measurement.
It produced rates *above* linear and completion counts above the run's own
target, which is what exposed it. Fixed by provisioning once and stopping each
manager before the next point.

Both would have produced a confident, plausible, wrong answer to the question
being asked.

## What the number costs

From the R16 decomposition: **exactly 1 goroutine and under 1 KiB of heap per
worker, per controller, per workspace.** So at the wired census of 5 controllers:

| Workers | Goroutines/workspace | At 800 workspaces | Per-workspace burst |
|---|---|---|---|
| 1 | 70 | 56,000 | 1 / reconcile |
| **2 (today)** | **75** | **60,000** | 2 / reconcile |
| 4 | 85 | 68,000 | 4 / reconcile |
| 8 | 105 | 84,000 | 8 / reconcile |
| 10 (upstream default) | 115 | 92,000 | 10 / reconcile |

Doubling to 4 costs 13% more goroutines and halves worst-case drain time.
Reaching upstream's 10 costs 53%.

## The real finding: the aggregate is enormous, the partition is tiny

A shard at 800 workspaces already has **8,000 worker goroutines** across 5
controllers. If even 5% were active at once that is 400 concurrent reconciles
hitting kcp — the aggregate capacity is not the problem and arguably never was.

The problem is that those 8,000 workers are **statically partitioned 2 per
workspace per controller**. A workspace with a burst of 100 Machines gets 2,
while 7,998 workers sit idle in other workspaces. Load across tenants is bursty
and uncorrelated, which is exactly the condition under which a shared pool beats
a static partition.

Under fleet-wide controllers (`fleet-wide-controllers.md` option C or D), one
controller serves the whole fleet and its workers are a **pool**:

| | Static partition (today) | Shared pool (fleet-wide) |
|---|---|---|
| Goroutines at 800 workspaces | 8,000 | ~50 |
| Available to one bursting workspace | 2 | ~50 |

Better on both axes by two orders of magnitude, and it is the same change that
`fleet-wide-controllers.md` scores for goroutine count and that
`split-deployments.md` wants for the engagement multiplier. **This is the third
independent argument for it, and the first one that is about performance rather
than footprint.**

## Recommendation

1. **Raise the default from 2 to 4.** It buys a 2× improvement in the
   worst case a single tenant can hit, for 13% more goroutines, and the
   measurement shows the return is linear so the trade is exact rather than
   hoped for. FR-010 requires the default be *chosen* for many-tenant operation;
   it can now be chosen against evidence instead of intuition.

2. **Do not chase upstream's 10 by raising the partition.** At 53% more
   goroutines it is the expensive way to buy burst capacity, and it still leaves
   a workspace unable to use idle capacity elsewhere in the shard.

3. **Treat worker pooling as a first-class reason for fleet-wide controllers**,
   not a side effect. It is the only change that raises per-workspace burst
   capacity and lowers total goroutines at the same time.

## What this does not establish

- **The reconciler sleeps rather than computing.** That isolates queueing from
  CPU contention, which is what makes the worker count the only variable — and
  it means these figures say what the worker count permits, not what the machine
  sustains. A real reconciler competing for cores would do worse, and this
  measurement cannot say by how much.
- **CPU is still not measured.** The harness records no CPU time.
- **kcp's own capacity is not measured.** At 800 workspaces bursting
  simultaneously the API server, not the worker pool, would likely bind first.
  Nothing here bounds that.
- **One controller per workspace, not the wired five.** Five controllers
  watching the same type would split one backlog five ways; this measures a
  single controller's pool draining its own queue, which is the quantity
  `MaxConcurrentReconciles` actually governs.
