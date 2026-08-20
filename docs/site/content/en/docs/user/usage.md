---
title: Usage
description: Day-to-day usage of kcp-cluster-api.
weight: 20
---

{{% pageinfo color="info" %}}
This page will fill in as KCP-aware functionality lands. Today one manager
process reconciles every workspace bound to the export, engaging each as it
binds and stopping when it unbinds — no workspace is named in configuration.
Webhooks are the exception: one workspace or none. See
[Installation](installation.md) for how to run it, and
[Per-workspace wiring](../design/per-workspace-wiring.md) for what that
exception costs and why it is there.
{{% /pageinfo %}}

## Seeing it work

[`task demo`](demo.md) brings up a cluster in each of several workspaces from
one manager, and waits for them to be ready, in about a minute — starting its
own kcp server. It builds them from a `ClusterClass`, and it is the fastest way
to see what the rest of this page describes.

## A cluster is a ClusterClass based cluster

A workspace holds one or more `ClusterClass`es and the templates they refer to,
and a `Cluster` names a class, a version and a shape:

```yaml
apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: demo-00
  namespace: default
spec:
  topology:
    classRef:
      name: demo
    version: v1.34.0
    controlPlane:
      replicas: 1
    workers:
      machineDeployments:
        - class: default-worker
          name: md
          replicas: 1
```

Everything under it — the infrastructure cluster, the control plane, the worker
`MachineDeployment`, and the per-cluster templates each is stamped from — is
created by the topology controller in that workspace, from that workspace's
class. Nothing is shared between workspaces, including a class of the same
name.

Writing the objects out by hand still works: `spec.infrastructureRef` and
`spec.controlPlaneRef` are unchanged, and the reconcilers that act on them do
not know how the objects got there. But the class is the supported shape, and
it is the one this project's demo, integration tests and measurements are built
on.

Two things a single-tenant installation gets from a webhook and this one does
not, until the webhook dispatch layer lands:

- **Class variables are not defaulted.** A `Cluster` must state every variable
  its class declares, because the defaulting is done by an admission webhook.
  Prefer a class with no variables until then.
- **Nothing else is defaulted either.** A worker class must spell out its
  rollout strategy; an infrastructure template for a container-backed cluster
  must spell out its control plane port. See
  [The demo](../design/demo.md).

## What is different, and what is not

The API surface is upstream's, unchanged: `Cluster`, `Machine` and the rest
behave as the
[Cluster API user documentation](https://cluster-api.sigs.k8s.io/user/quick-start)
describes, and the reconcilers acting on them are upstream's own. What
changes is *where* those objects live and how the controllers reach them:

- Resources live in a kcp workspace that has an `APIBinding` to the
  `APIExport` publishing the Cluster API types, rather than in a single
  physical cluster.
- The manager discovers and caches workspaces through that export's
  `APIExportEndpointSlice`, and engages every one of them as its `APIBinding`
  becomes ready. No workspace is named in configuration. Webhooks are the
  exception — one workspace or none, named by
  `--webhook-workspace-cluster-name`.
- Cross-references between objects (`spec.infrastructureRef`,
  `spec.bootstrap.configRef`, `spec.controlPlaneRef`) resolve through a
  kcp-aware contract-metadata resolver, because a workspace consuming a type
  via `APIBinding` has no `CustomResourceDefinition` object to read contract
  labels from.

## What a workspace costs

One process per provider serves many workspaces, and each of them engages a
workspace separately — so the practical question when sizing an installation is
what one workspace adds to each process. Measured per deployment, twenty
workspaces each:

| Deployment | Goroutines/ws | Discovery/ws | Requests/ws | Streams held |
|---|--:|--:|--:|--:|
| `core-manager` | 2 | 3 | 7 | 8 |
| `dev-infrastructure-manager` | 2 | 3 | 8 | 6 |
| `kubeadm-bootstrap-manager` | 2 | 4 | 16 | 7 |
| `kubeadm-control-plane-manager` | 2 | 7 | 72 | 7 |
| **All four** | **8** | **17** | **103** | **28** |

Engaging a workspace costs the same two goroutines in every deployment, flat
from one workspace to twenty, with **no watch connections to the shard** added
by a workspace: reads come from one shared wildcard cache per deployment, so
the shard sees the same 28 streams whether the installation serves one
workspace or twenty. A departing workspace gives all of it back.

Serving ClusterClass based clusters is visible in exactly one cell of that
table: `core-manager` holds eight streams where it held six, for the
`ClusterClass` and `MachinePool` its topology controllers watch. Four more
controllers in the process, and a workspace costs what it did.

What differs between deployments is what they then do. The kubeadm control
plane provider costs an order of magnitude more per workspace than core,
because per workspace it generates a cluster's certificates and creates its
first machine. Size that one against your cluster count, not just your
workspace count.

One thing the table does not include: **the clusters in a workspace cost more
than the workspace does.** A workspace holding a running control plane costs
about 57 goroutines in the process that runs its infrastructure provider,
against the 8 that serving the workspace costs across all four — and about 484
reconcile requests, against 103. Size the infrastructure deployment against
your cluster count, not your workspace count.

`task test:sweep` is the instrument, and it needs no container runtime. See
[Workspace resource usage](../design/workspace-resource-usage.md) for the
method and the full results.

**Unbinding tears the clusters down with it.** Deleting an `APIBinding` while
the workspace still holds clusters removes every bound object at once, and the
providers clean up as it goes: the binding finishes deleting once they have.
Expect it to take a couple of minutes rather than to be instant, and treat it
as destructive — there is no "stop using Cluster API but keep my clusters".
Delete the clusters yourself first if you want to watch that happen separately.

## Not supported yet

- Webhooks: nothing defaults or validates the objects a tenant writes, so a
  manifest must spell out what a webhook would otherwise fill in
- `WorkspaceType` with `defaultAPIBindings`, so a tenant workspace binds each
  provider's export by hand
- Class variables, patches and runtime extensions: the machinery is wired and
  the `RuntimeSDK` gate is off, so a class that declares variables or patches
  is not served the way a single-tenant installation would serve it
- MachineHealthCheck and MachinePool: `MachinePool` is published so that a
  managed topology can be reconciled at all, but nothing acts on one
- `clusterctl`, published manifests, or any installation flow other than
  building and running the managers yourself

## Reporting issues

Please file issues and feature requests against this repository rather than
upstream Cluster API, unless the problem also reproduces on upstream Cluster
API outside kcp.
