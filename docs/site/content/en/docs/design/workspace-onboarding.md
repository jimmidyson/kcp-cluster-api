---
title: Workspace onboarding
description: The WorkspaceType a tenant onboards with, the roles it writes, and the two controllers that keep a workspace's permissions right as providers come and go.
weight: 29
---

Three questions have to be answered before a workspace can run clusters, and
none of them is "is the code wired up":

1. **What is served here?** An `APIBinding` to Cluster API's core `APIExport`.
2. **Who may use it?** RBAC inside the workspace — which nothing in kcp writes
   for you.
3. **What may Cluster API's controllers reach?** A permission claim per
   provider resource, carrying an identity hash the server assigns.

Before this feature, all three were the demo's job, done by hand, per
workspace. This page is how they are answered by the system instead.

The user-facing half is [Onboarding a workspace](../user/onboarding.md); the
decision this implements is
[ADR-0001](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0001-provider-api-permissions.md),
whose open questions 3 and 6 it closes.

## The shape

```
                     provider workspace (root)
  ┌───────────────────────────────────────────────────────────────┐
  │  APIExport cluster-api-core          ← claims maintained      │
  │  APIExport cluster-api-<provider>    ← labelled by contract   │
  │  APIExport cluster-api-workspace     ← claims-only            │
  │  WorkspaceType cluster-api                                    │
  └───────────────────────────────────────────────────────────────┘
        │ defaultAPIBindings                 ▲ discovery
        │ + Maintain                         │
        ▼                                    │
  ┌──────────────────────────┐        ┌──────────────────────────┐
  │  tenant workspace        │        │  cmd/workspace-manager   │
  │  ├─ APIBinding: core     │◀───────│  claims controller       │
  │  ├─ APIBinding: provider │  roles │  initializer             │
  │  └─ ClusterRoles         │◀───────│  role maintainer         │
  └──────────────────────────┘        └──────────────────────────┘
```

## The WorkspaceType

`internal/capiworkspaces` builds a `WorkspaceType` named `cluster-api` with
three properties, each replacing a manual step:

- **`defaultAPIBindings`** binds the core export, and the onboarding export
  below, into every workspace of the type.
- **`defaultAPIBindingLifecycle: Maintain`** makes kcp keep those bindings'
  accepted claim lists in step with the exports' declared ones, on every
  export update. This is the half of ADR-0001's decision 3 that needs no code
  here at all.
- **`initializer: true`** holds the workspace out of `Ready` until this
  project's initializer has written its roles. That ordering is the only
  reason to use an initializer rather than to write the roles once the
  workspace is up: a tenant is never handed a workspace that serves Cluster
  API and grants nobody the use of it.

`initializerPermissions` is set rather than left empty. Empty means kcp's
initializing virtual workspace impersonates the workspace owner —
cluster-admin — for every workspace in the installation, which is far more
than writing two `ClusterRole`s needs. The rules include `get` on the
discovery non-resource URLs, without which every request fails with
`no rule allows get on /api` before RBAC on any object is consulted.

### The bindings are ordered, and it is load-bearing

`DefaultExports` puts the onboarding export **before** core. kcp labels an
object with the permission claims its workspace had accepted at the moment the
object was written, and it is the onboarding binding that accepts the claim on
`apibindings`. Bind it second and the core binding written moments earlier
carries no claim label, so the controller that maintains the workspace's roles
cannot see the very binding that tells it Cluster API is here.

## The roles, and why they are derived

`capiworkspaces.Roles` is a pure function from the workspace's `APIBinding`s to
two `ClusterRole`s:

- `cluster-api-admin` — **read** across every Cluster API group the workspace
  serves, **write on `clusters` alone**, read-only on `Secret`s (a cluster's
  admin kubeconfig is one, and a tenant who could write them could forge a
  certificate authority), and full use of `apibindings`, which is what lets a
  tenant enable a provider for themselves.
- `cluster-api-view` — the same read set, minus `Secret`s and minus the ability
  to enable anything.

One writable Cluster API type, because a cluster here is a ClusterClass based
cluster: the `Cluster` names a class and a shape, and the infrastructure
cluster, the control plane, the worker `MachineDeployment` and the templates
each is stamped from are created by the topology controller under the
*manager's* identity. Scaling and version changes are fields of
`spec.topology`, so write on `clusters` already carries them. See
`capiworkspaces.WritableResource`, and
[the demo's design notes](demo.md) for the same decision where it was first
made.

Read is a wildcard over the discovered groups rather than a list of resources,
so that a provider publishing a new type does not silently fall outside what an
owner may watch. Write is one rule naming one resource, because that is the
whole of what a tenant writes and it should be readable as such.

The rule that moves is the API group list, and it cannot be written down:
which providers a workspace uses is decided after the workspace exists, and by
somebody else. So it is read off `status.boundResources` — what the workspace
actually *serves*, not what it has asked for — filtered to `cluster.x-k8s.io`
and anything ending in `.cluster.x-k8s.io`.

That suffix is a shortcut and it is stated rather than hidden: it is what the
Cluster API contract's group naming gives, and what `clusterctl` relies on to
find providers. A provider publishing its types in some other group would be
bound, served, and left out of these roles — visibly, at the first `kubectl
get`, and fixed by a role of the tenant's own.

Two callers run that function, and they differ only in when: the initializer,
once, before the workspace is `Ready`; and the fleet-wide maintainer, on every
`APIBinding` event thereafter. Sharing the function is what stops the roles a
workspace is created with drifting from the roles it is kept at.

## The onboarding export

The maintainer needs two things inside a tenant workspace: to see every
`APIBinding` it holds, and to write `ClusterRole`s. Both are permission claims,
and both are on types kcp serves everywhere, so neither carries an identity
hash. `apibindings` is claimable because kcp exempts its own `apis.kcp.io`
group from the identity requirement; without that claim an `APIExport`'s
virtual workspace serves back only the one `APIBinding` that binds it, and the
controller would watch itself.

They are a **separate export** — `cluster-api-workspace`, publishing no types —
rather than more claims on core's. Core already reaches every `Secret` in every
workspace; letting it write `ClusterRole`s there as well would let the provider
that holds every tenant's kubeconfig also grant itself anything else. The cost
is one more export, one more binding per workspace and one more manager; the
alternative is a privilege that has no business being where it would be.

## The claim controller

`internal/workspacemanager` watches the `APIExport`s in the provider workspace
and rewrites each maintained export's `spec.permissionClaims`. This is
ADR-0001's "self-maintaining claim list", and it settles that ADR's open
question 3: **a provider is recognised by the label
`cluster.x-k8s.io/provider-contract`**, whose value is one of `core`,
`bootstrap`, `control-plane` or `infrastructure`.

The split between what is declared and what is discovered follows from who can
know it:

- **Declared** — what a provider does with *core's own* types. A provider is
  written against `Cluster` and `Machine` by name, so naming them is stating
  what the code does. These live in `Provider.ProviderClaims`.
- **Discovered** — what core (and the control plane provider) do with
  *whatever answers a reference*. `spec.infrastructureRef` is resolved at run
  time against a type this repository may never have heard of, so the resource
  list comes from the export and only the verb set is declared, in
  `Provider.DiscoveredClaims`.

A discovered claim covers **every** resource its export publishes, because
which of them a `Cluster` will reference is not knowable from outside — a
`ClusterClass` can reach an infrastructure provider's cluster, machine and both
templates. The verbs stay as narrow as the upstream `+kubebuilder:rbac` markers
justify; it is the resource list that widens, not the permission. Two claims
that meet on one resource are merged for the union of their verbs, because kcp
scopes a claim by resource and verb: two claims on one resource are one
permission, and the narrower would silently be the one that lost.

The result reproduces the hand-written list this repository used to carry,
which is asserted without a server in `capiexports_test.go`. It widens it in
two places, both stated: the control plane provider now claims the
infrastructure provider's *cluster* types as well as its machine types, and
the bootstrap provider claims the control plane provider's templates as well as
its control planes.

## Two things kcp does that a reader will otherwise rediscover

### An impersonated user is scoped to one workspace

kcp scopes an impersonated user to the logical cluster the request addresses
unless the impersonator is in `system:masters`
(`pkg/server/filters/impersonation.go`). A scoped user is refused everywhere
else whatever RBAC says — the authorizer reports `NoOpinion` for the local
policy of any other workspace.

That makes an impersonated tenant *less* able than the real tenant they stand
in for, and it breaks exactly the step worth demonstrating: enabling a provider
is authorised as `bind` on the `APIExport`, in the workspace the export lives
in rather than the tenant's own. Impersonated from an ordinary admin, a tenant
is refused with `no permission to bind to export …`, and no RBAC anywhere fixes
it. The demo therefore impersonates from the shard admin — see
`demo.Options.ImpersonationConfig`.

None of this applies to a real deployment, where a tenant authenticates as
themselves and is not scoped at all.

### A claim on an unbound provider races the workspace becoming Ready

Core's export claims the types of every installed provider, including ones a
given workspace has not enabled. kcp is built for that: its
`permissionClaimMaterialiserReconciler` creates a bound CRD for a claimed
resource whose producing export nobody in the workspace has bound, so the claim
can be applied anyway.

The two halves race. kcp's `permissionclaimlabel` controller starts trying
immediately, fails with `unable to find informer for <group>.<resource>` while
the bound CRD does not exist, and exhausts its workqueue retries in about
thirteen seconds. Nothing re-enqueues the binding afterwards, so
`PermissionClaimsApplied` stays `False` — and kcp's `system:apibindings`
initializer waits on exactly that condition before letting the workspace become
`Ready`.

**Measured on kcp v0.32.3**, running `task demo`: the workspace is `Ready` in
about ten seconds when the materialiser wins, and had not become `Ready` two
minutes later when it did not — on roughly half of the runs.

`capiworkspaces.NudgeUnappliedClaims` closes it: while kcp's own initializer is
still pending, the workspace initializer patches an annotation onto any
`APIBinding` whose claims are not applied, which re-enqueues it with a fresh
retry budget. It touches nothing kcp owns — `Maintain` rewrites `spec`, never
annotations — so the two do not fight. **It is a workaround for a kcp defect
and is meant to be deleted rather than maintained**; what retires it is kcp
re-enqueueing the `APIBinding` when the bound CRD it was waiting for appears.

## Per-workspace cost

Measured, at twenty workspaces, by `test/integration/sweep`'s workspace shape —
`bin/sweep-report-workspace.md` after a `task test:sweep`:

| Per active workspace | `workspace-manager` | The four providers, added up |
|---|--:|--:|
| Goroutines | **7** | 8 |
| Watch streams | **0** | 0 |
| Discovery requests | **3** | 17 |
| Reconcile requests | **5** | 103 |
| Retained after departure | **1** | 0 |

An installation of all five therefore pays 15 goroutines and 20 discovery
requests per active workspace, and holds 32 streams on the shard at rest. The
totals in [Workspace resource usage](workspace-resource-usage.md) add up to an
installation's again.

Two numbers are worth reading rather than quoting. **Five reconcile requests**
is the smallest of any deployment, and it should be: what this one writes is
two `ClusterRole`s that stop changing once a workspace's providers stop
changing. **Seven goroutines** is the largest, and that is the wiring rather
than the work — the role maintainer is built with `mcbuilder`, which engages a
controller per cluster, where the providers put their watches on the shard's
cache once through `capicontrollerutil.WildcardRegistry`. Five of the seven,
and the one goroutine a departure retains, are that difference; moving the
maintainer onto the registry is what would retire them, and it has not been
done.

The initializer is not in the figure and does not belong there: its fleet is
only the workspaces that have not finished initializing, so it holds nothing
per steady-state workspace. Neither is the permission-claim controller, which
watches the one workspace the exports live in however many tenants there are.

**A workspace leaves this deployment by being deleted, not by unbinding.** The
onboarding `APIBinding` is written by the `WorkspaceType` with
`defaultAPIBindingLifecycle: Maintain`, so kcp recreates one a tenant deletes.
That is also the only departure this deployment *can* observe, and the reason
is a kcp property worth knowing before designing against it: the apiexport
virtual workspace skips the `APIBinding` check for wildcard requests, so a
fleet-wide watch is filtered by the permission-claim label alone — and nothing
ever removes that label, because `permissionclaimlabel`'s reconcile recomputes
labels from the binding and returns early once the binding is gone. **No
claimed object leaves a wildcard view when its binding is deleted** (kcp
v0.32.3, observed). Deleting the workspace does delete its `LogicalCluster`,
which is a real delete rather than a label going stale — which is why that is
the object this deployment discovers workspaces by. See
`providerwiring.WithLogicalClusterDiscovery`.

## Where the code is

| Package | What it holds |
| --- | --- |
| `internal/capiworkspaces` | the `WorkspaceType`, the roles, the initializer and the role maintainer |
| `internal/workspacemanager` | the three managers, and the permission-claim controller |
| `internal/capiexports` | the contract label, provider discovery, and the claim computation |
| `cmd/workspace-manager` | the deployment |
| `test/integration/onboarding` | the promises above, asserted against a real kcp |
