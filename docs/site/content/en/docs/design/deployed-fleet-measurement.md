---
title: Measuring a Deployed Fleet
description: The four managers as four Deployments on a real cluster, measured per deployment — the figures without the co-location caveat.
weight: 29
---

Every resource figure elsewhere in this documentation carries the same line,
written on each report:

> **none — four deployments co-located, so one engagement per workspace rather
> than four**

A workspace in those runs pays **one** engagement where an installation pays
four, and shares **one** `ClusterCache` where an installation has one per
deployment. [`capacity.md`][capacity] says the same thing as its R17: *capacity
is per deployment role, not one number.*

That caveat cannot be refined away by a better in-process measurement, because
it is not a measurement error. It is a property of measuring four deployments
inside one process. This is the measurement that removes it: the four managers
as four `Deployment`s, each in its own container with its own limits.

Specified in
[`specs/20260831-210000-deployed-fleet-scale`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260831-210000-deployed-fleet-scale/spec.md).

## What it measures that nothing else can

| Quantity | In process | Deployed |
|---|---|---|
| Memory per deployment | one heap, split by arithmetic | each container's resident set |
| CPU | **not modelled at all** | CPU seconds per container |
| Engagements per workspace | one | four — the real number |
| `ClusterCache` instances | one, shared | one per deployment |
| Heap → container limit | *"a separate step with its own stated multiplier"*, unmeasured | the multiplier itself |
| Network between components | none — one address space | real hops, real TLS |
| Failure at capacity | a larger heap number | an OOMKill |

The heap-to-limit row is the sleeper. `capacity.md` reports live heap and says
converting it to a resident-size budget is a separate step with a stated
multiplier — and then never states one, because nothing could measure it. A
deployed run measures both quantities on the same process at the same moment,
which turns every existing figure in this repository into a container limit.

## A kubeconfig, not kind

The harness takes a kubeconfig and is indifferent to what produced it. kind is
one way to get a cluster and the only one automated here; a real multi-node
cluster is where the figures are worth quoting from.

That indifference is enforced rather than intended. A test asserts that no
manifest uses host networking, no `hostPath`, no loopback address in any
argument or environment variable, and that kcp is reached through a
`ClusterIP` Service rather than a node address — each of which would work on
one node and fail on a real cluster, which is the failure discovered last.

```sh
# On a laptop: creates a local kind cluster, builds and loads the images, runs.
task test:scale:kind
task test:scale:kind:down          # when you are finished with it

# Components on different machines, which is the reason to care where one runs.
task test:scale:kind WORKERS=3 SPREAD=true

# Tune the fleet. The same knobs on every scale task.
task test:scale:kind CLUSTERS=32 NODES_PER_CLUSTER=5
task test:scale:cluster CLUSTERS=200 NODES_PER_CLUSTER=50 CONTROL_PLANE_NODES=3 \
  MANAGER_IMAGE=registry.example/kcp KUBECONTEXT=my-cluster

# On any cluster. Build the images somewhere it can pull from, then run.
KO_DOCKER_REPO=registry.example/kcp task test:scale:images
task test:scale:cluster MANAGER_IMAGE=registry.example/kcp KUBECONTEXT=my-cluster
```

`test:scale:kind` is a wrapper and nothing more. The measurement itself knows
nothing about kind and must not: a harness that assumed one node could never
measure components that are not on the same one.

The context is named rather than taken from whatever is current. A run creates
workloads, and one meant for a throwaway local cluster — started while the
current context points somewhere else — would create them somewhere else.

### Calibrating against the cheap instrument

`COMPONENTS` narrows the run to some of the managers, which is how a deployed
figure gets checked against an in-process one:

```sh
task test:scale:kind COMPONENTS=core-manager
```

One manager cannot take a cluster to readiness — all four do that together — so
a narrowed run measures the weaker end state of *engaged workspaces holding
their objects*, which is what the in-process deployment sweeps measure too.
That is the comparison, and asking a partial set for readiness is refused up
front rather than discovered by waiting twenty minutes for a machine count that
never moves.

### Credentials are generated, not read back

kcp will sign its own serving certificate — for the address it detected for
itself. What a pod on another node needs is the Service DNS name, which kcp has
no way to know, and getting it wrong is a TLS failure at the first request from
a certificate nobody can inspect without a shell in the pod.

So the harness mints the CA, a serving certificate covering every name kcp is
addressed by, and the bearer token, before anything is deployed. Nothing has to
exec into a running pod to fetch a kubeconfig, and the same credentials serve
the managers inside the cluster and the driver outside it — which is what lets
one measurement address kcp by two names. The certificate covers loopback for
exactly that second case.

### kcp has to reach the address it advertises

kcp is told to advertise its Service name as its shard URL, and its own
apibinder initializer resolves the `APIExport`s it binds through that address.
So kcp has to be able to reach it — from inside the pod kcp is running in.

A virtual IP does not satisfy that. A pod dialling a `ClusterIP` whose only
endpoint is itself is the hairpin case, and where it does not work the failure
is silent and misattributed: the default `APIBinding`s never bind, the
`system:apibindings` initializer is never removed, and every workspace sits in
`Initializing` for ever — reported against whatever created the workspace
rather than against the server that could not reach itself.

The Service is therefore **headless**, so the name resolves straight to the pod
and kcp reaches itself at its own address. The managers reach it the same way,
and the serving certificate covers the name for both.

This was established by experiment rather than by reading: kcp started with an
advertised address it could not reach hung workspaces in exactly that way, and
the same kcp advertising a reachable one did not.

### Two ways in, for one reason each

The driver runs outside the cluster, so that a managed cluster it cannot be
scheduled into is still a valid target.

- **Metrics** are read through the API server's pod proxy. A pod IP is routable
  from inside the cluster and from nowhere else.
- **kcp** is reached through a forwarded port. It cannot go through the same
  proxy: a driver needs full API semantics against kcp — watches, its own
  bearer token, its own trust of kcp's CA — and a proxy that re-terminates the
  request gives none of them.

## What a run reports

Per deployment, never only as a total. A sum that hides which deployment moved
is the failure this measurement exists to fix.

Three things the report states about itself, because each of them silently
invalidates the figures otherwise:

- **Placement.** A run where every component landed on one node measured a
  co-located deployment whatever the manifests asked for. The report says so in
  its own table rather than leaving a reader to work it out from a node list.
- **Restarts.** Process metrics reset when the process does, so a restarted
  container reports a fresh process's small numbers — at the widest point of a
  run, that drags the slope negative. Such samples are excluded from every fit
  and the restart is reported. There is no honest correction.
- **OOMKills.** A process cannot report the moment it was killed. Without
  reading the pod, a run would record the fleet getting *cheaper* as its
  containers died. An OOMKill is a capacity finding, and never a smaller
  measurement.

Memory limits are set and CPU limits are not, deliberately. The memory limit is
what makes an OOMKill possible; a CPU limit produces throttling, which would be
measured as the system being slower.

## Reconciled against the cheap instrument

A second instrument measuring one process is worse than one instrument being
wrong, because a disagreement leaves neither side obviously at fault — this
repository has already
[decided that once](workspace-resource-usage.md), and `internal/scaleharness`
stopped measuring because of it.

So a deployed run is admissible only with a check that keeps it honest. The
managers now serve the Go runtime and process collectors, which
controller-runtime's registry does not carry by default, and those are exactly
the quantities the in-process instruments report. A deployed run is compared,
per deployment, against a committed in-process sweep: the same program doing
the same work should agree, and where it does not the run is a finding about
one of the two instruments rather than a figure about the fleet.

That check is why the first run to make is a narrowed one. Until the two
instruments agree about a single deployment, nothing the deployed one says
about four is worth having. They have now been compared, and they agree — see
below.

## The two instruments agree

They were checked, on the narrowed run this page said to make first:

```sh
task test:scale:kind COMPONENTS=core-manager CLUSTERS=8
```

| Quantity | Deployed | In process | Ratio | Within 20% |
|---|--:|--:|--:|---|
| goroutines per workspace | 1.7 | 2.0 | 0.86x | yes |

Core-manager, deployed as its own Deployment on a kind cluster, holding 329,
331 and 339 goroutines at 2, 4 and 8 engaged workspaces. Two rigs sharing no
code path — one sweeping an in-process manager, one scraping a pod's metrics
endpoint through a Kubernetes API server — land 14% apart on the quantity the
whole cost model turns on.

Measured, with the run committed as
[`deployed-core-8x1.json`][evidence]. That is what makes the rest of what this
instrument says worth reading.

### The gap at the other end state is the point, not a fault

A run with all four providers measures core-manager at 17.0 goroutines per
workspace rather than 1.7 — reproducibly, from clean linear fits at 2/4/8 and
at 3/5/10 workspaces. Both are right, and the difference is the thing this
whole measurement exists to expose.

A complete provider set takes every cluster to Ready. A ready cluster costs the
core manager a live ClusterCache — a connection to the workload cluster, its
informers and their goroutines — which a run stopping at engagement never
opens. The in-process sweeps stop at engagement, so that cost has never
appeared in any figure this repository publishes.

The reconciliation therefore checks only runs that share the reference's end
state, and records the others as what they are: a ratio between two instruments
that measured different work, with the reason beside it. Widening a tolerance
until the two agreed would have hidden a real difference behind a number chosen
to make a failure go away.

The per-connected-cluster figure implied by that gap is roughly 15 goroutines.
It is **not** measured: no run producing it has its evidence committed. It is
recorded here as the explanation for a gap, not as a number to quote.

## What a fleet costs, so far

Six runs of all four managers, 25 to 100 clusters, at one, five and ten
clusters per workspace. Fitting goroutines against both counts, over all of
them at once — 21 samples per deployment ([evidence][evdir]):

| Deployment | fixed | per cluster | per workspace | largest residual |
|---|--:|--:|--:|--:|
| core-manager | 344 | 15.00 | 2.00 | 1 |
| kubeadm-bootstrap-manager | 149 | 13.00 | 1.00 | 0 |
| kubeadm-control-plane-manager | 155 | 46.07 | 0.88 | 18 |
| dev-infrastructure-manager | 175 | 29.01 | 0.93 | 17 |

Core-manager fits to within one goroutine and the bootstrap provider to zero,
across every run and every fleet shape.

**The per-workspace term is the calibration figure.** core-manager pays 2.00
goroutines per workspace, fitted from deployed runs at three packing ratios —
and the in-process sweep, a different rig measuring a different way, reports
2.0. The deployed instrument prices a workspace exactly as the reference does,
and additionally prices a cluster, which the reference never saw.

**A cluster costs roughly fifty times what a workspace costs.** So how a fleet
is divided into workspaces barely matters and the cluster count is what to size
against — which is what the original 200x1-against-20x10 question was asking.

### The model predicts out of sample

Fit to the runs of fifty clusters and fewer, then asked for the hundred-cluster
runs it had never seen:

| Deployment | Run | Predicted | Measured |
|---|---|--:|--:|
| core-manager | 100x1 | 2044 | 2044 |
| core-manager | 10x10 | 1864 | 1864 |
| kubeadm-bootstrap-manager | 100x1 | 1549 | 1549 |
| kubeadm-control-plane-manager | 100x1 | 4852 | 4851 |
| dev-infrastructure-manager | 10x10 | 3094 | 3082 |

Exact for two of the four deployments in both distributions; worst case 0.4%.

Core-manager at a hundred clusters shows the whole model in two numbers: 2044
goroutines spread over a hundred workspaces, 1864 packed into ten. The
difference is 180, which is 2.00 x 90.

The memory figures are weaker than the goroutine ones and each report says so
where its own heap series wanders: a checkpoint is sampled when it is reached,
which is wherever that falls relative to a garbage collection.

## Status

**Calibrated, modelled to a hundred clusters, single-node.** The harness runs
end to end on kind, the two instruments agree about one deployment, and a cost
model fitted below fifty clusters predicts a hundred at two different packings
without having seen either.

Not measured, and so not stated:

- **The 200-cluster figure.** The largest run is a hundred. The model predicts
  about 22,400 goroutines for 200 clusters in 200 workspaces and 21,500 for 200
  in 20 — a 2x extrapolation from a model that survived a 2x holdout, which
  makes it a good prediction and still a prediction. Per
  [Principle IX][constitution] it is not printed as a measurement, and the run
  that would make it one has not been taken.
- **Anything at 50 nodes per cluster.** Every run is one node per cluster. The
  target is 10,000 Machines and nothing has been near it.
- **Anything multi-node.** Every run so far is co-located, which each report
  says on its own face. `SPREAD=true` with `WORKERS` is how that changes.
- **The cost of a connected workload cluster**, above.

[evidence]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260831-210000-deployed-fleet-scale/evidence/deployed-core-8x1.json
[ev25]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260831-210000-deployed-fleet-scale/evidence/deployed-all-25x1.json
[evdir]: https://github.com/jimmidyson/kcp-cluster-api/tree/main/specs/20260831-210000-deployed-fleet-scale/evidence
[ev50]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260831-210000-deployed-fleet-scale/evidence/deployed-all-50x1.json

[capacity]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md
[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
