# Feature Specification: One scale test, two control planes

**Feature Branch**: `claude/scale-test-200-clusters-vfin19`

**Created**: 2026-09-04

**Status**: Draft — design agreed, nothing built.

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
| Cluster under test | the CAPX cluster's own API server | a single kcp shard, deployed onto it | by design different |
| Control plane budget | 3 × 16 vCPU / 32 GiB | 3 × 16 vCPU / 32 GiB, dedicated pool | **yes** |
| Store | kubeadm etcd, 3 members, node-local disk, 8 GiB quota | etcd StatefulSet, 3 members, node-local PV, 8 GiB quota | **yes** |
| Shards | n/a | **one**, deliberately | — |
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
- **R4 One shard.** The comparison is against one API server, so it is against
  one shard. Sharding is the next question and not this one.
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
- **S4** A statement about the control plane process: the shard against the API
  server, at the same fleet sizes, on the same hardware.
- **S5** Both sides soaked at the largest fleet that converged, with the
  peak-aware drift check.

## Open questions, to be answered with measurements

- **Can the hosting control plane shrink?** In the kcp run the CAPX cluster's
  own API server only hosts pods — it is not the subject and should be nearly
  idle. If its utilisation during a kcp run is low, the 3 × 32 GiB control plane
  it was given for the stock run is oversized for this one, and the nodes could
  go to the pool that is under test. This is a measurement to take during the
  first kcp run, not an assumption to build on: the sampler already reads the
  CAPX API server, so the run will say.
- **Does the shard need the same shape as three API servers?** One shard against
  three API servers is not a like-for-like process count, and R5 gives the
  budget rather than the topology. Whether a single shard can use three nodes'
  worth of anything is itself a finding.

## What this is not

- **Not a sharded kcp measurement.** One shard, by R4.
- **Not a verdict on kcp.** It is one workload, the in-memory DevCluster backend,
  at one shard, on one cluster. A ratio from it is a ratio about this, and the
  report says so.
