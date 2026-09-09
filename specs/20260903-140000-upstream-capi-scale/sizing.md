# What to provision, and where the numbers come from

Every figure here is an **extrapolation from the kcp runs onto different code**
(stock v1.14.1 against the v1.15.0-kcp.12 fork) unless it says otherwise. It is
sizing guidance so a run is not starved, not a prediction the run is meant to
confirm. Provision generously: a rung that dies because the box was small
measures the box.

## The shape of the ask

The ladder climbs 25 → 50 → 100 → 200 → 400 clusters, and the run is the
`nodes-per-cluster` you choose. Two useful settings:

- **10 nodes per cluster** — 400 clusters is 4,000 Machines. Reachable, and the
  interesting rungs are in the middle.
- **50 nodes per cluster** — 200 clusters is the 10,000 Machines the kcp
  specification set as its target and never reached. This is the run worth
  taking.

## Nodes

| Role | Count | What runs on it | Ask for |
|---|--:|---|--:|
| Control plane | **3** | kube-apiserver, etcd | **64 GiB, 16 vCPU** each, fast SSD, etcd on its own disk |
| Generic pool | **4** | all four managers, cert-manager, metrics-server | **32 GiB, 16 vCPU** each |

Seven nodes, one worker pool, no taint, and the managers kept off the control
plane nodes by the prepare tool.

### What 32 GiB control plane nodes hold, measured

The control plane figure above was 32 GiB until the runs of 8 September 2026,
which are the first taken with fresh API servers, the providers on the workers
and etcd on its own disk. They fix the number rather than extrapolate it:

| Fleet | Outcome on 3 x 32 GiB control plane, 4 x 32 GiB workers |
|---|---|
| 500 clusters, 5,000 Machines | converged in 11 min; API servers 14 to 21 GiB each |
| **1,000 clusters, 10,000 Machines** | **converged in 11 min; API servers 17 to 27 GiB each, etcd 1.9 GiB file, 74% of the control plane's allocatable memory in use** |
| 1,500 clusters, 15,000 Machines | converged in 11 min once the harness read the fleet from a watch; API servers 22 to 25 GiB each, 77% of allocatable. The two runs before, with the harness listing every Machine through the VIP every fifteen seconds, failed here on the VIP holder's etcd member |
| 2,000 clusters, 20,000 Machines | did not hold: API servers 23 to 28 GiB each with 212 requests in flight on the VIP instance, and the etcd member under the VIP holder timed out kube-vip's lease eight minutes in |

So **3 x 32 GiB is a suitable control plane for 1,000 clusters of ten nodes
with margin, and holds 1,500 as its edge**. The qualification on 1,500 is that
it converged once, at 77%, with nothing but the platform on the control plane
nodes and a harness that costs the API server nothing per poll; a production
cluster's own external clients enter through the same VIP the harness did.
It is not suitable for 2,000. The API server's resident set grows roughly 3 to
4 GiB per 500 clusters at this point on the curve, and what it takes comes out
of the page cache etcd's backend file has to live in. 64 GiB is the ask for
anything above 1,000, and the default the provisioning script now uses.

A further run the same day, with etcd's page cache fenced (`MEMORY_QOS=true`,
`ETCD_MEMORY_REQUEST=6Gi`, `memory.low` read back at 6 GiB on every member's
cgroup), moves the failure rather than removing it, which is what settles the
question. etcd's disk path stayed flat at every rung: WAL fsync 3 ms, backend
commit 6 to 8 ms, and no member showed the five thousand slow applies that the
VIP holder's member showed in every unfenced run. What failed instead was the
node. One control plane node held the etcd leader, the kube-controller-manager
lease and, from 1,500, the VIP; its API server did two to four times the CPU
of the other two at every rung and stopped growing at 25.4 GiB from 1,500 on,
with the whole node at 28 GiB of 30.9 allocatable. At 2,000 that API server's
CPU halved, kube-controller-manager could not renew its lease against it for
107 s and stepped down, etcd's peer round trip on all three members went from
5 ms to 250 to 390 ms, the leader moved, and 788 proposals failed. 1,000 was
the clean rung; 1,500 converged with the VIP moving once. Fenced or not,
32 GiB per node is the number that ends the climb between 1,500 and 2,000,
and the fence is worth keeping for what it does to etcd's own numbers.

### What 64 GiB control plane nodes hold, measured

The run of 9 September 2026, on the same cluster rebuilt with 64 GiB control
plane nodes, the fence on (`memory.low` read back at 6 GiB on every etcd
member), fresh API servers, and the harness recording sidecar deaths as
incidents rather than stopping on them:

| Fleet | Outcome on 3 x 64 GiB control plane, 4 x 32 GiB workers |
|---|---|
| 1,000 clusters, 10,000 Machines | converged in 10m45s; API servers 17 to 24 GiB each, 36% of allocatable |
| 2,000 clusters, 20,000 Machines | converged in 10m48s; API servers 28 to 41 GiB each, 59% |
| **3,000 clusters, 30,000 Machines** | **converged in 10m51s; API servers 37 to 50 GiB each, 76%; the busiest node at 54 GiB of 62** |
| 3,500 clusters, 35,000 Machines | converged in 11m7s, clean; API servers 40 to 49 GiB each, 78%; etcd 2.5 GiB of its 8 GiB quota, no failed proposals, no leader change, no incident on any rung |
| 4,000 clusters, 40,000 Machines | converged in 10m57s, clean (second run, managers at 16, 12, 8 and 24 GiB); busiest node at 51 GiB |
| **4,500 clusters, 45,000 Machines** | **converged in 11m45s, clean; API servers 36 to 52 GiB each, 71%; the busiest node at 55.4 GiB of 62, 89%** |
| 5,000 clusters, 50,000 Machines | converged in 11m39s, reached and not clean: the cloud controller manager lost its lease during the twelve-second defragmentation of the etcd leader between the rungs, the harness's own doing; busiest node at 55 GiB; held for a 33-minute soak with the fleet unchanged |

Every rung converged and no rung failed on either run, so 5,000 is a floor
under the answer, not a ceiling, and 4,500 is the largest clean rung. The pace held at 1.28 to 1.34 s per added cluster from
500 to 3,500, so reconciliation was not slowing as the fleet grew. What the
run shows about where the ceiling is:

- **The busiest control plane node is the limit, not the total.** The node
  holding the VIP and both leases did two to three times the API server CPU
  of the others at every rung and carried the largest API server, 49 to 50 GiB
  from 3,000 on. That node was at 54 GiB of 62 allocatable at 3,000. The
  other two had 12 to 24 GiB to spare. Sizing by the sum would say the control
  plane was a quarter empty; the node that decides is at 87%.
- **The managers' own limits are the next ceiling, and they are ours.** At
  3,500 the control plane manager was at 96% of its 6 GiB limit and the core
  manager at 93% of its 8 GiB, both governed by the GOMEMLIMIT the prepare
  tool sets below the limit, with live heaps of 2.2 and 3.3 GiB. That is the
  runtime holding to its budget, not an OOM about to happen, but a Go process
  run this close to GOMEMLIMIT spends its CPU collecting. The second run
  raised them by flag to 16, 12 and 8 GiB, none of it a default, and at 5,000
  the core manager held 5.1 GiB of live heap in 15 GiB resident against 16,
  the control plane manager 3.3 in 11.4 against 12, the DevCluster provider
  4.9 in 13.6 against its unchanged 24. The live heaps grew at the rates the
  model carries, to the cluster.
- **The API server stops growing.** The busiest API server climbed 8 GiB per
  500 clusters to 3,000 and then sat between 50 and 52 GiB from 3,000 to
  5,000 on both runs, its live heap flat at about 35 GiB while the objects it
  stored went from 175,000 to 300,000. Its cost is not the object count's;
  it is dominated by things that saturate, and the fit below carries the
  measured curve rather than the line that put 50,000 Machines past the node.
- **The defragmentation between rungs is a perturbation of its own.** The
  one incident on the second run was the cloud controller manager losing its
  lease, at 10:45:36, inside the twelve seconds the etcd leader spent
  rewriting a file that had nothing free in it, between the 4,500 and 5,000
  rungs. A member being defragmented answers nothing, the manager renews
  through its local API server and so through that member, and its renew
  deadline is ten seconds. The harness now leaves a member alone when less
  than a fifth of its file is free, rewrites the leader last, and flags a
  rewrite longer than the shortest renew deadline on the cluster.
- **The managers were not restarted before this run**, so their resident
  figures carry the previous day's high-water mark; the live heaps are the
  numbers to read, and the report now says when a manager predates the run.

So **3 x 64 GiB is a suitable control plane for 4,000 clusters of ten nodes
with margin, held 4,500 cleanly at 89% of the busiest node, and reached
5,000**, with the stacked topology's coupling unchanged: one node carries the
VIP, the leases and the largest API server, and it is that node's 64 GiB that
the number is measured against. The managers need their limits raised past
the defaults from about 3,500 clusters, which is a flag on the prepare tool.

The workers at 32 GiB were not the limit at any rung. At 1,500 clusters the
four managers held 1.2 to 8.8 GiB resident each with live heaps of 0.4 to
3.0 GiB, all within their limits.

### How many clusters a management cluster is expected to hold

The two node sizes climbed give a fit, and the fit is written into the
harness as `upstreamscale.Capacity` so that a run is told before it creates
anything whether its ladder is past what its cluster is expected to give. The
model is of the busiest control plane node, since that node and not the sum
was the limit every time, and of each manager against its own limit:

| Component | Fit, from the runs of 8 and 9 September 2026 |
|---|---|
| hottest API server, resident | the measured curve: 17 GiB at 5,000 Machines, 24 at 10,000, 31 at 15,000, 39 at 20,000, 45 at 25,000, 50 at 30,000, 51.5 at 35,000, 52 at 50,000; beyond that half a gigabyte per 1,000, a guess |
| etcd member beside it, heap and file | 0.5 GiB + 20 MiB per 1,000 Machines, between defragmentations |
| kube-controller-manager | 35 MiB per 1,000 Machines |
| everything else on the node | 0.9 GiB |
| a node holds a fleet when the sum is | under 90% of its allocatable memory |
| core manager, live heap | 0.1 GiB + 1.05 GiB per 1,000 clusters |
| kubeadm control plane manager, live heap | 0.1 GiB + 0.63 GiB per 1,000 clusters |
| kubeadm bootstrap manager, live heap | 0.05 GiB + 0.27 GiB per 1,000 clusters |
| DevCluster provider, live heap | 0.4 GiB + 1.0 GiB per 1,000 clusters |
| a manager holds a fleet when its limit is | at least twice its live heap |

Read against the measured points: a 32 GiB node (30.9 GiB allocatable) is
expected to hold about 11,000 Machines, and it held 10,000 and failed 20,000;
a 64 GiB node (62.1 GiB) about 47,000, and it held 45,000 cleanly at 89% and
reached 50,000 at the same node figure, since the API server had stopped
growing. An 8 GiB core manager is expected to hold about 3,700 clusters and
was at 93% of its limit at 3,500; a 6 GiB control plane manager about 4,600
and was at 96%, which is where the twice-live-heap line was drawn; at 16 and
12 GiB they are expected to hold about 7,500 and 9,400 and were at 5,000 with
live heaps the model predicted within a tenth. The DevCluster provider's
slope is an in-memory backend holding every fake node and stands in for no
real provider; CPU, disk and network are not modelled because none was the
ceiling in any run.

So, for clusters of ten nodes on this topology: 3 x 32 GiB is a 1,000-cluster
control plane and 3 x 64 GiB a 4,500-cluster one. What 3 x 128 GiB would hold
is the tail of the curve, which nobody has climbed: the API server's growth
had flattened by 35,000 Machines and the model continues it at half a
gigabyte per thousand, which reads as about 15,000 clusters and should be
read as "past 5,000, measure". The managers, at the prepare tool's default
limits, run out at about 3,700 clusters for core; their limits are flags and
the model reads the deployed limit, so raising them moves the expectation
without touching the code.

The run reports the expectation as its `capacity` fact and logs a warning
when the ladder is past it. A warning, never a refusal: a run that reaches its
top rung cleanly regardless is exactly the evidence that moves the fit, and
the fit should then be moved.

### Why there is no dedicated node any more

There was one, and Guaranteed resources took the reason away. Guaranteed means
requests equal to limits, so a component's memory is reserved and cannot be
taken by a neighbour, and an OOM kill is a cgroup kill against its own limit
whatever else is on the node — which is the signal the ladder classifies, so the
classification does not need isolation to be trustworthy.

What is left is smaller than it looks:

- **The provider gets a node to itself anyway.** It asks for 24 GiB of a 32 GiB
  node. After kubelet and system reservations nothing else of consequence fits
  beside it, so the scheduler gives it one without being told to. With one
  correction the first fresh-cluster run paid for: a control plane node counts
  as empty to the scheduler, since kubeadm's API server has no memory request,
  and clusterctl's provider tolerates the control plane taint. The prepare
  tool now keeps every manager off those nodes with a required node affinity;
  see `hack/upstream-capi-scale/README.md`, "The managers are kept off the
  control plane nodes".
- **The fourth node is the headroom.** The loop this run is built around is
  raise-the-limit-and-retry, and on a full node there is nowhere to raise it to
  without evicting a neighbour — which resets that neighbour's process metrics
  and makes the next rung incomparable with the last. A spare node in the pool
  solves that without a taint.

What Guaranteed does **not** give, stated so it is a decision rather than an
assumption: it is not exclusive cores. A Guaranteed pod gets a CFS quota and
shares, not pinned CPUs, unless the kubelet runs `cpuManagerPolicy: static` —
so the provider still contends for L3 cache and memory bandwidth with whatever
shares its socket. For a process whose hot path is JSON decoding into maps that
is a real effect and a second-order one. The CPU limits here are whole numbers,
so static policy would work if it is wanted; it is node configuration, it is not
free to get wrong, and the time to reach for it is when a rung fails as "did not
converge" with throttling or contention to show for it — not before.

Three control plane nodes, so the run measures Cluster API against an API server
behind a real etcd quorum rather than a single member. It is one apiserver's
cost that gets fitted either way; what HA changes is that write latency includes
raft, which is the thing a scale test on a single member quietly leaves out.

### Why the control plane is the big one

The kcp shard cost **1.41 MiB of retained heap per Machine and 7.99 MiB per
cluster**, measured across three node counts. Carried to 200 clusters of fifty
nodes that is about 16 GB of retained heap, and retained heap is not resident:
allow for the collector's headroom on top. 32 GiB was the smallest number that
left room to find the ceiling somewhere other than at the box, and the
measured section above says where that ceiling turned out to be.

Whether an ordinary kube-apiserver costs the same as the kcp shard is
**precisely what this run is for**. If it costs much less, the earlier finding
was about kcp; if it costs the same, it is about serving Cluster API's CRDs as
unstructured objects, and it applies to every management cluster anyone runs.

### etcd

At fifty nodes per cluster a cluster is roughly 370 stored objects, so 200 of
them is around 74,000 objects. Cluster API objects serialize to a few kilobytes,
so the database is not large — but the **default 2 GiB backend quota** is a
cliff, and revisions between compactions are what fill it during a climb.

- `--quota-backend-bytes=8589934592` (8 GiB)
- Compaction is the API server's job (`--etcd-compaction-interval`), and it
  stays at the 5-minute default. Shortening it was tried after a compaction
  stalled the store for a minute on a cold page cache, and reverted: it shrinks
  the history etcd keeps to one or two minutes, which at this scale turns any
  interruption into a full re-list by every API server. Do not turn it off
  either, for a soak or otherwise; the revisions between compactions are what
  fill the quota.
- Fast local SSD, **on a disk of its own**. etcd's fsync latency is the quietest
  way for a scale test to turn into a latency test, and on CAREN's template
  `/var/lib/etcd` shares the root disk with the API server's audit log and the
  container logs. The provisioning script attaches a 32 GiB data disk, mounts it
  on `/var/lib/etcddisk` and points kubeadm's `etcd.local.dataDir` at a
  subdirectory of it — twice the quota for a defragmentation's second copy,
  plus the WAL; see `hack/upstream-capi-scale/README.md`, "etcd on a disk of its
  own".
- **Its page cache is shared with the API server**, and on 32 GiB nodes that
  is the ceiling: the API server's heap evicts the backend file, and a
  compaction then reads it cold for a minute. The remedies in order are etcd on
  its own machines, node memory, and cgroup v2 `memory.low` through the
  kubelet's `MemoryQoS` gate and `TieredReservation` policy with a memory
  request on etcd (the gate alone sets nothing on 1.36), which the
  provisioning script offers as `MEMORY_QOS` and `ETCD_MEMORY_REQUEST`; see the
  README, "Fencing etcd's page cache from the API server".

### Where each component lands, and why the report says so

Only the DevCluster provider is placed. The other three take the generic pool
wherever the scheduler puts them, which is the honest arrangement: an
installation does not pin its managers either. What matters is that the report
names the node each component ran on and flags the case where they shared one,
because a figure measured on a node with three managers on it is not the same
figure as one measured alone — that caveat is already in the report and is not
new here.

The corollary is that a restart matters twice over. Process metrics reset when
the process does, and a rescheduled pod may also land somewhere else, so a rung
containing a restart is not comparable with the rungs below it on either count.
The ladder treats a restart as a failed rung for exactly this reason.

### The provider is still the component to watch

Every in-memory workload cluster in the fleet is served from **one process**:
each gets a listener on a port from 20000-30000, so 10,000 clusters is a hard
ceiling for one pod, and every fake Node, Pod and lease in the fleet lives in
that process's heap. It is the component most likely to be the ceiling. It is
given the most memory and the most room to grow for that reason, rather than a
node of its own.

Pinning it is still supported (`Dedicate`), and the run will use a node selector
and toleration if given them. Nothing needs them by default.

### Node labels have to be in a domain Cluster API will propagate

Whatever labels the pools carry, they cannot be arbitrary. Cluster API copies a
Machine label to its Node **only** if it has `node-role.kubernetes.io` as a
prefix, or belongs to the `node-restriction.kubernetes.io` or
`node.cluster.x-k8s.io` domains — or matches a regex passed to the core
manager's `--additional-sync-machine-labels`.

So a label like `scale-role=devcluster` set through a `MachineDeployment`
template reaches the Machine and stops there: the Node never gets it, a node
selector against it never matches, and the pod stays Pending with everything
looking correctly configured. Use `scale-role.node.cluster.x-k8s.io/...` or
`node-role.kubernetes.io/...` instead.

## Guaranteed resources on everything

Requests equal limits on every container, so every component is in the
**Guaranteed** QoS class and none of them can borrow from a neighbour. A
Burstable component that finished its work measured a node that happened to have
room; worse, its numbers move between rungs of the same climb as the fleet grows
around it, so a cost model fitted across those rungs is fitted partly to how
contended each rung was.

Starting values. Each is a **prediction** from the kcp runs' per-cluster figures
carried to 200 clusters of fifty nodes, and the loop is meant to be run: if
something is OOM killed, raise it and say so in the report.

| Component | CPU | Memory | Where the memory comes from |
|---|--:|--:|---|
| DevCluster provider | 6 | **24 Gi** | Holds every fake cluster in the fleet in one process. The least predictable of the four and the one given the most room. |
| core manager | 4 | 8 Gi | 3.94 MiB per cluster measured at ten nodes; ~4x that at fifty, times 200 clusters, doubled for headroom. |
| kubeadm control plane | 4 | 6 Gi | 1.79 MiB per cluster measured, and the highest goroutine count of the four at 47 per cluster. |
| kubeadm bootstrap | 2 | 4 Gi | 1.39 MiB per cluster measured, and the cheapest of the four in every run so far. |

Each container also gets **GOMEMLIMIT** at its limit less 10% (capped at 512
MiB of headroom). This is not optional and it is the lesson the kcp runs paid
for: a Go process cannot see its cgroup limit, and kcp was OOM killed against
4 GiB while holding 1.63 GiB of live heap because the collector had grown the
heap to 3 GiB with nothing telling it a ceiling existed. The identical fleet
reached its target once GOMEMLIMIT was set. Without it an OOM kill means "the
collector was uninformed", and the raise-and-retry loop would be buying headroom
for garbage.

### The cost of CPU limits, stated

Guaranteed QoS means a CPU limit, and a CPU limit means CFS throttling. A
throttled reconciler is slow for a reason that has nothing to do with Cluster
API, and the ladder's most interesting failure — "the fleet did not arrive and
nothing died" — is exactly the one throttling can counterfeit.

So the run records `container_cpu_cfs_throttled_seconds_total` per component at
every rung, and a rung that fails that way is reported with its throttling
figure beside it. If a ceiling turns out to be a throttling ceiling, that is a
finding about the limits chosen and the fix is to raise them and re-run, not to
argue about it.

## Cluster prerequisites

- **cert-manager**, which `clusterctl init` requires and which stock Cluster API
  webhooks depend on. The webhooks are left switched on: they are part of what
  is being measured.
- **metrics-server**, or resident memory cannot be read. pprof gives heap and
  goroutines; nothing in a Go process reports its own RSS to a remote scraper.
  It runs on the generic pool with everything else, which is worth one thought:
  it is part of the instrument, so a starved metrics-server degrades the
  measurement rather than the fleet. It is small, the pool is not, and the run
  records the resident figures it got — but a rung whose resident numbers are
  missing is a metrics-server problem and will say so rather than being read as
  a component that shrank.
- Permission to patch the provider Deployment (the run removes the Docker socket
  mount) and to read `/debug/pprof` on the managers.
- A kubeconfig context named to the run. It creates namespaces of its own and
  the `clusterctl` provider namespaces, and nothing else.

## Knobs that change the answer, and are recorded

The provider's defaults are part of what a ceiling means:
`--devmachine-concurrency=50`, `--devcluster-concurrency=50`,
`--clustercache-concurrency=100`, `--kube-api-qps/--kube-api-burst`. A ceiling
found at the defaults is a ceiling for the defaults. If the run stops because
reconciliation did not keep up rather than because something died, these are the
first things to raise — and then it is a different measurement, recorded as one.

`--kube-api-qps/--kube-api-burst` are left at Cluster API's own 100/200 rather
than raised, and that is the lesson of raising them. At 500/1000 the managers
put five times the write rate onto the store, which then could not commit a
leader lease: managers exited with `leader election lost` and no rung finished.
A throttled manager is a measurement with a caveat. A run that cannot complete
a rung is not a measurement.
