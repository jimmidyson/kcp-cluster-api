# Implementation Plan: Per-workspace wiring for every bound workspace

**Spec**: [spec.md](spec.md) | **Branch**: `claude/whats-next-0k5ksj`

## Approach

One new package, `internal/providerwiring`, sitting between
multicluster-runtime's engagement callbacks and a provider binary's reconciler
wiring. Everything on either side of it is an existing library interface; the
package's whole job is the lifecycle in between.

The seam a provider binary implements is one function type:

```go
type SetupFunc func(ctx context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error
```

`cmd/core-manager` supplies one, calling `coremanager.SetupReconcilers`.
Phase 3's tracks — the bootstrap, control-plane and docker-infrastructure
provider ports — each supply their own and differ in nothing else. Landing
this type before those tracks fan out is what the conversion plan asks for
under "pin G1–G3's behavioural descriptions into actual Go interface
signatures first".

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | No project-owned interface for discovery (G1) | `multicluster.Provider` and `mcmanager.Manager` are already interfaces owned by their implementers. Wrapping them adds a layer with one implementation and one caller, which Principle VIII prohibits ahead of a second. Revisit on adopting a second provider, or on the hand-rolled fallback |
| 2 | No workspace-scoped `rest.Config` builder (G3) | No caller. Trigger recorded: P5 (clusterctl), or anything reaching a specific workspace from outside the engaged pool |
| 3 | Per-workspace runnables get their own context, not the process's | multicluster-runtime's per-workspace manager delegates `Add` to the host manager, so the context supplied to `Engage` — the one cancelled on disengage — controls nothing. Interposing a manager whose `Add` binds to that context is what makes disengagement mean something |
| 4 | `SkipNameValidation` on every per-workspace controller | controller-runtime's controller-name set is process-global and never emptied. Without this the second workspace fails outright, as does the second engagement of any one workspace |
| 5 | Webhooks stay out of `SetupFunc`, and the per-workspace manager refuses to register them | The webhook builder skips an already-registered path rather than rejecting it, so per-workspace wiring silently leaves one tenant's client serving everyone. Refusing is the only option that is not silently wrong, since routing (G4) is unbuilt and needs human review |
| 6 | The dev-provider backend is created once per process | `NewWorkloadClustersMux` binds a fixed debug port, so a second one fails to construct. Its cluster-name-keyed listeners are consequently shared; stated as a limitation of upstream's test-only provider, with P3 as the trigger |
| 7 | Process-global resolvers are backed by the static contract-metadata registry, never a workspace client | There is one slot and many workspaces. A workspace's client stored there would answer every other workspace's lookups, and last-writer-wins is not an error anyone would see |
| 8 | `test:integration` splits into `:kcp` and `:docker` | Most of what the suite proves needs kcp and nothing else. Running it as one container-runtime-gated step reports runnable coverage as "could not run", which is the failure Principle IV exists to prevent, pointing the other way |

## Acceptance conditions

Per Constitution Principle IV, each is a command whose exit status is the
answer.

| Item | Acceptance | Where |
|---|---|---|
| G1 — discovery | `task test:integration:kcp` — a manager built on the apiexport provider engages two workspaces | `test/integration/providerwiring` |
| G2 — every bound workspace is set up (FR-001, FR-002, FR-007) | `task test:integration:kcp` | `TestEveryBoundWorkspaceIsWired` |
| G2 — disengage stops that workspace's work, and only that workspace's (FR-004) | `task test:integration:kcp`, `task test:unit` | same, plus `TestRunnablesStopOnDisengage`, `TestDisengageLeavesOtherWorkspacesRunning` |
| G2 — rebind works (FR-005) | `task test:integration:kcp`, `task test:unit` | same, plus `TestReEngageAfterDisengage` |
| G2 — setup failure is reported and isolated (FR-006) | `task test:unit` | `TestSetupFailureIsReportedAndIsolated`, `TestSetupFailureStopsPartialWiring` |
| G2 — real reconcilers survive a second workspace (FR-005) | `task test:integration:docker` | `TestCoreManagerClusterToMachine`, second-workspace section |
| G2 — reuse is rejected (FR-003, enforceable half) | `task test:unit` | `TestStartTwiceIsRejected` |
| Webhooks refuse a second workspace (FR-008) | `task test:unit`, `task test:integration:docker` | `TestSetupWebhooksRefusesASecondWorkspace`, `TestWebhookRegistrationIsRefused` |
| Process globals hold no workspace client (FR-009) | `task test:unit` | `TestProcessGlobalsResolveWithoutAWorkspaceClient`, `TestNoReaderRefusesToRead` |
| G1/G3 decisions recorded (FR-010, FR-011) | Review | `docs/site/content/en/docs/design/per-workspace-wiring.md`, and this table |
| Whole change | `task verify` reports pass, or "could not run" naming the capability | `bin/verify-result.json` |

## Phasing

1. **Seam** — the spec, this plan, the `SetupFunc`/`ManagerGetter` contract
   and its documentation. No behaviour, so nothing to test yet; this is the
   shape Phase 3's tracks are written against.
2. **Implementation** — the wiring, the per-workspace lifecycle, the
   `coremanager` changes it forces, `cmd/core-manager`, and the tests above.
