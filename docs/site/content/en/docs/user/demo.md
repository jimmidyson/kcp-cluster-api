---
title: Demo
description: Provision clusters in several kcp workspaces from one manager, in one command.
weight: 5
---

```sh
task demo
```

That starts a single-shard kcp server, publishes **one `APIExport` per
provider** out of its `root` workspace, creates two workspaces bound to all of
them, runs each provider's controllers — the same wiring each provider's own
deployment runs — and provisions a cluster in each workspace, printing what
they are doing until they are all up:

```
WORKSPACE         LOGICAL CLUSTER   CLUSTER  PROVISIONED  DETAIL
root:capi-demo-1  1vxo3icpjf48qwvy  demo-00  yes          infrastructure provisioned
root:capi-demo-2  2hnz892clhia2ohv  demo-00  yes          infrastructure provisioned
```

Both clusters are called `demo-00`, in both workspaces, on purpose: identical
names are what makes a cross-workspace confusion visible rather than plausible.
One shard, one manager per provider, every workspace served by all of them —
and each workspace's objects stay its own.

The demo runs those managers in a single process for convenience. A deployment
runs one process each; see
[One APIExport per provider](../design/provider-exports.md).

It takes about a minute, needs no container runtime, and pulls no images.

## Prerequisites

- Go — the version in [`go.mod`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/go.mod)
- [task](https://taskfile.dev/): `go install github.com/go-task/task/v3/cmd/task@latest`

`task demo` downloads the pinned kcp server binary into `bin/` the first time,
and keeps the server's state and log under `.demo/`. Nothing is installed
outside the repository, and the server is stopped when the demo exits.

## Looking around while it runs

```sh
task demo DEMO_FLAGS="--wait"
```

`--wait` leaves the server and the manager running after every cluster is
provisioned. The demo prints a `kubectl` command per workspace as it finishes —
each one is the same kubeconfig with a different `--server`, because a kcp
workspace *is* a URL path:

```sh
kubectl --kubeconfig .demo/kcp/admin.kubeconfig --context base \
  --server https://localhost:35891/clusters/root:capi-demo-1 \
  get clusters,devclusters -A
```

Ctrl-C stops the manager and the server.

## Options

Pass flags through `DEMO_FLAGS`, or run `go run ./cmd/demo --help` for the full
list.

| Flag | Default | What it does |
|---|---|---|
| `--workspaces` | 2 | How many workspaces to create, bind and provision in |
| `--clusters` | 1 | Clusters per workspace |
| `--control-plane-machines` | 0 | Control plane machines per cluster. Any number above zero also wires the kubeadm bootstrap provider |
| `--backend` | `inmemory` | `inmemory` needs nothing; `docker` provisions real containers and pulls `kindest` images |
| `--wait` | false | Stay up after provisioning |
| `--kcp-kubeconfig` | — | Run against a kcp server you already have, instead of starting one |
| `--no-manager` | false | Create the workspaces and objects only, against a `core-manager` you started yourself |
| `--timeout` | 5m | How long to wait for every cluster |

Ten workspaces is as easy as two, and is the more interesting run:

```sh
task demo DEMO_FLAGS="--workspaces 10 --wait"
```

## Machines, and the bootstrap provider

```sh
task demo DEMO_FLAGS="--control-plane-machines 1"
```

Each cluster gets a `KubeadmControlPlane` with that many replicas, and the run
also publishes and wires the kubeadm bootstrap and control plane providers. The
control plane provider creates the Machines, their `KubeadmConfig`s and their
`DevMachine`s itself — the demo does not name them:

```
WORKSPACE         CONTROL PLANE  INITIALIZED  READY  DETAIL
root:capi-demo-1  demo-00-cp     yes          0/1    control plane initialized
root:capi-demo-2  demo-00-cp     yes          0/1    control plane initialized

WORKSPACE         MACHINE           BOOTSTRAPPED  DATA SECRET       PHASE        DETAIL
root:capi-demo-1  demo-00-cp-ndnxq  yes           demo-00-cp-ndnxq  Provisioned  bootstrap data ready
root:capi-demo-2  demo-00-cp-g5ccb  yes           demo-00-cp-g5ccb  Provisioned  bootstrap data ready
```

**Initialized** means the control plane can accept requests: there is a cluster
there. Each workspace's bootstrap data and cluster certificate authority are its
own — different bytes under identical names, which is what
`test/integration/bootstrap` asserts.

`READY 0/1` alongside `INITIALIZED yes` is not a contradiction: a control plane
is initialized by its first machine and reports a replica ready once that
machine passes every health check the provider makes against the workload
cluster.

## Against your own kcp

```sh
task demo DEMO_FLAGS="--kcp-kubeconfig ~/.kube/kcp.kubeconfig"
```

The kubeconfig context has to be a cluster-unaware one (`base` in a kcp admin
kubeconfig): the demo scopes it to each workspace itself. Everything it creates
is idempotent, so re-running it against the same server picks up where it left
off rather than failing.

## What it does and does not show

It shows what this project is for: unmodified upstream Cluster API reconcilers,
provisioning infrastructure in many kcp workspaces at once, from one manager
that was told about none of them — each workspace is engaged because its
`APIBinding` became ready, and nothing names a workspace in configuration.

By default it stops at cluster infrastructure — `DevCluster`.
`--control-plane-machines` adds a control plane, and with it the kubeadm
bootstrap and control plane providers. It does not serve webhooks: those are single-workspace by construction
until the webhook dispatch layer (G4) lands, so every object the demo creates
is fully specified rather than defaulted.

The same run is an integration test — `test/integration/demo`, part of
`task verify` — which additionally asserts the isolation the table cannot show:
that each workspace sees exactly its own cluster, and that the status written
into it was written for that workspace.
