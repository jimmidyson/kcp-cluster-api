---
title: The demo in a UI
description: Browse the demo's workspaces in Headlamp, and watch the UI change with the workspace.
weight: 6
---

`task demo` proves what it proves in a terminal. The same run can be browsed,
one workspace at a time, in [Headlamp](https://headlamp.dev) — and a UI shows
one thing the tables cannot: **the UI itself changes with the workspace**.

## What the run gives you

Every demo run writes a second kubeconfig beside the server's own:

```
.demo/kcp/workspaces.kubeconfig
```

It has one context per workspace, named after the workspace path, from the top
of the tree down:

```
root
root:capi-demo
root:capi-demo:alice
root:capi-demo:alice:capi-demo-1
root:capi-demo:bob
root:capi-demo:bob:capi-demo-1
alice@root:capi-demo:bob:capi-demo-1
```

The contexts differ only in the `/clusters/<path>` on the end of the server
URL, which is all a workspace is. A tenant's own workspaces are browsed **as
that tenant**, so what the UI shows is that tenant's authorization rather than
an admin's rehearsal of it; the workspaces above them, which belong to nobody,
use the demo's own credential.

The last one is deliberate. It browses **bob's** workspace **as alice**, and
everything in it is refused — a refusal you can click on, which is the only
part of the isolation story a UI can show.

Point Headlamp at the file:

```sh
headlamp-server -kubeconfig .demo/kcp/workspaces.kubeconfig -plugins-dir <plugins>
```

Use `--workspace-kubeconfig` to write it somewhere else.

{{% alert title="It carries credentials" color="warning" %}}
The file contains the demo's admin certificate, and — for the impersonating
contexts — the shard admin's, which can act as anybody. It is written `0600`
beside the kubeconfig it was derived from. It is for a demo on one machine.
{{% /alert %}}

## The plugins

Two, both loaded from Headlamp's plugins directory:

- [**kcp workspaces**](https://github.com/jimmidyson/headlamp-kcp-plugin) —
  navigates the workspace tree, hides what the workspace does not serve, and
  shows a plugin's section only where the bindings behind it exist.
- [**Cluster API**](https://artifacthub.io/packages/headlamp/headlamp-plugins/headlamp_cluster-api)
  — `Cluster`s, `Machine`s, `MachineDeployment`s and control planes. Use a
  build that detects Cluster API by **discovery**; a build that looks for
  `CustomResourceDefinition`s reports "Cluster API Not Detected" in every
  workspace, because a bound API has no CRD in the workspace serving it. See
  [Carried changes](#carried-changes).

## What to look at

**The workspace chooser.** The app bar names the workspace you are in and the
one above it. Clicking it opens the navigator: where you are, what is inside,
and what each of those workspaces serves.

**Moving down the tree.** From `root:capi-demo`, alice and bob are one click
each. From alice's home, `capi-demo-1` is one more — and the navigator says it
lights up **Cluster API** before you go there, because the workspace's
`APIBinding`s say so.

**The sidebar changing.** In `capi-demo-1` there is a Cluster API section with
the demo's cluster in it. One workspace up there is not. Nothing about that is
configured per workspace: the section is bound to the `cluster.x-k8s.io` API
group, and the group is there or it is not.

**What is missing everywhere.** No Pods, no Deployments, no Nodes. A kcp
workspace serves the APIs bound into it, and none of them are among those.
The sidebar shows what the workspace answers for and nothing else.

**What each workspace binds.** The navigator's last section lists the
workspace's `APIBinding`s and the API groups each one serves — the join
between "a tenant enabled a provider" and "this section appeared".

## Carried changes

The Cluster API plugin upstream detects Cluster API by reading the
`CustomResourceDefinition` for `clusters.cluster.x-k8s.io`, and derives the
API version to request from it. Neither works in a workspace: the types arrive
through an `APIBinding` and no CRD for them exists there. The changes that fix
it — discovery for both, and the server's own table conversion for the printed
columns — are carried in a fork until they land upstream. They are not changes
to Cluster API and so are not `DRIFT.md` entries; they are recorded with the
plugin fork.

## Without the demo

Nothing here is demo-specific. Any kcp shard works: one kubeconfig context per
workspace, named after the path, and the plugins do the rest.
