---
title: The demo in a UI
description: Browse the demo's workspaces in Headlamp, and watch the UI change with the workspace.
weight: 8
---

`task demo` proves what it proves in a terminal. The same run can be browsed,
one workspace at a time, in [Headlamp](https://headlamp.dev) — and a UI shows
one thing the tables cannot: **the UI itself changes with the workspace**.

## What the run gives you

Every demo run writes a kubeconfig per audience beside the server's own:

```
.demo/kcp/workspaces.kubeconfig   every workspace, as the admin
.demo/kcp/alice.kubeconfig        what alice can reach, as alice
.demo/kcp/bob.kubeconfig          what bob can reach, as bob
```

Each context is named after the workspace path, and the contexts differ only
in the `/clusters/<path>` on the end of the server URL, which is all a
workspace is. `--workspace-kubeconfig-dir` writes them somewhere else.

**One file per tenant, on purpose.** A UI shows what it was given: a chooser
listing both tenants' workspaces would make being somebody else a menu item,
when the thing worth demonstrating is that they are separate. Being alice
means loading alice's kubeconfig, and being bob means loading bob's.

A tenant's file holds their home workspace and the workspaces they own, all
browsed **as them**, so what the UI shows is that tenant's authorization
rather than an admin's rehearsal of it. The workspaces above their home are
left out: nothing grants a tenant anything there, so those contexts would only
ever error.

There is deliberately **no route into the other tenant's workspaces** — not
even a context that would be refused. Headlamp cannot enter such a workspace
at all, so it reports the 403 the only way it knows how, by asking for a login
token, and a login box reads as "you are not signed in" rather than "this is
not yours". The isolation is in there being one file each.

Refusals *inside* a workspace you can enter do render properly: alice's own
workspace shows `Forbidden - alice cannot list workspaces` where the child
list would be, because listing children is a right she was not given.

{{% alert title="They carry credentials" color="warning" %}}
The demo's tenants have no credentials of their own — kcp evaluates each
request as the impersonated user, from the shard admin. So a tenant's file
embeds the shard admin's certificate, which can act as anybody, and is *not* a
credential to hand to that tenant. Every file is written `0600`. This is for a
demo on one machine.
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

## Two ways to point Headlamp at it

**Hand the file to the backend.** Nothing to click, and every workspace the
run created is there:

```sh
headlamp-server -kubeconfig .demo/kcp/alice.kubeconfig -plugins-dir <plugins>
```

The credentials stay on the backend, which is also the limit: a plugin cannot
read them, so the workspaces reachable are exactly the contexts in the file.
The navigator lists workspaces it has no context for and says so rather than
offering them.

**Load the file into Headlamp itself**, from the desktop app's *Load
KubeConfig*, or a server run with `-enable-dynamic-clusters` and the file
imported through the UI. Now the kubeconfig is in front of the plugin, and the
tree becomes properly dynamic: the navigator copies the context you are using,
rewrites its server URL, and opens **any** workspace it can list — including
ones created after Headlamp started. One context is enough to reach the whole
subtree under it.

This is the mode that shows what the plugin is actually doing. The demo writes
whole trees because it cannot click *Load KubeConfig* for you, not because the
contexts are needed.

Switching tenant is loading the other tenant's file, in both modes. That is
the point of there being two.

## What to look at

**The workspace chooser.** The app bar names the workspace you are in and the
one above it. Clicking it opens the navigator: where you are, what is inside,
and what each of those workspaces serves.

**Moving down the tree.** With the admin's file, alice and bob are one click
each from `root:capi-demo`; with alice's, her home is where you start. Either
way `capi-demo-1` is one more click, and the navigator says it lights up
**Cluster API** before you go there, because the workspace's `APIBinding`s say
so.

**A refusal, rendered.** In alice's own `capi-demo-1`, the navigator says
`Forbidden - alice cannot list workspaces` where the child list would be. She
owns the workspace; listing what is inside it is a right she was not given.

**Being the other tenant.** Stop Headlamp, point it at `bob.kubeconfig`, and
the same UI shows Bob's workspace and none of Alice's. Nothing about the shard
changed — only who is asking.

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
