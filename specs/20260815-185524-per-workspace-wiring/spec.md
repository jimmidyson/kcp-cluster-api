# Feature Specification: Per-workspace wiring for every bound workspace

**Feature Branch**: `claude/whats-next-0k5ksj`

**Created**: 2026-08-15

**Status**: Shipped in [#25](https://github.com/jimmidyson/kcp-cluster-api/pull/25)

**Input**: Phase 2 of [`docs/conversion-plan.md`](../../docs/conversion-plan.md),
items G1 (discovery and cache engine), G2 (per-workspace glue) and G3
(workspace-scoped `rest.Config` builder).

## Purpose

`cmd/core-manager` today reconciles exactly one workspace, named on the
command line. That was Phase 1's deliberate scope: prove the mechanism against
a single hardcoded workspace before building the dynamic machinery. The
mechanism is proven — see [ADR-0001's Phase 1 results][adr] — so the
restriction is now the only thing standing between this project and the thing
it exists to do, which is reconciling Cluster API resources across many
logical clusters from one control plane.

This feature removes that restriction: every workspace bound to the project's
`APIExport` gets its reconcilers wired automatically as it joins, and has them
stopped when it leaves.

It is also the point at which tenancy stops being hypothetical. Phase 1's
wiring is not merely single-workspace, it is *silently* single-workspace in
three separate places, each verified against the dependency's source rather
than inferred:

| Behaviour | Where | Effect at the second workspace |
|---|---|---|
| The webhook builder skips a path already registered | `controller-runtime` `pkg/builder/webhook.go`, `isAlreadyHandled` | The second workspace's admission handlers are never registered. The first workspace's handlers — holding the first workspace's client — serve every workspace's admission requests, with no error logged |
| Controller names are checked against a process-global set that is never emptied | `controller-runtime` `pkg/controller/name.go`, `checkName` | The second workspace's setup fails with `controller with name cluster already exists`. Re-engaging a workspace after it leaves fails the same way |
| A scoped manager's `Add` delegates to the *host* manager | `multicluster-runtime` `pkg/manager/manager.go`, `scopedManager.Add` | The context passed to `Engage`, which is cancelled on disengage, controls nothing. Controllers for a departed workspace run for the life of the process |

The first of these is the failure Constitution Principle VIII was written
about. This specification treats all three as the feature's substance rather
than as incidental defects: the deliverable is wiring that cannot serve one
tenant with another tenant's client, and cannot accumulate work for tenants
that have gone away.

[adr]: ../../docs/adr-0001-per-workspace-manager-pool.md#phase-1-results

## Out of Scope

- **Webhook serving across workspaces (G4).** A single listener fanned out
  across tenant boundaries is the one component the conversion plan requires
  a human review checkpoint for, and its workspace-resolution design is
  explicitly unfinished. This feature therefore does not make webhooks
  multi-workspace; it makes the existing single-workspace webhook wiring
  *refuse* to be used as though it were multi-workspace. See FR-008.
- **A workspace-scoped `rest.Config` builder (G3).** Deferred, with the
  trigger recorded — see FR-011.
- **Reaching feature parity with upstream `core/main.go`'s reconciler set.**
  The wired set stays as Phase 1 left it. This feature changes *how many
  workspaces* that set runs against, not *which reconcilers* run.
- **Per-workspace metric partitioning (P9).** Recorded as a deviation with
  its trigger — see "Known deviations".
- **Horizontal sharding across replicas (D6/Phase 4).**

## User Scenarios & Testing *(mandatory)*

### User Story 1 - A workspace binds and is reconciled, without an operator naming it (Priority: P1)

An operator runs one `core-manager` against an `APIExportEndpointSlice`. A
tenant creates a workspace and binds it to the project's `APIExport`. Without
anyone restarting, reconfiguring, or naming that workspace anywhere, its
`Cluster` and `Machine` objects begin reconciling. A second tenant does the
same and gets the same result, concurrently, with neither tenant's
reconcilers reading or writing the other's objects.

**Why this priority**: this is the feature. Everything else here is the
correctness bar it has to clear.

**Acceptance**: `task test:integration` — a test that publishes one
`APIExport`, binds two workspaces, and asserts that setup runs exactly once
per workspace and that each workspace's wiring is scoped to its own
workspace.

### User Story 2 - A workspace unbinds and stops costing anything (Priority: P2)

A tenant deletes its `APIBinding`. The controllers that were running for that
workspace stop. A subsequent re-bind of the same workspace starts them again,
rather than failing because the previous generation left process-global
residue behind.

**Why this priority**: without it, workspace churn is an unbounded leak of
goroutines and workqueues, and the second bind of any workspace is a hard
failure rather than a recovery. Both are silent — nothing logs, the process
simply gets slower and then stops working for that tenant.

**Acceptance**: `task test:unit` for the lifecycle contract against a fake
provider, and `task test:integration` for the real disengage path.

### User Story 3 - An operator cannot accidentally serve one tenant's admissions with another's client (Priority: P1)

An operator configures webhook serving. Because G4 does not exist yet, the
only correct configuration is one that serves exactly one workspace. The
manager either does that, having been told which workspace, or serves no
webhooks at all. There is no configuration in which it appears to serve many
workspaces and in fact serves one tenant's requests using another tenant's
client.

**Why this priority**: this is a cross-tenant data path, and its current
failure mode is silent. A loud, documented limitation is strictly better than
a quiet, plausible-looking wrong answer.

**Acceptance**: `task test:unit` — registering webhook wiring for a second
workspace returns an error naming the constraint.

### Edge Cases

- Setup fails for one workspace: that workspace is not left half-wired, and
  the failure does not prevent other workspaces from engaging.
- The same workspace is engaged twice without an intervening disengage
  (the provider's own de-duplication is best-effort — it documents a race):
  setup runs once, not twice.
- A workspace engages before the process is elected leader, or after the
  manager has already started: both are the normal case, not an error.
- A workspace engages while another workspace's setup is still running.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The manager MUST run the per-workspace setup for every
  workspace the provider engages, without any workspace being named in
  configuration.
- **FR-002**: The per-workspace setup MUST run exactly once per engagement of
  a given workspace.
- **FR-003**: Registration of per-workspace wiring MUST happen before the
  manager starts. The coordinator fans `Engage` out only to components
  registered at the time of the call and never replays earlier engagements,
  so a component registered later silently misses every workspace that
  engaged before it.

  What is enforceable is narrower than the rule, and the difference is stated
  rather than papered over: the wiring cannot observe whether the manager it
  is being added to has already started, because neither the manager nor the
  coordinator exposes that. What it MUST enforce is that one wiring is used
  once — a second start is rejected — which catches reuse, the form of this
  mistake that a running process can actually make. The rest is a caller
  obligation, documented at the registration function and satisfied by the
  one caller in `cmd/core-manager`.
- **FR-004**: Runnables registered during a workspace's setup MUST stop when
  that workspace is disengaged, and MUST NOT outlive it.
- **FR-005**: Disengaging and re-engaging a workspace MUST leave it working.
  No process-global state may accumulate per engagement in a way that makes
  the second engagement fail.
- **FR-006**: A setup failure for one workspace MUST NOT prevent other
  workspaces from being set up, and MUST be reported rather than swallowed.
- **FR-007**: Two engaged workspaces MUST NOT share a client, a cache, or a
  reconciler instance. Each workspace's reconcilers read and write that
  workspace only.
- **FR-008**: Webhook wiring MUST be constrained to a single, explicitly
  named workspace. An attempt to wire webhooks for a second workspace MUST
  fail with an error that names the constraint and points at G4. Serving no
  webhooks MUST be a supported configuration.
- **FR-009**: Process-global state that upstream requires (contract-metadata
  resolution, conversion API-version resolution) MUST be installed once per
  process and MUST NOT capture any single workspace's client.
- **FR-010**: The decision not to introduce a project-owned interface for
  discovery (G1) MUST be recorded, with what would trigger revisiting it.
- **FR-011**: The deferral of the workspace-scoped `rest.Config` builder (G3)
  MUST be recorded, with what would trigger building it.

### Key Entities

- **Workspace** — a kcp logical cluster, identified to this code by the
  provider's `ClusterName`, which is the internal logical cluster name and
  not the human-readable workspace path.
- **Per-workspace wiring** — the component that observes engagement and
  disengagement and runs a caller-supplied setup function per workspace. It
  is the only piece of this feature that is bespoke to this project; both
  sides of it are library interfaces.
- **Setup function** — what a provider binary supplies: given a workspace's
  own manager, register that workspace's controllers.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: With two workspaces bound, an integration test observes setup
  running for both, having named neither.
- **SC-002**: With a workspace disengaged, its per-workspace runnables have
  stopped, observed rather than assumed.
- **SC-003**: A workspace that is disengaged and re-engaged is wired again
  successfully.
- **SC-004**: `task verify` reports pass, or reports "could not run" naming
  the missing capability. It does not report pass for a step that skipped.
- **SC-005**: `cmd/core-manager` no longer accepts a flag naming the one
  workspace it reconciles.

## Known deviations

Stated explicitly per the Governance section of the constitution.

- **Metrics are not partitioned per workspace.** Controller-runtime derives
  metric labels from the controller name, and this feature makes many
  workspaces share one name. Reconcile metrics are therefore aggregated
  across tenants rather than attributable to one. This is not a data path
  between tenants — no tenant's objects become readable to another — so it is
  a reporting limitation rather than an isolation one. Trigger for fixing it:
  P9 (observability), or the first operator question that cannot be answered
  from aggregated metrics.
- **Webhooks remain single-workspace.** Per FR-008 and "Out of scope". This
  is a reduction in capability relative to nothing — Phase 1 also served one
  workspace — but it is now enforced rather than assumed. Trigger: G4.

## Assumptions

- The provider engages a workspace only after its cache has synced. Verified:
  `multicluster-runtime` `pkg/clusters` waits for cache sync, and applies
  field indexes, before calling `Engage`.
- The context passed to `Engage` is cancelled when the workspace is removed.
  Verified: same package, `Clusters.Remove` cancels the per-cluster context
  stored at add time.
- One process, one leader election, shared across every engaged workspace.
  Verified in Phase 1 and unchanged here.
