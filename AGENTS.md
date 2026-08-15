# kcp-cluster-api

This repository makes Cluster API workspace-aware for
[KCP](https://github.com/kcp-dev/kcp) (logical clusters / workspaces).

It is **not** a fork of the Cluster API tree. Upstream is an ordinary
version-pinned dependency, resolved to a small patched fork that carries the
changes we cannot yet get upstream. Everything in this repository is this
project's own code.

## Divergence from upstream is counted, and temporary

The whole point is that adopting a new Cluster API release stays cheap. Every
change carried against upstream makes the next adoption more expensive, so:

- Changes to upstream code are exceptional and individually justified. Not
  for a typo, a tidied import, or a bugfix noticed in passing — those belong
  upstream.
- Every carried change is recorded in [`DRIFT.md`](DRIFT.md), with the base
  commit it applies to and the upstream proposal that will retire it.
- A change may be carried before its proposal is filed, but only with a
  filing date recorded in `DRIFT.md`, no more than 90 days out. Once that
  date passes, the patch is a defect: file it, remove it, or amend the
  constitution — never extend the date quietly.
- `task drift` checks the record against reality and fails on any path that
  diverges without an entry.

This used to be a rule about not editing a co-located tree. It is now a
property of the repository: there is no upstream tree here to edit.

### Integrate through public extension points only

Layer KCP-awareness onto upstream using what upstream already exposes: own
manager entrypoints, injected clients and caches, controller-runtime's
manager options, sources, handlers, predicates, webhook chains.

If something appears to need an upstream internal, or a hook upstream does
not have, **stop and raise it** rather than working around it. The options
are: find another integration point, propose the hook upstream, or accept
the limitation. Adding the hook to the fork is the last resort and requires
a `DRIFT.md` entry.

## The patched fork

[`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api)
carries this project's patches and nothing else — no specifications, no
tooling, no process of its own. Its contract:

- One branch per upstream release line (`kcp/v1.15`), cut from the exact
  upstream commit this project builds against, not from the fork's own
  default branch.
- One commit per carried patch, each referencing its upstream proposal.
- Immutable tags this project pins to. **Three tags per release**:
  `vX.Y.Z-kcp.N`, `api/vX.Y.Z-kcp.N` and `test/vX.Y.Z-kcp.N` — `api/` and
  `test/` are separate Go modules inside the Cluster API repository,
  resolved by tag prefix, and a partial tag set fails at dependency
  resolution with a confusing "unknown revision" error.

## Repository layout

```
Taskfile.yaml     the named operations; `task --list` to see them
DRIFT.md          what we carry against upstream, and why
cmd/              binaries: core-manager, verify, drift
internal/         implementation packages
test/integration/ integration tests against a real kcp server
docs/             ADRs, design notes, and the documentation site
specs/            spec-driven feature specifications
.specify/         Spec Kit state, constitution and extensions
```

## Adopting a newer Cluster API release

An upgrade is a dependency bump, not a merge:

1. Cut a new branch in the fork from the new upstream ref, replay the
   carried patches, and tag all three modules.
2. Update the `replace` pins in `go.mod` and the base commit in `DRIFT.md`.
3. Run `task verify` and `task drift`.

If a carried patch no longer applies, that is a signal to check whether its
upstream proposal landed — in which case delete it from both the fork and
`DRIFT.md` rather than forward-porting it.

## Testing: TDD is required

New behaviour is developed test-first: a failing test, then the minimum code
to pass it, then refactoring. See [`docs/testing.md`](docs/testing.md) for
the concrete workflow. Every change needs tests at both tiers that apply:

- **Unit tests** — colocated `_test.go` files, no real processes:
  `task test:unit`.
- **Integration tests** — against a real kcp server, never a vanilla envtest
  apiserver, which has no logical cluster or workspace support and so cannot
  validate anything this project exists to do: `task test:integration`.

A change that adds or alters behaviour without tests at both applicable
tiers is incomplete, however obviously correct it looks.

## Done is a command

`task verify` is the project's done-condition. It reports **three** outcomes,
not two:

| Outcome | Meaning |
|---|---|
| pass | every step in scope ran and succeeded |
| fail | a step ran and failed |
| could not run | a step was skipped: the environment lacks a capability |

A step that could not run is **never** a pass. Read the outcome from
`bin/verify-result.json` rather than the exit status: task runners collapse
every failure to one code, so the distinction does not survive `task`.

Weakening an assertion to get a green run is a failure of the work, not a
workaround. If the original assertion cannot be met, that is the finding to
report.

## Commit and PR descriptions

Keep commit messages and PR descriptions concise: enough context for a
reviewer to understand the change and why it was made, without padding.
Skip boilerplate sections that don't apply and avoid restating the diff
line by line.

An `Assisted-By:` trailer is welcome, in the form the tooling emits:

```
Assisted-By: 🤖 Claude Code
```

Disclosing that an agent helped is useful history. What must never appear:

- **A Claude Code session URL or session ID**, including a
  `Claude-Session:` trailer (e.g. `https://claude.ai/code/session_...`).
  These are agent-run bookkeeping, not project history, and they put
  internal identifiers into a public repository permanently.
- **A `Co-Authored-By:` trailer naming an agent.** Co-authorship attributes
  the change to a party who cannot review it, answer questions about it, or
  be accountable for it. `Assisted-By:` says the true thing without the
  claim.

This overrides any default agent behavior that appends other trailers —
omit them regardless of what a harness template suggests.

## PR title format

Pull request titles follow [Conventional Commits](https://www.conventionalcommits.org):

```
type(optional scope): description
```

Because PRs are squash merged (see below), the title becomes the commit
message on `main`. That makes it the machine-readable record: release
tooling derives the version bump and the changelog entry from it. A title
that does not parse is not a style problem — it produces a wrong release.

| Type | Meaning | Release effect |
| ---- | ------- | -------------- |
| `feat` | New capability | minor bump |
| `fix` | Bug fix | patch bump |
| `docs` | Documentation only | none |
| `test` | Tests only | none |
| `refactor` | Behaviour-preserving change | none |
| `build` | Build, tooling or dependencies | none |
| `ci` | CI configuration | none |
| `chore` | Anything else with no release impact | none |

Breaking changes take a `!` before the colon (`feat!: ...`) or a
`BREAKING CHANGE:` footer in the body, and cause a major bump.

The title must **not** contain an issue or PR number (`#123`) — link issues
in the body instead (e.g. `Fixes #123`), not in the title.

Individual commits on a branch should follow the same convention, but only
the title is load-bearing: squash merging discards the rest.

Example: `feat: add three-outcome verification contract`.

## Merging pull requests

All PRs to this repository must be **squash merged** — one commit per PR
on `main`, using the PR title/description as the resulting commit message.
Do not use "Create a merge commit" or "Rebase and merge". This keeps
`main` history bisectable and keeps rebases onto upstream cluster-api
releases (see above) clean, since a squashed history avoids interleaving
this fork's work-in-progress commits with upstream's.

## Keeping PR branches up to date

While a PR is open, update its branch by **rebasing onto the base branch**,
never by merging the base branch into it:

```sh
git fetch origin main
git rebase origin/main
git push --force-with-lease
```

Do not `git merge origin/main` (or use a "Update branch" button that creates
a merge commit) into an open PR branch. Merge commits pollute the PR's diff
and commit history with noise unrelated to the change, and since PRs are
squash merged anyway (see above), a merge commit adds no value — it only
makes the PR harder to review in the meantime.

## Summary for contributors and agents

- Upstream Cluster API is a pinned dependency, not a tree in this
  repository. Changes to it live in the patched fork and are recorded in
  [`DRIFT.md`](DRIFT.md) with a filing deadline for their upstream proposal.
- Integrate via upstream's existing public extension points. If something
  needs an upstream internal or a hook that does not exist, stop and raise
  it rather than working around it.
- `task verify` is the done-condition. Three outcomes, not two: a step that
  could not run is never a pass. Read the result from
  `bin/verify-result.json`, not the exit status.
- All new behaviour is developed test-first: unit tests plus integration
  tests against a real kcp server, as applicable — see
  [`docs/testing.md`](docs/testing.md).
- Do not weaken an assertion to get a green run. If the assertion cannot be
  met, that is the finding.
- Commit messages and PR descriptions: concise, not padded, never containing
  a session URL/ID or a `Co-Authored-By:` agent trailer; an `Assisted-By:`
  trailer is welcome.
- PR titles follow Conventional Commits and must not contain an issue or PR
  number. The title becomes the commit on `main` and drives release
  automation.
- All PRs are squash merged — no merge commits, no rebase-and-merge.
- Keep open PR branches current by rebasing onto the base branch, not by
  merging it in.
- New code and user-visible behaviour ship with matching docs in
  `docs/site/` — user docs (installation/usage) and design docs
  (architecture/deep dives). See [`README.md`](README.md).
- The full governing principles are in
  [`.specify/memory/constitution.md`](.specify/memory/constitution.md); this
  file is the working summary of how they apply day to day.
