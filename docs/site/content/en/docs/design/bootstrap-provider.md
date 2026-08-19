---
title: The Bootstrap Provider
description: Wiring the kubeadm bootstrap provider fleet-wide, and the permission claim that makes it possible.
weight: 26
---

The kubeadm bootstrap provider is the conversion plan's P1, and it is the first
provider whose port was not mechanical. This page is why.

## The provider is made of Secrets

Every other reconciler this project wires reads and writes the types the
`APIExport` publishes. The bootstrap provider's *output* is a Secret — the
bootstrap data a machine boots from — and for the first control plane machine
of a cluster with no control plane provider, it also generates the cluster's
certificate authorities, as Secrets. Its control plane init lock is a ConfigMap.
None of those is a type the export publishes.

That mattered because of something this project had already learned and written
down: a virtual workspace serves exactly what its export serves, so a core
`v1.Secret` has no REST mapping there. It is why the `ClusterCache`'s kubeconfig
Secret is read through a separate, shard-scoped client
([`NewWorkspaceSecretReader`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/internal/coremanager/secretreader.go)).
Applied to this provider, the same conclusion would have meant threading a
second client through every Secret call in an upstream reconciler.

It is only true of an export that claims nothing.

## The claim, and what it took to establish

kcp's mechanism is a **permission claim**: an `APIExport` declares a claim on a
resource it does not publish, and a workspace that binds the export accepts it.
Every provider's export in this project claims `secrets`, and core's and the
bootstrap provider's claim `configmaps` too (see `internal/capiexports` and
[One APIExport per provider](provider-exports.md)); `kcpfixtures.BindExport`
accepts them on each binding — which is
[ADR-0001][adr]'s decision 3 in the form this project can currently express it;
a deployment automates acceptance through a `WorkspaceType`'s `Maintain`
lifecycle instead.

With the claims in place, one cluster-aware client covers the whole reconciler:
reads and writes of claimed resources go through the virtual workspace exactly
as everything else does, and land in the workspace being reconciled.

Two tests establish that, and the difference between them is the point:

- `TestClaimedSecretsAreServedThroughTheVirtualWorkspace` asks whether **kcp**
  serves claimed Secrets and ConfigMaps through the export's virtual workspace.
- `TestFleetClientWritesClaimedResources` asks whether **this deployment's own
  client** — the one every reconciler holds, resolving the workspace from the
  context — can write them.

The second exists because the first passed while the provider did not work. A
binding that has not accepted a claim still *reads* like a working setup: the
export lists the claim, the binding reports `Ready`, and a `Get` for a claimed
object returns `NotFound` rather than `Forbidden`. Only a write is refused, and
only at the point a reconciler tries one:

```
configmaps is forbidden: User "kcp-admin" cannot create resource "configmaps"
in API group "" in the namespace "default": access denied
```

kcp's own log names the authorizer that said no —
`failed to find suitable reason to allow access in APIBinding` — and that is the
sentence to look for. The fault was a binding created without the claims it
should have accepted, in code that was passing them everywhere else.

## What claims can carry, and what that means for splitting the exports

Claims are not only for core Kubernetes types. `test/integration/claims`
also establishes what a **per-provider APIExport split** would rest on: with
two exports, one publishing `cluster.x-k8s.io` and one publishing
`infrastructure.cluster.x-k8s.io`, core's virtual workspace can

- **read** a `DevCluster` published by the other export,
- **write** to it — the owner reference the Cluster reconciler stamps on an
  infrastructure object, landing in the workspace under the infrastructure
  provider's own API, and
- **watch** the type across workspaces, on the `/clusters/*` path a fleet-wide
  controller uses, with each event carrying the logical cluster it came from.

The watch matters as much as the other two. Core's watch on an infrastructure
object is not static: `external.ObjectTracker` adds it the first time a Cluster
references one. A claim serving `Get` and `Update` but not `Watch` would leave
every cross-export reconcile firing once and never again.

The same test carries its own control. `DevMachine` is published by that same
export and bound in the same workspace, and core does not claim it: it is not in
core's API surface at all (`no matches for kind "DevMachine"`). So the claims are
what is doing the work, rather than a virtual workspace that would have served
everything anyway.

One constraint shapes how such a split gets built: a claim on another export's
resource must carry that export's **identity hash**, which the server assigns
and which is per kcp instance. It cannot be written into a manifest ahead of
time, which is what makes [ADR-0001][adr]'s self-maintaining claim-list
controller a prerequisite for the split rather than a later convenience.

## The init lock is the one thing that changed

`SetupWithMulticlusterManager` on the KubeadmConfig reconciler is the same For,
the same watches in the same order, the same map functions and predicates as
`SetupWithManager`, on the builder that keys the queue on a request carrying the
cluster. One thing is not a straight copy: `KubeadmInitLock` defaults to a mutex
over the reconciler's own cluster-aware client rather than over the manager's.

The lock is a ConfigMap in the cluster being reconciled, and it exists so that
exactly one control plane machine runs `kubeadm init`. The manager's client
addresses no cluster in particular, so defaulting it the single-cluster way
would either serialize control plane initialization across every workspace in
the fleet through one object, or fail to find it at all.

## What this costs, and what is not built

**Secret caching.** Upstream gives this reconciler a `SecretCachingClient` whose
cache is filtered to the Secrets it owns. Here both fields are the same
cluster-aware client, so a Secret read starts a wildcard informer over every
workspace's Secrets. That is a real cost and a real exposure, stated rather than
absorbed; sizing it needs a measurement (`task test:sweep`) that has not been
taken.

**The claim is coarse.** `secrets` and `configmaps` are claimed with `all: true`.
kcp supports narrower selectors, and a deployment holding tenant credentials
should want them. What is established here is that the mechanism serves the
resource at all.

**Machines still do not reach Running.** The provider produces bootstrap data;
turning that into a node needs the control plane provider (P2) or an
infrastructure provider that can act on it. `task demo --control-plane-machines
1` shows the state this reaches: bootstrap data ready, machine provisioning.

[adr]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0001-provider-api-permissions.md
