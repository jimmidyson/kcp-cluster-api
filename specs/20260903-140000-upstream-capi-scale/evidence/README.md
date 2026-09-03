# Runs of the stock Cluster API climb

| File | Clusters | Nodes each | What it is |
|---|---|--:|---|
| `stock-2-to-4x3.json` | 2, 4 | 3 | First contact. Found the teardown ordering and the container name the sampler read. |
| `stock-2-to-4x3-warm-baseline.json` | 2, 4 | 3 | The same, an hour later. Both fixes held; the baseline was not cold. |
| `stock-2-to-4x3-refixed.json` | 2, 4 | 3 | With the cAdvisor, defrag-before-baseline, object-split and heap-provenance fixes. |
| `stock-2-to-4x3-retention-probe.json` | 2, 4 | 3 | A third identical shape, to see whether cost accumulates per cluster ever created. It does not. |
| `stock-25x1.json` | 25 | 1 | Node sweep, cold controllers. **Its baseline is unusable — see below.** |
| `stock-25x5.json` | 25 | 5 | Node sweep. |
| `stock-25x10.json` | 25 | 10 | Node sweep. |

None is a measurement of Cluster API's ceiling: every one stopped where
`MAX_CLUSTERS` told it to, so each "ceiling" line reads as a floor, correctly.
The sweep does answer **S2**, which is what it was for.

```sh
task test:capi:scale START_CLUSTERS=2 MAX_CLUSTERS=4 NODES_PER_CLUSTER=3 SOAK=0
task test:capi:scale START_CLUSTERS=25 MAX_CLUSTERS=25 NODES_PER_CLUSTER=10 SOAK=0 OUT_NAME=25x10
```

Stock Cluster API v1.14.1 by clusterctl, in-memory DevCluster backend, every
controller Guaranteed with GOMEMLIMIT set by `task test:capi:cluster`.

## S2: goroutines are per-Cluster, heap and etcd are per-Machine

Twenty-five clusters, at one, five and ten nodes each — 25, 125 and 250
Machines — sampled at the end state:

| Component, goroutines | 25×1 | 25×5 | 25×10 |
|---|--:|--:|--:|
| core | 1496 | 1502 | 1506 |
| capd | 1312 | 1282 | 1294 |
| kubeadm control plane | 1097 | 1078 | 1082 |
| kubeadm bootstrap | 405 | 403 | 403 |

**Ten times the Machines, the same goroutines** — within 2%, which is the
run-to-run reproducibility established by the three 2→4 runs. Goroutine cost is
a function of Clusters and is flat in Machines.

Heap is the opposite:

| Component, live heap at 25 clusters | 25×1 | 25×5 | 25×10 | per Machine |
|---|--:|--:|--:|--:|
| core | 20.6 MB | 30.4 MB | 41.1 MB | ~91 KB |
| capd | 24.9 MB | 34.1 MB | 42.3 MB | ~77 KB |
| kubeadm control plane | 20.8 MB | 24.0 MB | 28.2 MB | ~33 KB |
| kubeadm bootstrap | 10.6 MB | 12.2 MB | 14.0 MB | ~15 KB |

And etcd is the cleanest signal in the whole exercise. Baseline keys were 725,
726, 726 across the three runs — the same number three times — and the fleet's
own keys were 612, 1416 and 2174. The 5- and 10-node points fit

    fleet keys ≈ 26.3 per Cluster + 6.06 per Machine

to within one key at 250 Machines. **Two points, not three**: this is a fit
that meets the arithmetic and not the repository's three-point rule, so it
needs a third node count before it is quoted as a cost model.

### Why the 1-node point is not on the curve

That fit predicts 810 keys for 25×1 and the run measured 612. Not noise — a
different shape. `NODES_PER_CLUSTER=1 CONTROL_PLANE_NODES=1` leaves zero
workers, and `demo.NewCluster` omits the `Workers` topology entirely when there
are none, so those clusters have **no MachineDeployment, MachineSet or worker
template at all**. It is a structurally different cluster, not a smaller one.

A node sweep wanting three comparable points should use 3, 5 and 10 nodes.

## The baseline has to wait for the managers to start

The 25×1 baseline caught the kubeadm control plane manager at **35 goroutines**.
Three minutes later, in the 25×5 run, with no fleet created in between, the same
pod reported **375**. Then 378. The 35 was a manager that had not finished
starting.

This corrects what the earlier runs here were read to mean. The warm baseline
in `stock-2-to-4x3-warm-baseline.json` was put down to cost retained from the
first fleet; it was not. The managers reach ~1060 (core) and ~900 (capd)
goroutines with **no clusters at all**, given a minute or two after start. The
mechanism is time since start, not clusters ever created — which is why the
three 2→4 runs agreed on their baselines to about 1%, each having created and
deleted six clusters in between:

| Baseline goroutines | run 2 | run 3 | run 4 |
|---|--:|--:|--:|
| core | 1069 | 1057 | 1061 |
| capd | 905 | 900 | 903 |

So the ladder is sound — rungs do not accumulate — and the fix is not a restart
between rungs but a **wait before the baseline**. The harness now polls until
two consecutive samples agree within 2% and records whether they did
(`baseline` in the facts). Left unfixed, a baseline taken 340 goroutines low
inflates every slope measured from it, which is exactly what the 25×1 run's
apparent per-rung cost did.

## What the API server and etcd bytes cannot tell you yet

Both are contaminated **across runs**, and the sweep shows it plainly:

| Baseline, no clusters | 25×1 | 25×5 | 25×10 |
|---|--:|--:|--:|
| API server resident | 873 MiB | 1.2 GiB | 2.2 GiB |
| etcd backend | 16.6 MiB | 34.7 MiB | 119.3 MiB |
| etcd keys | 725 | 726 | 726 |

The same empty cluster, three times, costing 2.5x more each time. Two separate
causes:

- **The API server's allocator never gives it back.** `heapSys` is a high-water
  mark, so each run starts where the last one peaked. The 25×10 run measured
  its 250-Machine fleet as costing 31 MiB of resident growth, against 559 MiB
  for the 125-Machine fleet before it — not a result, an artefact of an
  allocator that was already large enough. API server memory can only be read
  **within** one run, and only cleanly after a restart.
- **etcd's backend holds uncompacted revisions.** 119.3 MiB against 726 keys is
  the previous run's teardown churn, not live data, and a defrag cannot reclaim
  it: those revisions are still in use until the API server's next compaction.
  The defrag before the baseline reclaimed 9-10 MiB in the first two sweep runs
  and 0.09 MiB in the third, which is the tell. **Keys are trustworthy; bytes
  are not, without a compaction first.**

The API server's heap figure is separately unusable on this cluster, and now
confirmed why: `--profiling=false`, which is CIS benchmark 1.2.18 and comes
from CAREN's ClusterClass. Every run says `heap not post-collection` because
the forced-collection request cannot land, and the numbers show it — the heap
moved by 150 MiB between rungs, in both directions, while the fleet only grew.

`scale-cluster.sh clusterclass` now adds a patch turning it back on, off by
`APISERVER_PROFILING=false`. It rolls the control plane, which is also the only
thing that resets the API server's allocator high-water mark, so it is worth
doing immediately before the run whose absolutes are meant to be quoted.

Until that patch has rolled, use the API server's **goroutines and resident**,
which are monotonic and reproducible within a run, and not its heap.

## What to run next

1. ~~Smoke, retention probe, cold restart, node sweep~~ — done, above.
2. **The ladder** (S1, S3): 25 → 50 → 100 → 200 → 400 at 10 nodes with the
   soak. Note its baseline inherits the sweep's high-water API server and
   uncompacted etcd, so read its *within-run* slopes and not its absolutes.
3. **A third node count** — 3 nodes at 25 clusters — to put the etcd fit on
   three points and off the 1-node structural cliff.
4. **A clean-room ladder**: `./scale-cluster.sh clusterclass` to roll the
   control plane with profiling on — which restarts the API server and clears
   its high-water mark — then restart the four controllers, let the settle wait
   do its job, and climb. That is the run whose absolute numbers can be quoted,
   and the first one whose API server heap means anything.

A prediction, stated as one: the controllers will not be the ceiling. At 25
clusters and 250 Machines the largest is capd at 42 MB of live heap against a
24 GiB limit; extrapolating the fits to 400 clusters and 4000 Machines gives
roughly 670 MB. The candidates are the API server's memory — already 2.4 GiB
holding 250 Machines — and etcd, at roughly 35,000 keys and, at the 27 KB per
key the 25×1 run implies, something under a gigabyte of live data against an
8 GiB quota, before churn and fragmentation.
