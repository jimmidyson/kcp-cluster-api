# Contract: the service-characterisation seam

**Status**: **UNCONDITIONAL**, and deliberately *not* a general-purpose utility.
Built in P2 alongside the harness.

**Requirements**: FR-038, FR-039 | **Criteria**: SC-019, SC-020 |
**Architecture**: [ADR-0002](../../../docs/adr-0002-shard-appliance-scaling.md) A5

---

## What this is, and what it is not

The intent recorded in ADR-0002 A5 is a utility that can be pointed at any
controller to gather its scaling characteristics and thresholds. **This contract
does not build that utility.** There is one controller today; Constitution
Principle VIII prohibits the abstraction before a second real caller.

What it builds is the **seam** that makes the utility cheap later: the
service-agnostic machinery separated from the service-specific parts by a narrow
interface. **Trigger to generalise: the conversion plan's P1**, the bootstrap
provider port — a real planned caller, not a hypothetical one.

## The split

**Service-agnostic** — written once, reused unchanged for every future service:

- the sweep across geometrically spaced workspace counts
- knee detection (FR-030's defined procedure)
- coefficient fitting and held-out validation (FR-034, FR-035)
- reporting through `internal/verify`'s three-outcome contract

This is the larger half, and it is service-agnostic because the cost structure
is a property of the architecture rather than of any controller: listener count,
cached objects, workers and dispatch cost have the same *form* for anything
built on `providerwiring`'s `SetupFunc` seam. Only coefficients differ.

**Service-specific** — supplied per controller, behind the interface:

| Responsibility | Why it cannot be generic |
|---|---|
| Construct a profile's objects | A `Cluster` is not a `Database`. Object shape and the references between them are service knowledge |
| Report the watch set | Which GVKs this controller actually watches, which sets the listener term |
| Report engaged workspaces | Needs the controller's own view of what it has wired |

## Required properties

| # | Property | Requirement | Verified by |
|---|---|---|---|
| S1 | The sweep, fit and knee machinery contains no reference to Cluster API types | FR-038 | Compile-time: the agnostic package does not import CAPI |
| S2 | A second implementation of the interface, constructing different objects, drives the same machinery unchanged | FR-038, SC-019 | A test implementation exercised in unit tests |
| S3 | Every reported figure records synthetic or observed | FR-039, SC-020 | Unit test asserting the mode is non-empty on every emitted figure |
| S4 | A synthetic figure is distinguishable from an observed one at the point of publication, not only internally | FR-039 | The published tables carry the mode |

**S2 is the whole point of the contract.** A seam with one implementation is an
assertion; a seam with two is evidence. The second implementation lives in tests
rather than in production code, because a second *real* controller does not exist
yet — that is P1.

## The two modes

**Synthetic.** Objects are generated for the sweep — for a future service, from
its `APIResourceSchema` OpenAPI. Available before a service has users, which is
when planning matters most.

Its weakness is real and must be surfaced, not managed away: generated objects
may fail validation or take cheap error paths instead of genuine reconcile
paths, so synthetic load can **under-measure**. A sizing figure derived from
synthetic load and not labelled as such is worse than no figure, because it
invites a memory limit that will not hold.

**Observed.** Coefficients fitted from a running deployment's natural variation
in workspace and object counts. Always measures real work; yields nothing for a
service not yet deployed, or a fleet that does not vary.

Neither dominates. Both are supported, and FR-039 requires the mode travel with
the number.

## Non-goals

- **Not a general-purpose utility.** See above. FR-038 states the trigger.
- **Not a characteriser for controllers outside this project's seam.** A
  controller not built on `providerwiring` would fall back to generic
  controller-runtime metrics and a weaker model. Supporting that is not in scope
  and has no caller.
- **Not a replacement for measuring the real service.** A synthetic
  characterisation is a planning input, not an acceptance of capacity.
