# ADR-0001: Provider API permissions for core CAPI controllers

Status: **accepted**, verb scope now audited across all six core
reconciler packages that touch provider-owned objects — `cluster`,
`machine`, `machineset`, `machinedeployment`, `machinepool`, and
`topology/cluster` (ClusterClass) — plus the automatic-claim-acceptance
mechanism (decision 3). No remaining known gaps in reconciler coverage;
open items 2 (MachinePool mutating-verb citation) and 3/6
(implementation-time design work) remain below.

This covers the permission-claim portion of D3 and the identity-model
portion of D5 in [`conversion-plan.md`](conversion-plan.md); it does not
cover D3's schema versioning question or D5's non-permission identity
concerns, which can be split into follow-up ADRs if needed.

## Context

Each infrastructure/bootstrap/control-plane provider is its own
`APIExport`, published and versioned independently — this is what gives
"separate API bindings" its extensibility payoff: a tenant workspace binds
only the providers it uses, and a new provider can ship without touching
any other provider's manifests.

Core's reconcilers (`core/reconcilers/cluster`, `core/reconcilers/machine`,
etc.) are unmodified upstream code (AGENTS.md), and they already read and
write provider-owned objects dynamically — `Cluster.spec.infrastructureRef`,
`Machine.spec.bootstrap.configRef`, `Machine.spec.infrastructureRef`, and
similar `ObjectReference` fields point at arbitrary GVKs resolved at
runtime, not statically known to core. So core's manager identity needs
read/write on GVKs it can't enumerate ahead of time, scoped per-workspace
to whatever that workspace actually bound — and it needs this without
core's own code or manifests naming those providers, or the "separate API
binding" extensibility goal breaks.

kcp's mechanism for this is **permission claims**: an `APIExport` declares
a claim on other resources, and a workspace that binds the export must
separately accept the claim before those resources become
visible/writable to the exporting identity.

## Decisions made

Resolved by explicit sign-off (2026-08-14), not silently picked by an
agent, per AGENTS.md's requirement for D3/D5:

1. **Extensibility scope: third-party providers must work day one**, not
   just in-repo providers. This is the swing factor that was originally
   framed as choosing between option A (wildcard claims) and option D
   (named claims, in-repo only, escalate later). See "Revised mechanism"
   below for why the literal reading of option A turned out not to be
   implementable, and what was chosen instead to still satisfy this
   requirement.
2. **Identity model (D5): single system identity via the core `APIExport`'s
   virtual workspace**, not per-workspace impersonation. Consistent with
   Phase 2's G1 (`WildcardCache`) design already committed to in the
   conversion plan. Option E (per-workspace impersonation + ordinary RBAC)
   is ruled out unless Phase 1's spike finds a reason G1's assumptions
   don't hold.
3. **Claim acceptance is automatic on provider bind, not a manual tenant
   step.** When a tenant binds a provider's `APIExport`, core's access to
   that provider's resources must become active without the tenant
   separately accepting a permission claim by hand. See "Automatic claim
   acceptance" below for the confirmed kcp-native mechanism this rides on.

## Technical finding that revises the original option set

The original draft of this ADR posed option A as "core's `APIExport`
claims broadly (e.g. every resource, or every resource in a
`*.cluster.x-k8s.io`-style group)". Checking the actual API
(`github.com/kcp-dev/sdk@v0.32.3`, `apis/apis/v1alpha2/types_apiexport.go`,
already pinned in `kcp/go.mod`) shows this isn't representable:

```go
type PermissionClaim struct {
    GroupResource `json:",inline"`  // Resource is +required, pattern
                                     // ^[a-z][-a-z0-9]*[a-z0-9]$ — no "*"
    Verbs []string `json:"verbs"`   // "*" IS allowed here
    IdentityHash string
}
```

`GroupResource.Resource`'s doc comment: *"you can not ask for permissions
for resource provided by a CRD not provided by an api export."* And the
accept-side `APIBinding` selector supports `MatchAll: true` for object
*instances* of an already-claimed resource, but that's orthogonal — it
doesn't let a claim's resource field itself be a wildcard.

So: **verbs can wildcard, instance scope can wildcard at accept-time, but
the claimed resource cannot** — every claim in
`APIExport.spec.permissionClaims` must name one concrete, already-exported
group+resource. A single static "claim everything" entry is not an option
kcp exposes.

### Revised mechanism: self-maintaining claim list

To still deliver "third-party providers work day one" (decision 1) without
a human editing core's `APIExport` manifest per provider, core's claim
list must be **maintained by a small controller, not hand-written**:

- A discovery component (naturally sits alongside G1's discovery/cache
  engine, or as its own small reconciler) watches for provider
  `APIExport`s matching a provider-contract convention — e.g. a label such
  as `cluster.x-k8s.io/provider-contract: infrastructure` (exact
  convention TBD, not blocking this ADR) — and reconciles matching
  resources into core's `APIExport.spec.permissionClaims`.
- Each entry it adds is still a concrete, named `GroupResource` — this
  satisfies kcp's API shape and keeps every individual claim auditable —
  but the *list* grows automatically as new providers are discovered, so
  no core-manager redeploy or manifest edit is needed per provider.
- Claim *acceptance* inside each tenant workspace does not need a manual
  per-tenant step either — see "Automatic claim acceptance" below, which
  supersedes the earlier assumption (in this ADR's first draft) that a
  workspace owner would explicitly accept each claim by hand.

This folds the original options A and D into one: A's goal (zero-touch
onboarding) achieved through D's mechanism (concrete, named,
per-resource claims), automated instead of hand-maintained. It does not
change decision 2 (single virtual-workspace identity) — the
claim-maintaining controller can itself run under a narrower, separate
identity if desired (it only needs read access to provider `APIExport`
objects and patch access to core's own `APIExport`), which is worth
scoping as its own small decision when this is implemented, but doesn't
block accepting this ADR.

### Automatic claim acceptance: `WorkspaceType` `Maintain` lifecycle

Decision 3 (claim acceptance is automatic on provider bind) is achievable
entirely with an existing, built-in kcp mechanism — no bespoke
tenant-side controller needed. Verified by reading the actual reconciler
in `kcp-dev/kcp` (cloned read-only for this ADR; not vendored into this
repo), not just the SDK's type comments:

`pkg/reconciler/tenancy/defaultapibindinglifecycle/
default_apibinding_lifecycle_reconcile.go` implements the
`WorkspaceType.spec.defaultAPIBindingLifecycle: Maintain` behavior. For
every `WorkspaceType` (transitively) applied to a workspace with
`DefaultAPIBindingLifecycle == Maintain`, its reconcile loop (function
`reconcile`, lines 94–190):

1. Iterates **every** `PermissionClaim` currently declared on the
   referenced `APIExport` (`apiExport.Spec.PermissionClaims`, line 134) —
   not a fixed snapshot from when the binding was first created.
2. Builds an `AcceptablePermissionClaim` for each one with
   `State: ClaimAccepted` (line 157), using the claim's own
   `DefaultSelector` if set, else `MatchAll: true` (lines 135–141).
3. **Unconditionally overwrites** the managed `APIBinding`'s
   `spec.permissionClaims` with this freshly-built list on every
   reconcile (line 181: `apiBinding.Spec = apiBindingSpec`, followed by a
   commit) — a full replace, not a merge.
4. Is triggered by an informer watch on `APIExport` update events
   (`default_apibinding_lifecycle_controller.go`, lines 135–140:
   `apiExportsInformer.Informer().AddEventHandler(...enqueueAPIExport...)`),
   which re-enqueues every workspace referencing that export — so this
   isn't poll-based or restart-required; a change to core's
   `APIExport.spec.permissionClaims` (e.g. the self-maintaining discovery
   controller adding a newly-onboarded provider's resource) propagates to
   every `Maintain`-mode tenant workspace's accepted-claims list on the
   next reconcile after that change.

Combined with decision 1's self-maintaining claim-list controller (which
keeps core's `APIExport.spec.permissionClaims` itself in sync with
discovered providers), this closes the loop with **zero additional code**
on the acceptance side: onboard a provider once → its claim lands on
core's `APIExport` → kcp's own `DefaultAPIBindingController` propagates
acceptance into every tenant workspace that uses a `Maintain`-mode
`WorkspaceType` for its core binding. A tenant binding a provider's own
`APIExport` afterward just makes the (already dormantly-accepted) claim
functionally relevant, since there's nothing to access until the
provider's CRDs actually exist in that workspace.

**Requirements this imposes on implementation** (tracked as open question
6 below):

- kcp-cluster-api must ship (or document) a `WorkspaceType` that tenants
  use to create CAPI-enabled workspaces, with `spec.defaultAPIBindings`
  referencing core's `APIExport` and
  `spec.defaultAPIBindingLifecycle: Maintain`. A tenant who instead
  hand-creates their own `APIBinding` to core (bypassing this
  `WorkspaceType`) opts out of automatic propagation and must maintain
  their own `spec.permissionClaims` by hand — worth flagging in user docs
  (P10), not just assumed.
- Each claim on core's `APIExport` should leave `DefaultSelector` unset
  (or set explicitly to `MatchAll: true`, which is the fallback anyway)
  so acceptance covers all instances of that resource type per workspace.

**Trade-off to flag explicitly, not implement silently:** this removes
the tenant's per-claim consent step kcp's permission-claim model is
otherwise built around — decision 3 deliberately chooses automatic
propagation over that friction, but it means (a) a tenant on the managed
`WorkspaceType` cannot selectively reject one provider's claim while
accepting others (it's all claims on core's `APIExport`, or none, via
this path — a narrower opt-out means bypassing the managed binding
entirely per the point above), and (b) any manual edit to the *managed*
`APIBinding`'s `spec.permissionClaims` is silently reverted on the next
reconcile (full overwrite, not merge). Both are consequences of decision 3
as stated, not implementation bugs — call this out in user docs so it
isn't surprising in practice.

## Verb scoping (from `core/reconcilers/*` audit)

Audited 2026-08-14 against `core/reconcilers/{cluster,machine,machineset,
machinedeployment,machinepool,topology/cluster}` and
`controllers/external/`. Full findings in the audit transcripts; summary
below.

Cross-cutting findings that apply to every role below:
- **No reconciler ever calls `update`** against a provider object — all
  mutations are `patch` (JSON/strategic merge, or Server-Side-Apply) or
  the one SSA-based create path. `update` should not be claimed anywhere.
  This holds even in the topology/ClusterClass controller, which patches
  full provider-object *specs* (not just ownerRef/labels like the other
  controllers) to keep them in sync with class definitions — it still
  does so via SSA `PATCH`, never `PUT`/`update`. Since kcp (like
  Kubernetes RBAC generally) scopes claims/rules by resource+verb, not by
  field, a claim never needs to distinguish "patches only ownerRef" from
  "patches full spec" — both are just the `patch` verb. Noted here only
  so a future reader doesn't assume field-scoped least-privilege is
  achievable through the claim/RBAC layer; it isn't.
- **No `deletecollection` usage anywhere**, and **`list` against a
  provider GVK is rare** — only MachineSet's orphan-cleanup pass uses it;
  the topology controller's `list` calls are all against **native** CAPI
  types (MachineDeployment/MachinePool lists), never provider GVKs, even
  though it's the most create/delete-heavy controller of the six.
- SSA-`Apply` of an object that doesn't yet exist is carried over HTTP
  `PATCH`, but Kubernetes RBAC additionally requires the `create` verb the
  first time the object is materialized — so create-capable paths need
  both `create` and `patch`, not `patch` alone.
- Today's upstream `kubebuilder:rbac` markers
  (`cluster_controller.go:72,69` in both `core/reconcilers/cluster` and
  `core/reconcilers/topology/cluster`, `machine_controller.go:90`,
  `machineset_controller.go:93`, `machinepool_controller.go:63`) request
  `resources=*,verbs=get;list;watch;create;update;patch;delete` per group —
  broader than what's actually used (includes unused `update` everywhere
  audited, and `list`/`watch` even on controllers that don't do either).
  Since kcp claims can't use a `resources=*` glob within a group anyway
  (previous section), translating these markers to claims requires
  enumerating each concrete provider resource — a good opportunity to
  also drop `update` and tighten `list`/`watch` to only where audited as
  used, rather than copying the upstream marker's verb set 1:1.

Per-role minimum claim verbs (union across every controller that touches
that role, since decision 2's single identity means the claim verb set
must cover every controller's needs on that resource, not per-controller):

| Role | Referenced via | Touched by | Verbs |
|---|---|---|---|
| InfraCluster / ControlPlane object | `Cluster.spec.infrastructureRef` / `.controlPlaneRef` | Cluster controller (get/watch/patch/delete) + topology controller (get/watch/create-and-patch via SSA/delete, when ClusterClass-based) | `get`, `watch`, `create`, `patch`, `delete` — no `list` (topology never lists provider GVKs, only native types; something outside core, e.g. clusterctl or a user, creates these for non-ClusterClass clusters) |
| InfraMachine / BootstrapConfig object | `Machine.spec.infrastructureRef` / `.bootstrap.configRef` | Machine controller (get/watch/patch/delete) + MachineSet controller (get/list/create-via-SSA/patch/delete, for per-replica generated objects) | `get`, `list`, `watch`, `create`, `patch`, `delete` |
| InfraMachineTemplate / BootstrapConfigTemplate / ControlPlane's InfrastructureMachineTemplate | `MachineSet`/`MachineDeployment` `.spec.template.spec.*Ref`, and topology's class-driven template management (create, rotate-on-change, delete-old-on-rotation) | MachineSet + MachineDeployment (get/patch, ownerRef only) + topology controller (get/create/patch-full-spec-via-SSA/delete) | `get`, `create`, `patch`, `delete` — **no `watch`**: confirmed unwatched by both the earlier audit and topology's controller, which explicitly watches only the current InfraCluster/ControlPlane objects (`cluster_controller.go:422-444`), not templates; template changes rely on periodic Cluster reconcile triggers |
| MachinePool's infra ref object | `MachinePool.spec.template.spec.infrastructureRef` | MachinePool controller | `get`, `watch` confirmed by audit; `patch`/`delete` presumed to mirror Machine's pattern but not individually cited — **verify before finalizing this row specifically** (open question 2) |

## Options considered (retained for record)

### A — Wildcard/group-level permission claims from core's `APIExport`

Superseded — see "Technical finding" above. What survives from this
option's *goal* (zero-touch third-party onboarding) is folded into the
"Revised mechanism" section as an automated, per-resource claim list
rather than a true wildcard.

### B — Explicit named claims per provider CRD, maintained by hand in core's `APIExport`

- **Pros:** tenant sees exactly what's claimed and why; least-privilege by
  construction; no extra controller to build.
- **Cons:** onboarding a provider — including a third-party one — means
  editing and redeploying core's `APIExport`. Rejected by decision 1
  (third-party providers must work day one) as the sole mechanism, but its
  claim-shape (concrete, named, verb-scoped entries) is exactly what the
  automated reconciler in "Revised mechanism" produces — B's output, not
  B's process.

### C — Redraw the boundary so core doesn't need provider-CRD writes at all

- **Cons:** doesn't hold. The audit confirms core creates (via MachineSet),
  ownerRef-patches, and deletes provider objects today — real write
  coupling to arbitrary provider GVKs that can't be redrawn away without
  patching upstream reconcile logic, which AGENTS.md forbids.

### D — Hybrid: named claims for in-repo providers now, revisit A later

- Superseded by decision 1 (day-one third-party support was chosen over
  deferring it) — but its "always use concrete, named claims" mechanic is
  exactly what the automated reconciler still does; only the "who
  maintains the list, and how often" question changed.

### E — Skip permission claims: per-workspace impersonation + ordinary RBAC

- **Cons:** conflicts with decision 2 (single virtual-workspace identity)
  and the WildcardCache shape G1 already commits to. Ruled out per
  decision 2 unless Phase 1's spike overturns those assumptions.

## Open questions before implementation

1. ~~Audit `core/reconcilers/topology/cluster`~~ — **done** (2026-08-14);
   folded into the verb table above. Notable result: topology is the only
   controller that SSA-patches full provider-object specs (not just
   ownerRef/labels), and the only one that creates InfraCluster/
   ControlPlane objects directly (for ClusterClass-based clusters) — both
   now reflected in the InfraCluster/ControlPlane row's verb set.
2. **Confirm MachinePool's `patch`/`delete` behavior** against
   `machinepool_controller_phases.go` specifically (the audit confirmed
   `get`/`watch` but not the mutating verbs for this controller).
3. **Design the provider-discovery convention** the claim-maintaining
   controller uses to recognize a provider `APIExport` (label, annotation,
   or something else) — not blocking this ADR, but needed before the
   "Revised mechanism" is buildable.
4. ~~Confirm claim-acceptance propagation timing~~ — **mechanism confirmed**
   against `kcp-dev/kcp` source (see "Automatic claim acceptance" above):
   propagation is informer-event-driven, not poll-based. Still worth an
   empirical end-to-end timing check in the Phase 1 spike (claim added →
   `DefaultAPIBindingController` reconcile → resource visible through
   `kcp-dev/multicluster-provider`'s discovery watch), but this is now a
   "measure the latency" task, not an open design question.
5. Still tangled with **D1** (target kcp version to pin): the SDK/server
   version audited here is v0.32.3 (SDK) — `pkg/reconciler/tenancy/
   defaultapibindinglifecycle` was read from a fresh clone of
   `kcp-dev/kcp`'s default branch, so pin the exact server version
   alongside D1 and re-check this reconciler's behavior hasn't changed if
   D1 lands on a materially different version.
6. **Ship the `Maintain`-mode `WorkspaceType`** tenants use to onboard to
   CAPI (see "Requirements this imposes on implementation" above) — this
   is new, concrete manifest work that should be folded into P6
   (APIExport/APIBinding manifests) in `conversion-plan.md`, and its
   existence/behavior documented in P10 (user docs), including the
   opt-out trade-off (hand-created `APIBinding`s don't get automatic
   propagation).
