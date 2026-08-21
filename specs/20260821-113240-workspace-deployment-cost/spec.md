# Feature Specification: What the workspace deployment costs

**Feature Branch**: `claude/capi-workspace-roles-bindings-nujv8q-4`

**Created**: 2026-08-21

**Status**: Draft

**Input**: The measurement
[Workspace onboarding](../20260821-063000-workspace-onboarding/spec.md) scoped
out — "Taking the new figure is its own change, with its own `task test:sweep`
run and its own evidence" — and the discovery change taking it turned out to
need.

## Purpose

`workspace-resource-usage.md` reports what a workspace costs an installation by
measuring each deployment on its own and adding them up, and
`cmd/sweeptotals` refuses to print a total when one is missing, because a sum
of some of the deployments is not what an installation pays. Onboarding added a
fifth deployment that nothing measured, so the totals stopped being an
installation's — and the tool did not notice, because its required list still
named four.

Taking the figure turned out to need a change rather than just a run: the
sweep's departure step deletes the swept `APIBinding` and waits for the
workspace to leave the fleet, and this deployment's workspaces never left.

## Out of Scope

- **Making the deployment cheaper.** Five of its seven goroutines per workspace
  are the role maintainer's `mcbuilder` wiring rather than its work, and moving
  it onto the shard-wide watch registry the providers use is what would retire
  them. That is a change to the deployment, measured by this one; it is not
  attempted here.
- **Fixing the retained goroutine upstream.** `startWithinContext` parking a
  goroutine on the controller's context is a multicluster-runtime defect. It is
  recorded and asserted here, not fixed.
- **Making an unbind observable.** No claimed object leaves a wildcard view
  when its binding is deleted, for the two kcp reasons in FR-002. Changing that
  is kcp's, and this feature establishes that it does not need changing for
  this deployment.

## Requirements

### FR-001 — a workspace's departure is the event that deployment can observe

The sweep harness takes a departure sample after a workspace leaves the fleet.
Which event that is differs by deployment, and the harness must let a shape say
which: a provider is bound and unbound by a tenant, so its departure is the
`APIBinding` being deleted; the onboarding deployment's binding is written by a
`WorkspaceType` with `defaultAPIBindingLifecycle: Maintain`, so kcp recreates
one a tenant deletes, and its departure is the `Workspace` being deleted.

The default stays the binding, so no existing shape changes.

### FR-002 — discovery is by the object whose lifetime the fleet follows

The deployment discovers workspaces by the workspace's own `LogicalCluster`
rather than by an `APIBinding`, and claims `logicalclusters` to see it.

The reason is that it also claims `apibindings`, which replaces the virtual
workspace's normally-filtered one-binding-per-workspace view with every
`APIBinding` the workspace holds. Its discovery object stops being one per
workspace and its engagement turns on objects that are not its business.

What this does **not** do is make an unbind observable, and this is recorded
because it is the thing a reader will assume. Two kcp v0.32.3 behaviours
combine: the apiexport virtual workspace skips the `APIBinding` check for
wildcard requests, so a fleet-wide watch is filtered by the permission-claim
label alone; and nothing removes that label when a binding is deleted, because
`permissionclaimlabel`'s reconcile recomputes labels from the binding and
returns early once it is gone. **No claimed object leaves a wildcard view when
its binding is deleted** — which holds for the `APIBinding` view too once
`apibindings` is claimed, so swapping the discovery object does not change it.

### FR-003 — the fifth deployment is measured, and the totals are an installation's again

`task test:sweep` runs a shape for `cmd/workspace-manager` alongside the four
provider shapes, and `cmd/sweeptotals` requires its report before printing a
total.

### FR-004 — what a fleet-wide shape retains is measured, not assumed

The harness budgeted zero retained goroutines per departed workspace for a
shape whose controllers are fleet-wide. That branch was never exercised until
this shape existed, and it is wrong: multicluster-runtime engages a cluster by
wrapping each source in `startWithinContext`, which parks a goroutine on the
*controller's* context waiting to cancel the cluster's — so disengaging does
not release it. One source, one goroutine, measured. The budget states that
number and asserts it does not grow.

## Verification

- `task test:sweep` — the workspace shape engages twenty workspaces, makes each
  active, deletes each `Workspace` and samples after every step. It asserts
  FR-001 (the workspace disengages), FR-004 (the retention budget) and the
  claims every shape asserts: no watch stream per workspace, no LIST per
  workspace, nothing addressed to a tenant's own logical cluster.
- `task test:unit` — the onboarding export's claim on `logicalclusters` is
  asserted read-only in `internal/capiexports`, because it is what the fleet is
  discovered by.
- `task test:integration:kcp` — `test/integration/onboarding` covers FR-002 in
  the sense that matters to a tenant: the deployment still engages every
  onboarded workspace and writes its roles.

## Results

At twenty workspaces, `GOMAXPROCS=4`, Go 1.26.3:

| Per active workspace | `workspace-manager` | The four providers, added up |
|---|--:|--:|
| Goroutines | 7 | 8 |
| Watch streams | 0 | 0 |
| Discovery requests | 3 | 17 |
| Reconcile requests | 5 | 103 |
| Retained after departure | 1 | 0 |

Seven goroutines is the largest per-workspace figure of any deployment and five
reconcile requests the smallest, which is the shape of a deployment that
reconciles the permission to use Cluster API rather than any Cluster API
object. A goroutine profile diffed across five engagements splits the seven:
two are what every deployment pays (`ScopedCluster.Start` and
`providerwiring.Wiring.Engage`), and five are multicluster-runtime engaging a
controller per cluster. See
[Workspace resource usage](../../docs/site/content/en/docs/design/workspace-resource-usage.md).

## Notes

`cmd/sweeptotals` required four deployments and printed a total from whatever
reports were in `bin/`. A stale report from an earlier run therefore counted as
this run's, and a missing fifth deployment did not stop a total being printed
at all. The required list now names five; the staleness is unaddressed and is
worth knowing when reading a total from a directory that was not cleaned.
