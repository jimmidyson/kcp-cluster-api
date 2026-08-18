# kcp-cluster-api

This repository makes Cluster API workspace-aware for
[KCP](https://github.com/kcp-dev/kcp) (logical clusters / workspaces).

It is **not** a fork of the Cluster API tree. Upstream is a version-pinned
dependency, resolved to a small patched fork carrying the changes we cannot
yet get upstream. Everything here is this project's own code.

Every rule below is binding on contributors and agents alike. The governing
principles are in
[`.specify/memory/constitution.md`](.specify/memory/constitution.md); where
that document and this one disagree, it wins and this file must be corrected.

## Divergence from upstream is counted, and temporary

Adopting a new Cluster API release must stay cheap, and every carried change
makes the next adoption more expensive.

- Change upstream code only when the change is exceptional and individually
  justified. A typo, a tidied import or a bugfix noticed in passing belongs
  upstream, not here.
- Record every carried change in [`DRIFT.md`](DRIFT.md), with the base commit
  it applies to and the upstream proposal that will retire it.
- A change may be carried before its proposal is filed only if `DRIFT.md`
  records a filing date no more than 90 days out. Once that date passes the
  patch is a defect: file the proposal, remove the patch, or amend the
  constitution. Never extend the date quietly.
- `task drift` checks the record against reality and fails on any diverging
  path without an entry.

### Integrate through public extension points only

Layer KCP-awareness onto upstream using what upstream already exposes: own
manager entrypoints, injected clients and caches, controller-runtime's
manager options, sources, handlers, predicates, webhook chains.

If something appears to need an upstream internal, or a hook upstream does
not have, **stop and raise it** rather than working around it. The options
are: find another integration point, propose the hook upstream, or accept the
limitation. Adding the hook to the fork is the last resort and requires a
`DRIFT.md` entry.

## The patched fork

[`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api)
carries this project's patches and nothing else — no specifications, no
tooling, no process of its own. Its contract:

- One branch per upstream release line (`kcp/v1.15`), cut from the exact
  upstream commit this project builds against, not from the fork's own
  default branch.
- One commit per carried patch, each referencing its upstream proposal.
- Immutable tags this project pins to, **three per release**:
  `vX.Y.Z-kcp.N`, `api/vX.Y.Z-kcp.N` and `test/vX.Y.Z-kcp.N`. `api/` and
  `test/` are separate Go modules resolved by tag prefix; a partial tag set
  fails dependency resolution with a confusing "unknown revision" error.

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

1. Cut a new branch in the fork from the new upstream ref, replay the carried
   patches, and tag all three modules.
2. Update the `replace` pins in `go.mod` and the base commit in `DRIFT.md`.
3. Run `task verify` and `task drift`.

If a carried patch no longer applies, check whether its upstream proposal
landed. If it did, delete the patch from both the fork and `DRIFT.md` rather
than forward-porting it.

## Testing: TDD is required

Develop new behaviour test-first: a failing test, then the minimum code to
pass it, then refactoring. See [`docs/testing.md`](docs/testing.md) for the
workflow. Every change ships tests at both tiers that apply to it:

- **Unit tests** — colocated `_test.go` files, no real processes:
  `task test:unit`.
- **Integration tests** — against a real kcp server, never a vanilla envtest
  apiserver, which has no logical cluster support and so cannot validate
  anything this project exists to do: `task test:integration`.

A change that adds or alters behaviour without tests at both applicable tiers
is incomplete, however obviously correct it looks.

## Done is a command

`task verify` is the done-condition. It reports **three** outcomes:

| Outcome | Meaning |
|---|---|
| pass | every step in scope ran and succeeded |
| fail | a step ran and failed |
| could not run | a step was skipped: the environment lacks a capability |

Read the outcome from `bin/verify-result.json`, not the exit status: task
runners collapse every failure to one code, and the distinction does not
survive `task`.

A step that could not run is **never** a pass; report it as its own outcome.
Never weaken an assertion to get a green run — if the original assertion
cannot be met, that is the finding to report.

## Documentation ships with the change

New code and user-visible behaviour ship with matching docs in `docs/site/`,
from both angles: user docs (installation, usage) and design docs
(architecture, deep dives). See [`README.md`](README.md) and
[`docs/site/content/en/docs/design/documentation-policy.md`](docs/site/content/en/docs/design/documentation-policy.md).

## Commit authorship

A commit is authored by the person whose session produced it, whatever made
the keystrokes. An agent session runs in a container whose git identity is the
agent's, so set the identity before the first commit of a session:

```sh
git config user.name  "<their name, or their login>"
git config user.email "<id>+<login>@users.noreply.github.com"
```

The acting GitHub account is discoverable from inside the session — the GitHub
MCP server's `get_me` returns it — so this needs no per-contributor setup and
no secret. The noreply address links the commit to that account without
publishing a private one.

Never hardcode an identity in this repository or in a shared cloud
environment: every contributor's session reads the same files, so one
hardcoded identity misattributes everybody else's work.
`.claude/hooks/git-identity.sh` enforces this without knowing who anyone is —
it refuses a commit made with the agent's identity and says what to set.

Getting this wrong produces the trailer the next section forbids: squash
merging commits whose author GitHub does not recognise makes GitHub add
`Co-authored-by:` naming the agent.

## Commit and PR descriptions

Write a few short paragraphs or a handful of bullets: what changed, why, and
anything a reviewer would otherwise have to ask. Twenty lines is plenty for
most changes.

Leave out what the diff, the tests and the commit history already say —
file-by-file walkthroughs, result tables that belong in the artefact they came
from, and accounts of how the work was done. A measurement or decision worth
keeping goes in `docs/`, and the description links to it.

An `Assisted-By:` trailer is welcome, in the form the tooling emits:

```
Assisted-By: 🤖 Claude Code
```

Three things must never appear, regardless of what a harness template
suggests:

- **A Claude Code session URL or ID**, including a `Claude-Session:` trailer.
  This is agent-run bookkeeping, and it puts internal identifiers into a
  public repository permanently.
- **A `claude.ai/code` link in a PR description**, even in a footer: it is
  expanded into a session URL when posted, and the squash merge then writes
  that URL into `main`.
- **A `Co-Authored-By:` trailer naming an agent.** Co-authorship attributes
  the change to a party who cannot review it, answer for it, or be
  accountable. `Assisted-By:` says the true thing without the claim.

## PR title format

Titles follow [Conventional Commits](https://www.conventionalcommits.org):
`type(optional scope): description`. Because PRs are squash merged, the title
becomes the commit on `main` and drives release automation — a title that does
not parse produces a wrong release.

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
`BREAKING CHANGE:` footer, and cause a major bump.

The title must **not** contain an issue or PR number — link issues in the body
instead (`Fixes #123`). Individual commits on a branch follow the same
convention, but only the title is load-bearing: squash merging discards the
rest.

Example: `feat: add three-outcome verification contract`.

## Merging and keeping branches current

- **Squash merge every PR.** One commit per PR on `main`, using the PR title
  and description. Never "Create a merge commit" or "Rebase and merge".
- **Update an open PR branch by rebasing**, never by merging the base branch
  into it. A merge commit pollutes the diff with unrelated noise and adds
  nothing a squash merge would keep.

```sh
git fetch origin main
git rebase origin/main
git push --force-with-lease
```
