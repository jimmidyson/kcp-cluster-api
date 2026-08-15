# kcp-cluster-api

Makes [Cluster API](https://github.com/kubernetes-sigs/cluster-api)
workspace-aware for [kcp](https://github.com/kcp-dev/kcp), so a single
control plane can reconcile Cluster API resources across many logical
clusters.

Upstream Cluster API is consumed as a version-pinned dependency, not vendored
or forked wholesale. The handful of changes we cannot yet get upstream live
in a small patched fork and are recorded in [`DRIFT.md`](DRIFT.md) with the
date each one's upstream proposal is due.

> **Status: early.** A walking skeleton reconciles `Cluster` → `Machine`
> against a real kcp server through unmodified upstream reconcilers. Known
> design corrections for multi-workspace operation are outstanding — see
> [`docs/conversion-plan.md`](docs/conversion-plan.md).

## Getting started

You need a Go toolchain and, for the integration tests, a container runtime.
Nothing else: tooling installs itself at pinned versions.

```sh
go install github.com/go-task/task/v3/cmd/task@latest

task --list      # what you can run
task check       # the fast subset: build, lint, unit tests
task verify      # everything, including integration tests
```

### `task verify` reports three outcomes, not two

| Outcome | Meaning |
|---|---|
| pass | every step in scope ran and succeeded |
| fail | a step ran and failed |
| could not run | a step was skipped: the environment lacks a capability |

A step that could not run is **never** reported as a pass. Read the outcome
from `bin/verify-result.json`, which is also what CI reads — task runners
collapse every failure to a single exit code, so the distinction does not
survive the runner.

### How long it takes

**`task verify`: 5 min 13 s.** Measured, not estimated — that is the first
green CI run of the full suite on a GitHub-hosted `ubuntu-latest` runner
([run 31891936950][first-green], commit `30ee953`), timed from the start of
the `task verify` step to its end, so it includes downloading the pinned kcp
server and starting a real one.

That figure is the budget. A change that pushes it materially higher is a
change to be argued for, not absorbed: verification has to stay inside the
feedback loop of writing code, or it gets deferred to CI and stops being a
done-condition. `task check` is the sub-minute subset for the inner loop.

[first-green]: https://github.com/jimmidyson/kcp-cluster-api/actions/runs/31891936950

## Layout

```
Taskfile.yaml     the named operations
DRIFT.md          what we carry against upstream, and why
cmd/              binaries: core-manager, verify, drift
internal/         implementation packages
test/integration/ integration tests against a real kcp server
docs/             ADRs, design notes and the documentation site
specs/            spec-driven feature specifications
```

## Documentation

Everything this project adds is documented for two audiences, in the
[Hugo](https://gohugo.io/) + [Docsy](https://www.docsy.dev/) site under
[`docs/site/`](docs/site/):

- **User docs** (`content/en/docs/user/`) — installing and running it.
- **Design docs** (`content/en/docs/design/`) — architecture and deep dives,
  for developers and agents changing the code.

A feature is not done until both are updated, or a no-op is genuinely
correct: an internal change with no user-visible behaviour still needs a
design write-up. Build the site with `task docs:build`.

## Contributing

Read [`AGENTS.md`](AGENTS.md) first — it applies equally to people and to
agents. The short version:

- Upstream is a dependency. Changes to it go in the patched fork and get an
  entry in `DRIFT.md`, with an upstream proposal due within 90 days.
- Integrate through upstream's public extension points. If something needs
  an internal, stop and raise it rather than working around it.
- New behaviour is developed test-first, with integration tests against a
  real kcp server.
- PR titles follow [Conventional Commits](https://www.conventionalcommits.org);
  PRs are squash merged, so the title becomes the commit on `main` and drives
  releases.

The governing principles are in
[`.specify/memory/constitution.md`](.specify/memory/constitution.md).
