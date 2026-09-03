# Feature Specification: How far stock Cluster API goes on one management cluster

**Feature Branch**: `claude/scale-test-200-clusters-vfin19`

**Created**: 2026-09-03

**Status**: Specified; harness being built

**Input**: "i would like to create a scale test against a standard kubernetes
cluster. i can provide a CAPX cluster with whatever resources this scale test
would require. i would like to push the CAPI mechanics to the limit and
understand what scale we could reach with CAPI using in-memory DevCluster to
test the CAPI internals, not the infra provisioner itself. i can provide a
dedicated node for the DevCluster controller as necessary."

## Purpose

Every scale figure this repository holds is Cluster API **on kcp**: workspaces,
`APIBinding`s, a shard, and a fork of Cluster API that knows about all three.
The figures are good — a cost model that predicted a 200-cluster fleet to 0.07%
before it was run — and they cannot answer the question underneath them, which
a reader asked in as many words: *is Cluster API scalable with a centralised
management cluster at all?*

This measures that directly. Stock upstream Cluster API, released images,
`clusterctl init`, on an ordinary Kubernetes cluster. No kcp, no fork, no
workspaces. The only unusual thing about it is the infrastructure provider:
`DevCluster` with the **in-memory backend**, so that what is being pushed is
Cluster API's own machinery — reconcilers, caches, watches, `ClusterCache`
connections, the API server behind them — and not a cloud's provisioning
latency.

It answers one question and it answers it by climbing until something breaks:

> **How many clusters, and how many Machines, can one stock Cluster API
> management cluster take to Ready and hold there — and what gives out first?**

## What this is not

- **Not a comparison with the kcp figures.** Stock upstream's latest release is
  v1.14.1; the fork those runs used is v1.15.0-kcp.12, off the v1.15 development
  line. Different code, different instrument, and the number that comes out is
  its own. Where the two are put side by side it will be labelled as an
  indication, not a subtraction.
- **Not a test of an infrastructure provider.** The in-memory backend creates no
  VMs and talks to no cloud. A `DevCluster`'s "API server" is a listener inside
  the provider's own process. What is measured is what Cluster API does around
  that.
- **Not a Kubernetes conformance or workload test.** The workload clusters are
  fake and known to be fake.

## What the shape of the fleet means here

kcp runs are shaped `workspaces x clusters-per-workspace x nodes-per-cluster`.
There are no workspaces on an ordinary cluster, so the tenancy unit is the
**namespace**, and a run is shaped

    namespaces x clusters-per-namespace x nodes-per-cluster

with the same knobs and the same meaning: a cluster is a `Cluster` +
`DevCluster` + `KubeadmControlPlane` + `MachineDeployment` and their
`Machine`/`DevMachine`/`KubeadmConfig`/`Secret` fanout; a node is a Machine.

## Climb, then hold

A fixed target says whether that target is reachable. This is asked to find a
ceiling, so it climbs:

1. **Rungs.** Start small and double the fleet each rung — 25, 50, 100, 200,
   400 clusters, and so on — taking every rung fully to *every control plane
   ready and every Machine Ready* before the next is created.
2. **Sample every rung**, every component, plus the management cluster's own API
   server and etcd. A rung that converges contributes a point to the cost model
   whether or not a later one fails.
3. **Stop at the first rung that does not converge**, and name why: a component
   OOM-killed or restarted, or the fleet not reaching the end state inside the
   step timeout. Report the last rung that did converge as the measured ceiling
   and the failing one as the bound.
4. **Then hold the last good rung** for a soak, sampling throughout, because
   reaching a fleet and holding it are different questions: resync cost, memory
   drift, and whether anything falls out of Ready when nothing is being asked
   of it.

The ladder, the failure classification and the soak all belong to the harness.
A run produces one report describing all four phases.

## What has to be built, and what already exists

Reused unchanged from the kcp harness (`internal/deployedscale`): the report
and its evidence conventions, the least-squares fits with their three-point
rule and residual gate, the idle-baseline discipline, the co-location and
restart caveats, and the rule that a figure the run cannot support is printed
as "not measured" with the reason.

New, and each of these is a finding in its own right about measuring stock
Cluster API:

- **A pprof-based scraper.** controller-runtime's metrics registry is a bare
  `prometheus.NewRegistry()`, so a stock Cluster API manager serves workqueue
  and reconcile metrics and **no `go_goroutines`, no `go_memstats_*` and no
  `process_resident_memory_bytes` at all**. The kcp managers only have them
  because this repository added them (`internal/managermetrics`). Every upstream
  manager does however take `--profiler-address`, so the sample is taken from
  pprof instead: `/debug/pprof/heap?gc=1&debug=1` forces a collection and
  returns a `runtime.MemStats` dump in the same response, and
  `/debug/pprof/goroutine?debug=1` opens with the goroutine total. That keeps
  the forced-collection discipline the kcp runs ended up needing, for free.
- **Resident memory from the cluster**, since pprof cannot give it:
  `metrics.k8s.io` working-set bytes per pod.
- **Patched controller manifests — all four of them, in two different ways.**
  Every controller gets Guaranteed resources, a GOMEMLIMIT below its limit and a
  pprof endpoint; those three are how the run is measured at all and how its
  rungs stay comparable. One of them additionally loses the Docker socket: the
  released `DevCluster` provider mounts `/var/run/docker.sock` from the host and
  runs `privileged: true`, because it is the Docker provider and the in-memory
  backend is a mode of it, and there is no such socket on a containerd node.
  `cmd/capiscale-prepare` does both, idempotently, from the same functions the
  unit tests cover.
- **The management cluster's API server and etcd as measured components.** The
  kcp runs found the shard, not the controllers, was what ran out, and that its
  memory was the unstructured representation of CRD-backed objects at roughly
  200 KB per stored object. Whether an ordinary kube-apiserver serving the same
  CRDs costs the same is the single most interesting thing this run can say, and
  it is the reason the API server is sampled rather than assumed.

## Requirements

- **R1** The run takes a kubeconfig and a context. It creates nothing outside
  the namespaces it owns and the Cluster API namespaces `clusterctl` creates.
- **R2** Provider versions are pinned and recorded in the report. A figure
  without the version it was measured on is not a figure.
- **R2a** The cluster under test runs the scale test's providers only — core,
  both kubeadm providers, and docker for `DevCluster`. The providers that built
  it (CAPX, CAREN) stay on the kind bootstrap cluster, and nothing is pivoted:
  a self-managed cluster would have them reconciling against the API server the
  measurement is reading. No CSI is installed; nothing here asks for a
  PersistentVolume.
- **R2b** etcd's backend quota is raised through a copy of CAREN's ClusterClass
  rather than an edit of it. CAREN has no variable for the quota, and the
  ClusterClass it supplies is managed by whatever installed it — so an edit is
  liable to be reverted underneath a running experiment, which would present as
  a cluster that got slower halfway up the ladder.
- **R3** The `DevCluster` provider runs with one replica, on a node the run may
  be told to require by label, and its in-memory mux advertises its pod IP. The
  mux allocates one port per workload cluster from 20000-30000, so **10,000
  in-memory clusters is a hard ceiling per provider pod** and is recorded as a
  known bound rather than discovered.
- **R4** Every rung's samples are taken after a forced collection, and the
  report says so, because a run whose heap figures are post-collection and one
  whose are not must not be compared.
- **R5** A rung that fails is classified — OOM, restart, or not converged in
  time — and the classification is in the report. "It broke" is not a result.
- **R6** The soak reports drift, not just an endpoint: first and last sample of
  the held rung, and whether every cluster was still Ready at the end.
- **R7** Teardown removes what the run created, and is safe to run against a
  cluster where a previous run died.
- **R8** Every component runs Guaranteed — requests equal to limits on every
  container — with GOMEMLIMIT set below its memory limit, and the report states
  the QoS class it worked out from the manifests rather than assuming the
  setting took. A component that is not Guaranteed is a component whose numbers
  carry its neighbours.
- **R9** CPU throttling is sampled per component at every rung. A Guaranteed
  component has a CPU limit, a CPU limit means CFS throttling, and throttling
  counterfeits the one failure mode this run most wants to believe: a fleet that
  did not arrive with nothing dead. A rung that fails that way is reported with
  its throttling beside it.
- **R10** Where a component is placed by node selector, the run also tolerates
  the taint that keeps everything else off that node — doing one without the
  other leaves it Pending beside an idle machine — and any node label it selects
  on is in a domain Cluster API propagates to Nodes
  (`node-role.kubernetes.io`, `node-restriction.kubernetes.io`,
  `node.cluster.x-k8s.io`). A label outside those reaches the Machine and stops
  there. No component is pinned by default: see sizing.md.

## Success Criteria

- **S1** A ceiling with a named failure mode, and the fleet size one rung below
  it, measured.
- **S2** A cost model per component fitted across the rungs that converged, held
  to the same three-point and residual rules as every other figure here.
- **S3** A statement about the management cluster's API server: what it costs
  per stored Cluster API object, and whether that is the ~200 KB the kcp shard
  cost.
- **S4** The soak either holds the last good rung with no drift worth reporting,
  or names what drifted.

## Risks and open questions

- **The driver may become the bottleneck.** Creating 400 clusters' worth of
  objects through one client is work; the harness has to be sure it is measuring
  Cluster API rather than its own object creation. Creation concurrency is a
  recorded knob.
- **Webhooks.** Stock Cluster API installs validating and defaulting webhooks
  with cert-manager. That is a real part of the system at scale and is left in
  rather than disabled, but it means cert-manager is a prerequisite.
- **Provider concurrency defaults are a variable, not a constant.** The
  `DevCluster` provider defaults to 50 concurrent DevMachine reconciles and 100
  ClusterCache workers. Those defaults are part of what is being measured and
  are recorded; a ceiling found with them is a ceiling for them.
- **v1.14.1 is not the forked code.** See "What this is not".
