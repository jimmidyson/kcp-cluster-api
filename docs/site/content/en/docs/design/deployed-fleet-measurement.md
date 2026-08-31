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
# Build the images. KO_DOCKER_REPO=kind.local loads straight into a kind
# cluster; a registry address is what a real cluster needs.
KO_DOCKER_REPO=kind.local task deployed:images

# M1: core-manager alone, checked against the committed in-process sweep.
task test:scale:deployed MANAGER_IMAGE=kind.local

# M2: all four, with the split that has no co-location caveat.
task test:scale:deployed MANAGER_IMAGE=... COMPONENTS=all

# M3: one component per node.
task test:scale:deployed MANAGER_IMAGE=... COMPONENTS=all SPREAD=true
```

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

That check is why M1 deploys `core-manager` alone. Until the two agree for one
deployment, nothing the deployed instrument says about four is worth having.

## Status

**No deployed run has been taken.** The harness, its manifests, its
measurement and its reconciliation are built and unit tested; the run needs a
cluster and a container runtime. Per
[Principle IX][constitution] a figure that has not been measured is not
predicted into the gap, so there are no numbers on this page.

[capacity]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/specs/20260815-211812-workspace-wiring-scale/evidence/capacity.md
[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
