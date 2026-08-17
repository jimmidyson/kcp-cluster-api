# Splitting controllers into separate deployments

**The split is not optional.** Cluster API's provider model puts infrastructure
providers in their own deployments, authored and versioned by third parties —
AWS, Azure, vSphere, Metal3. A design that requires every provider to live in
one process is not extensible, and no measurement outranks that.

An earlier version of this file recommended deferring the core/infrastructure
split on cost grounds. That framed a structural requirement as a trade, and was
wrong. What follows is the same evidence read against the constraint rather than
against a choice.

**The question that remains is not whether to split, but what the split costs
and where the cost lands.** It lands on per-workspace engagement, paid in full
by every deployment a workspace uses — which makes the engagement repairs the
highest-value work in this feature rather than one item among several.

## Engagement is sparse per export — verified

The multiplier is smaller than "one per deployed provider", and this is the
fact that makes the architecture work.

`multicluster-provider@v0.8.0` `pkg/provider/provider.go:259-300`: a provider
watches the virtual-workspace URLs of **its own** `APIExportEndpointSlice`, and
`ObjectToWatch` defaults to `APIBinding`. So a deployment engages only the
workspaces that have bound *its* export.

That gives two distinct deployment roles with different capacity
characteristics:

| Role | Engages | Capacity binds on |
|---|---|---|
| **Core** | every workspace using Cluster API | total workspaces in the shard |
| **Provider** | only workspaces that bound that provider | that provider's adoption in the shard |

A workspace using one infrastructure provider is engaged **twice** — by core and
by that provider — not once per deployed provider. Adding a fifth provider to a
shard costs nothing for workspaces that do not use it.

This is exactly the independent scaling the appliance model wanted: providers
scale with their own adoption, core scales with the shard.

## The cache is per-type and lazy — verified

`multicluster-provider@v0.8.0` `pkg/cache/wildcard.go` — `wildcardCache` embeds
controller-runtime's `cache.Cache` and resolves `GetSharedInformer` per GVK.
Informers are created on first request for a type. A process therefore holds
informers only for the types its own controllers watch.

Splitting duplicates a type's cache **only where two deployments both watch it**.

### The overlap, from the census

| Controller | Types watched |
|---|---|
| `clustercache` | Cluster |
| `cluster` | Cluster, Machine, MachineDeployment, MachinePool |
| `machine` | Machine, Cluster, MachineSet, MachineDeployment |
| `devcluster` | DevCluster, Cluster |
| `devmachine` | DevMachine, Machine, DevCluster, Cluster |

Seven distinct types. **`Cluster` is watched by all five controllers and
`Machine` by three** — so a core/infrastructure split duplicates exactly the two
largest object populations. That is not incidental; Cluster API's controllers
are cross-cutting on those two types by design.

## Cached objects are cheap — measured

Varying objects per workspace at a fixed 14 watches and 5 controllers:

| Objects/workspace | Live heap/workspace | Goroutines/workspace |
|---|---|---|
| 0 | 1,188 KiB | 75.0 |
| 10 | 1,291 KiB | 75.0 |
| 30 | 1,280 KiB | 75.0 |

Going from 10 to 30 objects — three times the cached population — moved live
heap per workspace by **less than the measurement noise**. The 0→10 step costs
~103 KiB, most of which is materialising the type's store rather than the
objects in it.

So duplicating a type's cache across two deployments costs very little in object
storage. **The wiring costs; the objects do not.**

## What each role costs — measured

Splitting the wired set along the mandatory boundary — core (`clustercache`,
`cluster`, `machine`) and infrastructure (`devcluster`, `devmachine`):

| Deployment | Controllers | Watches | Goroutines/ws | Live heap/ws | Footprint/ws |
|---|---|---|---|---|---|
| Core | 3 | 9 | **47.0** | 1,104 KiB | 2.38 MiB |
| Infrastructure | 2 | 6 | **32.0** | 1,030 KiB | 2.16 MiB |
| *(combined, for reference only)* | 5 | 14 | 75.0 | 1,291 KiB | 2.83 MiB |

Both split figures were **predicted by the R16 formula before being run** — 47
and 32 — and both measured exactly. Fourth and fifth out-of-sample
confirmations.

The combined row is a reference point, not a deployment shape. Nothing will run
that way.

### A shard's capacity is the minimum over its deployments

Not a single number. Core engages every workspace, so **core binds first** and a
shard's workspace capacity is core's. At a 4 GiB per-process limit, core holds
about **1,710 workspaces** — more than the 1,439 the combined figure suggested,
because core alone is cheaper per workspace than core plus an infrastructure
provider.

Each provider deployment is then sized for its own adoption, independently.

### The asymmetry that matters for Phase 3

**Parity growth lands almost entirely on core.** Of the deferred controllers —
ClusterClass and topology, MachineSet, MachineDeployment, MachinePool,
MachineHealthCheck, ClusterResourceSet, RuntimeSDK — every one is core. A
provider deployment stays at roughly 2 controllers and 6 watches.

| | Today | At core parity |
|---|---|---|
| Core deployment | 47 goroutines/ws | ~206 goroutines/ws |
| Provider deployment | 32 goroutines/ws | ~32 goroutines/ws |

Core grows 4.4×; providers stay flat. **Core is the scaling problem, and it is
the deployment every workspace is engaged by.** Anything that reduces
per-workspace cost is worth roughly four times as much in core as in a provider.

## What the split actually costs, and it is not the cache

The duplicated cost is not the cache. It is the two fixed per-workspace costs
measured in `goroutine-decomposition.md`:

- **engagement** — 2 goroutines and ~464 KiB per workspace, for the scoped
  cluster, its REST mapper and its client;
- **first watch** — a further ~415 KiB per workspace the moment a workspace
  watches anything at all.

Together about **880 KiB per workspace**, and **every deployment pays it in
full**. A deployment watching one type pays the same engagement as one watching
seven.

For a workspace using core plus one provider, that is ~1.76 MiB of its ~4.54 MiB
total — **39% of a workspace's cost is fixed engagement overhead**, and under the
provider model it is structural rather than avoidable.

The cache duplication that comes with it is real but small: `Cluster` and
`Machine` are watched on both sides, and cached objects measured at less than
the noise floor. Splitting duplicates the *wiring*, not the data.

## What follows: engagement is now the top priority

The four `build` determinations — FR-004, FR-005, FR-006, FR-008 — are all the
engagement path. Before this constraint they were four of several things worth
doing. Under a mandatory split they are the only work whose value is
**multiplied by an architectural requirement**:

| Repair | Effect per workspace | ×2 deployments |
|---|---|---|
| FR-004/FR-005 — interposed cache | removes store replay and the process-wide stall | twice over |
| FR-008 — shared REST mapper | removes one discovery round trip per workspace | **two round trips per workspace** |
| Option D — fleet-wide registration | 880 KiB → 464 KiB fixed | **1.76 MiB → 0.93 MiB** |

FR-008 in particular gets worse than recorded: each deployment builds its **own**
dynamic REST mapper per workspace, on its **own** serialized engagement path. A
workspace using core and one provider pays two discovery round trips, queued
behind two separate single-goroutine engagement loops.

And the engagement repairs are worth most in **core**, which engages every
workspace and whose census grows 4.4× on the way to parity.

## The split axis still matters — but as a gradient, not a gate

The core/infrastructure split is the expensive kind: `Cluster` and `Machine` are
watched on both sides, so their caches are duplicated. That is a cost to be
aware of, not a reason to avoid the split.

A deployment for a genuinely new service — the VM-as-a-service or
DB-as-a-service cases ADR-0002 anticipates — watches **disjoint** types and
duplicates no cache at all, only engagement. Those are cheaper still, and the
sparse-engagement property means they cost nothing at all for workspaces that do
not use them.

## Consequences to carry forward

1. **FR-037 is unconditional.** It reads "where more than one controller
   deployment serves a shard, the model MUST be produced per deployment". There
   will always be more than one. The per-deployment model is required, not
   contingent, and `capacity.md` must state capacity per deployment role rather
   than as one number.

2. **A shard's capacity is core's capacity**, since core engages every
   workspace. Provider deployments are sized separately against their own
   adoption.

3. **FR-009's stated budget is per deployment.** A workspace's total idle cost
   is the sum over the deployments it is engaged by — today ~2.09 MiB in core
   plus ~2.16 MiB in its provider, not 2.09 MiB total.

4. **Engagement cost is the primary scaling term**, not a secondary one, because
   it is the only term multiplied by deployment count.

## What this does not establish

- **Per-type informer fixed cost is unmeasured.** The probe watches one type, so
  the cost of a *second* informer in a process — reflector, indexer, watch
  connection — is not isolated here. It is a fixed cost, not per workspace, so
  it should be small at any real fleet size, but it is not measured.
- **CPU is not measured at all**, so nothing here says whether splitting helps
  or hurts reconcile throughput.
- **Real provider deployments are bigger than the dev provider measured here.**
  `devcluster`/`devmachine` are 2 controllers and 6 watches; a production AWS or
  vSphere provider wires more. The provider figures are a floor.
- **The sparse-engagement property assumes providers publish separate
  `APIExport`s.** That is how the provider model maps onto kcp, and it is what
  the source supports, but this repository has not yet wired a second export to
  demonstrate it.
