# ADR-0005: One repository — the forks become vendored trees

Status: **proposed**, drafted 2026-11-16. Supersedes the first question of
[ADR-0004](adr-0004-scaling-to-many-provider-forks.md) and the "patched fork"
contract in `AGENTS.md`; leaves ADR-0004's L3 — one integration module per
provider, in this repository — as it is.

This project has had three shapes. It began as a fork of the Cluster API tree
with its own code under `kcp/`. It inverted that in #22: upstream became a
version-pinned dependency, resolved to a patched fork in another repository,
and this tree kept only its own code. ADR-0004 then settled how that split
scales to several providers, and concluded the fork count would track the
provider count.

This ADR asks whether that split still serves the project, given a requirement
it was not designed against, and decides that it does not.

## The requirement

**Each provider must be releasable and testable on its own.** A change to the
Nutanix provider's workspace-aware wiring should produce a Nutanix release,
proven against a real kcp, without waiting on or being blocked by the core
providers — and the same the other way round.

The current layout cannot meet this at all, and the reason is structural
rather than a gap to be filled. The forks carry patches to upstream code and
nothing else. Everything that knows kcp exists — the APIExport cluster
provider, virtual-workspace resolution, fleet construction, the APIExport
definitions and the kcp test fixtures — lives in this repository under
`internal/`, where no other module can import it. A fork therefore cannot
build a manager that runs under kcp, and cannot write a test that needs one.
Its unit tests and envtest run; nothing in it is ever exercised against a
workspace. "Testable" for a fork today means "compiles and passes upstream's
tests with the patches applied", which is not the property the requirement
names.

## Where the split stands, counted

The numbers below are read from the records at the pins in `go.mod` on the
day this was drafted; the records are the count and this ADR does not
promise to stay current with them.

| | Cluster API fork | Nutanix fork |
|---|---|---|
| Pin | `v1.15.0-kcp.13` | `v1.11.0-kcp.3` |
| Recorded paths | 67 | 23 |
| Of which modifications to upstream files | 38 | 15 |
| Signed, annotated tags per release | 3 | 1 |
| Vendored size (source, no history) | 34 MB, 612 non-test Go files | 4.7 MB, 73 |

Two of ADR-0004's predictions have since resolved, and both in the direction
that weakens the case for separate repositories:

- **The forks are permanent derivatives, not patch queues.** ADR-0003 decided
  not to propose the workspace-aware wiring upstream, so the bulk of both
  records is carried deliberately, with no upstream proposal and no expiry.
  The design that put them in their own repositories — the one the site's
  "Dependency Architecture" page still describes — was for a small, shrinking
  set of changes with proposals attached. That is not what is being
  maintained.
- **The cost ADR-0004 called "the expensive half of an upgrade" moved to the
  repository with the least machinery pointed at it**, and has stayed there.
  Every reconciler is now two halves: its `*_workspace.go` setup in a fork,
  and the `Setup*Controllers` call that wires it here, with a fork commit,
  three signed tags and a pin bump between them. Test-first development
  across that seam is not possible, because the test cannot see the change
  until it has been tagged.

## Three shapes, compared

Each was considered against the requirement, and each has a cost the others
do not. The comparison is kept because the decision is only as good as the
alternatives it was taken against.

### A. The current split

Meets no part of the requirement, for the structural reason above. It would
have to change regardless. Kept here as the baseline the other two are
measured from: N+1 repositories, a tag ceremony per fork release, and the
`replace`-pin propagation hazard ADR-0004 observed biting on the first
consumer that was not this repository.

### B. Independently releasable forks, by inverting one dependency

Make the kcp glue a public library in this repository with no Cluster API
dependency — nearly free, since the APIExport provider's only Cluster API
import is the wildcard registry and it calls one method on it — and give
each fork a nested module carrying a thin manager entrypoint, the fleet
setup, a kcp integration test using the library's fixtures, and an image
workflow keyed to the fork's tag.

This meets the requirement. Its cost is the highest standing cost of the
three: N+1 repositories become N+1 release pipelines plus a versioned
library; the tag-and-pin cycle is deepened rather than removed (a fork's kcp
module pins the library, this repository pins the forks, and a library change
a fork needs is still two steps); and it is a partial return to the pre-#22
layout, with each of that layout's failure modes answered by a rule rather
than removed. It wins only if a provider fork is to be consumed by someone
other than this project, and nothing today suggests one will be.

### C. One repository, with the forks as vendored trees

Bring each upstream tree into this repository as a snapshot under its own
directory, keeping its module path and its own `go.mod`, so that
`forks/cluster-api` is still `sigs.k8s.io/cluster-api` and the Nutanix
provider imports it unchanged. Every consuming module replaces the upstream
path with a relative one instead of a version, a committed `go.work` makes
the whole thing one build, and each provider's kcp wiring, tests and release
sit beside the code they wire.

This meets the requirement through per-component releases and path-filtered
CI, and it is the only shape that removes the cross-repository cost instead
of managing it. The two things it costs are a flat diff in place of a
per-patch commit series, and about 40 MB of vendored source that grows with
each provider.

## Why the reasons against C no longer hold

The project wrote down, twice, why one repository was the wrong shape. Both
arguments were about a layout in which upstream was the repository root, and
neither survives upstream being a subdirectory.

**"Seven inherited CI workflow files accumulated local edits."** GitHub runs
workflows from the root `.github/` only. A `.github/` under `forks/cluster-api`
is inert — nothing reads it, so nothing has to be edited to keep it working,
and it can be vendored verbatim with no entry in the record. The Cluster API
fork carries nine such files today; in a subdirectory they are text.

**"Dependency bots rewrote upstream `go.mod` and `go.sum` in four separate
modules."** Dependabot's Go configuration is per directory. The root
`dependabot.yaml` already lists `/` and excludes the Nutanix module by path
precisely so its graph moves only with its fork; the vendored trees are
excluded the same way. A bot cannot edit a directory it is not pointed at.

**"The rule was enforced by asking people to remember it."** It no longer has
to be. The drift checker exists, reads a record, compares a tree against an
upstream base commit, and fails on any unrecorded path. In the current layout
it runs on a schedule because the thing it measures is in another repository,
and gating every pull request on another repository's state was rightly
rejected. Once the tree is here, the thing it measures is in the pull request,
and the check runs on every one that touches a vendored path. The invariant
the mono-tree asked people to remember becomes a property CI asserts, which is
the same conversion #22 made for the opposite layout, applied in this one.

The third argument, from ADR-0004's first question, was that merging
everything into the fork "would make each fork a full mono-tree with its own
specification process and release automation." That is true of one
repository *per fork*. It is the opposite of true for one repository for all
of them, which is the arrangement ADR-0004 itself chose for L3 on exactly
these grounds: one Taskfile, one drift checker, one verification contract,
one specification process. This ADR extends that choice from the integration
modules to the forks they integrate.

## The decision

**Option C.** The two forks become vendored trees in this repository; the
fork repositories are archived once nothing pins them.

The shape, stated as decisions rather than mechanisms — the specification
that implements this chooses the mechanisms, and ADR-0004's L3 section still
describes the modules it lands beside:

- **Snapshot, not patch series.** The checked-in tree is the built tree.
  Carried changes are the diff between `forks/<name>` and the upstream base
  commit the record names, which is the same definition of drift the checker
  uses today. A patches directory applied onto a pristine upstream at build
  time was considered and rejected: source that is not what is built makes
  review and tooling worse, and the project already reasons about drift per
  path rather than per patch.
- **Complete trees, nothing deleted.** Upstream's docs, hack scripts and
  workflows are vendored as they are, inert where they are inert. Deleting
  them would make every upstream adoption a modify/delete conflict on paths
  the project does not care about, to save disk the project does not need.
- **Module paths and nested modules are preserved.** `forks/cluster-api` is
  `sigs.k8s.io/cluster-api`, with its `api/` and `test/` modules unchanged,
  so the vendored Nutanix provider and every module here import upstream
  under upstream's name. The three `replace` directives in every consuming
  module become relative paths. A relative replace cannot disagree with
  another on a version, which retires the pin-propagation hazard rather than
  documenting it.
- **A committed `go.work`.** The gitignore comment that forbids one is about
  a workspace overriding a version pin. With no pins, the workspace is the
  truth. Each module also carries its own relative replaces, so it stays
  tidy-able and buildable alone, which the Taskfile already requires.
- **Upstream adoption is a subtree merge into the directory**, not a branch
  replay. The merge's conflicts are the modified paths in the record and
  nothing else, which is the number the record already says matters.
- **One drift record per vendored tree, where the records are now.** The
  `Fork:` line becomes the tree's path; the base commit stays. The checker
  measures a local directory against an upstream commit instead of cloning a
  fork, and runs on every pull request that touches that directory.
- **Releases stay per component.** release-please already treats this
  repository as a set of packages, one per module; a vendored tree is not a
  package, it is source a package builds from. Image tags carry the upstream
  base version where they carry a fork tag today, and the fork's `-kcp.N`
  suffix goes away because the component version already says what it said.
- **Independently testable means path-filtered.** A change under a vendored
  tree runs that tree's own tests and the kcp integration tests of the
  managers built from it, and nothing else. Running upstream's unit suite on
  every documentation change is the one way this shape could make the project
  slower, and the filters are not optional.

### What this changes in the governing documents

Each is a recorded edit in the pull request that lands the corresponding
layer, not a side effect:

- `AGENTS.md` — "The patched fork" and "Adopting a newer Cluster API release"
  are rewritten; "Divergence from upstream is counted" stands, with the check
  now gating every pull request that touches a vendored tree.
- The constitution — Principle I's sentence on the enforcing mechanism
  ("now a separate patched fork") names a third mechanism. A PATCH amendment:
  the principle does not change.
- `DRIFT.md` and the Nutanix record — the "Where the check runs, and why not
  on every pull request" reasoning inverts, for the reason given above.
- The site's "Dependency Architecture" and "Adopting Upstream Releases" pages
  are rewritten against the new arrangement. The former still opens by
  describing a layout this ADR retires.
- ADR-0004 — question 1 is superseded; the L2 contract (branch per release
  line, signed tags) is retired with the fork repositories; L3 stands.

## Consequences

- A reconciler change, its fleet wiring and its kcp test land in one pull
  request. This is the first arrangement in which the testing rule in
  `AGENTS.md` can be followed across the upstream seam at all.
- The `replace`-pin propagation hazard is gone, not mitigated.
- The signed three-tag ceremony per Cluster API release is gone. The signing
  obligation that remains is the one every commit on `main` already carries.
- **Every pull request can now touch an upstream file.** In the split, the
  wall was physical: the files were not here. Now it is the drift check, and
  a pull request that fails it is the system working. Review should expect
  that signal and not route around it.
- The repository grows by the vendored source, and by one tree per future
  provider. It is modest and it is permanent.
- The per-patch commit series is lost. `git log upstream..kcp/v1.15` was the
  patch queue; here it is a diff. Extracting one patch from a diff for an
  upstream proposal is a known operation, and ADR-0003 means few are headed
  there.
- A provider can never ship against a Cluster API tree other than the one it
  sits beside. The requirement asked for independent releases, not
  independent upstream versions, and this is the point at which the two part.

## What would change this decision

- **A consumer of a fork outside this project.** Then a fork is a product
  with its own users, option B's cost is being paid for a reason, and the
  tree should go back to its own repository with a tag-prefixed module here
  as the bridge. Nothing today is such a consumer.
- **Upstream accepting the wiring.** A vendored tree with no modifications is
  a dependency with extra steps; if the records empty, the trees should be
  dropped and the modules pinned at upstream directly. This is the outcome
  the project is nominally working towards, and this ADR does not change
  that.
- **Subtree merges proving dearer than tag replays.** The first adoption of a
  new Cluster API minor under this layout is the measurement, and ADR-0004's
  "adopting a new release must stay cheap" is the claim it tests. If the
  merge costs more than the replay did, that is a finding to record here,
  not a reason to quietly stop adopting releases.

## What is not established

- Subtree merge against a tree with 38 modified paths has not been run here.
  The first adoption will show whether its conflicts are the modified paths
  and nothing else, as claimed above.
- Whether ko resolves a build whose main module replaces `sigs.k8s.io/cluster-api`
  with a relative path inside a `go.work` — the `.ko.yaml` already records
  the fallback for nested modules, and this may need it.
- CI time with the vendored unit suites gated by path. The filters are
  decided; their cost when they do fire is not measured.
- Whether release-please is untroubled by Go modules in the tree that are not
  packages. It should ignore them; "should" is not a measurement.

Each is the first layer's job to settle, and the first layer is the one that
changes no behaviour: vendor the two trees, replace the pins, retarget the
checker, and show `task verify` and `task drift` passing with nothing pinned.
Everything else in this ADR stacks on that.
