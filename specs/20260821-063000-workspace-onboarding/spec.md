# Feature Specification: Workspace onboarding

**Feature Branch**: `claude/capi-workspace-roles-bindings-nujv8q`

**Created**: 2026-08-21

**Status**: Draft

**Input**: "1. use a workspace type to create roles and CAPI core API bindings.
2. show how a user would enable (create API binding) for an CAPI provider (can
use DevCluster). 3. automate the updating of permissions when a new CAPI
provider is bound in workspace so user does not have to manually update CAPI
core permissions"

## Purpose

Everything a workspace needs before it can run clusters was, until now, the
demo's job: an `APIBinding` to Cluster API's core `APIExport`, a `ClusterRole`
saying what a tenant may do with the types it serves, and a claim list letting
core's controllers reach whatever provider that tenant uses. All three were
written out by hand, per workspace, by a process holding admin credentials.

That is not a multi-tenant system. It is one operator doing a tenant's work,
and it hides the two questions a real deployment turns on: whether a tenant is
*allowed* to do the thing, and what has to change when the set of providers
does.

This feature makes **creating a workspace the whole of onboarding**, and
**enabling a provider a tenant's own step** — and makes the permissions on both
sides of that step maintain themselves.

## Out of Scope

- **Identity provisioning for the managers.** ADR-0001's decision 2 stands:
  each provider deployment acts through its `APIExport`'s virtual workspace,
  under one identity. This feature provisions RBAC for *tenants*, not for
  controllers.
- **Per-workspace measurement.** This adds a deployment and a binding per
  workspace, so the totals in `workspace-resource-usage.md` no longer add up.
  Taking the new figure is its own change, with its own `task test:sweep` run
  and its own evidence.
- **Retiring the claim-retry workaround.** `NudgeUnappliedClaims` works around
  a kcp race; the fix belongs upstream and is not attempted here.

## Requirements

### FR-001 — a workspace of the Cluster API type is ready to use

Creating a `Workspace` of type `cluster-api` MUST produce, before the workspace
reports `Ready`:

- an `APIBinding` to Cluster API's core `APIExport`; and
- the `cluster-api-admin` and `cluster-api-view` `ClusterRole`s.

A tenant MUST never observe a workspace that serves Cluster API types and
grants nobody the use of them.

### FR-002 — the type does not choose the tenant's providers

The `WorkspaceType` MUST NOT bind any provider `APIExport`. Which
infrastructure, bootstrap and control plane providers a workspace uses is the
tenant's decision.

### FR-003 — a tenant enables a provider with their own permissions

A tenant holding `cluster-api-admin` in their workspace and `bind` on a named
provider `APIExport` MUST be able to create the `APIBinding` that enables it,
authorised as themselves.

A tenant without that `bind` grant MUST be refused, even holding
`cluster-api-admin`. Enabling a provider is a permission somebody grants, not
the absence of one.

### FR-004 — the tenant's role follows what they enabled

After an `APIBinding` to a provider is bound, the workspace's
`cluster-api-admin` and `cluster-api-view` roles MUST cover that provider's API
group, with no edit by anybody. The same MUST hold for a provider this
repository does not ship.

### FR-005 — core's claim list follows the providers installed

An `APIExport` labelled `cluster.x-k8s.io/provider-contract` MUST have every
resource it publishes claimed by core's `APIExport`, with that export's
identity hash and the verbs core's own RBAC markers justify — without any
provider being named in this repository's code.

Every Cluster API workspace MUST accept those claims without a tenant action.

### FR-006 — the hand-bound path keeps working

A workspace MAY be onboarded by hand: created with kcp's `universal` type, with
every `APIBinding` and role written by whoever owns it. That path MUST remain
exercised, because it is the opt-out ADR-0001 documents and the only shape in
which a workspace can be taken apart — nothing recreates a binding deleted
from it.

### FR-007 — a run says what happened

A demo run MUST report, read back from the server rather than from its own
intentions: what each workspace was bound to without asking, what its tenant
enabled and who enabled it, what its roles ended up covering, and which
permission claims were discovered rather than declared.

## Verification

- `task test:unit` — the roles, the claim computation and the `WorkspaceType`
  shape are pure functions and are asserted without a server. The claim
  computation is checked to reproduce the hand-written list this repository
  used to carry.
- `task test:integration` — `test/integration/onboarding` asserts FR-001,
  FR-003, FR-004 and FR-005 against a real kcp;
  `test/integration/teardown` exercises FR-006.
- `task demo` — FR-007, and the whole of the above end to end.

## Notes

Two properties of kcp cost a session each to find and are recorded so the next
reader does not pay again. Both are in
[Workspace onboarding](../../docs/site/content/en/docs/design/workspace-onboarding.md).

- An impersonated user is **scoped to one logical cluster** unless the
  impersonator is in `system:masters`. A scoped tenant cannot be authorised in
  the workspace holding the `APIExport`s, which is where the right to enable a
  provider is checked — so an impersonated tenant is strictly weaker than the
  real one, and the demo has to impersonate from the shard admin to be
  demonstrating anything.
- A claim on a provider the workspace has not bound **races the workspace
  becoming `Ready`**. Measured on kcp v0.32.3: `Ready` in about ten seconds
  when kcp's bound-CRD materialiser wins that race, and not `Ready` two minutes
  later when it does not, on roughly half of the runs.
