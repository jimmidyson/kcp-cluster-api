# Contract: capacity report and gate determinations

**Status**: **UNCONDITIONAL.** The feature's primary deliverable — the
deployment model reaches its target by composing bounded shards, so the bound is
the product.

**Requirements**: FR-026, FR-027, FR-028, FR-029, FR-031 | **Criteria**: SC-013

---

## Two artifacts

### 1. The published capacity report (FR-026, FR-027)

Lives in `docs/site/content/en/docs/user/capacity-planning.md` — user
documentation, because its audience is an operator planning a regional shard,
not a developer changing the code.

One entry per profile, stated in **the units that actually consume capacity**:

| Field | Example shape | Why |
|---|---|---|
| Profile | `idle-heavy` | Capacity is not one number (FR-026) |
| Watched objects | count | A real consumer of cache and dispatch |
| Sustained event rate | rate | The other real consumer |
| Workspace guidance | derived count | **Secondary**, derived from the above (FR-027) |
| Derived from | sweep run, tolerance, point count | Reproducibility |
| Headroom | margin below the departure point | The figure is not the departure point |
| Extrapolated? | yes/no | Honesty about what was measured |

Workspace count is deliberately the *derived* figure. An operator can check
watched objects and event rate against their own shard; a raw workspace count
silently assumes a shape their fleet may not have.

### 2. The gate determinations (FR-031)

Eight records, one per gated requirement — FR-001, FR-003, FR-004, FR-005,
FR-006, FR-008, FR-009, FR-011 — committed in the feature directory before P5
begins.

```text
requirement: FR-001
verdict:     build | close
evidence:    the measurements supporting it, either way
trigger:     (close only) what would reopen this — FR-025
```

**A `close` verdict is a successful outcome.** It records that a cost was
measured and found not to bind below a capacity anyone would configure, which is
Principle VIII applied to this feature's own contents. Closing seven of eight
would be a good result, not a failed project.

**Review obligation**: the constitution requires review to check that an
acceptance condition actually ran and actually passed, and to treat a weakened
assertion as a finding. For determinations that means a reviewer checks the
evidence exists and supports the verdict — a `close` with no figures is a
finding, not a decision.

## Runtime reporting (FR-028, FR-029)

The published figure is useless if a running process cannot be checked against
it. So `core-manager`:

- reports its own position against configured capacity — engaged workspaces,
  watched objects, observed event rate, as a fraction of the stated bound;
- makes exceeding it **observable**, not inferred from degradation.

**What it explicitly does not do** (FR-029): claim to *enforce* the limit.
Workspace placement onto shards is kcp's, and admission-time enforcement needs
G4, which is unbuilt. Refusing to engage a workspace over the limit would also
mean a bound workspace silently not reconciling — a worse failure than a
reported overrun. So: state, report, and degrade observably.

## Relationship to `task verify`

The capacity report is **not** a `task verify` step. Verify is the
done-condition for a change; capacity is a measured property of a release,
established deliberately in an environment that can host the sweep. Making
verify depend on a multi-workspace kcp would hold every unrelated change hostage
to that environment — the same reasoning `DRIFT.md` records for keeping the
drift check off every pull request.

SC-011 does require `task verify` to keep passing unchanged, with no new drift
entry.
