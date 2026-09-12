---
title: Simulated workload clusters
description: Drive any infrastructure provider's Machines to Running with no real nodes, for scale tests without hardware.
weight: 8
---

`workloadsim` fakes the workload cluster behind a Cluster API infrastructure
provider. Pointed at a management cluster, it gives every `Cluster` an
in-memory API server at the `Cluster`'s declared control plane endpoint, and
every `Machine` a `Node` carrying the providerID the infrastructure provider
reported. That is the chain the core `Machine` controller walks to set a
`NodeRef` and move a `Machine` to `Running`, so a provider whose own backend is
simulated — CAPX against [`ntnx-sim`](https://github.com/jimmidyson/cluster-api-provider-nutanix/tree/ai/focused-meitner-3h1zuv/test/simulator)
is the first — reaches the same end state a real one does, with no hardware.

It is a separate binary and a separate Go module under
[`workloadsim/`](https://github.com/jimmidyson/kcp-cluster-api/tree/main/workloadsim),
pinned to **stock** Cluster API rather than to this project's fork. Nothing else
in this repository imports it and it imports nothing else here: it runs against
a plain management cluster, and the only thing it shares with the rest of the
project is the reason it exists — a scale test needs many clusters and cannot
pay for real nodes.

## What it watches, and the one contract

It watches only the core types, `Cluster` and `Machine`, so it needs to know
nothing about the provider:

- **`Cluster`** — once `spec.controlPlaneEndpoint` is set, a listener is opened
  at exactly that port and answers as that cluster's API server, with a serving
  certificate signed by the cluster's CA.
- **`Machine`** — once `spec.providerID` is set, which the core provider copies
  from the infrastructure machine when it is ready, a `Node` is written to the
  cluster's API server with that providerID, the `Machine`'s addresses and
  version, a `Ready` condition, and the control plane label and taint when the
  `Machine` is one.
- **A control plane `Machine`** additionally gets everything a
  `KubeadmControlPlane` inspects before it counts a replica healthy and adds
  the next: an etcd Pod and a member behind it that answers member lists and
  health through a port-forward, the `kube-apiserver`, `kube-scheduler` and
  `kube-controller-manager` Pods, kubeadm's `ConfigMap` and RBAC, `kube-proxy`
  and CoreDNS. The shapes are the docker/dev provider's in-memory backend's,
  where they were worked out against `KubeadmControlPlane`. The first replica
  mints the etcd cluster and leads it, every later one joins, and a replica
  that goes leaves.

The contract with whatever creates the clusters is a single thing: **the
`Cluster`'s control plane endpoint must name this process's host and a port in
its range.** For CAPX that field is user input on the `NutanixCluster`, so the
harness that writes the `NutanixCluster` chooses `127.0.0.1:<port>` from the
range it started `workloadsim` with. An endpoint on any other host is reported
and left alone, because nothing this process does can make it answer.

## Standing in for a control plane provider

With no control plane provider in the loop, nothing creates the cluster's CA
or its kubeconfig secret, and without them the core `Machine` controller has no
way to reach the workload cluster to find its `Node`. `--generate-cluster-secrets`
(on by default) generates them when they are absent, using Cluster API's own
helpers, and never replaces what exists — with a control plane provider present
it does nothing.

## How many control plane nodes

`workloadsim` never decides that. It writes one `Node` per `Machine`, so the
count is whatever produced the `Machines`: a `KubeadmControlPlane` at three
replicas yields three control plane `Machines`, three VMs from the provider,
three `Nodes`, and three etcd members. With no control plane provider, the
harness decides by labelling `Machines` as control plane.

## Running it

```sh
cd workloadsim && go build -o ../bin/workloadsim ./cmd/workloadsim
bin/workloadsim --kubeconfig "$KUBECONFIG" --host 127.0.0.1 --port-min 20000 --port-max 30000
```

| Flag | Default | Meaning |
|---|---|---|
| `--host` | `127.0.0.1` | Address the fake API servers are reachable at; every `Cluster`'s endpoint must name it |
| `--port-min`, `--port-max` | `20000`, `30000` | Range a `Cluster`'s endpoint port must fall in |
| `--debug-port` | `19000` | Debug endpoint listing the fake clusters and the objects behind each |
| `--generate-cluster-secrets` | `true` | Generate absent CA and kubeconfig secrets, standing in for a control plane provider |
| `--max-concurrent-reconciles` | `10` | Workers per controller |

## Plugging it into the CAPX scale harness

Against CAPX and `ntnx-sim`, the fork's `simrun` driver starts `workloadsim`
as a fourth process next to the simulator, the core manager and the CAPX
manager, puts `127.0.0.1:20000` on the `NutanixCluster`, labels the first
`--control-plane-machines` of its `Machines` as control plane, and waits for
`Running` instead of `Provisioned`. Nothing on either side imports the other:
`make test-sim` installs `workloadsim` with `go install`, and
`WORKLOADSIM_REF` picks the branch or tag.

That change to the fork is
[`workloadsim/simrun-workloadsim.patch`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/workloadsim/simrun-workloadsim.patch)
in this repository, ready to `git am` onto the simulator branch. Swapping
CAPD for CAPX in a scale run is then a matter of which harness is started;
`workloadsim` is the same either way.

## What is proven

Three tiers, each stated for what it is.

**At the seam**, by the unit tests: the real in-memory mux is stood up and
read the way the core `Machine` controller would, through the kubeconfig
secret written for the `Cluster`, listing nodes on the API server it points
at; and etcd's member list is read the way `KubeadmControlPlane` does, through
a port-forward with a certificate signed by the cluster's etcd CA, from more
than one member.

**Against stock Cluster API**, by `task test:workloadsim:e2e`: the real core,
kubeadm bootstrap and `KubeadmControlPlane` managers, as the binaries a user
deploys, run against envtest with `workloadsim` and a fake infrastructure
provider that does exactly what CAPX does against `ntnx-sim`. A
`KubeadmControlPlane` at three replicas reports initialized with three ready
replicas, two worker `Machines` reach `Running`, and the `Cluster` deletes
cleanly. One run on a development container took about a minute to come up
and ten seconds to go; that is a timing from one run, not a measurement of
anything.

**Against CAPX itself**, by the patched `simrun` on the fork branch: the
unmodified CAPX manager against `ntnx-sim` takes `Machines` to `Running`
through `workloadsim`. One run with five `Machines`, three of them control
plane, had every VM created and powered on by `ntnx-sim`, every `Machine`
`Running` with a `NodeRef` eight seconds after creation, and the `Cluster`
and every VM gone six seconds after deletion. Timings from one run on a
development container, with the simulator's task durations at zero.

The test builds the three stock managers from the module cache on first run
and needs envtest binaries, which `task tools:envtest` fetches.
