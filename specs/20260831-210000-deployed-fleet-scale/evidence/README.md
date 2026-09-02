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
| `deployed-all-50x1-with-baseline.json` | 50 | 1, at one node each — the same run at a second node count |
| `deployed-all-50x5-with-baseline.json` | 50 | 1, at five nodes each — and a third |
| `deployed-all-50x1-collected.json` | 50 | 1, at one node each, **sampled after a forced collection** |
| `deployed-all-50x5-collected.json` | 50 | 1, at five nodes each, sampled the same way |

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

| Deployment | 25 clusters | 50 clusters | 50 again, with a baseline | fixed cost, 25 | fixed cost, 50 |
|---|--:|--:|--:|--:|--:|
| core-manager | 17.0 | 17.0 | 17.0 | 344 | 344 |
| kubeadm-bootstrap-manager | 14.0 | 14.0 | 14.0 | 149 | 149 |
| kubeadm-control-plane-manager | 46.0 | 47.0 | 46.8 | 174 | 153 |
| dev-infrastructure-manager | 29.0 | 30.1 | 29.7 | 194 | 171 |
| **TOTAL** | **105.9** | **108.0** | **107.5** | | |

Goroutines per cluster, and the intercept each fit implies. The third column is
`deployed-all-50x1-with-baseline.json`, taken later on a fresh set of pods with
the fits then restricted to the loaded samples: core and bootstrap land on the
same integers for a third time, and the total moves 0.5%.

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

**The first run to sample the shard before it had anything to serve** was
`deployed-all-50x10.json`, at ten nodes per cluster. Read on its own it prices a
cluster at 13.0 MiB of live heap; read against the two runs that followed it,
that number is an artefact of when the collector last ran, which is the section
after this one.

| | goroutines | live heap | resident | CPU |
|---|--:|--:|--:|--:|
| idle, no workspaces | 5,757 | 478.8 MiB | 733.5 MiB | 30.9s |
| 13 workspaces, 130 Machines | 6,439 | 1.31 GiB | 2.20 GiB | 399s |
| 25 workspaces, 250 Machines | 7,061 | 1.48 GiB | 2.81 GiB | 745s |
| 50 workspaces, 500 Machines | 8,357 | 1.78 GiB | 3.60 GiB | 1,427s |

The three loaded samples lie on their line to within 1.4% of the range they
span, which is the tightest memory fit this harness has taken — and it is a
tight fit to the wrong quantity.

### The same fifty clusters at one, five and ten nodes each

`deployed-all-50x1-with-baseline.json` and `deployed-all-50x5-with-baseline.json`
repeat the ten-node run at other node counts, so three runs differ in one
variable — Machines per cluster.

Per cluster, with each run's own worst residual beside it:

| per cluster | 1 node | 5 nodes | 10 nodes |
|---|--:|--:|--:|
| goroutines | 52.05 (0.3%) | 52.33 (0.1%) | 51.84 (0.0%) |
| CPU seconds | 10.63 (0.3%) | 20.23 (0.5%) | 27.71 (0.8%) |
| live heap | 7.6 MiB (14.1%) | 33.7 MiB (2.5%) | 13.0 MiB (1.4%) |
| resident | 17.5 MiB (4.4%) | 34.9 MiB (2.1%) | 37.7 MiB (7.0%) |

**A Machine costs the shard no goroutines.** Three runs, spanning a tenfold
change in Machines per cluster, agree on 52 per cluster to within 1% — and the
middle one is the highest, so there is not even a trend to argue about. Whatever
those goroutines are, they are per logical cluster and not per object in it.
(This is stated as three figures agreeing rather than as a fitted slope on
purpose: a flat series has almost no range, so the residual test that guards
every other fit here is meaningless against it. The spread is 0.49 goroutines.)

**A Machine costs 1.5 to 2.4 CPU-seconds** to provision. The three per-cluster
figures are 10.6, 20.2 and 27.7, so the first four Machines cost 2.4 CPU-seconds
each and the next five cost 1.5 — the shard gets cheaper per Machine as clusters
get bigger, which is why this is a range and not a slope. A straight line
through the three reads 1.88 per Machine plus 9.5 per cluster and misses by
7.8%.

**And a Machine's memory cost is still not measured — but the reason is now
identified rather than suspected.**

### Live heap sampled at a checkpoint is not comparable between runs

The three heap slopes are 7.6, 33.7 and 13.0 MiB per cluster. The five-node run
prices a cluster at more than twice what the ten-node run does, with half the
Machines in it. That is not a fleet behaving strangely; it is the instrument.

Each run fitted its own samples well — 14.1%, 2.5% and 1.4% — because within a
run the collector's state is roughly consistent from checkpoint to checkpoint.
Between runs it is not. At each run's last sample, live heap as a fraction of
what the runtime had taken from the OS was:

| | 1 node | 5 nodes | 10 nodes |
|---|--:|--:|--:|
| live heap / heapSys | 55% | 73% | 52% |

The five-node run was scraped near the top of a collection cycle and the other
two after one. `go_memstats_heap_alloc_bytes` is what has been allocated and not
yet freed, so it carries that timing into every figure derived from it.

Resident memory does not have that problem — 17.5, 34.9, 37.7 MiB per cluster is
at least monotonic — but it has the opposite one: it carries the collector's
headroom, and at ten nodes it carries the GOMEMLIMIT ceiling as well. A
two-term model fitted to those three misses by 27.8%.

**So the harness now spends a collection to get an answer.** Before each sample
it asks the shard to collect, through net/http/pprof's own `?gc=1`, and every
heap figure taken afterwards is the retained set rather than the retained set
plus whatever had not been swept. See `deployedscale.CollectGarbage`. Runs taken
before that change carry no `kcpHeapSample` fact and should not be compared with
runs taken after it, which say so on their face.

Three runs and a third node count were spent to find this out, which is the
result: **the two quantities that survive contact with a second run are the two
that do not depend on when the collector last ran.**

### The fix works, on its first run

`deployed-all-50x1-collected.json` is the one-node run again, with the shard
asked to collect before each sample. Against the same shape taken the hour
before:

| | as scraped | after a forced collection |
|---|--:|--:|
| live heap as a fraction of heapSys, at the three samples | 63%, 49%, 55% | 47%, 46%, 47% |
| worst residual on the heap fit | 14.1% (refused) | **0.4%** |
| heap per cluster | 7.6 MiB | 9.5 MiB |
| the idle-to-loaded step | 229 MB | 82 MB |

The collector is now in the same state at every sample, which is the whole
point: the ratio is flat to one percentage point where before it wandered by
fourteen. The heap fit goes from the loosest thing this harness has refused to
the tightest it has accepted, and the figure it produces is 26% higher than the
uncollected one — the earlier number was not merely noisy, it was low.

Two things move the other way, and both are the forced collection showing up
where it should. The resident fit gets slightly worse (4.4% to 6.2%, now
refused), because resident is measured immediately after a collection this
harness caused. And the idle-to-loaded step shrinks to 82 MB, because the idle
sample is collected too.

**The per-Machine term is one and two runs away.** The five- and ten-node runs
need retaking this way; until they are, the only collected figure is 9.5 MiB per
one-node cluster. Against 50.8 stored objects per cluster that is 192 KiB of
retained heap per object — the first per-object figure here that is not an order
of magnitude.

**The managers are not collected before sampling**, because they do not serve
pprof. Their heap fits show the same artefact — in this run core-manager's and
the dev provider's were refused while the other two passed — and their resident
figures are what the cost model uses. This is an asymmetry, not an oversight:
the shard is what runs out, so the shard is what got the fix.

### Two collected runs agree on what an object costs

The five-node run retaken the same way:

| per cluster | 1 node, collected | 5 nodes, collected |
|---|--:|--:|
| goroutines | 52.18 (0.2%) | 52.08 (0.0%) |
| live heap | 9.52 MiB (0.4%) | 14.79 MiB (0.4%) |
| stored objects | 50.8 | 81.7 |
| **live heap per stored object** | **196.5 KB** | **189.8 KB** |

**Two runs at different node counts agree on 190 KB of retained heap per stored
object, to within 3.4%.** Uncollected, the same arithmetic on three runs gave
174, 420 and 117 KB. That agreement is the strongest single statement this file
has about the shard: its memory is its object count times something close to
190 KB, for objects that serialize to a few kilobytes.

The five-node run's own slope moved from 35.3 MB per cluster to 15.5 MB — a 56%
drop — which is the pre-collection figure for what it was. Both collected runs
fit their own samples to 0.4%.

**A per-Machine figure is now one run away.** The two collected slopes, 9.98 and
15.51 MB per cluster at one and five nodes, arithmetically split into 1.32 MiB
per Machine plus 8.20 MiB per cluster — still a two-point split and still not
quoted as a measurement. The ten-node run, retaken with collection, is the third
point. It is worth noting that this two-point split agrees with a completely
different route to the same number: the two runs differ by 7.73 stored objects
per extra Machine, and 1.38 MB over 7.73 objects is 179 KB each, against the
190 KB the whole-fleet ratio gives.

### The collections cost CPU, and the run now says how much

Forcing a collection is work the shard would not otherwise do, and it lands on
whichever checkpoint forced it. The five-node run's CPU per cluster went from
20.2 seconds uncollected to 22.2 collected — the same order as the per-Machine
figure being drawn from it. So the CPU figures in a collected run are inflated
by an amount that grows with the heap, and the earlier statement of 1.5 to 2.4
CPU-seconds per Machine came from uncollected runs.

Runs from here on scrape either side of each collection and record
`kcpForcedCollectionCPUSeconds`, so the inflation is a number a reader can
subtract rather than a bias they cannot see. Until a run carries that fact, the
honest range for a Machine's CPU is **1.5 to 3.0 CPU-seconds**, the wider end
being what the collected runs' uncorrected slopes give.

### What one Machine costs, as far as it is known

- **0 goroutines.** Measured, at three node counts, agreeing to 1%.
- **1.5 to 3.0 CPU-seconds** to provision, falling as clusters get larger. The
  narrow end is from the three uncollected runs; the wide end is what the
  collected runs give before subtracting the collections this harness forced,
  which runs from here on record.
- **Memory: one run short.** Two node counts retaken with collection give
  9.52 and 14.79 MiB per cluster, both fitted to 0.4%, and agree on 190 KB of
  retained heap per stored object to within 3.4%. Splitting them into a
  per-cluster and a per-Machine term is still a two-point fit; the ten-node run
  retaken the same way is the third point. What can be said is that a cluster costs the shard tens of MiB and
  that Machines are a part of it rather than the bulk: the one-node run prices a
  bare cluster at 17.5 MiB resident against 37.7 MiB for a ten-node one, so nine
  Machines roughly double a cluster and do not multiply it.

Three earlier per-Machine figures have now been withdrawn: 4 to 8 MiB (fitted
against heapSys, so tracking the collector), 1.6 MiB (a two-point delta on live
heap), and 1.30 MiB (the whole of a ten-node cluster attributed to its
Machines). Each was a plausible number derived from a series that could not
support it.

### The idle sample is not a point on the loaded line, and the fits leave it out

The rule these runs added is that a fit runs over the loaded samples only, with
the idle sample reported beside it. An idle process has not built the caches,
watches and decoded schemas the first bound workspace makes it build, so the
step from nothing to something is not the first stride of the line that follows.

**For the managers the step is real and the rule is corroborated.**
core-manager idles at 352 goroutines where its loaded line puts 399, and
including the idle point drags the slope from 17.0 to 17.8 goroutines per
cluster. 17.0 is what the 25x1 and 50x1 runs measured before a baseline sample
existed, and 17.0 is what all three baselined runs now report. The other three
managers behave the same way. A manager at rest has not started the controllers
a workspace makes it start; that is a step, and fitting through it prices it as
fleet.

**For the shard's goroutines there is no step to speak of.** kcp's loaded fits
put the intercept at 5,715, 5,750 and 5,765 against idle samples of 5,736, 5,802
and 5,757 — within 1% every time. The 52 goroutines per cluster sit on top of a
shard that already had all of them before the first workspace arrived.

**And for the shard's heap the step is not a quantity at all.** Measured three
ways it is 219, 50 and 701 MiB. An earlier version of this file reported the
last of those as a finding — "binding the first workspaces costs most of a
gigabyte" — on one run's evidence. Three runs say it is the same collector
artefact as everything else in the heap column, and it is withdrawn.

### Two caveats on the numbers above

**The top sample was against the ceiling.** kcp's 4 GiB limit puts GOMEMLIMIT at
3.87 GB, and resident at 50 workspaces was 3.86 GB — 99.9% of it. The collector
was holding the process at its soft limit rather than growing to demand, so this
run measured a shard at the edge of the container it was given, and 50 clusters
of ten nodes is roughly what a 4 GiB shard holds.

**Resident is not reported per cluster for kcp in that run, and that is the
reason.** The same three samples miss their resident line by 7% of its range,
against 1.4% for live heap, because resident carries the collector's headroom
and, at the top point, the ceiling. A limit is still set against resident — it
is in the table above — but it is not a cost per cluster and the harness no
longer prints it as one. In the one-node run the two swap places — resident fits
at 4.4% and heap misses at 14.1% — and in the five-node run both fit, at 2.1%
and 2.5%. Neither series is dependably the good one, which is why the check is
applied per series per run rather than settled once.

**The idle heap sample is noisier than the idle process.** The three runs
measured the same idle shard at 478.8, 347.0 and 481.7 MiB of live heap — a 38%
spread — while their idle resident agreed to within 3.7%: 733.5, 751.6 and 760.4 MiB.
Live heap at any instant depends on where the collector is in its cycle, and
with nothing running there is nothing to smooth it. The fitted fixed cost is
the more stable statement about a loaded shard than the idle heap sample is.

### Of the order of 100 KiB of heap per stored object

The shard held 2,276 objects at fifty one-node clusters, 4,204 at five nodes and
5,677 at ten — 45.5, 84.1 and 113.5 per cluster, so between six and ten stored
objects per Machine once the Events are counted.

Divided into each run's heap slope, that is 170, 410 and 117 KiB per stored
object. The spread is the same collector artefact as everything else in this
section, so the honest statement is an order of magnitude and not a figure:
**something like 10^5 bytes of live heap per stored object**, for objects that
serialize to a few kilobytes. The withdrawn 400 KiB figure was inside that band
by accident, having counted only four objects per Machine and no Events at all.

That ratio, not the fleet size, is what makes the shard the thing that binds.
The profile says where it goes.

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
- **Not a per-Machine memory figure.** Three node counts produced three heap
  slopes that disagree by a factor of four in the wrong direction, because live
  heap read from `/metrics` carries the collector's timing. The instrument now
  forces a collection before it samples and one run has been retaken that way;
  the other two node counts have not.
- **Not a per-cluster figure for kcp, strictly.** Every run that has sampled the
  shard put one cluster in each workspace, so its 52 goroutines and 13.0 MiB
  could as truthfully be called per workspace. The managers have been measured
  at three packings and are not ambiguous this way; kcp has not.
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
