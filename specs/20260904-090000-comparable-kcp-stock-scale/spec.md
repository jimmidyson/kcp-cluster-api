# Feature Specification: One scale test, two control planes

**Feature Branch**: `claude/scale-test-200-clusters-vfin19`

**Created**: 2026-09-04

**Status**: Draft — built and runnable from one command a side; not yet run
against the cluster, so nothing here is measured except where it says so.

## Purpose

`specs/20260903-140000-upstream-capi-scale` measured stock Cluster API to 1600
clusters and 16,000 Machines on one management cluster, with a cost model that
held to 2% four times beyond the range it was fitted on. The kcp figures it sits
beside come from a different harness, a different cluster and a different
Cluster API, and its own "Against the kcp runs" section spends most of its words
on why the two cannot quite be subtracted.

This makes them subtractable. **One driver, one instrument, one cluster, two
control planes**, so that a difference in the numbers is a difference in the
system rather than in how it was measured.

The question it answers is the one underneath the whole project:

> **What does workspace-awareness cost, per cluster, measured the same way on
> the same hardware as the stock alternative?**

## The equivalence being claimed

Comparability is a claim, and this is the one being made. Everything either side
holds constant, or is recorded as a difference:

| | stock | kcp | same? |
|---|---|---|:-:|
| Cluster under test | the CAPX cluster's own API server | one kcp shard, deployed onto it | by design different |
| Control plane budget | 3 × 16 vCPU / 32 GiB | 3 × 16 vCPU / 32 GiB, dedicated pool | **yes** |
| Serving processes | **3** kube-apiservers, active/active behind the VIP | **3** shard replicas, active/active | **yes** |
| Embedded controllers | none — kube-controller-manager is a separate process | in the shard, leader-elected across the replicas | by design different |
| Store | kubeadm etcd, 3 members, node-local disk, 8 GiB quota | etcd StatefulSet, 3 members, node-local PV, 8 GiB quota | **yes** |
| Shards | n/a | **one**, at three replicas | — |
| Controllers | clusterctl's four, on the worker pool | this project's four, on the worker pool | **yes** |
| Infrastructure | DevCluster, in-memory backend | DevCluster, in-memory backend | **yes** |
| Tenancy unit | Namespace | Workspace | by design different |
| Clusters per tenant | 10 | 10 | **yes** |
| Nodes per cluster | 10 | 10 | **yes** |
| Rungs | 25→400→1600 | the same | **yes** |
| End state | every control plane ready, every Machine Ready | the same | **yes** |
| Cluster API | v1.14.1 stock | v1.15.0-kcp.N fork | **no — recorded** |

The last row is the one that cannot be fixed and must be stated wherever a
figure appears. Everything else is either held or is the subject.

## Requirements

- **R1 One driver.** A single ladder, settle, sampler, defragmenter, soak,
  teardown and report, with the tenancy behind an interface. Two
  implementations: Namespace and Workspace. A fix to the instrument reaches both
  or it reaches neither.
- **R2 The instrument is the one the stock runs ended with.** Settle before the
  baseline, cAdvisor for resident and CPU, the API server heap floor, the
  stored-object split, defragmentation before the baseline and between rungs,
  peak-aware drift, ordered teardown, and a client that is not rate limited to
  5 QPS. The kcp side inherits all of it — `internal/deployedscale` has none of
  it today.
- **R3 kcp runs on external HA etcd.** Three members, one per node, 8 GiB
  quota, `:2381` metrics reachable, on persistent volumes. Not the embedded
  single-member etcd inside the shard's own pod, which shares the shard's
  memory limit and cannot be sampled or defragmented the way the stock store is.
- **R4 One shard, three replicas.** One *shard* — sharding is the next question
  and not this one. Three *replicas* of it, because the stock side is not one
  API server either: the CAPX control plane runs three, active/active behind
  the VIP, each holding its own full watch cache. A single-replica shard against
  three API servers compares one process with three, and gets the total cost of
  a control plane wrong by about that factor in whichever direction the reader
  is not expecting.

  kcp supports this directly — `--enable-leader-election`, with
  `--leader-election-name` and `--leader-election-namespace` — which is the
  same split Kubernetes makes between the API server and the controller
  manager, except that kcp puts both in one process. So three replicas serve
  the API and exactly one of them runs the shard's controllers.

  **Measured, not read off the flags.** `TestAThreeReplicaShardServesOneStore`
  in `test/integration/deployed` starts three kcp processes over one external
  etcd with the flags the Deployment gives them, and checks the three things
  that could each fail quietly: a workspace created through one replica leaves
  Initializing (so leader election works rather than three copies of kcp's
  controllers racing), the other two serve that workspace as the same logical
  cluster (so the mounted credentials make them one shard rather than three
  servers, despite each generating its own PKI into its own root directory),
  and a write through the third is read back through the first. It needs the
  kcp and etcd binaries and takes half a minute.
- **R4a Every replica is sampled, on both sides.** The consequence of R4, and
  it is a change to the instrument rather than a note about it. What it
  replaced:
  - The API server sample was taken through the cluster's own kubeconfig, which
    addresses the VIP, so **consecutive scrapes could land on different API
    servers**. Every stock figure recorded before this is therefore one
    arbitrary instance per sample, and its five-read heap floor is a floor
    across up to three processes rather than across one process's sawtooth.
  - `runningPodOf` returned the first running pod whose name began with a
    deployment's, its comment reasoning that a second would mean a rollout.
    With three shard replicas that is no longer true, and it would silently
    report one replica as "the shard".
  Both sides report **per process and summed**, because the two answer
  different questions: per process is what a single instance costs and bounds
  the node it sits on, and the sum is what the control plane costs.

  **Built** (`internal/upstreamscale/replicas.go`, `sample.go`): replicas are
  found through a Deployment's own selector rather than by name — four managers
  built from one stem is a prefix test away from summing two under one name —
  and each is sampled and named apart, one keeping the bare component name so
  the figures already recorded still line up. A control plane is read instance
  by instance through the pod proxy, with the heap floor applied per process,
  since the lowest of five reads spread over three processes is the smallest of
  three unrelated sawtooths rather than a floor of anything.

  **One caveat travels with the numbers.** The pod proxy forwards a request
  without the caller's credentials, so an API server that refuses anonymous
  requests to `/metrics` cannot be read instance by instance. That case falls
  back to the endpoint — one arbitrary instance, exactly as before — and says so
  in the line the report carries, rather than in a log line nobody reads beside
  the figure. Which of the two a given cluster does is not yet known: it has not
  been run against the real one.
- **R5 The control plane under test gets the same budget either way**: three
  nodes of the same size, with the shard and the etcd members pinned to them and
  spread one per node. A control plane that fits on fewer resources than the
  alternative is a finding, not a fairness problem — but it has to be *given*
  the same and observed to use less, rather than given less and observed to
  cope.
- **R6 Storage is Nutanix CSI.** The cluster template's trimmer removes the CSI
  addon today, on the reasoning that nothing asks for a PersistentVolume. The
  kcp side does, so CSI becomes a knob rather than a deletion, and the stock run
  keeps it off so its own measurement is unchanged.
- **R7 Both reports carry the same facts**, so that a diff of two report files
  is a readable answer rather than an exercise in matching field names.
- **R8 Neither run's teardown may leave the other's baseline dirty.** The stock
  runs already showed how far a contaminated baseline travels: an API server's
  allocator high-water mark survived three runs and made a 250-Machine fleet
  look like it cost 31 MiB.

## Success Criteria

- **S1** The same shape — 10 clusters per tenant, 10 nodes each, the same rungs
  — run against both, from one command each, on the one cluster.
- **S2** A per-cluster cost model for each side, fitted the same way, with the
  ratio between them stated per component and for the total.
- **S3** A statement about the store: what a cluster costs in etcd keys and
  bytes on each side, with both stores defragmented and compacted the same way.
- **S4** A statement about the control plane: the shard against the API server
  at the same fleet sizes on the same hardware, given **both** per process and
  summed across the three of each, and naming which replica held the shard's
  controller leadership.
- **S5** Both sides soaked at the largest fleet that converged, with the
  peak-aware drift check.

## The one figure that is not symmetric, and which way it leans

The stock cluster's API servers are started with `--profiling=false` — CIS
benchmark 1.2.18, from CAREN's ClusterClass — so `/debug/pprof` is not served at
all, whoever asks. Their heap can only be read as the lowest of several
scrapes: the sawtooth's floor, which is an **upper bound** on the retained set.
The kcp shard serves pprof to the run's privileged identity, so its heap is read
after a forced collection and **is** the retained set.

So a heap-for-heap ratio between the two sides is not like for like, and it
leans one way: the stock figure can only be too high, never too low, which
flatters kcp. Three things follow, and the reports carry all three rather than
leaving them to a reader:

- Every sample says which quantity it is — `heap is the lowest of N reads` or
  post-collection — because they are different quantities.
- **Resident memory is the figure to compare across sides.** Both are read the
  same way, from `process_resident_memory_bytes`, on both control planes and
  every manager. It is also what a container limit is set against, which is what
  a capacity finding is about.
- Heap is the figure to compare **within** a side, between rungs, where it is
  the same quantity throughout and is the one that reproduces.

This cannot be fixed by the harness: it is a property of the cluster under test,
not of how it is read. It could be fixed by a control plane started with
profiling on, and that is a decision about a real cluster rather than a change
to a measurement.

## Running it

Two commands, one per side, against the same cluster:

```sh
task test:capi:scale KUBECONFIG_PATH=bin/capi-scale.kubeconfig
task test:kcp:scale  MANAGER_IMAGE=<registry/prefix> KUBECONTEXT=<the same cluster> \
  ETCD_STORAGE_CLASS=<a Nutanix CSI class> CONTROL_PLANE_NODE_SELECTOR=<key=value>
```

The knobs are deliberately the same knobs with the same defaults —
`START_CLUSTERS`, `MAX_CLUSTERS`, `NODES_PER_CLUSTER`, `CONTROL_PLANE_NODES`,
`SOAK`, `CREATE_CONCURRENCY`, `CLIENT_QPS` — and the one that differs in name
means the same thing: `CLUSTERS_PER_NAMESPACE` on the stock side and
`CLUSTERS_PER_WORKSPACE` on the kcp side. Two runs whose knobs disagree are two
fleets, and the reports would be diffable without being comparable.

`ETCD_STORAGE_CLASS` empty takes the cluster's default class; the run refuses
before it applies anything when there is neither. `CONTROL_PLANE_NODE_SELECTOR`
empty leaves the shard and its store to the scheduler, which is not the budget
R5 asks for.

### Both sides on the one cluster

They share it, one at a time. The cluster `hack/upstream-capi-scale` builds is
three control plane nodes and four workers, all 16 vCPU and 32 GiB, and it holds
both runs — but only in sequence and only with three things arranged first.

**One: the store needs a provisioner, and the cluster is generated without one.**
`KEEP_CSI` defaults to false because nothing in the stock measurement asks for a
volume, and every addon left on is another controller reconciling against the
API server whose cost is the subject. The kcp side asks for three. Either
regenerate the cluster with `KEEP_CSI=true` or install a provisioner into the
one that exists; `kubectl get storageclass` is the check, and the run makes it
for you before it applies anything.

**Two: the stock providers have to be out of the way, and scaling them to zero
is not tidiness.** clusterctl's four managers *request* 16 CPU and 42 GiB
between them — the sizes `internal/upstreamscale.Controllers` gives them — which
is most of what four 32 GiB workers have to offer. Left running they do not
compete for work, since the kcp fleet is not in their API server, but they hold
that capacity against the scheduler and the shard's pods stay Pending:

```sh
for ns in capi-system capi-kubeadm-bootstrap-system           capi-kubeadm-control-plane-system capd-system; do
  kubectl scale deployment --all --replicas=0 -n "$ns"
done
```

Scale them back before the next stock run. Their CRDs and webhooks can stay:
the kcp side creates no Cluster API object in the hosting cluster.

**Three: the two pools have to be named, and the cluster names them.** R5 wants
the control plane under test on its own nodes, and on this cluster that is three
of the four workers holding one shard replica and one etcd member each — which
is exactly the shape kubeadm gives the stock side, an API server and an etcd
member per control plane node. The fourth worker takes the managers, and it
takes them because it is asked to.

The labels come from the topology rather than from `kubectl label node`:

```sh
CONTROL_PLANE_POOL_WORKERS=3 WORKER_COUNT=4 ./scale-cluster.sh cluster
```

which splits the generated worker pool into two labelled MachineDeployments,
and the label travels MachineDeployment → MachineSet → Machine → Node. A node
that is replaced comes back labelled, which a hand-run `kubectl label` does not
survive — and this is a cluster whose whole purpose is to be pushed until
something breaks.

**The label's domain is the mechanism, not a naming choice.** Cluster API does
not copy arbitrary Machine labels to a Node: the kubelet cannot self-assign
labels outside the NodeRestriction domains, so the Machine controller syncs only
what `util/labels.GetManagedLabels` admits — `node-role.kubernetes.io`,
`node-restriction.kubernetes.io` and `node.cluster.x-k8s.io`, each with their
subdomains, plus anything matching the core manager's
`--additional-sync-machine-labels`. A pool labelled `scale-role=control-plane`
would reach the MachineDeployment, the MachineSet and the Machine and stop
there, silently: the nodes come up unlabelled, the shard's pods are
unschedulable, and nothing says why. So the label is
`node.cluster.x-k8s.io/scale-role`, which propagates without asking anything of
the management cluster's own flags, and a unit test ties that constant to
upstream's so a rename there fails here rather than in a run.

The run then selects on it:

```sh
task test:kcp:scale MANAGER_IMAGE=... \
  CONTROL_PLANE_NODE_SELECTOR=node.cluster.x-k8s.io/scale-role=control-plane \
  MANAGER_NODE_SELECTOR=node.cluster.x-k8s.io/scale-role=managers
```

Without the second selector the managers are scheduled wherever there is room,
which is the three nodes the shard is pinned to — and a manager sharing a node
with the shard it is driving makes the shard's figures a measurement of both.

On a cluster that already exists, the same labels can be added to its topology
in place: Cluster API propagates a MachineDeployment's template metadata down to
existing Machines without a rollout, so relabelling costs nothing but a
reconcile.

**What each run leaves for the other.** Both tear down what they created and
wait for it to go, which is R8 and is where the stock side's own numbers were
once wrong. Two residues survive teardown either way and neither is repaired by
waiting: the hosting API server's allocator high-water mark, and its etcd's
uncompacted revisions. Neither is the subject of a kcp run — that run's control
plane is the shard and its store is its own — so what they cost is a slower
hosting cluster rather than a wrong figure. For a **stock** run whose absolutes
are to be quoted, roll the control plane first; the evidence README says why.

## Open questions, to be answered with measurements

- **Can the hosting control plane shrink?** In the kcp run the CAPX cluster's
  own API server only hosts pods — it is not the subject and should be nearly
  idle. If its utilisation during a kcp run is low, the 3 × 32 GiB control plane
  it was given for the stock run is oversized for this one, and the nodes could
  go to the pool that is under test. This is a measurement to take during the
  first kcp run, not an assumption to build on: the sampler already reads the
  CAPX API server, so the run will say.
- **Does the shard scale horizontally at all?** Three replicas is the shape R4
  requires for comparability, and whether the second and third earn their keep
  is a separate finding: kube-apiservers are stateless and share read load, and
  whether a kcp shard does the same under this workload is not something the
  existing runs can say. If a fleet size exists where one shard saturates and
  three do not, that is worth more than the cost ratio.
- **What does leadership cost?** With leader election exactly one replica runs
  the shard's controllers. The difference between that replica and the other two
  is the controller half of a shard, measured directly — something the stock
  side cannot show, because its controllers are in another process on another
  schedule.

## What this is not

- **Not a sharded kcp measurement.** One shard, by R4.
- **Not a verdict on kcp.** It is one workload, the in-memory DevCluster backend,
  at one shard, on one cluster. A ratio from it is a ratio about this, and the
  report says so.
