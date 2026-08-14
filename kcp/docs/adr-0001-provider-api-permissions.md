# ADR-0001: Provider API permissions for core CAPI controllers

Status: **accepted** for the scope audited below (core's `cluster`,
`machine`, `machineset`, `machinedeployment`, `machinepool` reconcilers).
**Not yet accepted** for `core/reconcilers/topology/cluster` (the
ClusterClass/topology controller) — see "Known gap" below; do not treat
this ADR as covering that controller's claim scope until it's audited the
same way.

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
- Tenants still explicitly accept each claim per kcp's normal UX. That's
  unavoidable and arguably desirable: a workspace owner should see and
  approve exactly which provider resources core gains access to in their
  workspace, even though core's own onboarding of the claim is automatic.

This folds the original options A and D into one: A's goal (zero-touch
onboarding) achieved through D's mechanism (concrete, named,
per-resource claims), automated instead of hand-maintained. It does not
change decision 2 (single virtual-workspace identity) — the
claim-maintaining controller can itself run under a narrower, separate
identity if desired (it only needs read access to provider `APIExport`
objects and patch access to core's own `APIExport`), which is worth
scoping as its own small decision when this is implemented, but doesn't
block accepting this ADR.

## Verb scoping (from `core/reconcilers/*` audit)

Audited 2026-08-14 against `core/reconcilers/{cluster,machine,machineset,
machinedeployment,machinepool}` and `controllers/external/`. Full findings
in the audit transcript; summary below. **`core/reconcilers/topology/
cluster` (ClusterClass) was not included — see "Known gap".**

Cross-cutting findings that apply to every role below:
- **No reconciler ever calls `update`** against a provider object — all
  mutations are `patch` (JSON/strategic merge, or Server-Side-Apply) or
  the one SSA-based create path. `update` should not be claimed anywhere.
- **No `deletecollection` usage anywhere.**
- SSA-`Apply` of an object that doesn't yet exist is carried over HTTP
  `PATCH`, but Kubernetes RBAC additionally requires the `create` verb the
  first time the object is materialized — so create-capable paths need
  both `create` and `patch`, not `patch` alone.
- Today's upstream `kubebuilder:rbac` markers
  (`cluster_controller.go:72`, `machine_controller.go:90`,
  `machineset_controller.go:93`, `machinepool_controller.go:63`) request
  `resources=*,verbs=get;list;watch;create;update;patch;delete` per group —
  broader than what's actually used (includes unused `update`, and
  `list`/`watch` even on controllers that don't do either). Since kcp
  claims can't use a `resources=*` glob within a group anyway (previous
  section), translating these markers to claims requires enumerating each
  concrete provider resource — a good opportunity to also drop `update`
  and tighten `list`/`watch` to only where audited as used, rather than
  copying the upstream marker's verb set 1:1.

Per-role minimum claim verbs:

| Role | Referenced via | Touched by | Verbs |
|---|---|---|---|
| InfraCluster / ControlPlane object | `Cluster.spec.infrastructureRef` / `.controlPlaneRef` | Cluster controller only (audited scope) | `get`, `watch`, `patch`, `delete` — no `create`/`list` seen; something else (user, clusterctl, or the topology controller) creates these today |
| InfraMachine / BootstrapConfig object | `Machine.spec.infrastructureRef` / `.bootstrap.configRef` | Machine controller (get/watch/patch/delete) + MachineSet controller (get/list/create-via-SSA/patch/delete, for per-replica generated objects) | `get`, `list`, `watch`, `create`, `patch`, `delete` (union across both controllers — single identity per decision 2 means claim verbs are the union of every controller's needs on that resource, not per-controller) |
| InfraMachineTemplate / BootstrapConfigTemplate | `MachineSet`/`MachineDeployment` `.spec.template.spec.*Ref` | MachineSet + MachineDeployment (`reconcileExternalTemplateReference`) | `get`, `patch` (ownerRef only) — never watched, never created/deleted here |
| MachinePool's infra ref object | `MachinePool.spec.template.spec.infrastructureRef` | MachinePool controller | `get`, `watch` confirmed by audit; `patch`/`delete` presumed to mirror Machine's pattern but not individually cited — **verify before finalizing this row specifically** |

## Known gap: `core/reconcilers/topology/cluster` not yet audited

The ClusterClass/topology controller (`core/reconcilers/topology/cluster/
{reconcile_state,current_state,status,cluster_controller}.go`) also reads
and writes `infrastructureRef`/`controlPlaneRef`-style fields — it's the
component that materializes a whole ClusterClass-based cluster's
InfraCluster, ControlPlane, and MachineDeployment templates from class
definitions. It almost certainly needs a verb set closer to MachineSet's
(`get`, `list`, `create`, `patch`, `delete`) on the InfraCluster/
ControlPlane role than the narrower `get, watch, patch, delete` shown
above for the Cluster controller alone — but this is inference from
general CAPI architecture, not confirmed against this fork's actual code.
**Do not finalize the InfraCluster/ControlPlane claim's verb list for
implementation until this controller gets the same file:line audit
treatment the others got.** Tracked as open question 1 below.

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

1. **Audit `core/reconcilers/topology/cluster`** the same way the other
   five packages were audited, and fold its verb requirements into the
   InfraCluster/ControlPlane and Template rows above before those rows are
   used to write actual `PermissionClaim` manifests or RBAC.
2. **Confirm MachinePool's `patch`/`delete` behavior** against
   `machinepool_controller_phases.go` specifically (the audit confirmed
   `get`/`watch` but not the mutating verbs for this controller).
3. **Design the provider-discovery convention** the claim-maintaining
   controller uses to recognize a provider `APIExport` (label, annotation,
   or something else) — not blocking this ADR, but needed before the
   "Revised mechanism" is buildable.
4. **Confirm claim-acceptance propagation timing**: in the Phase 1 spike,
   verify that once a tenant accepts a newly-added claim, the resource
   becomes visible through `kcp-dev/multicluster-provider`'s discovery
   watch promptly (no manual re-sync) — load-bearing for the "day-one"
   framing, since a slow/manual propagation path would undercut the
   automated claim list's value.
5. Still tangled with **D1** (target kcp version to pin): the SDK version
   audited here is v0.32.3, already pinned in `kcp/go.mod`; if D1 lands on
   a materially different kcp version, re-check `PermissionClaim`'s shape
   hasn't changed before relying on the "resource field cannot wildcard"
   finding above.
