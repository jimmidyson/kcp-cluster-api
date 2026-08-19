---
title: One APIExport per Provider
description: Why each Cluster API provider publishes its own types, and how the claims between them are kept current.
weight: 24
---

Cluster API is several providers, deployed as one process each. This project
publishes them as one `APIExport` each too, and the claims between those
exports are what let their controllers reach each other.

| Export | Deployment | Publishes |
|---|---|---|
| `cluster-api-core` | `core-manager` | `cluster.x-k8s.io` — Cluster, Machine, MachineSet, MachineDeployment, MachineHealthCheck |
| `cluster-api-bootstrap-kubeadm` | `kubeadm-bootstrap-manager` | `bootstrap.cluster.x-k8s.io` — KubeadmConfig and its template |
| `cluster-api-controlplane-kubeadm` | `kubeadm-control-plane-manager` | `controlplane.cluster.x-k8s.io` — KubeadmControlPlane and its template |
| `cluster-api-dev-infrastructure` | `dev-infrastructure-manager` | `infrastructure.cluster.x-k8s.io` — DevCluster, DevMachine |

The topology is `internal/capiexports`, and it is data rather than manifests:
the claims cannot be written down ahead of time, for the reason below.

## Why not one export for everything

One export is simpler for a tenant — one `APIBinding` — and wrong in three
ways that get worse as providers are added.

**It publishes the test provider into every installation.** The dev
infrastructure provider is upstream's *test* infrastructure: it exists for
development and e2e. An installation that will never run it should not be
offered its types, and with one export there is no way not to.

**It gives every process everything.** A deployment consumes an export, so one
export means the bootstrap provider's process can read and write the
infrastructure provider's objects, and vice versa. Splitting them makes what
each provider may touch a declared, reviewable list.

**It ties provider lifecycles together.** Adding a provider, or a version of
one, becomes a change to the export everything else consumes.

## The claim graph runs both ways

Splitting the types does not decouple the controllers, because Cluster API's
controllers genuinely reference each other. Core resolves
`spec.infrastructureRef`, `spec.bootstrap.configRef` and
`spec.controlPlaneRef`, takes ownership of what it finds and reads its status;
the bootstrap and infrastructure providers watch `Cluster` and `Machine`. So
each export claims what its own controllers touch:

```
core        ──claims kubeadmconfigs, kubeadmcontrolplanes──► bootstrap, controlplane
core        ──claims devclusters, devmachines─────────────► dev-infrastructure
bootstrap   ──claims clusters, machines───────────────────► core
bootstrap   ──claims kubeadmcontrolplanes─────────────────► controlplane
controlplane──claims machines (writes them) ──────────────► core
controlplane──claims kubeadmconfigs (writes them)─────────► bootstrap
controlplane──claims devmachines, devmachinetemplates─────► dev-infrastructure
dev-infra   ──claims clusters, machines───────────────────► core

every provider ──claims secrets; core, bootstrap and controlplane, configmaps
```

The control plane provider is the one that makes the graph's shape matter. It
does not only watch other providers' types, it **authors** them: a
KubeadmControlPlane creates the Machines its control plane is made of, the
KubeadmConfigs that bootstrap them, and a DevMachine per Machine from the
infrastructure template. Three of its claims are writes.

Each of those was found by running the thing, not by reading the code: a
missing claim does not fail at startup, it fails at the first reconcile that
needs it, with a message from the provider that needs it rather than from the
claim. `no matches for kind "DevMachine"` from the control plane provider is
what a missing `devmachines` claim looks like.

Those claims are exactly what `test/integration/claims` establishes is
possible: a claimed type from another export is readable, writable — core
stamps owner references on infrastructure objects — and **watchable** on the
`/clusters/*` path a fleet-wide controller uses, with each event carrying its
logical cluster. The same test's control shows the claims are load-bearing: an
unclaimed type from the same export is absent from the claiming export's API
surface entirely.

## Why the claim list is maintained at run time

A claim on another export's resource carries that export's **identity hash**.
kcp assigns it, and it differs per kcp instance — so a claim naming one cannot
be a static manifest. `capiexports.Publish` therefore works in two passes:
publish every export with the claims that need no identity, then resolve the
identities and fill in the rest. It is idempotent, which is what makes it a
reconcile rather than a one-shot: an export already in the requested shape is
left alone.

That two-pass structure is forced rather than chosen. The claims are mutually
referential — core claims the providers' types and they claim core's — so no
ordering of one-pass creates resolves them.

This is [ADR-0001][adr]'s self-maintaining permission-claim list, in the
smallest form that works. What it is not yet: a controller that watches
`APIExport`s and reacts to an identity changing under it. Nothing in kcp
rotates an identity today, and building for it before that is true would be
building against a guess.

## What a tenant workspace binds

One `APIBinding` per export, each accepting that export's claims. The demo
creates them directly; a deployment automates it with a `WorkspaceType`'s
`defaultAPIBindings`, because a tenant is not meant to hand-accept a claim per
provider. That `WorkspaceType` is the conversion plan's P6 and is not built.

## What the split costs, and what has not been measured

Each deployment runs its own manager, its own `multicluster-provider` and its
own wildcard cache. Two consequences follow, and only the shape of them is
known:

- **Per-workspace cost multiplies by the number of deployments.** A workspace
  measured at 2.0 goroutines under one fleet-wide manager costs that in each
  process that engages it. Three deployments is of the order of three times as
  much, plus each process's own engagement discovery.
- **Watches are per export.** `Cluster` is watched by all three, once each
  through its own virtual workspace, where a single export would have watched
  it once.

Neither has been measured since the split. The sweep
(`task test:sweep`) still measures a process wiring core *and* the dev provider
together, which is now a shape no deployment runs — so the figures in
[Workspace resource usage](workspace-resource-usage.md) describe the wiring
they were taken against and not this one. Per-deployment sweeps are the
measurement this page is missing, and until they exist the honest statement is
the one above: the direction is known, the number is not.

[adr]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0001-provider-api-permissions.md
