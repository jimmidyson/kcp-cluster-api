# ADR-0004: Scaling the fork model to many providers

Status: **proposed**, 2026-08-20

This project supports one infrastructure provider today, and that provider is
upstream's *test* one. Supporting a real one — CAPA, CAPZ, CAPV — means a
second patched fork, and then a third. This ADR asks whether the two-repository
split survives that, and decides the shape it has to take.

It does not decide when to build any of it. That distinction is the point of
the last two sections.

## The question

`kcp-cluster-api` and
[`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api)
are two repositories with one purpose between them. Two questions follow, and
only the second is interesting:

1. Should they be merged — everything into the fork, or everything out of it?
2. If not, is the boundary between them in the right place once there are
   four forks instead of one?

## Where the split stands, counted

[`DRIFT.md`](../DRIFT.md) at `v1.15.0-kcp.11`:

| | Count |
|---|---|
| Recorded paths | 56 |
| Carried deliberately, no upstream proposal intended | 44 |
| **Modifications to existing upstream files** | **32** |
| New files | 22 |

The 32 is the number that matters, and `DRIFT.md` says so itself: a new file
rebases for free and a modified one does not.

Two documents describe a fork that stopped existing when
[ADR-0003](adr-0003-workspace-aware-cluster-api.md) was accepted. The design
site says "The one patch carried today is exactly that case"; `go.mod`'s
comment says the fork "carries the single public seam recorded in DRIFT.md".
Both were true at `kcp.0`. Both are corrected in the change that adds this
ADR — noted here because the split's rationale is what a reader goes to those
documents for, and they have been describing a two-symbol patch queue while
the fork became a permanent derivative.

### The stated benefit partly migrated rather than materialised

The design site's case for the split is that "an upgrade becomes a change to a
version string, reviewed as a diff of pins." That is true of this repository
and false of the system. The 32 modifications must still be replayed onto each
new upstream release; what changed is *where* that happens.

It now happens in the fork, which by deliberate design has no drift check
(`DRIFT.md` explains why, and the reasoning holds), no specification process,
and no verification contract. The expensive half of an upgrade moved to the
repository with the least machinery pointed at it. That is not an argument for
undoing the split — it is the reason the boundary needs stating precisely
rather than by convention.

### The dev infrastructure provider is 17 of the 56

Grouping the record by area:

| Area | Paths |
|---|---|
| `test/infrastructure/**` (the dev provider) | **17**, of which **12** are modifications |
| `controllers/clustercache` | 8 |
| `util/controller` | 7 |
| `util/multicluster` | 6 |
| everything else (core, bootstrap, control plane, contract) | 18 |

Nearly a third of the drift record is one infrastructure provider, and it is
the simplest one that exists: a docker backend and an in-memory backend, with
no cloud API, no IAM, no region. The commits that landed between `kcp.7` and
`kcp.11` added five more modifications there. Three are one kind — qualifying
a backend's resource naming by logical cluster, because naming by namespace
and name lets two workspaces collide on one container or one listener. The
other two are a free-port fix in the in-memory server, which is the ordinary
kind of bug a fork accumulates rather than anything about kcp.

**Prediction, not measurement:** a cloud provider costs at least as much. The
patch class that dominates here — *the backend names its real-world resources
by namespace and name, and under kcp that is not unique* — applies to anything
tagging cloud resources, and a cloud provider has more such surfaces than
docker does, not fewer. No forked cloud provider exists, so nothing here is a
measurement of one, and this figure is re-derived when one does.

## Question 1: neither merge is available

**Everything out of the fork** is not a thing that can be done. Thirty-two of
the recorded paths are modifications to upstream files in place. A change to
`controllers/clustercache/cluster_cache.go` cannot be relocated into a
different module; it can only be copied (which is the same fork with the
divergence made invisible) or landed upstream. ADR-0003 decided against
proposing this wiring upstream. So while those 32 exist, a patched fork exists.

**Everything into the fork** is the layout this project already had, and
abandoned for reasons the design site records: seven inherited CI workflows
accumulated local edits, and dependency bots rewrote upstream `go.mod` and
`go.sum` across four modules. Those forces have not gone away, and they
multiply by the number of forks. It would also make each fork a full mono-tree
with its own specification process and release automation.

The split stays. The rest of this ADR is about question 2.

## Question 2: two blockers that fire at the second fork

Both are present today. Neither has bitten, because there is one fork and one
consumer.

### The `replace` directives do not propagate

The fork keeps the module path `sigs.k8s.io/cluster-api`, so the three
`replace` directives in `go.mod` are load-bearing rather than cosmetic. Go
honours `replace` only in the main module. Every future provider fork, and
anything downstream of one, must restate all three pins at matching versions.

With four forks that is a set of pins each consumer repeats, and a mismatch
does not fail loudly: it resolves genuine upstream Cluster API alongside the
fork, or fails as an "unknown revision" naming a tag that looks fine. This has
the shape Constitution Principle VIII calls out — structural to retrofit, and
silent when violated.

### Everything reusable is `internal/`

`internal/providerwiring`, `internal/capiexports`, `internal/coremanager` and
the rest are exactly the glue a forked provider needs, and are unimportable
from outside this module by construction. A forked CAPA can reuse none of it
today. This is not a design flaw — `internal/` was right for a set of one
consumer — but it is a wall, and the first third-party provider hits it before
it writes a line.

### The generic plumbing is generic, and is in the wrong repository

Checked against `kcp/v1.15`, these files import **zero** Cluster API packages:

| Path | Imports |
|---|---|
| `util/multicluster/client.go` | controller-runtime, multicluster-runtime |
| `util/multicluster/lift.go` | controller-runtime, multicluster-runtime |
| `util/multicluster/recorder.go` | controller-runtime, client-go |
| `util/multicluster/wildcard.go` | controller-runtime, multicluster-runtime |
| `util/controller/wildcard_registry.go` | controller-runtime |

They are a controller-runtime/multicluster-runtime adapter layer that happens
to live in a Cluster API fork, for no reason other than that being where they
were written. Every future provider fork needs them, and would reach them by
depending on this project's Cluster API fork — which is a strange edge for a
CAPA fork to have.

`util/controller/builder_workspace.go` is the counter-example and marks the
boundary: it imports `cluster-api/feature`, `cluster-api/util/cache` and
`cluster-api/util/predicates`, and belongs where it is.

## The decision: three layers

**L1 — shared multicluster plumbing.** A module of its own, depending on
controller-runtime and multicluster-runtime and *not* on Cluster API. The six
files above, plus whichever parts of `internal/` prove to have a second caller.
Every fork and every integration imports it. This is the layer that must not
exist four times.

**L2 — one thin patch-carrier fork per upstream repository.** Contract exactly
as today: one branch per release line cut from the upstream commit built
against, one commit per carried patch, three signed and annotated tags, and
nothing else. The admission rule tightens: **a fork may carry in-place
modifications to upstream files, and seams that cannot be expressed from
outside the module. Anything expressible as a new file belongs in L1.** That
rule is what keeps `git diff upstream..kcp/vX.Y` meaningful when there are four
of them.

**L3 — per-provider integration, as modules rather than repositories.**
`providers/aws/go.mod`, `providers/azure/go.mod` and so on, in this repository.

The dependency-conflict concern that prompted this ADR is real and is the
reason L3 is not one module: CAPA pins its own AWS SDK and CAPZ its own Azure
SDK, and one `go.mod` arbitrating all of them is a permanent job with no end
state. But that argues for separate *module graphs*, which separate modules
give, not for separate *repositories*. Keeping them here keeps one Taskfile,
one drift checker, one verification contract, one specification process and one
release configuration, instead of four copies that diverge.

The cost, stated because it is real: multi-module repositories with `replace`
are awkward. Each module builds independently in CI, `go.work` helps locally
but is not what CI resolves, and `release-please` needs per-module
configuration. The alternative — one repository per provider — pays that cost
as four sets of CI, four `AGENTS.md` and four release pipelines instead. This
ADR takes the multi-module repository, and records the trade rather than
claiming it away.

## What is built now: none of it

Constitution Principle VIII: abstraction layers must not be built ahead of a
concrete need, and the trigger is a second real caller, a measured constraint
or a stated requirement — not an anticipated one. There is no second caller.
There is one fork, one integration, and no forked provider outside
`sigs.k8s.io/cluster-api`.

Extracting L1 today would be building a shared library for a set of one, which
is the thing that principle exists to stop. So:

| Layer | State | Trigger to build |
|---|---|---|
| L1 | not built | the first provider fork outside `sigs.k8s.io/cluster-api` needing any of the six files |
| L2 | exists, one instance | the tightened admission rule applies from now; a second instance arrives with the first forked provider |
| L3 | exists, one instance, not yet a module boundary | the second provider integration |

Principle VIII also requires that deferral be recorded as a decision naming its
trigger, "because silent omission and deliberate deferral look identical
later". That is what the table above is for, and it is the substantive output
of this ADR.

### The two things worth doing before then

Both are corrections rather than construction, and neither builds for a
hypothetical caller.

**The stale documents.** Fixed in this change. A reader asking why the split
exists currently gets a description of a two-symbol patch queue.

**The pin-propagation hazard, recorded.** Building a checker for it is
VIII-barred with one consumer, but the hazard is silent when violated, so the
required pin set belongs written down where a second consumer will find it.
Recorded here and in `DRIFT.md`; mechanised when there is something to check.

### Tooling that is closer than expected

`cmd/drift` already takes `--fork` and `--ref`, and reads the base commit from
the record rather than a constant. One thing is fixed to a single fork:
`pinnedForkVersion` resolves `sigs.k8s.io/cluster-api` by name from the module
graph. Generalising it means passing the module path alongside the record path
— small, and not worth doing until there are two records to check.

## Consequences

- The fork's contract gains a rule it did not have: new files justify
  themselves against L1 before being carried. This applies to the next patch,
  not retroactively — the six files stay where they are until L1's trigger
  fires.
- Adding a provider becomes: fork it (L2), integrate it as a module here (L3),
  and — from the second one — import shared plumbing rather than copy it (L1).
- The drift record becomes N records, one per fork. `cmd/drift` grows a module
  path parameter at that point.
- This ADR does not revisit ADR-0003. Whether carrying the workspace-aware
  wiring rather than proposing it upstream was right is that ADR's question;
  this one takes 32 modifications as given and asks how they scale.

## What would change this decision

- **Upstream accepting the wiring.** If the `clustercache` and `util/controller`
  seams landed upstream, the modification count would drop sharply and L2 might
  reduce to nothing for some providers. `DRIFT.md` already names the
  `clustercache` locking fix as upstreamable, and it is the second largest
  group.
- **A provider that needs no in-place modification.** Then it needs no fork,
  only an L3 module, and the fork count stops tracking the provider count.
- **Cloud providers costing far less than 17 paths.** The prediction above is
  the load-bearing input to "four forks is a thing worth designing for". If the
  first real one costs three paths, this is over-built and L3 alone suffices.

## What is not established

- No forked cloud provider exists. Every statement here about CAPA, CAPZ or
  CAPV is projection from the dev provider, labelled as such.
- The six L1 files are shown free of Cluster API *imports*. That they are free
  of Cluster API *assumptions* is likely but unverified; extraction is the
  test, and it has not been run.
- Whether L3's modules can share a `Taskfile` and verification contract across
  differing dependency graphs is untested. It is the main risk in preferring a
  multi-module repository to four repositories, and it is cheap to test at the
  second provider and expensive to discover at the fourth.
