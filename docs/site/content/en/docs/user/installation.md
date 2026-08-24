---
title: Installation
description: Prerequisites, building kcp-cluster-api, and running the manager.
weight: 10
---

{{% pageinfo color="info" %}}
kcp-cluster-api is early-stage. Nothing is released — no published binary, no
published image, no `clusterctl` provider — so everything here is built from
source. There is a container image and a set of manifests to build:
[On Kubernetes](kubernetes.md) deploys the shard and every provider as pods,
and this page is the same thing as processes. It reconciles every workspace
bound to the export; webhooks are the exception, and are served for one
workspace or none — see [Design & architecture](../design/_index.md).
{{% /pageinfo %}}

## Prerequisites

- Go — the version in the repository's root `go.mod`
- [task](https://taskfile.dev/), the entry point for every named operation:
  `go install github.com/go-task/task/v3/cmd/task@latest`
- A running [kcp](https://github.com/kcp-dev/kcp) instance, with **one
  `APIExport` per provider** you intend to run, an `APIExportEndpointSlice` for
  each, and an `APIBinding` to each in every workspace you want reconciled
- Each binding must **accept that export's permission claims**. They are what
  lets a provider reach the Secrets it writes and the types another provider
  publishes; without them a manager starts cleanly and its writes are refused.
  `internal/capiexports` is the topology, and `task demo` publishes it — see
  [One APIExport per provider](../design/provider-exports.md)
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
  --endpoint-slice-name cluster-api-core
```

- `--endpoint-slice-name` names the `APIExportEndpointSlice` **in the
  workspace your kubeconfig points at**. Its virtual workspace URLs are what
  the manager uses to discover and cache bound workspaces. It defaults to
  `cluster-api-core`, this provider's own export.
- Cluster access follows the usual controller-runtime resolution:
  `--kubeconfig`, otherwise in-cluster configuration.

No workspace is named anywhere. Every workspace whose `APIBinding` to the
export becomes ready is reconciled from that moment, and stops being
reconciled when it unbinds.

### The other providers

Each provider is its own deployment, consuming its own export — the way Cluster
API deploys providers:

```sh
go build -o bin/kubeadm-bootstrap-manager ./cmd/kubeadm-bootstrap-manager
go build -o bin/kubeadm-control-plane-manager ./cmd/kubeadm-control-plane-manager
go build -o bin/dev-infrastructure-manager ./cmd/dev-infrastructure-manager

bin/kubeadm-bootstrap-manager       --kubeconfig ~/.kube/kcp.kubeconfig
bin/kubeadm-control-plane-manager   --kubeconfig ~/.kube/kcp.kubeconfig
bin/dev-infrastructure-manager      --kubeconfig ~/.kube/kcp.kubeconfig
```

`--endpoint-slice-name` defaults to each provider's own export, so in the
standard topology it can be left off entirely. The bootstrap provider adds
`--bootstrap-token-ttl`; the control plane provider adds `--etcd-dial-timeout`,
`--etcd-call-timeout` and `--remote-conditions-grace-period` (which must be at
least two minutes, so the ClusterCache drops a connection before the provider
concludes a control plane is unreachable); the infrastructure provider needs a
container runtime, serves its own admission webhooks for one workspace on the
same terms as core, and takes `POD_IP` from the environment as the address its
in-memory workload clusters advertise.

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
  admission and conversion webhooks this process serves. It is the workspace's
  `spec.cluster` as its parent reports it; [the walkthrough](walkthrough.md#getting-a-workspaces-logical-cluster-id)
  covers what that identifier is and the three ways to read it. Getting it wrong
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

Two flags matter to anything that starts a manager before its workspaces
exist, which is what a `Deployment` does:

- `--startup-timeout` (default one minute) is how long a manager waits for its
  `APIExportEndpointSlice` to have a virtual workspace endpoint. kcp gives an
  export an endpoint only once a workspace has bound it, so a manager started
  before the first tenant waits here rather than exiting. The workspace
  manager's own `--startup-timeout` additionally covers the `cluster-api`
  `WorkspaceType` being published.
- `--metrics-addr` (default `:8080`) is the metrics endpoint. It is a flag
  because several of these managers on one machine cannot all take
  controller-runtime's default port — the second one to start fails with
  `address already in use`.

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

The core `Cluster` and `Machine` reconcilers, and nothing else: the
infrastructure and bootstrap providers are deployments of their own. It is
still narrower than upstream's full core reconciler set, per the scope decision
in `docs/adr-0001-per-workspace-manager-pool.md`. Their admission and conversion webhooks are served only for the workspace
named by `--webhook-workspace-cluster-name`, if any.

The manager also installs a kcp-aware contract-metadata resolver at startup,
because a workspace consuming a type through an `APIBinding` has no
`CustomResourceDefinition` object for upstream's default resolver to read. It
is process-wide and identical for every workspace, being derived from the CRD
manifests this build was compiled against rather than from any cluster.

## A worked example

The integration test under `test/integration/dockerbackend` is the executable
reference for the whole setup: it starts a real kcp server, publishes the
`APIExport`, binds it into a workspace, waits for the endpoint slice to get
an endpoint, and then runs the same wiring the manager does.

```sh
task test:integration
```

If a step in these instructions is ambiguous, that test is the authority —
it has to keep passing.

## Running it on Kubernetes instead

Everything above is the same wiring as pods:

```sh
task demo:kubernetes:kind
```

A kcp shard as a `StatefulSet` and one `Deployment` per provider, with the
credentials generated before anything starts — see
[On Kubernetes](kubernetes.md) for the whole of it and
[Deploying on Kubernetes](../design/kubernetes-deployment.md) for what it
needed.

## Not here yet

Nothing is published: no released binary, no image in a registry, and no
`clusterctl` provider metadata. Webhooks still serve one workspace or none.
Those are tracked in the design documentation rather than promised here.
