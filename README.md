# kcp-cluster-api

Makes [Cluster API](https://github.com/kubernetes-sigs/cluster-api)
workspace-aware for [kcp](https://github.com/kcp-dev/kcp), so a single
control plane can reconcile Cluster API resources across many logical
clusters.

Upstream Cluster API is consumed as a version-pinned dependency, not vendored
or forked wholesale: `go.mod` requires `sigs.k8s.io/cluster-api` (plus its
`api` and `test` modules) at `v1.15.0-kcp.1` and resolves it to a small
patched fork, [`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api).
Everything the fork carries is recorded in [`DRIFT.md`](DRIFT.md) with the
date each one's upstream proposal is due, and `task drift` fails if reality
and that record disagree.

> **Status: early.** `cmd/core-manager` discovers workspaces through an
> `APIExportEndpointSlice` using
> [multicluster-provider](https://github.com/kcp-dev/multicluster-provider)
> and [multicluster-runtime](https://sigs.k8s.io/multicluster-runtime), and
> wires unmodified upstream `Cluster`/`Machine` reconcilers — and the docker
> provider's `DevCluster`/`DevMachine` reconcilers — onto *every* workspace
> bound to the export, as it binds. Webhooks are the exception: they are
> served for one workspace or none, because routing an admission request to
> its own workspace is not built yet. See
> [Per-workspace wiring](docs/site/content/en/docs/design/per-workspace-wiring.md)
> for the contract and its remaining gaps,
> [`docs/conversion-plan.md`](docs/conversion-plan.md) for what comes next,
> and [`docs/adr-0001-per-workspace-manager-pool.md`](docs/adr-0001-per-workspace-manager-pool.md)
> for the decisions underneath.

## Getting started

You need a Go toolchain (the version in [`go.mod`](go.mod)) and, for the
integration tests, a container runtime. Nothing else: tooling installs itself
at pinned versions — `task test:integration` downloads the pinned kcp server
binary into `bin/` first.

```sh
go install github.com/go-task/task/v3/cmd/task@latest

task --list      # what you can run
task demo        # see it work: clusters in several workspaces, one manager
task check       # the fast subset: build, lint, unit tests
task verify      # everything, including integration tests
```

`task demo` is the quickest answer to what this repository does. It starts its
own single-shard kcp server, creates two workspaces bound to the Cluster API
`APIExport`, runs the same controllers `core-manager` runs, and provisions a
cluster in each — about a minute, no container runtime, no images pulled. See
[the demo docs](docs/site/content/en/docs/user/demo.md).

Every operation is a named `task` target, and CI invokes those same targets
and nothing else — so anything CI does is reproducible locally by name.

| Target | What it does |
|---|---|
| `demo` | Provision clusters across several kcp workspaces from one manager, in one command |
| `verify` | The done-condition: build, lint, unit tests, integration tests, resource sweep |
| `check` | The inner-loop subset: everything needing no container runtime |
| `build` | Compile all binaries |
| `lint` | Static analysis (`go vet`) |
| `test:unit` | Unit tests |
| `test:integration` | Integration tests against a real kcp server |
| `test:integration:kcp` | The subset needing only a kcp server, no container runtime |
| `test:integration:docker` | The subset also needing a container runtime |
| `test:sweep` | Measure per-workspace resource usage against a real kcp server |
| `drift` | Check the fork's divergence from its base against `DRIFT.md` |
| `tools` | Install pinned tooling (the kcp server binary) into `bin/` |
| `docs:build` | Build the documentation site |
| `clean` | Remove `bin/` |

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
AGENTS.md         the rules, for people and agents alike
DRIFT.md          what we carry against upstream, and why
cmd/              binaries: core-manager, kubeadm-bootstrap-manager, demo,
                  verify, drift
internal/         implementation packages
test/integration/ integration tests against a real kcp server
docs/             ADRs, design notes and the documentation site
specs/            spec-driven feature specifications
.specify/         Spec Kit state, constitution and extensions
```

## Continuous integration

| Workflow | When | What |
|---|---|---|
| `pr` | pull requests, pushes to `main` | Runs `task verify`, then reads `bin/verify-result.json`: "could not run" fails the job rather than passing it |
| `pr title` | pull requests, including edits | Enforces the Conventional Commits title format |
| `drift` | daily, on demand, and on PRs touching the pin or the record | Runs `task drift` |
| `docs` | pull requests touching `docs/site/` | Runs `task docs:build` |
| `release-please` | pushes to `main` | Opens release PRs and cuts tags from the squashed commit titles |

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
  entry in [`DRIFT.md`](DRIFT.md), with an upstream proposal due within 90
  days.
- Integrate through upstream's public extension points. If something needs
  an internal, stop and raise it rather than working around it.
- New behaviour is developed test-first, with integration tests against a
  real kcp server — never a vanilla envtest apiserver, which has no
  workspace support. See [`docs/testing.md`](docs/testing.md).
- Do not weaken an assertion to get a green run. If the assertion cannot be
  met, that is the finding to report.
- PR titles follow [Conventional Commits](https://www.conventionalcommits.org);
  PRs are squash merged, so the title becomes the commit on `main` and drives
  releases. Keep open branches current by rebasing, not by merging `main` in.

The governing principles are in
[`.specify/memory/constitution.md`](.specify/memory/constitution.md).
