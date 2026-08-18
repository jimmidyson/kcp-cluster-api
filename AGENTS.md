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
- Tags are **signed and annotated** (`git tag -s`), all three at the same
  commit. Annotated because a tag this project pins to should carry what
  changed and who made it, and signed because it is the artefact a build
  resolves rather than a name anyone can move. A lightweight tag is not an
  acceptable substitute, and this is written down because it is the kind of
  convention that is invisible until somebody produces the wrong thing.

## Repository layout

```
docs/conversion-plan.md   the roadmap and current state — read this first
Taskfile.yaml             the named operations; `task --list` to see them
DRIFT.md                  what we carry against upstream, and why
cmd/                      binaries: core-manager, verify, drift
internal/                 implementation packages
test/integration/         integration tests against a real kcp server
docs/                     ADRs, design notes, and the documentation site
specs/                    spec-driven feature specifications
.specify/                 Spec Kit state, constitution and extensions
```

## Tracking work

[`docs/conversion-plan.md`](docs/conversion-plan.md) is the record of what
this project is doing and where it has got to. It is the first thing to read
when picking work up and the last thing to update when putting it down. A
session that has to reconstruct the next step from the commit log is paying a
cost this file exists to remove.

- Every D/G/P item in the plan carries its own status. A change that lands one
  **updates that item's status in the same pull request**, with the evidence:
  the pull request number, and the spec directory or design doc it was built
  against. A stale status is a defect, exactly as a stale `DRIFT.md` entry is.
- The plan's **Next** section names what is dispatchable now and what gates
  the rest. Whoever lands something that changes that answer rewrites it —
  a wrong answer there is worse than no answer, because it is believed.
- A specification under `specs/` carries `Status: Draft` while it is being
  written and `Status: Shipped in #N` once its pull request merges. `Draft` on
  a merged feature is the same defect in the other direction.
- The two are not substitutes. The plan says what the project is doing and
  where it stands; a spec says what one change was meant to do and why. Each
  links to the other, so either one answers "what now?" without a search.

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

## Scalability claims are measured

This project exists to make Cluster API affordable at many workspaces, so a
statement about what a workspace costs is a load-bearing claim, not colour.

- A change that alters per-workspace cost reports the **measured** figure.
  `task test:scale` is the instrument; the run is committed under the
  feature's `evidence/` directory so the figure is re-derivable rather than
  quoted.
- A figure derived from a formula or from reading the code is a
  **prediction**, and says so wherever it appears — doc comment, commit
  message, design doc.
- A claim is re-measured when the wiring it describes changes. A figure that
  was true of an earlier design is not a figure about this one.
- A measurement that could not be taken is reported as "not measured", never
  rounded to the nearest available number.

The reason this is a rule: the fleet-wide conversion shipped with "Cluster
and Machine leave the per-workspace sum entirely" in its doc comment,
reasoned from a formula with five out-of-sample confirmations. A workspace
still cost 51.7 goroutines, and nothing in the repository disagreed until
the sweep was run. The measurement also found the cause, which is what made
the fix findable — an unmeasured claim hides the work that has not been
done.

See [Principle IX](.specify/memory/constitution.md).

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

### Check the description after posting, not before

Writing a compliant description is not enough, because the thing that posts it
may append to it. Creating a pull request through the Claude Code GitHub tools
adds a `_Generated by [Claude Code](https://claude.ai/code/session_…)_` footer
server-side — both of the first two forbidden items above, in one line, on a
description that was clean when it was submitted.

So the rule is: **read the body back from the API after creating a pull
request, and strip anything appended.** The create call reports success whether
or not it rewrote what you sent, so its return value proves nothing.

```sh
gh api repos/{owner}/{repo}/pulls/{n} --jq .body | tail -3
```

Two details worth knowing before trying to fix one:

- Stripping it with `gh api --method PATCH` does not work; that write path
  appends the footer again, though without the session ID. Use the tools'
  own update-pull-request operation, which does not append.
- A pull request whose body was only ever set by an update — never by a create
  — is clean. That asymmetry is the quickest way to tell which call did it.

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

## Prefer a stack to one large pull request

A change that spans decisions, tooling and behaviour is reviewed badly as one
pull request, because the part that needs scrutiny is diluted by the part that
does not. Prose is the usual culprit: a change can be more than half specs,
ADRs and committed evidence, leaving the code that alters behaviour as a small
minority of the diff and no way for a reviewer to see that from the outside.

**Split such a change into a stack.** Each layer is a pull request in its own
right — its own title, its own description, its own verification — and the
layers are ordered so that each is reviewable on the assumption that the ones
below it are correct. A reasonable default ordering is decisions, then tooling,
then the behaviour change, then the evidence it produced.

Use GitHub's stacked pull requests. A stack is **declared from pull requests
that already exist**, so open each one first, targeting the branch below it,
and then group them:

```sh
# bottom to top
gh api repos/{owner}/{repo}/stacks -f 'pull_requests[]=31' -f 'pull_requests[]=32'

gh api repos/{owner}/{repo}/stacks                    # list; filter with ?pull_request=N
gh api repos/{owner}/{repo}/stacks/{stack_number}     # one stack and its members
gh api repos/{owner}/{repo}/stacks/{stack_number}/add # append, ordered from the top upward
gh api repos/{owner}/{repo}/stacks/{stack_number}/unstack
```

Lower layers merge first, and GitHub retargets and rebases what is above them.
Squash merge still applies per layer, so each becomes one commit on `main` and
each title must parse as a Conventional Commit — a stack produces several
release-relevant commits rather than one, which is usually a truer history than
a single squashed `feat!`.

Two things learned building one here:

- **Every layer must build and pass on its own**, which is what makes them
  independently reviewable. Check each as you cut it, rather than only checking
  the top.
- **Cut the layers by content, not by cherry-picking commit ranges**, whenever
  something like the fork pin moves during the work. Intermediate commits can
  reference a tag that no longer exists, so a layer assembled from a range may
  not build even though the tip does. Reconstructing each layer as a curated
  diff off the one below trades the fine-grained history for layers that
  actually work — and squash merge discards that history anyway.

Confirm the whole stack reproduces the original branch before opening anything:
`git diff <top-layer> <original-branch>` must be empty.

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
