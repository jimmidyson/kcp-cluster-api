---
title: Usage
description: Day-to-day usage of kcp-cluster-api.
weight: 20
---

{{% pageinfo color="info" %}}
This page will fill in as KCP-aware functionality lands under `kcp/`. Until
then, day-to-day usage matches upstream Cluster API — see the
[Cluster API user documentation](https://cluster-api.sigs.k8s.io/user/quick-start)
for managing clusters, machines, and providers.
{{% /pageinfo %}}

## What to expect

Once workspace-aware components exist, this page will cover:

- Running Cluster API controllers against KCP workspaces instead of a single
  physical cluster
- Any KCP-specific CLI flags, manifests, or `clusterctl` changes needed to
  point at a KCP `APIExport`/virtual workspace
- Multi-workspace patterns and limitations specific to the KCP integration

## Reporting issues

Because everything KCP-specific lives under [`kcp/`](https://github.com/jimmidyson/kcp-cluster-api/tree/main/kcp),
please file issues and feature requests against this repository rather than
upstream Cluster API, unless the problem reproduces with unmodified upstream
code.
