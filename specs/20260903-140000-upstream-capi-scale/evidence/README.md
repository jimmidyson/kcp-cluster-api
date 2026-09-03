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
| `stock-ladder-400x10.json` | 25→400 | 10 | **The ladder.** Five rungs, a 30-minute soak of the largest. The result. |

None is a measurement of Cluster API's ceiling: every one stopped where
`MAX_CLUSTERS` told it to, so each "ceiling" line reads as a floor, correctly.
The sweep answers **S2** and the ladder answers **S1** and **S4**.

## The result

**One stock Cluster API management cluster took 400 clusters and 4000 Machines
to every control plane ready and every Machine Ready, and held them for 30
minutes without drifting.** Nothing failed, nothing was OOM killed, nothing
restarted. It is a floor and not a ceiling: 400 was the last rung the ladder
was given.

Getting there from nothing took **15.8 minutes** of wall clock, five rungs
included.

### The cost model (S2), five points, R² ≥ 0.9999

Per cluster, at ten nodes each, fitted across 25 / 50 / 100 / 200 / 400:

| Component | goroutines per cluster | live heap per cluster |
|---|--:|--:|
| core | 14.84 | 0.99 MB |
| capd (DevCluster) | 13.95 | 0.93 MB |
| kubeadm control plane | 27.99 | 0.65 MB |
| kubeadm bootstrap | 1.97 | 0.28 MB |
| **total** | **58.75** | **2.84 MB** |

The kubeadm control plane manager's fit is R² = 1.00000 at 27.99 — it added
exactly 28 goroutines per cluster at every rung. The heap figure is 284 KB per
Machine at this node count, which agrees with the node sweep's independent
~283 KB.

etcd holds **82.7 keys per cluster** at ten nodes (R² = 0.9997, with a 289-key
offset). The sweep's two-point fit predicted 86.9 — exact at the small rungs
and 5% high at 400, so the sublinearity is real but small.

For scale, and labelled as an **indication rather than a comparison**, since
this is a different version and a different instrument from the kcp runs: those
measured 51.7 goroutines per workspace, against 58.75 per cluster here.

### What is actually large

The four controllers hold **1.19 GB of live heap between them at 400 clusters**,
against the 42 GiB of limits `sizing.md` gives them. They are not the
constraint and are not close to being it.

The API server is, by an order of magnitude:

| | baseline | 25 | 50 | 100 | 200 | 400 |
|---|--:|--:|--:|--:|--:|--:|
| resident | 3.06 GB | 2.66 | 3.01 | 4.54 | 8.39 | **12.61 GB** |
| goroutines | 4198 | 4305 | 4329 | 4232 | 4251 | 4193 |
| etcd call latency | 5.1 ms | 5.7 | 6.0 | 6.9 | 9.4 | **12.1 ms** |

Two things worth separating. The API server's **goroutine count is flat** across
a sixteenfold change in fleet size — its cost is memory, not concurrency. And
its **etcd call latency more than doubled** while etcd's own disk numbers did
not move at all (wal fsync 1.5→1.6 ms, backend commit 2.7→2.8 ms), so that is
not a disk running out; it is the API server doing more work per call.

On 32 GiB control plane nodes, 12.6 GB resident at 400 clusters is the number
that decides where this stops.

### Convergence, paced per added cluster

The ladder is incremental, so these are the corrected figures — the report's own
`rung@N` facts divide by the fleet held and read better than the truth, because
that run predates the fix:

| rung | added | created | converged | per added cluster | driver's share |
|---|--:|--:|--:|--:|--:|
| 25 | 25 | 7s | 1m51s | 4.44s | 6% |
| 50 | 25 | 9s | 1m17s | 3.08s | 11% |
| 100 | 50 | 19s | 2m13s | 2.66s | 13% |
| 200 | 100 | 42s | 2m49s | 1.69s | 20% |
| 400 | 200 | 1m29s | 4m50s | 1.45s | 24% |

Per-cluster convergence **improves** with fleet size, three-fold from the first
rung to the last: the work batches.

**The last column is the driver, not Cluster API, and it was the harness's own
fault.** Creation was serial through one client, and that client was on
client-go's default rate limit — `DefaultQPS = 5`, `DefaultBurst = 10`, applied
whenever `QPS` is left at zero, which this driver never set. A 400-cluster rung
is about 680 objects, so over two minutes of it was pure client-side throttling
before the cluster was asked for anything. The first ceiling run made it
unmissable: 7m52s to create 400 clusters, against 1m29s for the 200 the ladder
added at its top rung — more than twice the time for twice the objects.

Both are fixed: namespaces are created concurrently (`CREATE_CONCURRENCY`, 16)
and the client's limits are raised (`CLIENT_QPS`, 200). The `created in` figures
in every run above therefore measure the driver's throttle, not the API server's
admission path, and the "driver's share" column should be read as an upper
bound on the harness's overhead rather than a property of Cluster API. The
convergence column beside it is unaffected — it is timed after creation
finishes.

### The soak (S4)

Thirty minutes holding 400 clusters. Every component's goroutine count and
retained heap ended within 10% of where it started, the stored object count did
not move (33883 throughout), and no control plane fell out of Ready. The one
thing that did move: etcd accumulated **1.1 GiB of reclaimable pages in 30
minutes** with the fleet completely unchanged, so a held fleet of this size
still turns the store over at roughly a gigabyte an hour.

### S3, and why it is loose

Resident growth from baseline to 400 clusters is 9.55 GB over 32,767 stored
objects — **~285 KiB per stored object**, against the ~200 KB the kcp shard
cost. But taking the rungs pairwise gives 511 KiB (100→200) and 240 KiB
(200→400), so this is a figure with a factor of two in it, not a measurement.

The reason is `--profiling=false`, so the heap figure is not post-collection and
only *resident* is usable — and resident carries allocator slack the fleet did
not ask for.

**Profiling cannot be turned on here.** Three ClusterClass patches were tried;
the first two were refused by validation and the third was accepted and broke a
control plane, because CAREN's runtime extension writes that configuration from
code and a whole-list replace discards it. `hack/upstream-capi-scale/README.md`
records all three and why no ordering fixes it.

So the harness now reads the API server's heap five times and keeps the lowest —
the sawtooth's floor, an upper bound on the retained set, labelled as one in the
report. It needs nothing from the cluster. S3 will tighten with it, but the
figure it produces is a bound rather than a measurement, and the ladder's
numbers above predate it in any case.

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

1. ~~Smoke, retention probe, cold restart, node sweep, the ladder~~ — done.
2. **Find the actual ceiling.** Everything so far is a floor. The cost model
   says the controllers have room for thousands and the API server does not:
   at 12.6 GB for 400 clusters on a 32 GiB node, and with etcd beside it,
   somewhere around **800 to 900 clusters** should exhaust the control plane.
   That is a prediction, stated as one, and the run that tests it is

   ```sh
   task test:capi:scale START_CLUSTERS=400 MAX_CLUSTERS=1600 NODES_PER_CLUSTER=10 \
     OUT_NAME=ceiling
   ```

   A rung that fails there is the first real ceiling this exercise has, and
   `Classify` will name which of the three ways it went.
3. **The clean-room run**, for the numbers that get quoted: clear the API
   server's allocator high-water mark, restart the four controllers, and climb
   with the heap floor in place. Not the profiling patch — that is gone, and why
   is in `hack/upstream-capi-scale/README.md`.

   The API server runs as one static pod per control plane node, so restarting
   it means **one node at a time**, waiting for each to come back before the
   next. Deleting them by label deletes all three at once, which is an outage:

   ```sh
   export KUBECONFIG=../../bin/capi-scale.kubeconfig
   for pod in $(kubectl -n kube-system get pod -l component=kube-apiserver \
                  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'); do
     before="$(kubectl -n kube-system get pod "$pod" -o jsonpath='{.status.startTime}')"
     kubectl -n kube-system delete pod "$pod" --wait=false
     # The kubelet recreates the mirror pod under the same name, so wait for the
     # start time to move rather than for the name to reappear.
     until [ "$(kubectl -n kube-system get pod "$pod" \
                  -o jsonpath='{.status.startTime}' 2>/dev/null)" != "$before" ]; do
       sleep 5
     done
     kubectl -n kube-system wait --for=condition=Ready "pod/$pod" --timeout=5m
     sleep 30
   done
   ```

   Check it actually restarted rather than assuming: the run's own
   `cpuSeconds` for `kube-apiserver` is cumulative since process start, so a
   restart drops it from thousands to near zero. If it does not move, the
   kubelet recreated the mirror pod without restarting the container, and the
   manifest has to be touched on the node instead
   (`mv /etc/kubernetes/manifests/kube-apiserver.yaml` out and back).

   **This is an optimisation, not a prerequisite.** Within-run slopes are valid
   without it — it only makes the run's *absolute* baseline honest.
4. **A third node count** — 3 nodes at 25 clusters — to put the per-Machine
   half of the etcd fit on three points and off the 1-node structural cliff.

A prediction, stated as one: the controllers will not be the ceiling. At 25
clusters and 250 Machines the largest is capd at 42 MB of live heap against a
24 GiB limit; extrapolating the fits to 400 clusters and 4000 Machines gives
roughly 670 MB. The candidates are the API server's memory — already 2.4 GiB
holding 250 Machines — and etcd, at roughly 35,000 keys and, at the 27 KB per
key the 25×1 run implies, something under a gigabyte of live data against an
8 GiB quota, before churn and fragmentation.
