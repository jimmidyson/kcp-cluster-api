---
title: The Demo
description: What the one-command demo demonstrates, how it makes two tenants real, what it deliberately leaves out, and the two environment facts it had to learn.
weight: 28
---

`task demo` builds Cluster API clusters across several kcp workspaces from one
manager and waits for them to be ready. This page is why it exists in the shape
it does. For how to run it, see [Demo](../user/demo.md).

## Why a demo is a deliverable

Everything this project does end to end was, until this existed, only
observable by reading an integration test. That is a real gap rather than a
presentational one: a system whose behaviour can only be seen through `go test`
cannot be shown to anyone deciding whether to use it, and cannot be poked at by
whoever is about to change it.

So the demo is not a script that reimplements the manager. `internal/demo`
calls `coremanager.SetupFleetControllers` — the same function
`cmd/core-manager` calls, with the same options — because a demo of a
reimplementation demonstrates the reimplementation. The same holds for each of
the other three providers' setup functions.

## What it asserts, and where

The demo prints a table; the assertions live in `test/integration/demo`, which
`task verify` runs. Both drive `internal/demo.Run`, so a demo that breaks fails
CI rather than being discovered at the next presentation.

The test asserts four things:

1. **Every workspace's cluster is ready** — the `Cluster`'s `Available`
   condition, every control plane replica it was asked for, and every `Machine`
   Ready — driven by one manager that was told about no workspace. Each is
   engaged because its `APIBinding` became ready.
2. **No workspace's objects are another's.** Each workspace sees exactly one
   `Cluster`, the `Cluster`s are distinct objects, and each `DevCluster` is
   owned by the `Cluster` in its own workspace.
3. **No tenant can read another tenant's workspaces.** Each user lists their
   own home and their own `Cluster`s, and is refused every other user's home,
   every other user's `Cluster`s, and the org workspace that holds all the
   homes.

4. **Onboarding took no manual step.** Each workspace was created with the
   `cluster-api` `WorkspaceType` and came up bound to core with its roles
   written; each tenant enabled their own providers, as themselves; and each
   workspace's role ended up covering what its tenant enabled, with nothing
   editing it. `test/integration/onboarding` asserts the same properties
   against a bare installation, including the refusal a tenant gets when
   nobody granted them the right to enable a provider.

The second and third are the conversion plan's P8, and they are the ones the
rest of the suite never made:
[`TestEveryBoundWorkspaceIsWired`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/test/integration/providerwiring/providerwiring_test.go)
exercises many workspaces without reconciling any of them to completion, and
`TestCoreManagerClusterToMachine` reconciles one workspace's cluster without
there being a second. Neither could have caught a reconciler writing one
workspace's status into another.

Identical object names in every workspace are load-bearing for that assertion.
A leak between two workspaces each holding a `demo-00` cannot hide behind names
that happen not to collide. The workspaces are named identically too — Alice's
`capi-demo-1` and Bob's — for the same reason.

The third assertion has a failure mode the second does not, and it is asserted
in both directions because of it. "No user read another's workspaces" is
satisfied completely by an RBAC bug that refuses everybody everything, so the
test requires an allowed read as well as a refused one, and a run that produced
no checks at all is not isolated rather than trivially isolated.

## Two tenants, and where the isolation actually comes from

The demo builds this:

```
root                     the four APIExports
└── capi-demo            the org workspace: nobody owns it, nobody may read it
    ├── alice            alice's home: alice may list the workspaces in it
    │   └── capi-demo-1  alice's workspace: her Cluster, her kubeconfig Secret
    └── bob
        └── capi-demo-1
```

Two grants per user, and nothing else: `access` plus workspace reads in their
own home, and read of the Cluster API groups plus write of one type — the
`Cluster` — in the workspaces they own. Everything a tenant cannot do is
something nothing granted them.

**The org workspace is the part that is not decoration.** Two facts about kcp
put it there:

- `root` binds `system:kcp:tenancy:reader` to `system:authenticated` by
  default, so every authenticated user on the shard can list root's direct
  children — including tenants who can enter none of them.
- A `Workspace` list is neither recursive nor filtered by what the caller can
  enter. It returns the workspaces stored in the one workspace addressed, all
  of them or none, since RBAC cannot narrow a list by resource name. There is
  no "the workspaces I have access to" for kcp to answer; what a tenant has is
  the path to their own home.

Homes directly under `root` would therefore have leaked the tenant list: the
demonstration would have been "Alice can see that Bob exists but cannot go in",
which is a weaker and much less interesting claim. One workspace in between,
granting nobody anything, makes the refusal complete: Alice cannot discover Bob
at all.

The access table reports the `root` row rather than asserting it, and
`Result.Isolated()` ignores it. What a shard's parent workspace serves an
authenticated user is that shard's policy; a deployment that has narrowed it
would otherwise be reported as a demo that locked its tenants out.

**Each user gets two bindings per workspace, because two authorizers decide.**
Before RBAC on the resource is consulted at all, kcp's workspace content
authorizer asks whether the user may be in the workspace — the verb `access` on
the **non-resource URL** `/`. Leave that grant out and every request is refused
with `access denied` no matter what the rest of the role says, including,
confusingly, requests for types the workspace plainly serves. It cost a
debugging session to find.

The grant is made by **binding kcp's own `system:kcp:workspace:access`
ClusterRole**, not by writing its rule out:

```yaml
roleRef:
  kind: ClusterRole
  name: system:kcp:workspace:access
```

kcp bootstraps that role into the local admin logical cluster and merges that
cluster's roles when it resolves the check, so a binding in any workspace
resolves it. Reproducing the rule would work today and would be a copy of kcp's
policy, drifting silently the day kcp changes the mechanism — and the shape is
odd enough that a copy invites being "corrected" into an ordinary resource
rule. Binding by name is what kcp's own e2e framework does
(`AdmitWorkspaceAccess`) and what its root workspace does for
`system:authenticated`. A unit test asserts the demo's own roles do **not**
carry the rule.

The second binding is the ordinary one, resolved by ordinary RBAC:
`demo-home-owner` in a home, which the demo defines, and
`cluster-api-admin` in a workspace, which it does not. That role is written by
the Cluster API `WorkspaceType`'s initializer before the workspace becomes
`Ready`, and kept covering whatever the tenant has enabled by a fleet-wide
controller — so the demo decides who holds it and nothing else. See
[Workspace onboarding](workspace-onboarding.md).

## One writable type, because the cluster is a topology

`cluster-api-admin` grants `get`/`list`/`watch` across every Cluster API group
the workspace serves, and `create`/`update`/`patch`/`delete` on `clusters`
alone.

The group list is derived rather than fixed at four — it follows the
`APIBinding`s the workspace holds, so a tenant who enables another provider
gets it — but the read/write split below is a decision, and it does not move
with the list.

That is a consequence of clusters being ClusterClass based. A tenant's `Cluster`
names a class and a shape; the `DevCluster`, the `KubeadmControlPlane`, the
worker `MachineDeployment` and the templates each is stamped from are created by
the topology controller under the **manager's** identity, never the tenant's.
Scaling and version changes do not reopen the question either — both are fields
of `spec.topology`, which write on `clusters` already carries. A tenant who
could write the objects underneath would be holding the grant the hand-built
model needed, against a demo that no longer builds clusters that way.

The blueprint is the other half. The `ClusterClass` and its five templates are
seeded into each workspace by whoever runs the demo, the way a platform operator
seeds a tenant's, and are read-only once there: writing a class decides what a
cluster in this installation is made of, which is not the tenant's answer to
give. Because they cannot create one either, the only class a tenant can point
`spec.topology.classRef` at is the one they were given — RBAC cannot narrow a
write by field, but here it does not need to.

Two things are deliberately absent. Write on `clusterclasses`, for the reason
above. And `delete` on `machines` — a real operation and a real temptation, but
it is remediation, which is the platform's job here, and a `Machine` deleted
underneath the topology controller is a change it did not make.

The role reads each group by wildcard rather than naming each type, so that an
export publishing a new type does not silently fall outside what an owner may
watch. The write rule names one resource, because that is the whole claim and
it should be readable as such.

Unit tests in `internal/capiworkspaces` hold both halves: every type the four
exports publish is readable, and none but `clusters` is writable. They live
with the role rather than with the demo, because the role is written by the
`WorkspaceType`'s initializer now and the demo only decides who holds it.

## Why impersonation rather than credentials

The demo authenticates as the kcp admin and sets `Impersonate-User` — which is
what `kubectl --as` sends — rather than minting a token per user.

kcp evaluates its whole authorization stack against the impersonated user, so
the answer is that user's authorization and not an approximation of it: the
`forbidden` rows in the access table are the same denials a tenant holding
their own credentials would get.

**With one condition that is not optional.** kcp scopes an impersonated user to
the logical cluster the request addresses unless the impersonator is in
`system:masters` (`pkg/server/filters/impersonation.go`), and a scoped user is
refused everywhere else whatever RBAC says — the authorizer returns
`NoOpinion` for any other workspace's local policy. That makes an impersonated
tenant *less* able than the real one, which is the opposite of what
impersonation is for here, and it breaks the step the demo exists to show:
enabling a provider is authorized as `bind` on the `APIExport`, in the
workspace the export lives in rather than the tenant's own. Impersonated from
the ordinary admin, a tenant is refused with `no permission to bind to export
…` and no RBAC anywhere fixes it.

So the demo impersonates from the **shard admin** —
`Options.ImpersonationConfig`, the `shard-base` context of a kcp kubeconfig —
and keeps it separate from the credential it does its own work with, so that
the privilege is used for exactly one thing. What impersonation avoids is a second
mechanism that would only work half the time. Tokens would mean a
`--token-auth-file` written before the server starts, which the demo can only do
for a kcp server it started itself — and `--kcp-kubeconfig` points it at one it
did not. One mechanism that works in both places is worth more here than
credentials that work in one.

The honest limit is that this demonstrates **authorization**, not
authentication. There is no identity provider, no accounts and no login; a
deployment's users arrive through OIDC or a workspace authentication
configuration, and nothing about that is exercised here.

## Why the clusters are ClusterClass based

A demo cluster is a `Cluster` that names a `ClusterClass`. Everything under it
— the `DevCluster`, the `KubeadmControlPlane`, the worker `MachineDeployment`
and the per-cluster templates each is stamped from — is created by the topology
controller from a class the demo puts in each workspace.

It used to be six objects written out by hand, and the change is not
cosmetic. A workspace is a tenant, and what a tenant of a fleet is given is a
class: eight lines to get a cluster, and one place where a version bump or a
fix is made for every cluster built from it. Demonstrating the six-object form
would be demonstrating the thing a class exists to replace.

It is also the harder case to serve, which is the better reason to demonstrate
it. A managed topology adds four reconcilers to the core provider, a
server-side apply of every object under the `Cluster` on every reconcile, and a
cross-object read — `Cluster` to `ClusterClass` to five templates — that has to
resolve inside one workspace and never across two. That last clause is what
this project is about, and the demo now exercises it.

**The names are pinned, and that is for the reader.** A class names what it
creates `{{ .cluster.name }}-{{ .random }}` by default. The demo's class sets
naming templates that produce exactly what it used to create by hand:
`demo-00`, `demo-00-cp`, `demo-00-md`. Nothing in the wiring depends on it — a
class that omits them works as well, and is what a real tenant would write —
but a walkthrough cannot print a name with five random characters in it, and
the status table would have to look objects up by owner rather than by name.

## Why it waits for ready rather than provisioned

It did wait for provisioned, back when a `Machine` reaching Ready needed a
bootstrap provider and a control-plane provider and neither was wired. Both are
wired now (the conversion plan's P1 and P2), so the reason to stop early is
gone — and stopping early was never free. Provisioned infrastructure, an
initialized control plane and a bootstrap data secret are all true of a cluster
whose machines never go Ready, which is the shape every bug in this wiring has
had. Two were sitting there when the done-condition moved, and the demo had
been reporting both as a success:

- The fleet-wide `ClusterCache` never registered the Node-by-`providerID`
  index the Machine reconciler lists through, so every Machine reconcile ended
  in `Index with name field:spec.providerID does not exist` and no Machine ever
  got a `nodeRef`.
- In the fork, a source declared with `WatchesRawSource` on a wildcard-mode
  controller was never started, because in that mode the multicluster builder
  is not what builds the controller. Nothing read the source, so the
  ClusterCache's Cluster-event sends to it blocked until they timed out, and no
  failed connection probe reached the control plane provider that asked to hear
  about one.

So the done-condition is readiness, and the table reports the milestones
alongside it rather than instead of it. A run that does not finish says which
condition it is still waiting on.

## What it leaves out, and why

**Webhooks.** They are served for one workspace or none until the webhook
dispatch layer (G4) lands, so a multi-workspace demo cannot use them. Two
consequences follow, and both are visible in the code: every object it creates
is fully specified, because nothing defaults it, and every published type is
trimmed to its storage version, because a multi-version type needs a conversion
strategy and a conversion strategy needs a webhook server.

## Two things about a managed topology that had to be learned

Both presented as something other than what they were, and both are recorded
because the next person to widen this project's export set will meet them
again.

**A published type is not the same as an enabled feature.** The topology
reconciler reads the cluster's `MachinePool`s on every reconcile of a managed
topology, whatever the `MachinePool` feature gate says — the gate guards the
watch and the reconcilers, not that read. A workspace that does not serve the
type gets

```
error reading current state of the Cluster topology: failed to read MachinePools
for managed topology: no matches for kind "MachinePool" in version
"cluster.x-k8s.io/v1beta2"
```

and its `Cluster` never leaves `Pending` — a message about a feature nobody
asked for, on a cluster that does not use it. So `machinepools` is published,
alongside the `MachineHealthCheck` and `MachineSet` that are published for the
same kind of reason, and the gate stays off because nothing here reconciles
one.

**The last thing a managed topology does needs a permission the rest of core
does not.** The topology reconciler creates a *cluster shim* `Secret` to own
the objects it stamps from a class before the `Cluster` can own them, and
deletes it once the real owner exists. Core's other Secret markers grant
everything except delete, so with a claim built from those the cluster comes up
**completely** — control plane available, both machines Running, workers
available — and then reports

```
TopologyReconciled=False: failed to delete the cluster shim object:
secrets "demo-00-shim" is forbidden
```

forever. A permission failure wearing the costume of a reconcile bug, and only
visible at the very end of an otherwise perfect run. Core's Secret claim now
includes delete, and cites the marker it comes from.

## Two things about the environment that had to be learned

Both cost a debugging session, and both are the kind of thing that is invisible
until it is not.

**Every URL kcp hands out is pinned to `localhost`.** A kcp server advertises
the address it detects for itself, and for a server started on this machine by
this process that address is wrong twice over: it need not be routable from
here, and where an `HTTPS_PROXY` is set — as in this project's own sandboxes,
and most corporate networks — client-go sends the connection to the proxy
instead of to the local port. Three URLs matter and only the first is visible
in the kubeconfig: the shard's base URL, its external URL, and the **virtual
workspace URL**, which is what ends up in the `APIExportEndpointSlice` and so
is the address the manager itself connects to. Pinning the first two and
leaving the third undoes the fix, and the failure arrives halfway through
wiring the reconcilers as a reset connection with nothing about proxies in it.

**Readiness is three events, not one.** `/readyz` goes green first, and the
root workspace does not exist yet when it does. The root workspace's
`LogicalCluster` appears next, and `tenancy.kcp.io` is still absent from that
workspace's discovery. Waiting on either of the first two produces a failure
with a misleading shape: a controller-runtime client caches discovery on first
use and rate-limits reloading it, so a client built in that window fails its
first `Workspace` call with `no matches for kind "Workspace"` — which reads
like a scheme registration bug and is not. The demo waits for the API it is
about to use, and retries the first create on top of that.
