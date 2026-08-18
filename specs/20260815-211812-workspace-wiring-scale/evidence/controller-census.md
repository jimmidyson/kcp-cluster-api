# Controller census: what is wired, and what parity would wire

Every per-workspace figure in this feature is `2 + 7×controllers + 1×workers×controllers
+ 2×watches` (R16). So the census is not bookkeeping — it *is* the cost.

**Reproduce with `task scale:census`** (`SCOPE=wired`, the default, or
`SCOPE=parity`). It parses the setup functions' syntax trees rather than
grepping them, because the builder's chained calls carry no leading dot — a
regex misses every `Watches()`, and does so *silently*, returning plausible
small numbers rather than an error.

That is exactly how the earlier estimate of "roughly 4 controllers, 19 watches"
went wrong. It was wrong in both terms, and it moved a published capacity figure
by 2.8× before it was caught.

## Currently wired — `internal/coremanager/setup.go`

| Controller | `For` | `Watches` | `WatchesRawSource` | Informer-backed |
|---|---|---|---|---|
| `clustercache` | 1 | 0 | 0 | 1 |
| `cluster` | 1 | 3 | 1 | 4 |
| `machine` | 1 | 3 | 1 | 4 |
| `devcluster` | 1 | 1 | 0 | 2 |
| `devmachine` | 1 | 3 | 2 | 4 |
| **Total** | **5** | **10** | **4** | **15** |

**5 controllers**, not 4. One of `cluster`'s three watches is behind the
`MachinePool` feature gate, so informer-backed sources are **14 or 15**
depending on it; the tool counts the source unconditionally and reports 15.

`task scale:census` reproduces this table, including that `Cluster` is watched
by all five controllers and `Machine` by three — the overlap that decides what
splitting deployments duplicates.

Measured at that shape — 5 controllers, 14 watches, 2 workers:

| | Predicted by R16 | Measured |
|---|---|---|
| Goroutines/workspace | 75 | **75.0** |
| Footprint/workspace | — | 2.83 MiB |

A third exact out-of-sample confirmation of the formula (after F at 76).

### Two costs this census does not include

1. **The 4 raw sources.** `ClusterCache.GetClusterSource` (used by `cluster`,
   `machine`, `devmachine`) and `devmachine`'s task-manager source are
   channel-backed, not informer-backed, so they do not create a
   `processorListener`. They are almost certainly not free — a channel source
   starts its own goroutine — but the probe registers only informer-backed
   watches, so their cost is **unmeasured**. Expect roughly +4.

2. **Dynamic watches from `external.ObjectTracker`.** Both `cluster` and
   `machine` set `externalTracker` with `Cache: mgr.GetCache()` and add watches
   at runtime as they encounter `infrastructureRef`, `controlPlaneRef` and
   `bootstrap.configRef` targets (R1). These appear only once real objects
   exist, and the probe's stub reconciler never creates them. Expect +2 to +4
   registrations, so +4 to +8 goroutines.

A realistic figure for the wired set is therefore **≈83–87 goroutines per
workspace**, against the 75 measured with static informer watches alone.

## What feature parity would wire

`setup.go` documents the current set as a walking skeleton, with
ClusterClass/topology, RuntimeSDK, MachineSet/MachineDeployment/MachinePool,
ClusterResourceSet and MachineHealthCheck explicitly deferred to Phase 3.

Upstream `core/main.go` at the pinned version wires **15** controllers.
`task scale:census SCOPE=parity` counts 14 of them — it excludes `crdmigrator`,
which is permanently out of scope here (below) — and independently reports **39
informer-backed sources** and **206 goroutines per workspace at 2 workers**,
confirming this file's hand-computed projection:

| Controller | Informer-backed sources | | Controller | Informer-backed sources |
|---|---|---|---|---|
| `cluster` | 4 | | `machineset` | 5 |
| `clusterclass` | 2 | | `topology/cluster` | 4 |
| `clusterresourceset` | 3 | | `topology/machinedeployment` | 2 |
| `clusterresourcesetbinding` | 2 | | `topology/machineset` | 2 |
| `extensionconfig` | 1 | | `clustercache` | 1 |
| `machine` | 4 | | `crdmigrator` | 1 |
| `machinedeployment` | 4 | | | |
| `machinehealthcheck` | 3 | | **Total** | **40**, plus 9 raw |
| `machinepool` | 2 | | | |

`crdmigrator` is **permanently** out of scope here, not deferred: `setup.go`
records that a workspace consuming a bound API via `APIBinding` has no
`CustomResourceDefinition` to migrate. So core parity is **14 controllers, 39
informer-backed sources**.

Adding the dev infrastructure provider already wired (2 controllers, 6 sources):

**Parity ≈ 16 controllers, 45 informer-backed sources.**

### What that costs per workspace

```
2 + 16×7 + 16×2 + 45×2 = 236 goroutines per workspace
```

**About 3.1× the current 75.** And that is before two things that push it
further:

- Upstream's per-controller concurrency defaults are not 2. `core/main.go`
  passes `concurrency(25)` to the topology `machinedeployment` and `machineset`
  controllers, among others. The worker term is `workers × controllers`, so a
  handful of controllers at 25 workers adds tens of goroutines per workspace on
  its own.
- The raw sources and dynamic watches above scale with the controller count too.

Several parity controllers sit behind upstream feature gates
(`ClusterTopology`, `RuntimeSDK`, `MachinePool`), so a deployment that leaves
them off pays less. The 236 is the fully-enabled figure.

## Why this changes the reading

**Capacity figures published today describe a walking skeleton.** At roughly a
third of core parity. Sizing a shard from 75 goroutines and 2.83 MiB per
workspace, then reaching Phase 3, would triple the per-workspace cost under a
capacity figure that was never measured for it.

**And the balance of the cost shifts as the set grows.** Comparing the two:

| Term | Wired today (5 ctl, 14 watch) | At parity (16 ctl, 45 watch) |
|---|---|---|
| Informer registrations | 28 (37%) | 90 (38%) |
| Controller machinery | 35 (47%) | 112 (47%) |
| Workers | 10 (13%) | 32 (14%) |
| Engagement | 2 (3%) | 2 (1%) |

The proportions barely move, because watches and controllers grow together. But
the earlier claim that cache interposition removes "50% at the wired shape" was
computed from the estimated 4-controller/19-watch shape and is **wrong**: at the
real census it removes **37%**, and at parity **38%**.

Controller machinery — the term only fleet-wide controllers can touch — is the
largest single share at both scales, and it stays that way as the set grows.
That strengthens the case in `fleet-wide-controllers.md` for option D rather
than weakening it, and it means option B's ceiling is lower than that document
claims.
