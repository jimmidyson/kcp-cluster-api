# kcp-cluster-api

Runs [Cluster API](https://github.com/kubernetes-sigs/cluster-api) on
[kcp](https://github.com/kcp-dev/kcp). One manager reconciles Cluster API
resources in every workspace bound to its export.

Early stage: there is no released binary or container image yet. Build from
source, or run the demo. Docs are published at
<https://jimmidyson.github.io/kcp-cluster-api/>.

## Try it

Go (the version in [`go.mod`](go.mod)) and [task](https://taskfile.dev/). The
demo starts its own kcp server.

```sh
go install github.com/go-task/task/v3/cmd/task@latest

task demo    # a ready cluster in each of two workspaces, from one manager
```

About a minute. No container runtime, no images pulled. What the run does is
in [the demo docs](docs/site/content/en/docs/user/demo.md). `task --list`
shows every other operation; `task verify` is the full check.

## Run it

Each provider is its own process. Core:

```sh
go build -o bin/core-manager ./cmd/core-manager

bin/core-manager \
  --kubeconfig ~/.kube/kcp.kubeconfig \
  --endpoint-slice-name cluster-api-core
```

No workspace is named. Every workspace whose binding to that export is ready
is reconciled. Prerequisites, the other providers, and webhooks are in
[Installation](docs/site/content/en/docs/user/installation.md). Getting a
workspace onto the providers is
[Onboarding](docs/site/content/en/docs/user/onboarding.md).

## Documentation

- [User docs](https://jimmidyson.github.io/kcp-cluster-api/docs/user/) — install, run, onboard a workspace.
- [Design](https://jimmidyson.github.io/kcp-cluster-api/docs/design/) — how it is built.

Source is [`docs/site/`](docs/site/). `task docs:build` builds the site.

## Contributing

Working rules are in [`AGENTS.md`](AGENTS.md). Principles are in
[`.specify/memory/constitution.md`](.specify/memory/constitution.md).

Upstream Cluster API is a pinned dependency, resolved to a small patched
fork. What the fork carries is [`DRIFT.md`](DRIFT.md). New behaviour is
test-first, against a real kcp server. Pull requests are squash merged, and
the title — a [Conventional Commit](https://www.conventionalcommits.org) —
becomes the commit on `main`.
