# Runs of the stock Cluster API climb

Every run with an evidence file, and what it was for:

| File | Rungs | Nodes per cluster | What it is |
|---|---|--:|---|
| `stock-2-to-4x3.json` | 2, 4 | 3 | **First contact, cold controllers.** Preflight, two rungs, a defrag between them, no soak. Found the teardown ordering and the container name the sampler was reading. |
| `stock-2-to-4x3-warm-baseline.json` | 2, 4 | 3 | **The same shape, an hour later, on controllers that had already served a fleet.** Confirms both fixes and shows that the first run's cost did not come back. |

Neither is a measurement of Cluster API's limits. Both stopped where
`MAX_CLUSTERS` told them to, so the ceiling line reads as a floor — correctly.
They are runs of the *instrument*.

## How to reproduce them

```sh
task test:capi:scale START_CLUSTERS=2 MAX_CLUSTERS=4 NODES_PER_CLUSTER=3 SOAK=0
```

Stock Cluster API v1.14.1, installed by clusterctl, on the CAPX cluster
`hack/upstream-capi-scale` builds; in-memory DevCluster backend; every
controller Guaranteed with GOMEMLIMIT set, by `task test:capi:cluster`.

## What the second run fixed, and what it found

Fixed, and visible in the file: every controller reads `ready: true` with its
real `memoryLimitBytes`, so the container-name bug is gone — and with it the
hole where an OOM kill would have been reported as "the fleet did not keep up".
The ordered teardown left nothing behind.

**The baseline is not cold, and that is the finding.** Same pods, never
restarted, in both runs:

| Component | Run 1 baseline | Run 1 @ 2 clusters | Run 2 baseline | Run 2 @ 4 clusters |
|---|--:|--:|--:|--:|
| core | 79 | 1101 | **1069** | 1133 |
| capd | 45 | 939 | **905** | 973 |
| kubeadm control plane | 35 | 426 | **375** | 492 |
| kubeadm bootstrap | 32 | 346 | **333** | 350 |

Run 2's *baseline* — no Clusters anywhere, the first run's fleet deleted and its
namespaces gone — sits within a few percent of run 1's *two-cluster* sample.
Roughly a thousand goroutines per controller arrived with the first fleet and
did not leave with it.

Two readings, and they have opposite consequences:

- **A one-time warm-up.** Caches, informers and worker pools that start on
  first use and then stay. If so the marginal cost is the ~15 goroutines per
  cluster both runs agree on, and a warm baseline is the *right* baseline.
- **Retention proportional to clusters ever created.** If so a climbing ladder
  accumulates every rung it has already left behind, later rungs report the sum
  of all previous ones, and no per-cluster figure from a multi-rung run means
  what it says.

Nothing in these two runs separates them, which is why the next run is the one
that does. Until it has run, **no slope from this harness should be quoted.**

## Two figures the runs could not take

- `residentBytes` and `cpuSeconds` are zero for all four controllers in both
  files. Resident was read from metrics.k8s.io and this cluster has no
  metrics-server; CPU time was not read at all. Both now come from the kubelet's
  cAdvisor exposition, which the harness was already scraping for throttling —
  fixed after these runs, so the zeros stand in the files. Not measured, and not
  to be read as small.
- The etcd column is **not comparable across rungs in either file**: 32.6 MiB
  holding two clusters and 14.1 MiB holding four, which reads as a store
  shrinking as the fleet grows. The defrag ran between rungs but not before the
  baseline, so the baseline and the first rung measured a store carrying a
  previous run's free pages and every later rung measured a defragmented one.
  Fixed by defragmenting before the baseline too.

## The object count moved 2x for the same fleet

Run 1 held 1907 stored objects at four clusters; run 2 held 910, an hour later,
same shape. Events expire on their own TTL, and at these sizes they are most of
the store — the first run was taken on a cluster whose bringup events had not
yet aged out. A per-object cost divided by that total would be wrong by 2x with
nothing in the report looking odd, so `apiserver_storage_objects` is now split:
Cluster API groups, events, and the total.

## What to run next

In this order, because each one gates the next.

1. **The smoke run again, unchanged** (~5 min). The four fixes above have not
   met a cluster. Read back: non-zero resident and CPU per controller, an etcd
   figure that grows with the fleet, a `defrag@baseline` fact, and the stored
   object line split three ways.
2. **The retention probe** — the same 2→4 shape a third time, and compare its
   baseline with run 2's. Plateau near 1069 means warm-up, and the ladder is
   sound. Another ~1000 means retention per cluster ever created, and the ladder
   needs a restart between rungs — which costs comparability, and would be the
   most important thing this exercise has found.
3. **Cold, then climb.** `kubectl rollout restart` all four controllers, confirm
   the baseline returns to ~45-79 goroutines, and only then start a real ladder.
   Every slope should be measured from a cold start, once, rather than from
   whatever the previous run left.
4. **A node-count sweep at a fixed cluster count** — 1, 5 and 10 nodes at the
   same number of clusters, which is what separates per-Cluster cost from
   per-Machine cost. Three points, the same rule as every other figure here (S2).
5. **The ladder**: 25 → 50 → 100 → 200 → 400 at 10 nodes, with the soak, which
   is S1 and S3.

A prediction, stated as one: nothing in these runs suggests storage is the
limit. At three nodes a cluster costs ~38 etcd keys, so 400 clusters is ~16k
keys against a store that held 412k in the kcp runs. The candidates are watch
and goroutine growth in core and capd, the API server's watch caches, and
time-to-converge — which the harness records only as a pair of timestamps and
should probably report per rung.
