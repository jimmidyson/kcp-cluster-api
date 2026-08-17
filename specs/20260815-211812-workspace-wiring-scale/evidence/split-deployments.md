# Splitting controllers into separate deployments

The question: would running the core controllers as separate deployments give
better horizontal scalability, or does the shared cache make it better to keep
them together?

**The shared cache is not the reason to keep them together.** It is per-type and
lazy, so a split deployment only caches what it watches, and cached objects turn
out to be cheap. The thing that gets duplicated is **per-workspace engagement**,
and it is duplicated in full by every deployment regardless of how little that
deployment watches.

So splitting works, and it improves the constraint that actually binds — but it
is priced per deployment, not per type, and the price is highest exactly where
this feature has not yet done its repair work.

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

## What a split actually costs and buys — measured

Splitting the wired set into a core deployment (`clustercache`, `cluster`,
`machine` — 3 controllers, 9 watches) and an infrastructure deployment
(`devcluster`, `devmachine` — 2 controllers, 6 watches):

| Deployment | Goroutines/ws | Live heap/ws | Footprint/ws |
|---|---|---|---|
| Combined (today) | **75.0** | 1,291 KiB | 2.83 MiB |
| Core only | **47.0** | 1,104 KiB | 2.38 MiB |
| Infrastructure only | **32.0** | 1,030 KiB | 2.16 MiB |
| Split total | 79.0 | 2,134 KiB | 4.54 MiB |

Both split figures were **predicted by the R16 formula before being run** — 47
and 32 — and both measured exactly. That is the fourth and fifth out-of-sample
confirmation.

### The trade, stated plainly

| | Combined | Split (2 deployments) | Change |
|---|---|---|---|
| Goroutines in the **binding** process | 75 | 47 | **−37%** |
| Footprint in the **binding** process | 2.83 MiB/ws | 2.38 MiB/ws | **−16%** |
| **Total** goroutines across processes | 75 | 79 | +5% |
| **Total** footprint across processes | 2.83 MiB/ws | 4.54 MiB/ws | **+61%** |

At a 4 GiB per-process limit, the binding deployment holds about **1,710
workspaces against 1,438 combined — 19% more per shard**, for 61% more total
memory.

**Whether that is a good trade depends on which limit is real.** If the
constraint is how large a single pod can be — and at 60,000 goroutines and
2.2 GiB for 800 workspaces it plausibly is — splitting relieves it. If the
constraint is total memory across the shard, splitting makes it worse.

## Why the duplication is 61% when the cache is nearly free

Because the duplicated cost is not the cache. It is the two fixed per-workspace
costs measured in `goroutine-decomposition.md`:

- **engagement** — 2 goroutines and ~464 KiB per workspace, for the scoped
  cluster, its REST mapper and its client;
- **first watch** — a further ~415 KiB per workspace the moment a workspace
  watches anything at all.

Together about **880 KiB per workspace**, and **every deployment pays it in
full**. A deployment watching one type pays the same engagement as one watching
seven. Two deployments, two payments: ~1.76 MiB of the split's 4.54 MiB per
workspace is duplication of costs that have nothing to do with what is cached.

## The sequencing conclusion

**Fix engagement before splitting, not after.**

Every one of the four `build` determinations (FR-004, FR-005, FR-006, FR-008) is
about the engagement path, and the per-workspace engagement cost is precisely
what a split multiplies. Splitting first locks in N copies of the cost the gate
just said to repair. Splitting after — or after the fleet-wide registration of
`fleet-wide-controllers.md` option D, where engagement is 2 goroutines and
464 KiB and there is no first-watch cost at all — is close to free.

Concretely: with today's engagement, a third deployment adds ~880 KiB per
workspace to the shard. Under option D it would add ~464 KiB and no goroutines
beyond 2. The more deployments the design anticipates, the more the engagement
repair is worth.

## The split axis matters more than the decision to split

The core/infrastructure split measured above is the **expensive** kind, because
`Cluster` and `Machine` are watched on both sides.

A deployment for a genuinely new service — the VM-as-a-service or
DB-as-a-service cases ADR-0002 anticipates — watches **disjoint** types. It
duplicates no cache at all, only engagement. That is the cheap kind, and it is
the shape the appliance model was already heading toward.

So the recommendation is not "split" or "don't split". It is:

1. **Split along new-service boundaries freely** — the only duplication is
   engagement, and FR-037 already requires a resource model per deployment
   rather than a blended one, which these measurements now make possible.
2. **Do not split Cluster API core from its infrastructure provider yet.** It
   duplicates the two hottest types' caches and both fixed engagement costs, to
   buy 19% more workspaces per shard. Revisit once the engagement repairs land,
   when the same split gets cheaper.
3. **Treat per-deployment engagement cost as a first-class capacity input.** It
   is ~880 KiB per workspace per deployment today, and it is the number that
   decides whether a split pays.

## What this does not establish

- **Per-type informer fixed cost is unmeasured.** The probe watches one type, so
  the cost of a *second* informer in a process — reflector, indexer, watch
  connection — is not isolated here. It is a fixed cost, not per workspace, so
  it should be small at any real fleet size, but it is not measured.
- **CPU is not measured at all**, so nothing here says whether splitting helps
  or hurts reconcile throughput — which is a separate reason to split, and
  arguably the more common one.
- **The 61% figure is for a 2-way split of this particular set.** It follows
  from the overlap table, and a different split has a different answer; the
  arithmetic is reproducible from the R16 formula plus the fixed costs.
