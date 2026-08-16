# Quickstart: running and reading the scale measurement

**Feature**: [spec.md](spec.md) | **Plan**: [plan.md](plan.md)

How to validate this feature end to end. The order below is the order the plan
builds in, and it is not arbitrary — **the measurement comes before the fixes it
justifies** (FR-031).

---

## Prerequisites

- Go 1.26.3 and the repository's pinned tooling: `task tools`
- A real kcp server: `task tools:kcp`. Vanilla envtest cannot validate any of
  this — it has no logical clusters, so the costs being measured cannot occur
  (Principle III).
- Container runtime for the dev infrastructure provider, as
  `task test:integration:docker` already requires.

If kcp cannot be started, every step below reports **"could not run"** rather
than passing. That is the contract, not a degraded mode.

---

## 1. Establish what the environment can host

**Do this before trusting any other number.** Resolves
[R10](research.md#r10--can-the-environment-host-a-meaningful-sweep--open).

```sh
task test:scale -- --probe-ceiling
```

Reports how many workspaces this environment can create and bind, and how long
creating them takes.

**Read it like this**: if the achievable ceiling is far below any interesting
knee, the sweep cannot locate one. Every capacity figure derived from such a run
is an **extrapolation** and must be labelled one — see the `extrapolated` field
in [contracts/capacity-report.md](contracts/capacity-report.md).

## 2. Baseline the current implementation

```sh
task test:scale -- --profile=idle-heavy   --sweep
task test:scale -- --profile=active-heavy --sweep
```

Produces, per profile: engagement latency, per-event delivery cost,
per-workspace footprint, throughput, and the knee per FR-030's procedure —
together with the tolerance and point count used, so the run is reproducible.

Validates **SC-001**.

## 3. Read the gate

The baseline decides how much of this feature gets built. For each of the eight
gated requirements — FR-001, FR-003, FR-004, FR-005, FR-006, FR-008, FR-009,
FR-011 — record `build` or `close` with the figures either way.

```sh
cat bin/verify-result.json     # outcome: pass | fail | could not run
```

Take the outcome from the JSON, not the exit status: task runners collapse every
failure to one code, so the three-outcome distinction does not survive `task`.
This is the same instruction AGENTS.md already gives for `task verify`.

Validates **SC-013**. **Nothing gated may be implemented before this exists.**

A `close` verdict on most of the list is a good outcome — it means the costs
were measured and found not to bind. Building anyway would violate Principle
VIII.

## 4. Verify the unconditional work

Independent of the gate, because each is wrong at any workspace count:

```sh
task test:unit
task test:integration          # includes engagement retry and churn
```

- **SC-014** — a transiently failing engagement recovers automatically, without
  waiting for the ~10h resync.
- **SC-009** — sustained bind/unbind churn leaves goroutines, memory and
  telemetry series flat.
- **SC-008** — a workspace generating disproportionate load is identifiable from
  telemetry alone, with bounded cardinality.
- **SC-015** — aggregate request rate respects its ceiling and backs off under
  shard pressure.

## 5. Verify built gated work, if any

For each requirement the gate marked `build`, re-run **the same profile on the
same harness** and compare against the step 2 baseline:

```sh
task test:scale -- --profile=idle-heavy --sweep --compare=<baseline>
```

- **SC-002** — per-event delivery cost flat across an order of magnitude.
- **SC-003** — time to first reconcile flat across an order of magnitude.
- **SC-004** — no delivery pause attributable to another workspace's join.
- **SC-005** — cold start no worse than linear.
- **SC-006** — idle per-workspace footprint within the stated budget.
- **SC-010** — before/after evidence from the same harness and profile.

A before/after pair drawn from different profiles is not evidence.

**The isolation check is not optional and is not gated**: FR-002 asserts no
workspace observes another's events, whatever routing is adopted. It is verified
at the integration tier against real kcp, not only in unit tests.

## 6. Verify horizontal scale

```sh
task test:integration -- -run TestSharding
```

- Every workspace reconciled by exactly one replica.
- Throughput rises with replica count — replicas are capacity, not standby.
- A killed replica's share is taken over within the stated bound.
- **No workspace is ever actively reconciled by two replicas**, including during
  handover and when a replica is partitioned from its peers but can still reach
  the shard.

Validates **SC-007**. Its entry criterion is
[R3](research.md#r3--the-sharded-coordinator-exists-and-is-pinned--verified-behaviour-assumed)'s
fencing spike — the fencing claim is `ASSUMED` until this test demonstrates it.

## 7. Confirm nothing regressed

```sh
task verify
cat bin/verify-result.json
task drift
```

- **SC-011** — the done-condition passes unchanged, with no new drift entry.
- **SC-012** — one- and two-workspace deployments behave as they do today.

`task drift` must stay clean: this feature is delivered entirely through public
extension points (FR-023), so it adds no carried patch.

---

## Reading a result honestly

Three failure modes this feature is specifically written against:

1. **Treating "could not run" as a pass.** An unreachable workspace count is not
   a success. Read `bin/verify-result.json`.
2. **Weakening an assertion to get green.** If a bound cannot be met, that is
   the finding to report — Principle IV, and the constitution names a past
   incident where a test asserted reconciliation "got past" a failure rather
   than reaching the intended state.
3. **Presenting an extrapolation as a measurement.** If the sweep did not reach
   the range a capacity figure describes, the figure says so.
