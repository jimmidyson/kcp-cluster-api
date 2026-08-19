---
title: Installation
description: Prerequisites, building kcp-cluster-api, and running the manager.
weight: 10
---

{{% pageinfo color="info" %}}
kcp-cluster-api is early-stage. There is no released binary, container image
or manifest yet, and no `clusterctl` provider: the only way to run it is to
build from source and start `core-manager` yourself. It reconciles every
workspace bound to the export; webhooks are the exception, and are served for
one workspace or none — see [Design & architecture](../design/_index.md).
{{% /pageinfo %}}

## Prerequisites

- Go — the version in the repository's root `go.mod`
- [task](https://taskfile.dev/), the entry point for every named operation:
  `go install github.com/go-task/task/v3/cmd/task@latest`
- A running [kcp](https://github.com/kcp-dev/kcp) instance, with an
  `APIExport` publishing the Cluster API types, an `APIExportEndpointSlice`
  for that export, and an `APIBinding` to it in each workspace you want
  reconciled
- If you run the bootstrap provider: that export must also **claim** `secrets`
  and `configmaps`, and each workspace's `APIBinding` must accept those claims.
  Without them the provider starts cleanly and every write it makes is refused
  — see [The bootstrap provider](../design/bootstrap-provider.md)
- A container runtime — only for the integration tests, which start a real
  kcp server

## Get the source

```sh
git clone https://github.com/jimmidyson/kcp-cluster-api.git
cd kcp-cluster-api
```

## Build

```sh
task --list      # every operation this repository has
task check       # build, lint, unit tests — the inner loop
task verify      # the done-condition, including integration tests
```

`task build` compiles every package as a check; it does not emit binaries.
To get the manager itself:

```sh
go build -o bin/core-manager ./cmd/core-manager
```

## Run the manager

One flag is required:

```sh
bin/core-manager \
  --kubeconfig ~/.kube/kcp.kubeconfig \
  --endpoint-slice-name <apiexportendpointslice-name>
```

- `--endpoint-slice-name` names the `APIExportEndpointSlice` **in the
  workspace your kubeconfig points at**. Its virtual workspace URLs are what
  the manager uses to discover and cache bound workspaces.
- Cluster access follows the usual controller-runtime resolution:
  `--kubeconfig`, otherwise in-cluster configuration.

No workspace is named anywhere. Every workspace whose `APIBinding` to the
export becomes ready is reconciled from that moment, and stops being
reconciled when it unbinds.

### The bootstrap provider

The kubeadm bootstrap provider is a second binary, deployed the way Cluster API
deploys providers — one process each, both pointed at the same endpoint slice:

```sh
go build -o bin/kubeadm-bootstrap-manager ./cmd/kubeadm-bootstrap-manager

bin/kubeadm-bootstrap-manager \
  --kubeconfig ~/.kube/kcp.kubeconfig \
  --endpoint-slice-name <apiexportendpointslice-name>
```

It serves no webhooks, and it needs the permission claims listed under
Prerequisites. `--bootstrap-token-ttl` is the one knob of its own.

### Serving webhooks

Webhooks are the one thing that is not multi-workspace, and they are opt-in:

```sh
bin/core-manager \
  --kubeconfig ~/.kube/kcp.kubeconfig \
  --endpoint-slice-name <apiexportendpointslice-name> \
  --webhook-workspace-cluster-name <logical-cluster-name>
```

- `--webhook-workspace-cluster-name` is the *internal logical cluster name* —
  not the human-readable workspace path — of the one workspace whose
  admission and conversion webhooks this process serves. Getting it wrong
  looks like a manager that starts cleanly, reconciles normally, and then
  exits after waiting for a workspace that never appears
  (`--engage-timeout`, 5 minutes by default).
- Left unset, no webhooks are served. Reconciliation is unaffected either
  way.

Serving more than one workspace's webhooks needs each admission request
resolved to its source workspace, which is not built. Asking for a second one
is an error rather than a silent partial success — see
[Per-workspace wiring](../design/per-workspace-wiring.md) for why that
distinction matters here.

Run `bin/core-manager --help` for the rest: webhook serving (`--webhook-port`,
`--webhook-cert-dir`, default port 9443), the health endpoint
(`--health-addr`, default `:9440`), and log configuration.

### Feature gates and bound types

Upstream's `Cluster` and `Machine` reconcilers watch every core type gated by
a feature flag they support — `MachinePool`, for one, is on by default — as
an event source, not just the types this skeleton actually reconciles. Every
such type has to be bound in the workspace as well, or that watch's cache
sync stalls the whole controller. `--feature-gates` is the escape hatch: turn
the gate off instead of publishing and binding a CRD you do not otherwise
need.

### What gets wired

Per workspace: the core `Cluster` and `Machine` reconcilers, plus the docker
development infrastructure provider's `DevCluster` and `DevMachine`
reconcilers — the scope decision recorded in
`docs/adr-0001-per-workspace-manager-pool.md`, not upstream's full reconciler
set. Their admission and conversion webhooks are served only for the workspace
named by `--webhook-workspace-cluster-name`, if any.

The manager also installs a kcp-aware contract-metadata resolver at startup,
because a workspace consuming a type through an `APIBinding` has no
`CustomResourceDefinition` object for upstream's default resolver to read. It
is process-wide and identical for every workspace, being derived from the CRD
manifests this build was compiled against rather than from any cluster.

## A worked example

The integration test under `test/integration/coremanager` is the executable
reference for the whole setup: it starts a real kcp server, publishes the
`APIExport`, binds it into a workspace, waits for the endpoint slice to get
an endpoint, and then runs the same wiring the manager does.

```sh
task test:integration
```

If a step in these instructions is ambiguous, that test is the authority —
it has to keep passing.

## Not here yet

No container image, no kustomize manifests, no `clusterctl` provider
metadata, and no webhook serving across more than one workspace. Those are
tracked in the design documentation rather than promised here.
