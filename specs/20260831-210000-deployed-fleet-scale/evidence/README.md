# What the deployed instrument costs, and whether it can be believed

Every run with an evidence file, and what it measured:

| File | Clusters | Clusters per workspace |
|---|--:|--:|
| `deployed-core-8x1.json` | 8 | 1 (the calibration: one manager, at the reference's end state) |
| `deployed-all-25x1.json` | 25 | 1 |
| `deployed-all-5x5.json` | 25 | 5 |
| `deployed-all-50x1.json` | 50 | 1 |
| `deployed-all-5x10.json` | 50 | 10 |
| `deployed-all-100x1.json` | 100 | 1 |
| `deployed-all-10x10.json` | 100 | 10 |
| `deployed-all-200x1.json` | 200 | 1 (the target) |
| `deployed-all-20x10.json` | 200 | 10 (the target, packed) |
| `deployed-all-50x10.json` | 50 | 1, at **ten nodes each** — the first run to sample kcp idle |

`deployed-core-8x1.json` comes first because a second instrument nobody has
checked is worth less than the one it was meant to corroborate.

## How to reproduce it

```sh
task test:scale:kind COMPONENTS=core-manager CLUSTERS=8
```

One manager rather than four, deliberately. The in-process sweeps stop at
engagement — every workspace bound and holding its objects — and a run of all
four providers goes past that to take every cluster to Ready. Only the
one-manager run measures the same work the reference did, so only it can check
the two against each other. See `deployedscale.EndStateEngaged`.

kind (single node), kcp v0.32.3, `IfNotPresent`, in-memory dev backend.

## What it says

Core-manager, per workspace, deployed as its own Deployment:

| Workspaces | Goroutines | Heap | Resident | CPU |
|--:|--:|--:|--:|--:|
| 2 | 329 | 11.1 MiB | 70.2 MiB | 0.4s |
| 4 | 331 | 10.8 MiB | 73.4 MiB | 0.7s |
| 8 | 339 | 15.1 MiB | 76.7 MiB | 1.2s |

- **goroutines per workspace: 1.7**
- resident bytes per workspace: 1.0 MiB

## The finding

| Quantity | Deployed | In process | Ratio | Within 20% |
|---|--:|--:|--:|---|
| goroutinesPerWorkspace | 1.7 | 2.0 | 0.86x | yes |

**The two instruments agree.** The same controllers, measured by two rigs that
share no code path — one sweeping an in-process manager, one scraping a pod's
metrics endpoint through a Kubernetes API server — land 14% apart on the
quantity the whole cost model turns on.

That is what makes the deployed figures worth quoting. It is also what makes
the *disagreement* at the other end state worth reading as a finding rather
than a fault: see below.

## Why a run of all four providers reports ten times this

A run with every provider deployed measures core-manager at 17.0 goroutines per
workspace, not 1.7 — reproducibly, from clean linear fits at 2/4/8 and at
3/5/10 workspaces. Both numbers are right. They differ because a complete
provider set takes every cluster to Ready, and a ready cluster costs the core
manager a live ClusterCache — a connection to the workload cluster, its
informers, and their goroutines — that a run stopping at engagement never
opens.

So roughly 15 goroutines per connected workload cluster, on top of 1.7 per
workspace engaged. That number is **not** measured here: this run has no ready
clusters in it, and the run that does has no evidence file committed. It is
recorded as the explanation for a gap, not as a figure to quote.

## What all four managers cost, and whether it stays linear

`deployed-all-25x1.json` and `deployed-all-50x1.json`, fitted against cluster
count:

| Deployment | 25 clusters | 50 clusters | fixed cost, 25 | fixed cost, 50 |
|---|--:|--:|--:|--:|
| core-manager | 17.0 | 17.0 | 344 | 344 |
| kubeadm-bootstrap-manager | 14.0 | 14.0 | 149 | 149 |
| kubeadm-control-plane-manager | 46.0 | 47.0 | 174 | 153 |
| dev-infrastructure-manager | 29.0 | 30.1 | 194 | 171 |
| **TOTAL** | **105.9** | **108.0** | | |

Goroutines per cluster, and the intercept each fit implies.

**It stays linear, and the numbers repeat.** Two runs taken minutes apart over
different ranges — 7/13/25 and 13/25/50 clusters — agree to 2% in total. Core
and bootstrap agree exactly, on both slope and intercept, with a maximum
residual of 0.0 goroutines: their three points are collinear to the integer.

That is worth more than either run alone. A slope that reproduces across a
doubled fleet, from a fresh set of pods, is a property of the software rather
than of the afternoon.

## The target, measured, both ways

200 clusters, every control plane ready and every Machine Ready, taken as one
cluster per workspace and as ten.

| Deployment | 200x1 | predicted | 20x10 | predicted |
|---|--:|--:|--:|--:|
| core-manager | 3,744 | 3,744 | 3,386 | 3,384 |
| kubeadm-bootstrap-manager | 2,949 | 2,949 | 2,769 | 2,769 |
| kubeadm-control-plane-manager | 9,551 | 9,545 | 9,375 | 9,387 |
| dev-infrastructure-manager | 6,172 | 6,163 | 5,991 | 5,996 |
| **TOTAL** | **22,416** | **22,401** | **21,521** | **21,535** |
| **resident** | **1.21 GiB** | | **1.16 GiB** | |

0.07% on both totals. The predictions come from the model below, fitted before
either run existed, to runs of a hundred clusters and fewer. Nothing was
refitted afterwards.

**The two distributions differ by 895 goroutines out of 22,416 — 4%.** Spread
or packed, 200 clusters cost about the same. That is the question this
specification opened with, answered.

No OOM kill, no restart in either run, and the largest container peaked at a
fifth of its 2 GiB limit.

## What the shard's memory actually is, and what it is not

The first runs to sample kcp itself, at ten nodes per cluster:

| machines | heapSys | RSS | RSS/heapSys | goroutines | cores busy |
|--:|--:|--:|--:|--:|--:|
| 130 | 2.10 GiB | 2.22 GiB | 1.06 | 6,439 | |
| 200 | 2.65 GiB | 2.64 GiB | 1.00 | 5,938 | |
| 250 | 2.99 GiB | 2.82 GiB | 0.94 | 7,060 | 3.5 |
| 300 | 3.43 GiB | 3.28 GiB | 0.96 | 5,935 | |
| 500 | 3.75 GiB | 4.00 GiB | 1.07 | 6,112 | 5.0 |

**It is not the embedded etcd.** kcp runs etcd in its own process and bbolt maps
its database file, so a database large enough to matter would show as resident
memory above what the Go runtime has taken from the OS. There is none: resident
tracks heapSys to within a few percent at every point, in both directions.
Whatever kcp is holding, it is holding it on the Go heap.

**It is not per workspace either.** 200 clusters in 200 workspaces, one node
each, completed inside the same 4 GiB limit. Twenty-five workspaces of ten
nodes did not. Workspace count is close to free; machine count is not.

**The marginal cost is 13.0 MiB of live heap per cluster of ten nodes**, from
`deployed-all-50x10.json` — the first run to sample the shard before it had
anything to serve.

| | goroutines | live heap | resident | CPU |
|---|--:|--:|--:|--:|
| idle, no workspaces | 5,757 | 478.8 MiB | 733.5 MiB | 30.9s |
| 13 workspaces, 130 Machines | 6,439 | 1.31 GiB | 2.20 GiB | 399s |
| 25 workspaces, 250 Machines | 7,061 | 1.48 GiB | 2.81 GiB | 745s |
| 50 workspaces, 500 Machines | 8,357 | 1.78 GiB | 3.60 GiB | 1,427s |

The three loaded samples lie on their line to within 1.4% of the range they
span, which is the tightest memory fit this harness has taken. Per Machine that
is 1.30 MiB — but this run cannot split a cost per cluster from a cost per
Machine, because it varied the two together at ten nodes each. What is measured
is the cluster; the per-Machine number assumes the whole of it is per Machine.

Two earlier figures for this were withdrawn, and both were withdrawn for the
same reason: they were differences between large numbers with nothing measured
at zero.

The first, 4 to 8 MiB, was fitted against heapSys — what the runtime has taken
from the OS, which bundles the collector's headroom with the live set. That
headroom grew from 1.44x to 1.85x of live during those runs, so the figure was
tracking the collector rather than the objects.

The second, 1.6 MiB per Machine, was a two-point delta on live heap. Two points
is what this repository's own reports refuse as a slope. It landed within 20% of
the answer, which is luck rather than method: the same arithmetic on the first
two points of *this* run gives 7.0 MiB per Machine, five times too high.

### The idle sample is not a fourth point on the line

It sits 701 MiB below where the loaded samples put the intercept: the fit's own
fixed cost is 1.19 GiB and the process idles at 478.8 MiB. So the shard does not
grow smoothly out of nothing — binding the first workspaces costs it most of a
gigabyte, and every cluster after that costs 13 MiB.

That step is why the fits here are taken over the loaded samples only, with the
idle sample reported beside them rather than inside them. Drawing one line
across both regimes nearly doubles the slope (25.5 MiB per cluster) while
missing the idle point it was fitted through by 290 MB.

**The change is checked against figures that were already established.** Fitted
across all four points, this run put core-manager at 17.8 goroutines per
cluster; over the loaded three it reads 17.0 — which is exactly what the 25x1
and 50x1 runs measured, twice, before a baseline sample existed. The other three
managers move the same way, from 25.6/47.0/31.2 to 25.0/47.1/31.0. Excluding the
idle point does not just tidy the memory fit; it restores the goroutine figures
this file already had.

What the run does **not** resolve is what that 701 MiB step is made of. A
shard binding its first workspaces is building caches, discovery and decoded
schemas, and it is also allocating hard: 4.4 cores were busy through the first
checkpoint. Live heap counts both what is retained and what has not been
collected yet.

### Two caveats on the numbers above

**The top sample was against the ceiling.** kcp's 4 GiB limit puts GOMEMLIMIT at
3.87 GB, and resident at 50 workspaces was 3.86 GB — 99.9% of it. The collector
was holding the process at its soft limit rather than growing to demand, so this
run measured a shard at the edge of the container it was given, and 50 clusters
of ten nodes is roughly what a 4 GiB shard holds.

**Resident is not reported per cluster for kcp, and that is the reason.** The
same three samples miss their resident line by 7% of its range, against 1.4% for
live heap, because resident carries the collector's headroom and, at the top
point, the ceiling. A limit is still set against resident — it is in the table
above — but it is not a cost per cluster and the harness no longer prints it as
one.

### Roughly 120 KiB per stored object

The shard held 5,677 objects at 50 workspaces — 500 Machines, 500 DevMachines,
457 KubeadmConfigs, 759 Secrets, 1,867 Events and the rest — which is about 113
objects per cluster. Against 13.0 MiB per cluster that is roughly 120 KiB of
live heap per stored object, for objects that serialize to a few kilobytes.

The withdrawn 400 KiB figure counted only four objects per Machine and ignored
the events, so it overstated the ratio by more than three times. 120 KiB is
still one to two orders of magnitude above the serialized size, and the profile
below says where it goes.

### The profile says what it is

Taken at 50 workspaces of ten nodes, `bin/kcp-heap-50x1.pb.gz` (the file is
not committed; the run's `kcpHeapTop` fact carries this table):

```
Showing nodes accounting for 1.12GB, 73.47% of 1.53GB total
      flat  flat%                cum   cum%
    0.33GB 21.68%             0.39GB 25.36%  encoding/json.(*decodeState).objectInterface
    0.18GB 11.88%             0.18GB 11.88%  etcd/api/v3/mvccpb.(*KeyValue).Unmarshal
    0.18GB 11.77%             0.18GB 11.77%  apimachinery/pkg/runtime.DeepCopyJSONValue
    0.06GB  3.97%             0.14GB  9.08%  sigs.k8s.io/json ... objectInterface
    0.06GB  3.84%             0.06GB  3.84%  reflect.mapassign_faststr0
    0.06GB  3.77%             0.06GB  3.77%  sigs.k8s.io/json ... unquote
    0.05GB  3.17%             0.51GB 33.48%  apimachinery/pkg/runtime.structToUnstructured
    0.03GB  1.96%             0.03GB  1.96%  etcdserverpb.(*InternalRaftRequest).Marshal
```


**It is the unstructured representation.** `objectInterface` is JSON being
decoded into `map[string]interface{}`. `DeepCopyJSONValue` is those maps being
deep-copied. `structToUnstructured` is 33.5% cumulatively. Between them, the
handling of objects as maps rather than as typed structs is most of the heap.

Every Cluster API type reaches this shard as a CRD through an APIBinding, and
CRD-backed resources are unstructured end to end — decoded, stored, cached,
deep-copied per read and re-encoded, with every field a string key and an
`interface{}` box. A Machine that serializes to a few kilobytes is a large
object graph of small allocations once it is a map.

That accounts for everything else measured. It is heap, because maps are heap.
It scales with objects rather than workspaces, because it is per object. It
comes with CPU saturation, because JSON decoding and deep-copying are what the
CPU is doing.

Goroutines are the one thing it does not account for, and they are not flat:
the shard runs 51.8 more of them per cluster, on top of 5,760 idle. An earlier
version of this file said they stayed near 6,000 across a fourfold change in
fleet size. That came from reading samples across runs of different shapes; a
single run with a baseline in it fits 51.8 per cluster with a worst residual of
a fifth of a goroutine.

And it settles the two earlier hypotheses, both of which this file previously
carried and both of which were wrong. Response buffering does not appear at all.
etcd does appear, and more than before: `KeyValue.Unmarshal` is 11.9% at fifty
workspaces against nothing at twenty-five, which is values being decoded out of
the store as watches are served, not a database held in memory. It is still not
where the heap is.

## The first limit found, and it is not the managers

`NODES_PER_CLUSTER=50` at 200 clusters does not run:

```
kcp stopped serving, so no workspace could advance and this run measured
nothing about the fleet: kcp last exited 137 (OOMKilled) and has restarted
1 time(s): it exceeded its memory limit
```

kcp was killed against its default 4 GiB limit before the first checkpoint, at
50 workspaces of one cluster and fifty nodes — on the order of 2,500 Machines.
In the same shape of run the four managers peaked at a fifth of their own
limits.

**That kill was not the shard being full.** Its live heap at 250 Machines was
1.63 GiB against a 4 GiB limit; the collector had grown the heap to 3.02 GiB
because nothing had told it a limit existed. kcp now runs with GOMEMLIMIT, and
the fleet size at which a 4 GiB shard actually runs out of room has not been
measured since.

So **the shard is what binds, not the controllers**. That is a finding in its
own right and it is not a number: what is measured is that 4 GiB is not enough
for that fleet, not where enough begins. `KCP_MEMORY` exists to map that, by
raising the limit until a run completes and reporting the size that needed it.

Nothing about the managers at fifty nodes per cluster is measured, because no
run has got far enough to sample them.

## The cost model

Runs that pack several clusters into one workspace separate what a cluster
costs from what a workspace costs; runs that give each cluster its own
workspace cannot, because the two rise together. With both kinds committed,
fitting `goroutines = fixed + a·clusters + b·workspaces` across all six
four-manager runs — 25 to 100 clusters, at 1, 5 and 10 clusters per workspace,
21 samples per deployment:

| Deployment | fixed | per cluster | per workspace | largest residual |
|---|--:|--:|--:|--:|
| core-manager | 344 | 15.00 | 2.00 | 1 |
| kubeadm-bootstrap-manager | 149 | 13.00 | 1.00 | 0 |
| kubeadm-control-plane-manager | 155 | 46.07 | 0.88 | 18 |
| dev-infrastructure-manager | 175 | 29.01 | 0.93 | 17 |

Core-manager and the bootstrap provider are fitted to within one goroutine and
zero goroutines respectively, across every run and every fleet shape.

**The per-workspace term is the calibration figure.** core-manager pays 2.00
goroutines per workspace here, fitted from deployed runs at three packing
ratios — and the in-process sweep, a different rig measuring a different way,
reports 2.0. The deployed instrument prices the workspace exactly as the
reference does and additionally prices the cluster, which the reference never
saw.

### It predicts out of sample

Fitting only to the runs of 50 clusters and fewer, then predicting the
100-cluster runs neither fit saw:

| Deployment | Run | Predicted | Measured | Error |
|---|---|--:|--:|--:|
| core-manager | 100x1 | 2044 | 2044 | 0.0% |
| core-manager | 10x10 | 1864 | 1864 | 0.0% |
| kubeadm-bootstrap-manager | 100x1 | 1549 | 1549 | 0.0% |
| kubeadm-bootstrap-manager | 10x10 | 1459 | 1459 | 0.0% |
| kubeadm-control-plane-manager | 100x1 | 4852 | 4851 | 0.0% |
| kubeadm-control-plane-manager | 10x10 | 4782 | 4765 | 0.4% |
| dev-infrastructure-manager | 100x1 | 3171 | 3171 | 0.0% |
| dev-infrastructure-manager | 10x10 | 3094 | 3082 | 0.4% |

Exact for core-manager and the bootstrap provider, in both distributions;
worst case 0.4%. A model fitted below fifty clusters predicts a hundred, at two
different packings, without being told either.

The clearest single illustration is core-manager at a hundred clusters: 2044
goroutines in a hundred workspaces, 1864 in ten. The difference is 180, which
is 2.00 x 90 — the per-workspace term, visible directly in the measurements.

## What this is not

- **Not multi-node.** Every component ran on the kind control-plane node, so
  this says nothing about a deployment whose managers sit on different
  machines. `SPREAD=true` with `WORKERS` is how that gets measured.
- **Not fifty nodes per cluster.** The largest node count measured is ten, and
  only at fifty clusters. The 200x50 target is 10,000 Machines; the largest
  fleet any run here has held is 500 Machines, and it did so against its
  ceiling.
- **Not a per-Machine figure for the managers.** Only kcp was sampled in the
  ten-node run's fits against a baseline; the manager figures in the target
  section come from one-node runs, where a cluster and a Machine are the same
  thing.
- **One quantity reconciled.** Goroutines per workspace is checked against the
  reference. The resident-bytes slope is reported and is not checked against
  anything.

## Provenance

The `source` path in the JSON was rewritten from the absolute path the run
recorded to the repository-relative one, so the reference it names resolves for
anyone who checks it out. `deployed-all-50x10.json` had the same rewrite applied
to its `kcpHeapProfile` fact. No measured value was altered.

That run's `facts` block is left exactly as the run emitted it, which means it
still carries `kcp.residentBytesPerCluster: 58173898` — 55 MiB per cluster,
fitted across the idle sample and the loaded ones together. That number is
wrong, it is what prompted the change described above, and it is kept rather
than corrected because the samples underneath it are the measurement and the
facts are derived from them. Re-derived from the same file today the harness
reports no per-cluster resident figure for kcp at all, and 13.0 MiB of heap.
