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

[`task demo`](demo.md) provisions clusters across several workspaces from one
manager in about a minute, starting its own kcp server. It is the fastest way
to see what the rest of this page describes.

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
| `core-manager` | 2 | 3 | 7 | 6 |
| `dev-infrastructure-manager` | 2 | 3 | 8 | 6 |
| `kubeadm-bootstrap-manager` | 2 | 4 | 16 | 7 |
| `kubeadm-control-plane-manager` | 2 | 7 | 72 | 7 |
| **All four** | **8** | **17** | **103** | **26** |

Engaging a workspace costs the same two goroutines in every deployment, flat
from one workspace to twenty, with **no watch connections to the shard** added
by a workspace: reads come from one shared wildcard cache per deployment, so
the shard sees the same 26 streams whether the installation serves one
workspace or twenty. A departing workspace gives all of it back.

What differs between deployments is what they then do. The kubeadm control
plane provider costs an order of magnitude more per workspace than core,
because per workspace it generates a cluster's certificates and creates its
first machine. Size that one against your cluster count, not just your
workspace count.

One thing the table does not include: **the clusters in a workspace cost more
than the workspace does.** A workspace holding a running control plane costs
about 45 goroutines in the process that runs its infrastructure provider,
against the 8 that serving the workspace costs across all four.

`task test:sweep` is the instrument, and it needs no container runtime. See
[Workspace resource usage](../design/workspace-resource-usage.md) for the
method and the full results.

**Unbinding is not a cancel button.** Deleting an `APIBinding` while the
workspace still holds clusters does not tear them down in the order Cluster API
needs, and the binding will not finish deleting. Delete the clusters first,
then unbind.

## Not supported yet

- Webhooks: nothing defaults or validates the objects a tenant writes, so a
  manifest must spell out what a webhook would otherwise fill in
- `WorkspaceType` with `defaultAPIBindings`, so a tenant workspace binds each
  provider's export by hand
- MachineHealthCheck, ClusterClass and topology: the core deployment wires
  `Cluster`, `Machine`, `MachineSet` and `MachineDeployment`, and no more
- `clusterctl`, published manifests, or any installation flow other than
  building and running the managers yourself

## Reporting issues

Please file issues and feature requests against this repository rather than
upstream Cluster API, unless the problem also reproduces on upstream Cluster
API outside kcp.
