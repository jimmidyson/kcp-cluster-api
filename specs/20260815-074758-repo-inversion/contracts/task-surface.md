# Contract: Named operation surface

**Feature**: [../spec.md](../spec.md) | **Satisfies**: FR-007 – FR-014, NFR-001 – NFR-003

This is the interface this project exposes to contributors, agents and
automation. It is a contract in the sense that matters here: CI invokes these
names, documentation refers to these names, and delegated work is graded by
one of them. Renaming or changing the meaning of an entry is a breaking
change to how work is handed off.

## Invocation

```
task <name>
```

Tooling is installed on demand into a repository-local `bin/` at pinned
versions. No target may require a system package manager, an environment
manager, or any prior setup beyond a Go toolchain and — for the targets
marked *needs container runtime* — a working container runtime.

## Targets

| Name | Purpose | Needs container runtime | Budget |
|---|---|---|---|
| `verify` | **The done-condition.** Tools, generation check, lint, build, unit tests, integration tests. This is what CI runs and what delegated work is graded by | yes | see NFR-001; measured from first CI run |
| `check` | The fast subset of `verify`: everything that needs no container runtime or external service | no | ≤ 60 s warm |
| `build` | Compile all binaries | no | — |
| `test:unit` | Unit tests only | no | ≤ 10 s warm |
| `test:integration` | Integration tests against a real kcp server | yes | — |
| `lint` | Static analysis | no | — |
| `generate` | Regenerate any derived artifact in the repository | no | — |
| `generate:check` | Fail if a derived artifact is out of date | no | — |
| `tools` | Install pinned tooling into `bin/` | no | — |
| `drift` | Report the fork's divergence from its base and compare against `DRIFT.md` | no | — |

`verify` MUST be defined as the composition of the other targets, not as a
reimplementation of them, so that a contributor running a subset runs exactly
what `verify` runs.

## Outcome contract

Three outcomes, not two. This is the part of the contract that exists because
the project has already shipped a test that reported a weaker result as
success (Constitution Principle IV).

| Outcome | Exit status | Meaning |
|---|---|---|
| Pass | `0` | Every step in scope ran and succeeded |
| Fail | non-zero | A step ran and failed |
| Could not run | non-zero, distinct from fail | A step was skipped because the environment lacks a required capability |

Rules:

1. A skipped step MUST NOT produce exit status `0`.
2. "Could not run" MUST be distinguishable from "fail" by automation, without
   parsing logs, and MUST name the missing capability.
3. Capability checks MUST run before the steps that depend on them, so an
   unmet capability is reported before work starts rather than part-way
   through (FR-013).
4. A summary MUST list every step and its outcome. A step that did not run
   MUST appear in that summary; silent omission is the failure mode this
   contract exists to prevent.

### Known capability requirements

| Capability | Required by | Detection |
|---|---|---|
| Container runtime | `test:integration`, `verify` | runtime responds to a version query |
| Container image source reachable | `test:integration`, `verify` | images resolvable |
| kcp server binary | `test:integration`, `verify` | present in `bin/` at the pinned version, or downloadable |

## CI contract

CI MUST invoke these names and MUST NOT reimplement their logic (FR-014). A
workflow step whose body is anything other than an invocation of a target
here — plus checkout, toolchain setup and result reporting — is a violation:
it creates behaviour that exists only in automation and cannot be reproduced
or debugged locally.

## Stability

The three outcome statuses and the names `verify` and `check` are the load-
bearing parts. Everything else may be added to freely; renaming or removing
an existing entry requires updating documentation, CI, and any delegated-work
instructions that reference it in the same change.
