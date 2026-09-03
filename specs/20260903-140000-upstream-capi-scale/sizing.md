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

| Role | What runs on it | Ask for |
|---|---|--:|
| Control plane | kube-apiserver, etcd | **32 GiB RAM, 16 vCPU**, fast SSD |
| Dedicated worker | the DevCluster provider, alone | **32 GiB RAM, 8 vCPU** |
| Worker | core, kubeadm bootstrap, kubeadm control plane managers | 16 GiB RAM, 8 vCPU |
| Worker | cert-manager and anything else | 8 GiB RAM, 4 vCPU |

A single-node control plane is fine and makes the API server easier to measure.
Three nodes if you want the etcd quorum behaviour too; the run measures one
apiserver either way.

### Why the control plane is the big one

The kcp shard cost **1.41 MiB of retained heap per Machine and 7.99 MiB per
cluster**, measured across three node counts. Carried to 200 clusters of fifty
nodes that is about 16 GB of retained heap, and retained heap is not resident:
allow for the collector's headroom on top. 32 GiB is the smallest number that
leaves room to find the ceiling somewhere other than at the box.

Whether an ordinary kube-apiserver costs the same as the kcp shard is
**precisely what this run is for**. If it costs much less, the earlier finding
was about kcp; if it costs the same, it is about serving Cluster API's CRDs as
unstructured objects and applies to every management cluster anyone runs.

### etcd

At fifty nodes per cluster a cluster is roughly 370 stored objects, so 200 of
them is around 74,000 objects. Cluster API objects serialize to a few kilobytes,
so the database is not large — but the **default 2 GiB backend quota** is a
cliff, and revisions between compactions are what fill it during a climb.

- `--quota-backend-bytes=8589934592` (8 GiB)
- Keep the default 5-minute auto-compaction; do not turn it off for a soak.
- Fast local SSD. etcd's fsync latency is the quietest way for a scale test to
  turn into a latency test.

### The dedicated node

Take the offer. Every in-memory workload cluster in the fleet is served from
**one process**: each gets its own listener on a port from 20000-30000, so
10,000 clusters is a hard ceiling for one pod, and every fake Node, Pod and
lease in the fleet lives in that process's heap. It is the component most likely
to be the ceiling, and the one whose measurement is most easily spoiled by a
noisy neighbour.

Label it and the run will pin the provider to it.

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
