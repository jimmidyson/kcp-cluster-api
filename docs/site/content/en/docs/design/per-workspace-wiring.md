---
title: Per-Workspace Wiring
description: The seam between workspace discovery and unmodified upstream reconcilers.
weight: 25
---

One `core-manager` process reconciles many kcp workspaces. This page is the
contract that makes that possible, and the reasons each clause of it exists.

It covers the conversion plan's Phase 2 groundwork items: G1 (discovery and
cache engine), G2 (per-workspace glue) and G3 (workspace-scoped
`rest.Config`). Two of the three are decisions *not* to build something, which
is why they are written down: a deferral and an omission look identical a year
later, and only one of them was a plan.

## The shape

```
kcp APIExport ──► multicluster-provider ──► multicluster-runtime manager
                    (discovery + shared            │
                     wildcard cache)               │ Engage(ctx, workspace, cluster)
                                                   ▼
                                        internal/providerwiring   ◄── this project
                                                   │
                                                   │ SetupFunc(ctx, workspace, mgr)
                                                   ▼
                                      unmodified upstream reconcilers
```

Both edges are somebody else's interface. The middle is the only bespoke part,
and it is small on purpose.

## G1 — discovery: no interface of our own

`multicluster.Provider` and `mcmanager.Manager` are already interfaces, owned
and implemented by the libraries that provide them. This project does not wrap
them.

Wrapping would give us a layer with one implementation and one caller, which
[Constitution][constitution] Principle VIII prohibits building ahead of a
second one. The cost of not wrapping is that a future swap touches
`cmd/core-manager` directly; that is a small, visible cost, paid once, if it
is ever paid at all.

**Revisit when**: a second provider is adopted alongside the `apiexport` one
(the `path-aware` provider is the candidate), or the conversion plan's
hand-rolled discovery fallback becomes necessary.

## G2 — per-workspace glue: `internal/providerwiring`

The seam a provider binary implements is one function:

```go
type SetupFunc func(ctx context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error
```

Given a manager scoped to one workspace, register that workspace's
controllers. That is the entirety of what each of the four Cluster API
provider binaries contributes — Phase 3's tracks (bootstrap, control plane,
docker infrastructure) differ from each other in nothing but the body of this
function.

### Four rules, each silent when broken

The rules below are not style. Each is a property of a dependency that was
established by reading its source, per [Constitution][constitution] Principle
V, and each fails without an error when violated — which is precisely why the
contract is stated here rather than left to whoever writes the next binary.

**1. Register before the manager starts.** multicluster-runtime's coordinator
fans `Engage` out to the components registered at the moment of the call, and
never replays earlier engagements. Registering afterwards is accepted and logs
nothing; the workspaces that engaged in the meantime are simply never wired.
`providerwiring` rejects late registration with `ErrStarted` rather than
inheriting that silence.

**2. Per-workspace runnables are bound to the workspace, not the process.**
The per-workspace manager multicluster-runtime hands out delegates `Add` to
the *host* manager. A controller registered the obvious way therefore outlives
the workspace it belongs to: the context supplied to `Engage` is cancelled on
disengage and nothing is listening to it. `providerwiring` interposes a
manager whose `Add` binds runnables to the engagement's context, so a workspace
that goes away stops costing anything.

**3. Controller name validation is disabled per workspace.** controller-runtime
keeps controller names in a process-global set which is never emptied, so the
second workspace to register a controller named `cluster` fails outright — as
does the second engagement of any single workspace. Per-workspace controllers
therefore set `SkipNameValidation`.

The validation exists to stop two controllers reporting the same metric, and
disabling it means exactly that happens: reconcile metrics are aggregated
across workspaces rather than attributable to one. That is a reporting
limitation, not an isolation one — no workspace's objects become visible to
another — and it is the conversion plan's P9 to fix.

**4. Webhooks are not part of a `SetupFunc`.** controller-runtime's webhook
builder *skips* a path that is already registered instead of rejecting it. Wire
webhooks per workspace and the second workspace's handlers are quietly
discarded, leaving the first workspace's handlers — holding the first
workspace's client — to serve every workspace's admission requests. Nothing
errors and nothing logs.

This is the failure [Constitution][constitution] Principle VIII was written
about. Routing an admission request to its own workspace is the conversion
plan's G4, which is unbuilt and carries a required human review checkpoint
because a bug there is a cross-tenant bleed rather than an ordinary defect.
Until it exists, `core-manager` serves webhooks for one explicitly named
workspace or for none, and asking for a second returns
`ErrWebhooksAlreadyWired`.

### Process-global state

Upstream requires two resolvers to be installed process-wide: contract
metadata (`external.SetGKMetadataGetter`) and conversion API versions
(`conversion.SetAPIVersionGetter`). Neither may capture a workspace's client,
because there is one slot and many workspaces — the last writer would win and
serve everyone.

Both are backed instead by `internal/contractmetadata`, a static registry built
from the CRD manifests of the pinned Cluster API modules. It is a pure function
of the build, identical for every workspace, so sharing it across tenants is
correct rather than merely tolerable.

## G3 — workspace-scoped `rest.Config`: deferred

Turning a workspace path plus a base kcp front-proxy config into a
`*rest.Config` has no caller today. Everything that talks to a workspace does
so through a manager the provider already engaged.

**Build when**: `clusterctl` gains workspace-awareness (P5), or anything else
needs to reach a specific workspace from outside the pool of engaged ones.

## Where to look next

- [`docs/conversion-plan.md`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md)
  — the phased plan this implements.
- [`docs/adr-0001-per-workspace-manager-pool.md`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/adr-0001-per-workspace-manager-pool.md)
  — the Phase 0 decisions and the Phase 1 results the contract above rests on.
- `internal/providerwiring` — the seam itself. Its package documentation is
  the authority; this page is the explanation.

[constitution]: https://github.com/jimmidyson/kcp-cluster-api/blob/main/.specify/memory/constitution.md
