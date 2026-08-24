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
task demo        # see it work: ready clusters in several workspaces, one manager
task check       # the fast subset: build, lint, unit tests
task verify      # everything, including integration tests
```

`task demo` is the quickest answer to what this repository does. It starts its
own single-shard kcp server, creates two workspaces bound to each provider's
`APIExport`, runs the same controllers the four manager binaries run, and
builds a cluster in each — a control plane machine and a worker apiece, waited
on until every one of them is ready. About a minute, no container runtime, no
images pulled. See [the demo docs](docs/site/content/en/docs/user/demo.md).

`task demo:kubernetes:kind` is the same demo in the topology an installation
has: a kcp shard as a `StatefulSet`, one `Deployment` per provider, and the
demo itself as a `Job` — six pods rather than one process, each manager
holding only its own credentials. That needs a container runtime, and it is
where the parts that only a deployment meets get exercised: the shard's
certificate naming its Service, credentials that exist before the shard does,
and managers that start before anything they watch exists. See
[On Kubernetes](docs/site/content/en/docs/user/kubernetes.md).

Every operation is a named `task` target, and CI invokes those same targets
and nothing else — so anything CI does is reproducible locally by name.

| Target | What it does |
|---|---|
| `demo` | Build a ready cluster in each of several kcp workspaces from one manager, in one command |
| `demo:kubernetes:kind` | The same demo on Kubernetes: a kind cluster, a kcp shard and one deployment per provider, as pods |
| `demo:kubernetes` | The same, against a cluster you already have |
| `demo:kubernetes:clean` | Remove a deployed demo — the namespace and everything in it |
| `image` | Build this repository's images — one per binary, with ko |
| `verify` | The done-condition: build, lint, unit tests, integration tests, resource sweep |
| `check` | The inner-loop subset: everything needing no container runtime |
| `build` | Compile all binaries |
| `lint` | Static analysis (`go vet`) |
| `test:unit` | Unit tests |
| `test:integration` | Integration tests against a real kcp server |
| `test:integration:kcp` | The subset needing only a kcp server, no container runtime |
| `test:integration:container-runtime` | The subset also needing a container runtime |
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
.ko.yaml          how the images are built: one per binary, no Dockerfile
AGENTS.md         the rules, for people and agents alike
DRIFT.md          what we carry against upstream, and why
cmd/              binaries: one manager per Cluster API provider
                  (core, kubeadm-bootstrap, kubeadm-control-plane,
                  dev-infrastructure), plus demo, deploy, verify, drift
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
| `docs` | pull requests touching `docs/site/`, pushes to `main` | Runs `task docs:build`; on `main` publishes the built site to [GitHub Pages](https://jimmidyson.github.io/kcp-cluster-api/) |
| `release-please` | pushes to `main` | Keeps the open release pull request current, and cuts the tag when it merges |

## Releases

Versions are derived from the Conventional Commits titles on `main` by
[release-please](https://github.com/googleapis/release-please), which is why
[the title format](AGENTS.md#pr-title-format) is enforced on every pull
request.

The **changelog** comes from elsewhere. `changelog-type` is `github`, so the
notes are GitHub's own generated release notes: a list of merged pull requests,
grouped by **label**, with contributor credit. `changelog-sections` in
`release-please-config.json` is inert while this is set.

GitHub has no way to group by commit type — `.github/release.yml` matches
labels and authors and nothing else. So the Conventional Commits title is
bridged to a label by
[`conventional-label.yaml`](.github/workflows/conventional-label.yaml), and
[`.github/release.yml`](.github/release.yml) turns those labels back into
sections. Three files have to agree:

| Title prefix | Label | Section |
|---|---|---|
| `feat` | `feature` | Features |
| `fix` | `bug` | Bug Fixes |
| `refactor` | `refactor` | Refactoring |
| `docs` | `documentation` | Documentation |
| `build` | `build` | Build and Dependencies |
| `!` / `BREAKING CHANGE` | `breaking-change` | Breaking Changes |
| `ci`, `test`, `chore`, `release` | `ignore-for-release` | excluded |

Editing one without the others loses a section quietly, which is why each file
says so at its top.

A release is started by hand and maintained by CI:

```
git checkout main
task release-please           # opens the release pull request
```

That pull request is titled `release(main): X.Y.Z`, from
`pull-request-title-pattern` — release-please's default would be
`chore(main): release X.Y.Z`. `release` is therefore in the allowed type list
in `pr-title.yaml`, without which release-please's own pull request would fail
the title check and be unable to merge. It bumps nothing on its own, which is
what a release commit should do.

Merging that pull request cuts the tag and publishes the release. Until it is
merged, the `release-please` workflow rewrites its branch, title and changelog
on every push to `main`, so it always describes everything landed so far —
there is no window in which starting a release freezes what goes into it.

That rewriting is also why the release pull request carries an Unverified
commit: release-please writes through the Git Data API, which signs nothing.
The commit that reaches `main` is signed by GitHub regardless, so this is
cosmetic — `task release-please:sign` fixes it for anyone who wants the pull
request itself to read Verified, run immediately before merging.

### Staying below 1.0.0 is a decision, not a default

This project stays pre-1.0 until somebody decides otherwise. That takes **two**
settings, because the first release and every release after it are decided by
different code.

**The first release** does not bump anything — with no release to bump from,
release-please ignores the versioning strategy and returns a fixed starting
version:

```js
protected initialReleaseVersion(): Version {
  if (this.initialVersion) {
    return Version.parse(this.initialVersion);
  }
  return Version.parse('1.0.0');   // the default
}
```

So `initial-version` is set to `0.1.0`. Without it the first release is
`1.0.0` however the rest of the config reads, and no amount of
`bump-minor-pre-major` prevents it — this repository opened a
`chore(main): release 1.0.0` pull request that way before the setting was
added. The manifest version does not help either: with no release found, this
path is taken regardless of what `.release-please-manifest.json` says.

**Every release after it** is a bump, and there `bump-minor-pre-major` governs:
while the major version is `0`, a breaking change bumps the **minor**, not the
major:

```js
if (breaking > 0) {
  if (version.isPreMajor && this.bumpMinorPreMajor) {
    return new MinorVersionUpdate();   // 0.3.0 + breaking → 0.4.0
  } else {
    return new MajorVersionUpdate();   // what happens without the flag
  }
}
```

There is no threshold, no commit count and no accumulation of breaking changes
that graduates `0.x` to `1.0.0`. So within `0.x`: a `fix` bumps the patch, a
`feat` bumps the minor, and a breaking change also bumps the minor.

**Do not remove `bump-minor-pre-major` as tidying.** It reads like a default
worth deleting, and deleting it means the next breaking change ships `1.0.0`
with nobody having chosen that. The config is JSON and cannot hold a comment,
which is why this is here.

Reaching 1.0.0 takes one deliberate act — a `Release-As:` footer in a commit
body on `main`:

```sh
git commit --allow-empty -m "chore: release 1.0.0" -m "Release-As: 1.0.0"
```

The next release pull request is then 1.0.0, and normal semantics resume from
there: `bump-minor-pre-major` only applies below 1.0.0, so breaking changes
bump the major from then on, as they should.

### Where the history starts

`main` carries 329 commits and only 38 of them are this project's: it is a fork
of [kubernetes-sigs/cluster-api](https://github.com/kubernetes-sigs/cluster-api)
and the other 291 are upstream's, which do not follow Conventional Commits and
are not ours to release. With nothing to stop at, release-please walked back
into them and pulled an upstream commit into the changelog as a feature of this
project.

So `release-please-config.json` pins `bootstrap-sha` to
`281e4e3` — the last upstream commit, one before
[`7b9ccb3`](https://github.com/jimmidyson/kcp-cluster-api/commit/7b9ccb30b1d6a9215cc06168162e409bd5347db0),
where this project's own history begins. The named commit is excluded, so the
38 that follow it are exactly what gets released. It applies only until the
first release exists and is ignored afterwards; delete it once `v0.1.0` is
tagged. The config is JSON and cannot hold a comment, which is why the
reasoning is here.

### Why CI cannot open the pull request

The split above is not a preference. GitHub forbids `GITHUB_TOKEN` from
*creating* a pull request unless "Allow GitHub Actions to create and approve
pull requests" is enabled for the repository; updating one that already exists
is allowed. So the workflow can do everything except the first step, and
`task release-please` is that step.

It runs from `main` and needs Node plus a GitHub token — set `GITHUB_TOKEN`, or
authenticate `gh` and it will use that. Neither is required by `task verify`.

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

The site is published at
<https://jimmidyson.github.io/kcp-cluster-api/> — every merge to `main` that
touches `docs/site/` deploys the build from that commit, so what is published
is whatever `main` says now.

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
