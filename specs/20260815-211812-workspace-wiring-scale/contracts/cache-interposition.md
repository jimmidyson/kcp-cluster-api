# Contract: cache interposition at the workspace manager seam

**Status**: **GATED.** This contract is only implemented if P3's measurement
records a `build` verdict for FR-001 / FR-004 / FR-005. It is specified now so
the gate has something concrete to decide about, and so P3's verdict is a
decision about a real design rather than a hypothesis.

**Requirements**: FR-001, FR-002 (unconditional), FR-003, FR-004, FR-005

---

## The seam

`internal/providerwiring`'s `workspaceManager` already interposes a
`manager.Manager`, overriding two methods for reasons its documentation records:
`Add` binds runnables to the workspace's lifetime, `GetWebhookServer` refuses
registration. This contract adds a third override at the same seam.

```go
// workspaceManager, existing:
func (m *workspaceManager) Add(r manager.Runnable) error
func (m *workspaceManager) GetWebhookServer() webhook.Server

// added by this contract:
func (m *workspaceManager) GetCache() cache.Cache
```

**Why this is sufficient**, verified in
[R1](../research.md#r1--the-cache-is-substitutable-at-mgrgetcache--verified):
every watch CAPI creates goes through `mgr.GetCache()`
(`controller-runtime@v0.24.1` `pkg/builder/controller.go:323,351,368`) or
through `external.ObjectTracker.Cache`, which reconcilers assign from
`mgr.GetCache()`. There is no third path.

**Why no upstream change**: `source.Kind` consumes only interface methods
(`pkg/internal/source/kind.go`). Nothing here touches Cluster API or
controller-runtime source, so FR-023 holds and no `DRIFT.md` entry is created.

## What the returned cache must satisfy

It implements `sigs.k8s.io/controller-runtime/pkg/cache.Cache`. Behaviour splits
in two:

**Reads — delegate unchanged.** `Get`, `List`, `IndexField`,
`WaitForCacheSync` pass through to the workspace-scoped cache the provider
supplies. Reads are already cluster-scoped through the index
(`multicluster-provider` `pkg/cache/forked_cache_reader.go:145-176`) and are not
a cost this feature addresses. **Reimplementing them is out of scope and would
be the copying-upstream-code failure R5 refuses.**

**Watches — interpose.** `GetInformer` / `GetInformerForKind` return an
informer whose `AddEventHandlerWithOptions` registers into a process-wide
per-GVK registry keyed by cluster, instead of creating a new
`processorListener` on the shared informer.

## Required properties

| # | Property | Requirement | Verified by |
|---|---|---|---|
| C1 | A handler registered for workspace A is never invoked with an object whose logical cluster is not A | FR-002 — **unconditional; the isolation invariant** | Unit, plus integration against real kcp |
| C2 | One real registration exists per GVK regardless of engaged workspace count | FR-001, FR-003 | Unit: assert registration count on a fake informer |
| C3 | Registering a workspace's watch does not enqueue other workspaces' objects, and does not block delivery for other workspaces | FR-004, FR-005 | Unit: assert no full-store traversal; harness: no pause during join |
| C4 | A registration reports `HasSynced` true only after that cluster's replay completes, and always eventually returns | FR-004 — see [R6](../research.md#r6--hassynced-for-a-per-cluster-registration--open) | Unit against a fake informer; **first TDD cycle** |
| C5 | Removing a workspace removes its handlers; the per-GVK entry is released when its last workspace goes | FR-012 — unconditional | Unit; harness churn run (SC-009) |
| C6 | Dispatch cost per event does not grow with engaged workspace count | FR-001 | Harness (SC-002) |

C1 and C5 are unconditional even though the contract as a whole is gated: if
this is built at all, it must not be able to leak across tenants or accumulate
state. That is Principle VIII's seam exception.

## Initial sync

A newly registered workspace's replay comes from the existing indexer, scoped to
its cluster:

```text
indexer.ByIndex(kcpcache.ClusterIndexName, kcpcache.ClusterIndexKey(cluster))
```

Verified available in
[R2](../research.md#r2--per-cluster-initial-sync-can-avoid-the-fleet-wide-replay--verified):
`multicluster-provider` `pkg/cache/wildcard.go:81-88` adds this index to every
informer it creates.

This replaces `client-go`'s behaviour at
`tools/cache/shared_informer.go:918-934`, which takes `blockDeltas` and enqueues
the entire store — the process-wide stall and the quadratic onboarding cost in
one.

## Explicit non-goals

- **Not a general-purpose cache.** It exists to route events for this project's
  wiring. Anything beyond the `cache.Cache` interface is out of scope
  (Principle VIII).
- **Does not change read semantics.** Any observable read difference is a bug.
- **Does not touch the webhook path.** G4 is untouched.
- **Does not share the REST mapper.** That is FR-008, blocked separately on
  [R5](../research.md#r5--sharing-a-rest-mapper-has-no-clean-seam--verified-and-is-a-principle-ii-finding)'s
  Principle II finding, and must not be solved by copying upstream code.
