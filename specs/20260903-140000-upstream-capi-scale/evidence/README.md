# Runs of the stock Cluster API climb

Every run with an evidence file, and what it was for:

| File | Rungs | Nodes per cluster | What it is |
|---|---|--:|---|
| `stock-2-to-4x3.json` | 2, 4 | 3 | **A smoke run of the machinery, not a measurement.** The first contact with a real cluster: preflight, two rungs, a defrag between them, no soak. It found the teardown ordering bug described in the spec. |

## How to reproduce it

```sh
task test:capi:scale START_CLUSTERS=2 MAX_CLUSTERS=4 NODES_PER_CLUSTER=3 SOAK=0
```

Stock Cluster API v1.14.1, installed by clusterctl, on the CAPX cluster
`hack/upstream-capi-scale` builds; in-memory DevCluster backend; every
controller Guaranteed with GOMEMLIMIT set, by `task test:capi:cluster`.

## What the smoke run says, and what it does not

Four clusters and twelve Machines converged, and the ladder stopped because
`MAX_CLUSTERS=4` was its last rung — so the report's ceiling line reads as a
floor, correctly. Nothing about Cluster API's limits follows from a run that
stopped where it was told to.

Two things it does establish, which are why it is kept:

- The sampler reads every component it is meant to. The four controllers'
  goroutines and heap come from pprof, the API server's from its own metrics,
  etcd's from the `:2381` port the ClusterClass patch opened. At four clusters
  the API server held 1907 objects for 489 MiB of live heap; the number at a
  real fleet is what the run exists to take.
- The defragmentation between rungs works and is recorded, reclaiming ~0.9 MiB
  per member at this size.

Two things in the file are wrong or absent, and the reader should know which:

- Every controller's `pod` block reads `ready: false`, `memoryLimitBytes: 0`,
  `restartCount: 0`. The pods were ready and Guaranteed; the sampler was
  reading a container that does not exist (it looked for one named after the
  deployment, and clusterctl names it `manager`). Fixed after this run; the
  evidence is kept as it was written.
- Every controller's `residentBytes` and `cpuSeconds` are zero. The
  metrics.k8s.io read returned nothing — no metrics-server the sampler could
  reach — and the pprof scraper does not read CPU time. Not measured, and not
  to be read as small.

And two things it found by breaking: see "First contact" in `../spec.md`.
