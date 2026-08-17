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
  `APIExportEndpointSlice`, and engages exactly one of them —
  `--workspace-cluster-name`. Running against a second workspace means
  running a second process.
- Cross-references between objects (`spec.infrastructureRef`,
  `spec.bootstrap.configRef`, `spec.controlPlaneRef`) resolve through a
  kcp-aware contract-metadata resolver, because a workspace consuming a type
  via `APIBinding` has no `CustomResourceDefinition` object to read contract
  labels from.

## What a workspace costs

One process serves many workspaces, so the practical question when sizing a
deployment is what each one adds. Measured against a real kcp server, with one
controller watching one type per workspace:

| Per active workspace | Cost |
|---|---|
| Goroutines | 12 |
| Retained heap | ~106 KiB |
| Watch connections to the shard | 0 |
| API requests of any kind | 0 |

Zero is not a rounding error: reads for every workspace come from one shared
wildcard cache, so the shard sees the same three streams whether the process
is serving one workspace or forty. Adding a workspace costs memory and
goroutines in the manager, not connections or request rate on kcp.

One thing to know when workspaces come and go frequently: two goroutines per
departed workspace are retained until the process stops serving workspaces
entirely. That accumulates with churn rather than with the number of
workspaces currently bound.

These figures come from `task test:sweep`, which measures them on your own
machine. See
[Workspace resource usage](../design/workspace-resource-usage.md) for the
method, the full results, and the conditions they hold under.

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
