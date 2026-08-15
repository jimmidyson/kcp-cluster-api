# Quickstart: validating the inversion

**Feature**: [spec.md](./spec.md) | **Date**: 2026-08-15

Runnable checks that prove this feature works. Each maps to a user story and
is worded so its result is an exit status, not an opinion — per Constitution
Principle IV.

## Prerequisites

| Requirement | Needed for |
|---|---|
| Go toolchain (1.26+) | everything |
| Container runtime | scenarios 2b, 4 |
| Network access to container image source | scenarios 2b, 4 |
| Fork tagged `v1.14.1-kcp.1` with the public resolver seam | everything — hard prerequisite |

Nothing else. If any scenario below requires a setup step not listed here,
that is itself a failure of FR-008.

---

## Scenario 1 — Upstream is absent (User Story 1)

**No upstream file is tracked** (SC-001, FR-001):

```sh
git ls-files | grep -E '^(api|bootstrap|cmd/clusterctl|controllers|controlplane|core|exp|feature|internal/(contract|controllers)|util|version)/' 
```

Expect: no output. Any line is a failure.

**The project builds with no co-located upstream tree**:

```sh
go build ./...
```

Expect: exit 0.

**No internal upstream package is imported** (FR-004):

```sh
go list -deps ./... | grep 'sigs.k8s.io/cluster-api/internal'
```

Expect: no output.

**The dependency resolves to an immutable fork tag** (FR-003):

```sh
go list -m -f '{{.Path}} => {{with .Replace}}{{.Path}} {{.Version}}{{end}}' sigs.k8s.io/cluster-api
```

Expect: a replacement at a tagged version, not a branch or a filesystem path.

---

## Scenario 2 — One command decides done (User Story 2)

### 2a. Fast subset, no container runtime needed

From a clean checkout with an empty module cache:

```sh
task check
```

Expect: exit 0, and completion within the 60 s warm budget (NFR-002). On a
cold cache the module download dominates; the budget is a warm figure.

### 2b. Full verification

```sh
task verify
```

Expect: exit 0 within the documented budget (NFR-001). On the first green CI
run, record the measured duration — that measurement *is* the budget, and
recording it is a task, not a footnote.

### 2c. Tooling installs itself

On a machine with no project tooling present:

```sh
rm -rf bin/ && task check
```

Expect: exit 0. Tooling is installed at pinned versions with no manual step
(FR-007 – FR-009).

### 2d. Second run reuses tooling (NFR-003)

```sh
task check && task check
```

Expect: the second run performs no downloads or rebuilds of tooling.

---

## Scenario 3 — The missing-capability outcome (SC-009, FR-012, FR-013)

The most important scenario here, because it is the one the project has
previously got wrong.

On a machine with **no container runtime**:

```sh
task verify; echo "exit=$?"
```

Expect, all of:

- exit status is **not** 0;
- exit status is **distinct** from the status produced by a genuine test
  failure;
- output names the missing capability (container runtime);
- the summary lists the integration step and marks it as not run — it is not
  silently omitted;
- the capability was reported **before** the integration step began, not
  after it failed part-way (FR-013).

A green result here is a failure of the feature, not a pass.

---

## Scenario 4 — CI runs what you run (User Story 3, SC-004)

```sh
grep -E 'run:' .github/workflows/*.yaml
```

Expect: every `run:` step is an invocation of a target from
[contracts/task-surface.md](./contracts/task-surface.md), plus checkout,
toolchain setup and reporting. Any other logic is a violation of FR-014.

```sh
ls .github/workflows/
```

Expect: only this project's own workflows (FR-015). Nine inherited files are
gone.

**PR-title check survives** (Constitution VII, research R6): open a pull
request with a non-compliant title and confirm it fails.

---

## Scenario 5 — Drift is measured (User Story 4)

```sh
task drift
```

Expect: exit 0, and a report of the fork's differences against its base
commit matching `DRIFT.md` exactly.

Negative case — add an unrecorded change to the fork branch and re-run:

Expect: non-zero exit, naming the unexpected path.

---

## Scenario 6 — Behaviour is unchanged (spec: Behavioural equivalence)

The existing suite is the definition of correct behaviour for this feature.

```sh
task test:unit && task test:integration
```

Expect: the same tests pass as before the inversion, with assertions no
weaker. A test modified to accommodate the move must be inspected: weakening
one to get a green run is a failure of this work, not a workaround.

---

## What "done" means

All six scenarios pass on a machine with a container runtime, and scenario 3
passes on one without. Scenario 2b's duration is recorded in the
documentation as the verification budget.
