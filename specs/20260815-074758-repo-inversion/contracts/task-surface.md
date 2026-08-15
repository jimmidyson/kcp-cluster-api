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
| `verify` | **The done-condition.** Tools, lint, build, unit tests, integration tests. This is what CI runs and what delegated work is graded by | yes | see NFR-001; measured from first CI run |
| `check` | The fast subset of `verify`: everything that needs no container runtime or external service | no | ≤ 60 s warm |
| `build` | Compile all binaries | no | — |
| `test:unit` | Unit tests only | no | ≤ 10 s warm |
| `test:integration` | Integration tests against a real kcp server | yes | — |
| `lint` | Static analysis | no | — |
| `tools` | Install pinned tooling into `bin/` | no | — |
| `drift` | Report the fork's divergence from its base and compare against `DRIFT.md` | no | — |

There is deliberately no `generate` target. Resource definitions are resolved
from the pinned dependency rather than generated into the repository
(spec FR-005/FR-006), so there is no derived artifact to regenerate and
nothing that can go stale. A generation target returns if and when deployable
manifests are needed — see the spec's Deferred section.

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

**The exit statuses above hold when the harness is invoked directly. They do
not survive a task runner.** go-task collapses every failing task to exit
code `201` regardless of what the command returned — verified against exit
codes 1, 2 and 7, all of which produced `201`. So a caller invoking
`task verify` can distinguish pass from not-pass and nothing more.

The outcome is therefore also written to a machine-readable report
(`bin/verify-result.json` by default, `--report` to change it):

```json
{ "status": "could-not-run", "exitCode": 2,
  "steps": [ { "step": "test:integration", "outcome": "could not run",
               "missingCapability": "container runtime", "reason": "..." } ] }
```

CI MUST read `status` from that file rather than inferring the outcome from
the runner's exit code. This is what satisfies "detectable by automation
without reading logs" (FR-012) in the presence of a runner that discards the
distinction.

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

A capability is something **the environment provides and this project cannot
install for itself**. That distinction is load-bearing: modelling an
installable dependency as a capability deadlocks the run, because the check
gates the step whose own setup would install it.

| Capability | Required by | Detection |
|---|---|---|
| Container runtime | `test:integration`, `verify` | socket present, or `DOCKER_HOST` set |

Deliberately **not** capabilities:

- **The kcp server binary.** `task tools` downloads it, and `test:integration`
  depends on that target. It was briefly modelled as a capability and CI
  caught the deadlock immediately: the check reported "could not run — kcp
  server binary not found; run `task tools`" on a runner where `task tools`
  was exactly what the blocked step would have done.
- **Container image reachability.** Not separately checked. A failed image
  pull surfaces as a test failure today; if that proves confusing in
  practice, it can become a capability then.

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
