# One cache: the fix, and what it changed

`fleet-two-caches.md` established why the sweep hung — watches were registered
on the local manager's wildcard cache while every read went through the
provider's, two informers over one endpoint with independent lag. This is the
fix and its measurement.

Instrument: `test/integration/sweep`, `TestCoreReconcilerWorkspaceSweep`,
against a real kcp, eight workspaces, one Cluster and one DevCluster each,
`GOMAXPROCS=4`. Raw output: `fleet-active-coremanager.{md,json}`.

The committed run is against the **tagged** fork at `v1.15.0-kcp.6`, with
`GOWORK=off` — the pinned version, not the local checkout a `go.work` would
substitute. That distinction is worth keeping: every earlier figure in this
directory was measured through the workspace override, which is right for
iterating and wrong for a number anyone else has to reproduce.

## The change

Watches are now registered on the cache the reconcilers read through — the
provider's, which is also the only one of the two that is kcp-aware in its
store keys and so can hold two workspaces' identically named objects apart.

Getting at it needed a different constructor. `apiexport.New` builds the
provider and exposes its caches only as an aggregate `Lister` — enough to read
through, not enough to watch. `provider.Options.NewCluster` is handed each
cache as it constructs a scoped cluster from it, and `apiexport.New` does not
forward that option, so `providerwiring.NewAPIExportProvider` assembles the
same provider from the base package with the hook set. Every other option is
`apiexport.New`'s own default, kept identical on purpose.

The ordering problem — controllers are wired before the manager starts, the
caches are built after it does — is what `capicontrollerutil.WildcardRegistry`
is for. A controller records what it wants to watch and a function that will
register it; a cache arriving later is offered to every controller already
registered, and a controller registering later is offered every cache already
known. Neither side has to be second.

## What it fixed

**The hang.** Three consecutive full runs pass, including the whole departure
phase down to zero workspaces. The six runs before the fix all failed, in
activation or in departure, at a workspace that varied run to run.

## What it cost, measured

| Quantity | Before | After |
|---|--:|--:|
| Goroutines per workspace | 2.0 | **2.0** |
| Goroutines at one active workspace | 297 | **243** |
| Goroutines retained per departed workspace | — | **0.0** |
| Watch streams per workspace | 0.00 | **0.00** |
| Watch-list streams per watched type | 5–6 | **1** |
| LIST requests across the sweep | 18 | **0** |
| Requests at eight active workspaces | 279 | **237** |
| Wall clock | 318s (failing) | **178s** |

The per-workspace figure is unchanged, which is the point: the marginal cost of
a workspace was never what was wrong. What changed is the fixed cost. Fifty-four
goroutines and every LIST in the sweep belonged to a second set of informers
that existed only because watches were pointed at the wrong cache. One
watch-list per watched type, for the whole shard, is the shape the design
claimed and now has.

**`goroutinesRetainedPerDepartedWorkspace` is 0.0**, measured over the full
departure from eight workspaces to none — 257 down to 243 at one workspace, and
136 with none bound. A workspace that unbinds gives back everything it took.
That column could not be measured at all before, because the sweep never
reached the end of the departure phase.

**Heap is still not usable from this run.** The harness reports 1.1 MB per
workspace; the table shows why not — flat at ~13.7 MiB through two workspaces,
one step to ~19.9 MiB, then 0.15 MiB per workspace to eight. A line fitted
through a single step is arithmetic, not a measurement. The idle sweep's
~113 KiB remains the better-supported figure and remains an upper bound.

## Shards

The registry replays every declared watch onto each cache as it appears, and a
watch added at runtime — the contract-versioned references the core reconcilers
resolve — goes through it too, so it reaches shards that appear afterwards.
Caches are keyed by the endpoint's host, which is what the provider builds one
per: `provider.endpointSliceUpdate` copies the base config and sets
`cfg.Host = url` for each URL in the `APIExportEndpointSlice`, one per shard.

The caches are disjoint — a logical cluster lives on one shard — so no object
arrives twice, and the demultiplexing handler keys each request on the object's
own cluster rather than on which cache delivered it.

**Not verified against a real multi-shard installation.** The test fixture runs
a single shard. What is exercised is the registry's own behaviour — both
arrival orders, fan-out of two controllers across three caches, idempotence
when the same cache is offered once per workspace, and error attribution — by
unit tests in the fork, plus the single-shard path end to end. A second shard
is expected to work and has not been watched doing it.

**Removal is not handled, deliberately.** controller-runtime has no way to
unregister a source from a running controller, so a shard whose endpoint
disappears leaves its sources behind on a stopped informer, receiving nothing.
That residue is bounded by the number of shards a fleet has ever had — a
handful of long-lived endpoints — rather than by tenants. Stated rather than
fixed, because the fix would have to come from controller-runtime.

## Events

Fixed after this run, and measured on the next one. Every event Cluster API
emitted was being rejected — the recorder was the local manager's, so events
went to the virtual workspace at `/clusters/*`, which serves no core `v1.Event`
and names no logical cluster to write to.

`test/integration/events` establishes both halves. A kcp workspace *does* serve
core v1 Events, so there is somewhere for a tenant's events to land; and with
one broadcaster and one sink serving two workspaces, each event reaches the
workspace of its own object and no other, with the routing annotation stripped
before the write.

`record.EventRecorder` takes no context, so the cluster cannot travel the way it
does for the clients. It travels on the event: the recorder marks each one with
the cluster of the object it is about, and the sink routes on the mark.

The cost is **three goroutines for the process and none per workspace** — the
sweep's baseline moves from 19 to 22, and `goroutinesPerWorkspace` stays at 2.0
with `goroutinesRetainedPerDepartedWorkspace` at 0.0. One broadcaster, whose
single watcher goroutine calls the sink, keeps events off the reconcile path.
Aggregation is shared but does not merge across workspaces, because client-go
keys it on the involved object's UID among other things.

Rejected events across a full eight-workspace sweep: **0**, against one per
event before.

## What is still open

- **`test/integration/coremanager`** compiles and vets against this change but
  skips here: it needs a container runtime.
- Not measured: above eight active workspaces, more than one object per
  workspace, or the parity controller set.
