# ADR-0001: Provider API permissions for core CAPI controllers

Status: **proposed** — needs explicit human sign-off before Phase 1/3 work
that depends on it starts. This covers the permission-claim portion of D3
and the identity-model portion of D5 in
[`conversion-plan.md`](conversion-plan.md); it does not cover D3's schema
versioning question or D5's non-permission identity concerns, which can be
split into follow-up ADRs if needed.

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
runtime, not statically known to core. The cluster controller creates the
InfraCluster from a template, sets ownerRefs on it, and deletes it on
teardown; it isn't just reading status. So core's manager identity needs
read/write on GVKs it can't enumerate ahead of time, scoped per-workspace
to whatever that workspace actually bound — and it needs this without
core's own code or manifests naming those providers, or the "separate API
binding" extensibility goal breaks.

kcp's mechanism for this is **permission claims**: an `APIExport` declares
a claim on other resources/groups, and a workspace that binds the export
must separately accept the claim before those resources become
visible/writable to the exporting identity.

## Decision drivers

- **Extensibility** — adding a provider shouldn't require editing core's
  own manifests or redeploying core-manager.
- **Least privilege** — core shouldn't get access to resources a
  workspace hasn't opted into, or to types unrelated to Cluster API.
- **Auditability** — a tenant workspace owner accepting a claim should be
  able to tell what they're granting and why.
- **Consistency with Phase 2's architecture** — G1 (WildcardCache,
  "one system identity via APIExport virtual workspace") is already the
  direction the conversion plan leans; an identity model that fights that
  shape adds cost elsewhere in the plan, not just here.
- **Upstream invariant** — whatever mechanism is chosen has to work by
  configuring the client/discovery layer (G1/G3), not by changing what
  core's reconcilers ask for.

## Options considered

### A — Wildcard/group-level permission claims from core's `APIExport`

Core's export claims broadly (e.g. every resource, or every resource in
`*.cluster.x-k8s.io`-style groups) instead of naming individual provider
CRDs. Any provider CRD present in a workspace that accepts the claim
becomes visible to core automatically.

- **Pros:** zero-touch extensibility — a new provider ships, a tenant
  binds it, core already has access; no core-manager change per provider.
- **Cons:** broadest possible grant per accepting workspace; kcp's
  claim-acceptance UX becomes an "accept access to everything" prompt,
  which is a weak audit trail and a scary consent screen for workspace
  owners.

### B — Explicit named claims per provider CRD, maintained in core's `APIExport`

Core's export lists individual claims — one per known provider
group/resource (e.g. `infrastructure.cluster.x-k8s.io/dockerclusters`,
`bootstrap.cluster.x-k8s.io/kubeadmconfigs`,
`controlplane.cluster.x-k8s.io/kubeadmcontrolplanes`) — kept in sync with
the providers this repo ships or has explicitly onboarded.

- **Pros:** tenant sees exactly what's claimed and why; least-privilege by
  construction.
- **Cons:** onboarding a provider — including a third-party one nobody on
  this repo controls — means editing and redeploying core's `APIExport`.
  That cuts against the "separate API bindings for extensibility" premise
  and gives core an ongoing maintenance coupling to every provider's CRD
  list, which core wasn't otherwise going to have.

### C — Redraw the boundary so core doesn't need provider-CRD writes at all

Re-scope core's responsibilities so provider managers own all writes to
their own CRDs, and core only reads status/conditions synced back onto
`Cluster`/`Machine`.

- **Pros:** would sidestep the permission-claims problem for writes
  entirely.
- **Cons:** doesn't actually hold — core's reconcilers create the
  InfraCluster from a template, set ownerRefs, and delete it on teardown
  today; that's real write coupling to arbitrary provider GVKs that can't
  be redrawn away without patching upstream reconcile logic, which
  AGENTS.md forbids. At best this narrows *what* needs claiming
  (create/delete/ownerRef-patch vs. full read/write on status too) — worth
  scoping precisely, but not a standalone alternative to A/B/D.

### D — Hybrid: named claims (B) for in-repo providers now, revisit A later if needed

Ship core's `APIExport` with named claims for the providers `kcp/` builds
and tests against today — kubeadm bootstrap, kubeadm control plane, docker
infra (e2e) — per Phase 3's P1–P3. Treat unbounded third-party provider
support as a later, explicit escalation to option A, made only if that
turns out to be a real near-term requirement rather than a hypothetical.

- **Pros:** matches what Phase 3 is actually building; least-privilege by
  default while the permission surface is small and known; keeps A on the
  table as a documented escalation path instead of ruling it out.
- **Cons:** depends on confirming that "extensibility" in the original
  framing means "providers can version/ship independently of core," not
  "arbitrary third-party providers must work with zero core changes." If
  the latter is actually a near-term goal, D understates the requirement
  and A should be picked directly instead.

### E — Skip permission claims: per-workspace impersonation + ordinary RBAC

Core assumes a distinct credential/impersonated identity per tenant
workspace, and each workspace grants ordinary `ClusterRole`/
`ClusterRoleBinding` RBAC to that identity for the provider GVKs present —
the same mechanism a human operator would use, no kcp-specific concept
involved.

- **Pros:** doesn't depend on kcp's permission-claim semantics/maturity;
  reuses plain k8s RBAC, which already needs to exist for other reasons
  (upstream's `kubebuilder:rbac` markers, aggregated in P9).
- **Cons:** conflicts with the single-system-identity/WildcardCache shape
  Phase 2's G1 already commits to — per-workspace impersonation
  reintroduces the per-workspace auth/credential overhead that the
  wildcard-cache design exists to avoid. It also doesn't solve
  *discovery*: core still needs to learn a provider CRD exists in a given
  workspace, which permission-claim acceptance gives for free as part of
  the binding flow, and plain RBAC does not. Treat this as the option to
  explicitly rule out unless Phase 1's spike overturns G1's assumptions,
  not a live alternative alongside A–D.

## Recommendation

Lean toward **D**: start with named, explicit permission claims (B) scoped
to the providers this repo actually ships and tests — kubeadm bootstrap,
kubeadm control plane, docker infra — and treat broad/wildcard claims (A)
as a deliberate future escalation, not the default. Pair this with **one
system identity via the core `APIExport`'s virtual workspace** for D5
(consistent with G1/G1's WildcardCache), explicitly ruling out E's
per-workspace impersonation model unless Phase 1's spike finds a reason
G1's assumptions don't hold.

This is a judgment call on how far "extensibility" is meant to reach, not
a technical constraint — flag for explicit sign-off rather than treating
as decided.

## Open questions before this can move from proposed to accepted

1. Does "separate API bindings for extensibility" need to support
   out-of-repo/third-party providers in the near term, or only independent
   versioning/lifecycle of the providers `kcp/` itself ships? This is the
   swing factor between D and A.
2. Under option C's framing: precisely which provider-CRD operations does
   core's *unmodified* reconcile code perform — full read/write, or a
   narrower create/delete/ownerRef-patch/status-read split? Worth an
   actual audit of `core/reconcilers/*` against `infrastructureRef`/
   `configRef` usage before finalizing claim scope, independent of which
   option is picked.
3. Does kcp (at the version pinned in D1) support group-level permission
   claims, or only claims against specific resources? If only the latter,
   option A's "claim a whole API group" framing needs restating as
   "claim every known resource in the group," which changes its
   maintenance-burden comparison against B.
4. Confirm in the Phase 1 spike that permission-claim acceptance actually
   surfaces newly-claimed provider resources through
   `kcp-dev/multicluster-provider`'s discovery watch promptly (no manual
   re-sync step) — this is load-bearing for D's "revisit A only if needed"
   framing, since D's cost model assumes claim updates propagate live.
