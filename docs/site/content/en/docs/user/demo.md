---
title: Demo
description: Build a ready cluster in each of several kcp workspaces from one manager, in one command.
weight: 5
---

```sh
task demo
```

That starts a single-shard kcp server, publishes **one `APIExport` per
provider** out of its `root` workspace, creates two workspaces bound to all of
them, runs each provider's controllers — the same wiring each provider's own
deployment runs — and builds a cluster in each workspace, printing what they
are doing until they are all ready:

```
WORKSPACE         LOGICAL CLUSTER   CLUSTER  PROVISIONED  READY  DETAIL
root:capi-demo-1  an4y05w1fxdiy8wi  demo-00  yes          yes    cluster ready
root:capi-demo-2  37tqgo784myry3pn  demo-00  yes          yes    cluster ready

WORKSPACE         CONTROL PLANE  INITIALIZED  READY  DETAIL
root:capi-demo-1  demo-00-cp     yes          1/1    control plane ready
root:capi-demo-2  demo-00-cp     yes          1/1    control plane ready

WORKSPACE         MACHINE                 BOOTSTRAPPED  READY  DATA SECRET             PHASE    DETAIL
root:capi-demo-1  demo-00-cp-6gkn6        yes           yes    demo-00-cp-6gkn6        Running  machine ready
root:capi-demo-1  demo-00-md-vm96z-dkxwb  yes           yes    demo-00-md-vm96z-dkxwb  Running  machine ready
root:capi-demo-2  demo-00-cp-9msd2        yes           yes    demo-00-cp-9msd2        Running  machine ready
root:capi-demo-2  demo-00-md-9zjvz-h6kpz  yes           yes    demo-00-md-9zjvz-h6kpz  Running  machine ready
```

Each cluster gets a control plane machine and a worker by default, because a
cluster is what the demo is for. **Ready** is the Cluster's `Available`
condition and is what the run waits for; provisioned infrastructure is a
milestone on the way there, reported alongside rather than mistaken for the
destination. A control plane whose machines never go Ready is provisioned, and
is not a cluster.

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
ready. The demo prints a `kubectl` command per workspace as it finishes —
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
| `--control-plane-machines` | 1 | Control plane machines per cluster, as a `KubeadmControlPlane`. Zero stops the run at provisioned infrastructure |
| `--worker-machines` | 1 | Worker machines per cluster, as a `MachineDeployment`. Needs `--control-plane-machines`: a worker has no control plane to join otherwise |
| `--backend` | `inmemory` | `inmemory` needs nothing; `docker` provisions real containers and pulls `kindest` images |
| `--wait` | false | Stay up after every cluster is ready |
| `--kcp-kubeconfig` | — | Run against a kcp server you already have, instead of starting one |
| `--no-manager` | false | Create the workspaces and objects only, against a `core-manager` you started yourself |
| `--timeout` | 5m | How long to wait for every cluster to be ready |

Ten workspaces is as easy as two, and is the more interesting run:

```sh
task demo DEMO_FLAGS="--workspaces 10 --wait"
```

## Machines, and the providers behind them

```sh
task demo DEMO_FLAGS="--control-plane-machines 3 --worker-machines 2"
```

Each cluster gets a `KubeadmControlPlane` with that many replicas and a
`MachineDeployment` with that many workers, and the run publishes and wires the
kubeadm bootstrap and control plane providers alongside core and the dev
infrastructure provider. Those providers create the Machines, their
`KubeadmConfig`s and their `DevMachine`s themselves — the demo does not name
them, which is why the names in the first table are ones it never chose. The
`-cp-` machines are the control plane's; the `-md-` ones are the
`MachineDeployment`'s.

**Initialized** means the control plane can accept requests: there is a cluster
there. **Ready** is stricter, and is what the run waits for — every replica the
control plane was asked for has passed every health check the provider makes
against the workload cluster, and every Machine's Node is healthy. A control
plane sits at `INITIALIZED yes` with `READY 0/1` in between, which is a state
worth watching rather than a contradiction.

Each workspace's bootstrap data and cluster certificate authority are its own —
different bytes under identical names, which is what
`test/integration/bootstrap` asserts.

For the cheapest possible run — infrastructure only, no bootstrap or control
plane provider — ask for no machines:

```sh
task demo DEMO_FLAGS="--control-plane-machines 0"
```

That stops at provisioned infrastructure rather than at ready, because a
`Cluster` with no control plane has no readiness to reach: its `Available`
condition summarises a remote connection and a control plane it does not have.

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
building clusters in many kcp workspaces at once and taking them to ready, from
one manager that was told about none of them — each workspace is engaged
because its `APIBinding` became ready, and nothing names a workspace in
configuration.

It does not serve webhooks: those are single-workspace by construction until
the webhook dispatch layer (G4) lands, so every object the demo creates is
fully specified rather than defaulted.

The same run is an integration test — `test/integration/demo`, part of
`task verify` — which additionally asserts the isolation the table cannot show:
that each workspace sees exactly its own cluster, and that the status written
into it was written for that workspace.

## Going through it a piece at a time

[The walkthrough](walkthrough.md) runs this same demo and then stops at each
part — workspaces, exports, bindings, permission claims, and the virtual
workspace one manager watches — with the `kubectl` commands to see each of them
for yourself. It assumes no kcp knowledge.
