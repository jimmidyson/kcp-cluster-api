# Contract: scale measurement harness

**Status**: **UNCONDITIONAL.** Everything else in this feature depends on it.
Built in P2, after [R10](../research.md#r10--can-the-environment-host-a-meaningful-sweep--open)
establishes what the environment can host.

**Requirements**: FR-020, FR-021, FR-022, FR-030 | **Criteria**: SC-001, SC-013

---

## The named operation

```sh
task test:scale
```

Joins the existing task surface alongside `test:unit`, `test:integration` and
`verify`. Runs against a **real kcp server** — Principle III forbids validating
workspace-shaped behaviour on vanilla envtest, which has no logical clusters and
therefore cannot exhibit the costs being measured.

Configuration (profile, workspace counts, tolerance, duration) is passed as task
variables so a run is reproducible from its recorded parameters.

## Reporting: reuse, do not reinvent

The harness reports through `internal/verify`'s existing types, verified present
in [R11](../research.md#r11--reuse-the-existing-three-outcome-contract--verified)
at `internal/verify/verify.go:49-110`:

```go
type Outcome int   // OutcomePass | OutcomeFail | OutcomeCouldNotRun
type Capability struct{ … }
type Step struct{ … }
type Result struct{ Outcome Outcome; Err error; … }
```

**"Could not run" is a first-class outcome, not a soft pass** (FR-022,
Principle IV). Specifically:

- A requested workspace count the environment cannot create → `could not run`.
- A sweep whose range never reaches a departure point → the departure point is `could not run`, and
  any capacity derived from it is an extrapolation and is labelled one.
- Never report a pass because the harness ran without crashing.

AGENTS.md's existing instruction — read the outcome from
`bin/verify-result.json`, not the exit status, because task runners collapse
failures to one code — applies unchanged.

## Measured quantities

Per FR-020, at each swept point:

| Quantity | Why | Feeds |
|---|---|---|
| Engagement latency (p50/p99) | Time to first reconcile for a joining workspace | SC-003 |
| Per-event delivery cost | Work to route one change to the controllers wanting it | SC-002 |
| Per-workspace footprint | Resident memory and goroutines attributable to one workspace | SC-006 |
| Throughput | Reconciles completed per unit time under the profile's event rate | SC-007 |
| Delivery pause during join | Whether an engagement stalls others | SC-004 |
| Cold start duration | Whole-shard engagement from empty | SC-005 |

Per-workspace attribution depends on P1's telemetry work — the harness cannot
report load the process does not expose.

## Departure detection (FR-030)

A defined, repeatable procedure, **not inspection**:

1. Sweep at least *k* geometrically spaced workspace counts on one profile
   (*k* set in P2 from what R10 shows achievable; recorded with the result).
2. Project linearly from the two smallest points.
3. The **departure point** is the smallest swept count at which any measured quantity
   exceeds that projection by more than the stated tolerance.
4. Record tolerance and point count with the result.

Two runs of one profile must yield the same departure point. If they do not, the procedure
is underspecified and that is a finding, not noise to average away.

## Profiles

At minimum the two from FR-026's resolution of Q1 — `idle-heavy` and
`active-heavy`. Capacity is stated per profile because idle and active
workspaces are not interchangeable units of load; an operator matches their own
shard against the nearer one.

## Before/after evidence (FR-021, SC-010)

Every numeric bound this feature claims comes from this harness. For each cost
the feature sets out to remove, evidence is recorded from **the same harness on
the same profile**, before and after. A before/after pair from different
profiles is not evidence.

Baselines are committed alongside the determinations they justify, so a reviewer
can check the acceptance condition actually ran — which the constitution's
Development Workflow requires of review.

## Non-goals

- **Not a benchmark suite for CI gating.** Its output is evidence for decisions,
  not a pass/fail on every pull request. Wiring it into `task verify` as a
  blocking step would make unrelated changes hostage to a multi-workspace kcp
  environment.
- **Not a load generator for kcp.** It measures `core-manager`, and reports
  honestly when the shard rather than the process is the constraint.
