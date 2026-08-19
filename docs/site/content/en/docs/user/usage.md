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

One process serves many workspaces, so the practical question when sizing a
deployment is what each one adds.

The measured figure for a fleet-wide manager is **2.0 goroutines per active
workspace**, flat from two workspaces to a hundred, with **no watch connections
to the shard** — reads for every workspace come from one shared wildcard cache,
so the shard sees the same streams whether the process serves one workspace or
twenty. A departing workspace gives all of it back.

Two things that figure does *not* account for, both stated rather than
estimated:

- **It was measured on one process wiring core and the dev infrastructure
  provider together.** Providers are now separate deployments, and a workspace
  is engaged by each of them, so a real installation pays that cost once per
  deployment it runs. The multiple is known; the number has not been
  re-measured. See [One APIExport per provider](../design/provider-exports.md).
- **Engagement costs a handful of discovery requests per workspace per
  process**, paid once when the workspace binds.

`task test:sweep` is the instrument, and it needs no container runtime. See
[Workspace resource usage](../design/workspace-resource-usage.md) for the
method and the full results, and read its figures as describing the wiring they
were taken against.

## Not supported yet

- Engaging every workspace bound to the export, instead of one named
  workspace
- Bootstrap and control-plane providers: only the core reconcilers and the
  docker development infrastructure provider are wired
- `clusterctl`, published manifests, or any installation flow other than
  building and running the manager yourself

## Reporting issues

Please file issues and feature requests against this repository rather than
upstream Cluster API, unless the problem also reproduces on upstream Cluster
API outside kcp.
