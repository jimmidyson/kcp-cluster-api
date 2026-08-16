# Where the 211 goroutines per workspace actually go

The question this answers: given that the `apiexport` provider already uses a
**wildcard cache** — one informer per type for the whole fleet — why does a
workspace cost 211 goroutines? Is that cache duplication that better sharing
would remove?

**It is not.** The wildcard cache is already saving everything it can save. What
remains is per-workspace *controller instantiation*, which no cache change
touches.

## Method

Five configurations against a real kcp, sweeping 8→64 workspaces each, varying
only how the same 19 watches are distributed and how many workers each
controller runs. Marginal cost is the delta between the smallest and largest
point.

| Config | Watches | Controllers | Workers each | Goroutines/ws | Live heap/ws |
|---|---|---|---|---|---|
| A | 19 | 19 | 2 | **211.0** | 1,505 KiB |
| B | 19 | 1 | 2 | 49.0 | 1,289 KiB |
| C | 19 | 19 | 1 | 192.0 | 1,494 KiB |
| D | 1 | 1 | 2 | 13.0 | 911 KiB |
| E | 0 | 0 | — | **2.0** | 464 KiB |

A is the real shape: the wired Cluster API set registers roughly 19 watches
across 5 controllers, each with its own queue and eagerly-started workers.

## The decomposition

Four equations, four unknowns, and it closes exactly:

```
goroutines/workspace = 2  (engagement)
                     + 7  × controllers
                     + 1  × workers × controllers
                     + 2  × watches
```

- **B − D** isolates the watch: 18 extra registrations cost 36 goroutines →
  **2 per watch**. That is exactly `client-go`'s `processorListener`, which
  starts a `run` and a `pop` goroutine per registration
  (`shared_informer.go:1063-1064`, recorded in R2).
- **A − C** isolates the worker: 19 controllers with one fewer worker each cost
  19 fewer goroutines → **1 per worker**.
- **A − B** isolates the controller: collapsing 18 controllers saves 162 →
  **7 per controller** of workqueue and start machinery, beyond its workers.
- **E** measures engagement with no controllers at all: **2 per workspace**,
  which is what the algebra predicts as the base. It was measured rather than
  inferred.

Checks: A = 2 + 19·7 + 19·2 + 19·2 = 211 ✓. B = 2 + 7 + 2 + 38 = 49 ✓.
C = 2 + 133 + 19 + 38 = 192 ✓. D = 2 + 7 + 2 + 2 = 13 ✓.

The internal breakdown of the 7 per controller is not enumerated here — it is a
measured coefficient, not a reading of controller-runtime's source.

## What this means for the wildcard cache

At the real shape, of 211 goroutines per workspace:

| Cost | Goroutines | Share | Can an interposed cache remove it? |
|---|---|---|---|
| Informer registrations (19 × 2) | 38 | **18%** | **Yes** — this is what R1/R2 propose |
| Controller machinery (19 × 7) | 133 | 63% | No |
| Workers (19 × 2) | 38 | 18% | No |
| Workspace engagement | 2 | 1% | No — already minimal |

**The wildcard cache is working.** Engagement costs 2 goroutines and 464 KiB per
workspace, and that is the whole per-workspace cache cost: one reflector, one
indexer, one watch connection to kcp, shared across the fleet. There is no
duplicated informer to remove.

**Cache interposition removes 18% of the goroutines, not most of them.** FR-003's
planned mechanism — replacing per-workspace event-handler registrations with map
entries in an interposed cache (R1, R2) — targets the 38. It leaves the 171 that
are controller-runtime instantiating a full controller per workspace, because a
cache cannot reach them.

This is a correction to an assumption running through the plan: that the
listener fan-out is the dominant per-workspace cost. Measured, it is the
*smallest* of the three controller-side terms.

## The levers that do move the 82%

1. **Fewer controllers per workspace.** B shows it: 19 watches on one controller
   costs 49 goroutines instead of 211, a 77% cut, with the listener count
   unchanged. But controller topology is upstream Cluster API's — five
   controllers with their own queues is how it is written — and changing it is
   exactly the divergence Principle I counts. Not ours to choose.

2. **Fewer workers.** Already configurable (`-max-concurrent-reconciles`, added
   earlier in this feature). Going from 2 to 1 saves 19 goroutines per
   workspace, 9%. Cheap and honest, but small, and it trades reconcile
   throughput for it.

3. **One controller set for the whole fleet.** The multicluster-runtime-native
   model: controllers keyed by type rather than by workspace, with
   `mcreconcile.Request` carrying the cluster name. Goroutines become **O(1) in
   workspace count** rather than O(W). This is the alternative R1 recorded and
   rejected, because it means not running upstream reconcilers unmodified —
   which is the premise of the repository.

The three are ordered by how much they save and by how much they cost in
divergence, and those orders are the same. That is the real tension in this
feature, and it is now quantified rather than argued.

## Memory follows the same shape, with one surprise

Marginal live heap per workspace: 21 KiB per watch registration, 12 KiB per
controller, under 1 KiB per worker. Small.

But **the first controller-and-watch in a workspace costs about 415 KiB more
than each subsequent one** — D is 911 KiB against E's 464 KiB, where the
marginal terms account for only ~33 KiB of the difference. (Roughly 33 KiB of
the gap is the profile: E ran idle-heavy, D active-heavy, and that difference
was separately measured.)

So a workspace pays a large one-off cost the moment it watches anything, on top
of the 464 KiB it pays for being engaged at all. The mechanism is **not
established** — the plausible candidate is the per-workspace scoped informer and
cache-reader machinery being instantiated for the type, but that is a hypothesis
this measurement does not test. It is worth a source read before any figure is
built on it.

Together the two fixed costs — 464 KiB engaged, ~415 KiB on first watch — are
about 58% of a workspace's 1.5 MiB at the real shape. The per-watch and
per-controller terms are the small part.
