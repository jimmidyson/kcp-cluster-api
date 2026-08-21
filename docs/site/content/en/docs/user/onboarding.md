---
title: Onboarding a workspace
description: Creating a Cluster API workspace, enabling the providers you want, and who is allowed to do either.
weight: 15
---

A workspace does not serve Cluster API because somebody applied a pile of
manifests into it. It serves Cluster API because it was created with the
`cluster-api` `WorkspaceType`, and it serves *your* provider because you
enabled it.

This page is the whole of that: two `kubectl` commands, and the permissions
behind them.

## Create the workspace

```yaml
apiVersion: tenancy.kcp.io/v1alpha1
kind: Workspace
metadata:
  name: my-clusters
spec:
  type:
    name: cluster-api
    path: root          # the workspace the Cluster API APIExports live in
```

```console
$ kubectl create -f workspace.yaml
workspace.tenancy.kcp.io/my-clusters created
$ kubectl get workspace my-clusters
NAME          TYPE          PHASE   URL
my-clusters   cluster-api   Ready   https://…/clusters/1y1mcokytk2rie2i
```

`Ready` is worth pausing on. A workspace of this type is **held out of Ready**
until it has everything a tenant needs, so a workspace you can enter is one
that is already set up. By the time you see `Ready` it has:

- an `APIBinding` to Cluster API's core `APIExport`, so `Cluster`,
  `ClusterClass`, `Machine`, `MachineSet` and `MachineDeployment` are served in
  it; and
- two `ClusterRole`s — `cluster-api-admin` and `cluster-api-view` — saying what
  may be done with them. `cluster-api-admin` reads every Cluster API type the
  workspace serves and writes exactly one of them, the `Cluster`: everything
  under it is created for you by the topology controller from the
  `ClusterClass` your `Cluster` names.

```console
$ kubectl get apibindings
NAME                       PHASE   AGE
cluster-api-core-aav7o     Bound   30s
cluster-api-workspace-2ya  Bound   30s
$ kubectl get clusterroles cluster-api-admin cluster-api-view
NAME                CREATED AT
cluster-api-admin   2026-08-21T06:44:55Z
cluster-api-view    2026-08-21T06:44:55Z
```

Nobody holds those roles yet. Binding them is the workspace owner's decision:

```console
$ kubectl create clusterrolebinding my-team-admin \
    --clusterrole cluster-api-admin --user alice
```

## Enable the provider you want

Cluster API is not one thing. A cluster needs an *infrastructure* provider, and
usually a *bootstrap* and a *control plane* provider, and which ones is your
choice — so the `WorkspaceType` does not make it for you. Enabling one is
creating an `APIBinding`:

```yaml
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: cluster-api-dev-infrastructure
spec:
  reference:
    export:
      path: root
      name: cluster-api-dev-infrastructure
```

```console
$ kubectl create -f provider.yaml
apibinding.apis.kcp.io/cluster-api-dev-infrastructure created
$ kubectl get apibinding cluster-api-dev-infrastructure
NAME                             PHASE   AGE
cluster-api-dev-infrastructure   Bound   4s
```

`DevCluster`, `DevMachine` and their templates are now served in the workspace,
and `cluster-api-admin` covers them — check it:

```console
$ kubectl get clusterrole cluster-api-admin -o jsonpath='{.rules[0].apiGroups}'
["cluster.x-k8s.io","infrastructure.cluster.x-k8s.io"]
```

That first rule is read-only. You will not get write on `DevCluster`, and you
do not need it: a cluster here is built from a `ClusterClass`, so you write the
`Cluster` and the topology controller creates the rest.

Nobody edited that role. It is derived from the `APIBinding`s the workspace
holds, and a controller rewrote it when yours appeared. Enable another provider
and it widens again; the same is true of a provider this project has never
heard of, as long as it serves its types in a `*.cluster.x-k8s.io` group, which
the Cluster API contract requires.

You also do not have to widen anything for Cluster API's *own* controllers.
Core's controllers reach a provider's objects through a kcp permission claim,
and that claim list is maintained for you — see
[Enabling a provider](#what-enabling-a-provider-actually-costs-you) below.

## Who may do either

Two permissions, checked in two different places, and the difference is the
usual source of confusion.

| To… | You need | In |
| --- | --- | --- |
| create a workspace of this type | `use` on `workspacetypes/cluster-api` | the workspace holding the type |
| enable a provider | `bind` on that provider's `apiexports` | the workspace holding the **export** |
| use the types once enabled | `cluster-api-admin`, or your own role | your own workspace |

The middle row is the one that surprises people: kcp authorises creating an
`APIBinding` as the verb `bind` on the `APIExport` being bound, evaluated where
that export lives — not where you are creating the binding. An operator grants
it once, naming the providers a tenant may turn on:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-api-provider-binder
rules:
- apiGroups: ["apis.kcp.io"]
  resources: ["apiexports"]
  resourceNames: ["cluster-api-dev-infrastructure"]
  verbs: ["bind"]
```

Applied in the workspace holding the exports, and bound there to whoever may
enable that provider. Naming the exports matters: `bind` on every `APIExport`
in that workspace is broader than "the Cluster API providers", because that
workspace is where every export in the installation lives.

## What enabling a provider actually costs you

Nothing, and the reason is worth knowing because it is the part that would
otherwise be manual.

Cluster API's core controllers write to objects they cannot name in advance: a
`Cluster` points at an infrastructure cluster, a `Machine` at a bootstrap
config, and the topology controller *creates* both from a `ClusterClass`. In
kcp those types belong to another `APIExport`, so core reaches them only
through a **permission claim** carrying that export's identity — a value the
server assigns, which cannot be written into a manifest.

So two things happen without you:

- **The claim appears.** A controller watches for `APIExport`s labelled
  `cluster.x-k8s.io/provider-contract` and adds every resource they publish to
  core's claim list. Installing a provider is publishing a labelled export;
  nothing else. The demo runs that case with a provider it does not reconcile
  at all — see [The Nutanix provider](demo.md#the-nutanix-provider).
- **Your workspace accepts it.** Because your core `APIBinding` is managed by
  the `WorkspaceType`, kcp rebuilds its accepted-claim list from the export's
  whenever that changes. A provider installed next month becomes reachable in
  a workspace created today, with nobody accepting anything.

Two consequences of that, worth knowing before they surprise you:

- **You cannot refuse one claim and keep the rest** through the managed
  binding. It is all of core's claims or none, and "none" means leaving the
  managed binding behind.
- **A hand-edit to the managed binding's `spec.permissionClaims` is reverted**
  on the next reconcile. kcp replaces the list rather than merging it.

## Opting out

Create the workspace with kcp's `universal` type instead, and write the
`APIBinding`s and roles yourself. Nothing recreates a binding you delete, and
nothing rewrites your claims — which is what you want if you are winding a
workspace down, and what you have to live with if you are not: a provider
installed later will not become usable in that workspace until you say so.

## See also

- [The demo](demo.md) — runs all of this and prints what happened.
- [The walkthrough](walkthrough.md) — the same run taken apart command by
  command, including this page's two steps against a live server.
- [Workspace onboarding](../design/workspace-onboarding.md) — why it is shaped
  this way, and what it costs per workspace.
