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

## What it deliberately does not fake

etcd members, the `kube-apiserver` Pods, kubeadm's `ConfigMap`, `kube-proxy`
and CoreDNS. Those are what a `KubeadmControlPlane` inspects to report
initialized and to join further members, and they are the next increment if a
control plane provider is wanted in the loop. Until then the shape is: Machines
reach `Running`, a `Cluster` reports its control plane initialized once a
control plane `Machine` has a `NodeRef`, and no `KubeadmControlPlane` is
involved.

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

Against CAPX and `ntnx-sim`, the fork's `simrun` driver starts it as a fourth
process next to the simulator, the core manager and the CAPX manager, sets
each `NutanixCluster`'s endpoint into the port range, and waits for `Running`
instead of `Provisioned`. Nothing on either side imports the other.

## What is proven

The package's unit tests stand up the real in-memory mux and read the result
the way the core `Machine` controller would: through the kubeconfig secret
written for the `Cluster`, listing nodes on the API server it points at. That
is proof at the seam. Machines reaching `Running` end to end against CAPX is
what `simrun` measures, on the fork branch, and is not asserted here.
