---
title: One Scale Test, Two Control Planes
description: Stock Cluster API and this project's, measured by one driver on one cluster, so that a difference in the numbers is a difference in the system.
weight: 31
---

Two questions this project has always answered with two instruments:

- What does a cluster cost stock Cluster API on an ordinary Kubernetes API
  server?
- What does it cost this project's Cluster API on a kcp shard?

Both have been measured. They were measured by different harnesses, against
different clusters, with different definitions of a heap figure — and most of
what could honestly be said about the two sets of numbers was why they could not
be subtracted.

This is the arrangement that makes them subtractable: **one driver, one
instrument, one cluster, two control planes.**

## What is shared, and what each side is allowed to differ in

The ladder, the settle before the baseline, the sampling, the defragmentation
between rungs, the soak, the drift check, the teardown and the report are one
piece of code — `upstreamscale.Runner`. Everything a side is permitted to differ
in is on one interface, `upstreamscale.Target`:

| | Stock | kcp |
|---|---|---|
| A tenant is | a Namespace | a Workspace |
| The fleet lives in | the hosting cluster's own API server | the shard |
| The managers are | four Deployments clusterctl installed, in four namespaces | four Deployments this run made, in one |
| The control plane is | three kube-apiservers behind a VIP | three replicas of one shard |
| The store is | kubeadm's three-member etcd | a three-member etcd this run deployed |

The fleet itself is not on that list. Both sides build their clusters from the
same ClusterClass in `internal/demo`, with the same in-memory DevCluster
backend, and both resolve the same shape into the same tenants holding the same
cluster names — which is checked by a test rather than by inspection, because a
difference there would be a difference in what was created rather than in what
it cost, and every figure downstream would carry it silently.

## Three replicas, not one

The stock side's control plane is three API servers, active/active, each holding
its own full watch cache. A single-replica shard measured against it compares
one process with three and gets the cost of a control plane wrong by about that
factor, in whichever direction the reader is not expecting.

So the kcp side runs three replicas of one shard, with `--enable-leader-election`
so that exactly one of them runs the shard's controllers — the split Kubernetes
makes between the API server and the controller manager, except that kcp puts
both in one process.

That shape is checked rather than assumed. `TestAThreeReplicaShardServesOneStore`
starts three kcp processes over one external etcd with the flags the Deployment
gives them and checks the three things that could each fail quietly: a workspace
created through one replica leaves `Initializing`, the other two serve it as the
same logical cluster, and a write through the third is read back through the
first. It needs the `kcp` and `etcd` binaries, which `task tools` installs, and
takes about half a minute.

## Every replica is sampled, on both sides

A control plane of three processes reported from one of them is a third of an
answer. Both sides therefore read every instance and report **per process and
summed**: per process is what one instance costs and bounds the node it sits on,
and the sum is what the control plane costs. Stored object counts are the only
figures not summed — they are the store's, which every instance reports in full.

How each instance is reached differs, and the report says which was used:

- The **stock** side reads each API server through the API server's own pod
  proxy. That path strips the caller's credentials, so an API server refusing
  anonymous requests to `/metrics` cannot be read this way; the reading then
  falls back to the endpoint — one arbitrary instance — and the line in the
  report says so, because every stock figure recorded before this was taken that
  way and a silent fallback would reproduce them under a heading claiming
  otherwise.
- The **kcp** side reads each replica through a port-forward of its own, with
  the run's privileged profiling identity. The tunnels are the driver's path and
  not the fleet's — the managers reach the shard through its Service — and how
  often one had to be rebuilt is a fact on the report.

## The same heap figure on both sides

A heap read from `/metrics` is whatever the collector had not yet swept at the
instant of the scrape. A heap read from pprof with `gc=1` is the retained set,
because the profile forces a collection first. Three runs of the same fleet once
disagreed by a factor of four for want of that distinction.

The stock side has always sampled its managers through pprof. This project's
managers therefore take a `--profiler-address` flag, off unless a measurement
asks for it, and the deployed manifests can open it on the port the stock side's
managers use. One sampler then reads either side without being told which it is
looking at.

## The one figure that is not symmetric

The stock cluster's API servers are started with `--profiling=false`, so
`/debug/pprof` is not served at all and their heap can only be read as the
lowest of several scrapes — an upper bound on the retained set. The kcp shard
serves pprof to the run's privileged identity, so its heap is the retained set.

A heap-for-heap ratio between the two sides therefore is not like for like, and
it leans one way: the stock figure can only be too high, which flatters kcp.
Every sample says which quantity it is. **Resident memory is the figure to
compare across sides** — both are read the same way, and it is what a container
limit is set against. Heap is the figure to compare within a side, between
rungs, where it is the same quantity throughout.

This is a property of the cluster under test rather than of how it is read.

## Running it

Two commands, one per side, against the same cluster:

```sh
task test:capi:scale KUBECONFIG_PATH=bin/capi-scale.kubeconfig

task test:kcp:scale MANAGER_IMAGE=<registry/prefix> KUBECONTEXT=<the same cluster> \
  ETCD_STORAGE_CLASS=<a CSI storage class> CONTROL_PLANE_NODE_SELECTOR=<key=value>
```

The knobs are deliberately the same knobs with the same defaults —
`START_CLUSTERS`, `MAX_CLUSTERS`, `NODES_PER_CLUSTER`, `CONTROL_PLANE_NODES`,
`SOAK`, `CREATE_CONCURRENCY`, `CLIENT_QPS` — and the one that differs in name
means the same thing: `CLUSTERS_PER_NAMESPACE` on the stock side,
`CLUSTERS_PER_WORKSPACE` on the kcp side. Two runs whose knobs disagree are two
fleets, and their reports would be diffable without being comparable.

Both write a report naming the same facts, so that a diff of the two files is a
readable answer rather than an exercise in matching field names.

### One cluster, two runs, in sequence

They share a cluster — that is the point of the arrangement — but not at the
same time, and three things have to be arranged first.

The store needs a provisioner. The cluster is generated for the stock side with
the CSI addon trimmed, because nothing in that measurement asks for a volume and
every addon left on is another controller reconciling against the API server
whose cost is the subject; the kcp side asks for one volume per etcd member. The
run checks for a usable storage class before it applies anything, rather than
letting the mismatch arrive as Pending pods and a timeout naming the shard.

The stock providers have to be scaled to zero. They do not compete for work —
the kcp fleet is not in their API server — but clusterctl's four managers
*request* 16 CPU and 42 GiB between them, which is most of what the worker pool
has, and they hold it against the scheduler.

Both pools have to be named: `CONTROL_PLANE_NODE_SELECTOR` puts the shard and
its store on the nodes the comparison gives the control plane under test, one
replica and one member each, which is the shape kubeadm gives the stock side;
`MANAGER_NODE_SELECTOR` keeps the managers off them, because a manager sharing a
node with the shard it is driving makes the shard's figures a measurement of
both.

The specification has the commands.

Neither has been run yet at the time of writing: this describes the instrument,
not a result. The specification is
[`specs/20260904-090000-comparable-kcp-stock-scale`](https://github.com/jimmidyson/kcp-cluster-api/tree/main/specs/20260904-090000-comparable-kcp-stock-scale),
and a figure from either side belongs in its `evidence/` directory rather than
in a sentence.
