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
| Control plane | **3** | kube-apiserver, etcd | **32 GiB, 16 vCPU** each, fast SSD |
| Dedicated, **tainted** | 1 | the DevCluster provider, alone | **32 GiB, 8 vCPU** |
| Generic worker | 1-2 | core, kubeadm bootstrap, kubeadm control plane | **32 GiB, 16 vCPU** |
| Generic worker | 1 | cert-manager, metrics-server | 8 GiB, 4 vCPU |

Three control plane nodes, so the run measures Cluster API against an API server
behind a real etcd quorum rather than a single member. It is one apiserver's
cost that gets fitted either way; what HA changes is that write latency includes
raft, which is the thing a scale test on a single member quietly leaves out.

### Why the control plane is the big one

The kcp shard cost **1.41 MiB of retained heap per Machine and 7.99 MiB per
cluster**, measured across three node counts. Carried to 200 clusters of fifty
nodes that is about 16 GB of retained heap, and retained heap is not resident:
allow for the collector's headroom on top. 32 GiB is the smallest number that
leaves room to find the ceiling somewhere other than at the box.

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
- Keep the default 5-minute auto-compaction; do not turn it off for a soak.
- Fast local SSD. etcd's fsync latency is the quietest way for a scale test to
  turn into a latency test.

### The dedicated node, and its taint

Every in-memory workload cluster in the fleet is served from **one process**:
each gets a listener on a port from 20000-30000, so 10,000 clusters is a hard
ceiling for one pod, and every fake Node, Pod and lease in the fleet lives in
that process's heap. It is the component most likely to be the ceiling and the
one most easily spoiled by a neighbour.

```sh
kubectl label  node <node> scale-role=devcluster
kubectl taint  node <node> scale-role=devcluster:NoSchedule
```

The run selects that label and tolerates that taint together, because doing one
without the other leaves the provider Pending beside an idle machine.

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
