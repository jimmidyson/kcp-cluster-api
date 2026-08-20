# ADR-0004: Scaling the fork model to many providers

Status: **accepted**, decided 2026-08-20. **Amended 2026-08-20: L1 is
retired** — the CAPX port fired its trigger and the layer turned out not to be
needed. See [What the CAPX port settled](#what-the-capx-port-settled). The
reasoning below is kept as it was written, so what the decision was taken
against stays legible.

This project supports one infrastructure provider today, and that provider is
upstream's *test* one. Supporting a real one means a second patched fork, and
then a third. This ADR asks whether the two-repository split survives that, and
decides the shape it has to take.

The second provider is **CAPX**,
[`cluster-api-provider-nutanix`](https://github.com/nutanix-cloud-native/cluster-api-provider-nutanix).
Naming it turns the triggers below from anticipation into a scheduled piece of
work, and it has already paid for itself: reading CAPX rather than reasoning
about a hypothetical provider found two cross-tenant faults and three
structural facts that change this ADR's own conclusions. Those are in
[What CAPX establishes](#what-capx-establishes).

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

**Prediction, not measurement:** a real provider costs at least as much. The
patch class that dominates here — *the backend names its real-world resources
by namespace and name, and under kcp that is not unique* — applies to anything
naming or tagging infrastructure. Reading CAPX confirms the class is present
there in two places (below), which raises this from a guess to a grounded
prediction; it remains a prediction, because no forked provider exists and the
path count is only known once one does.

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

A throwaway module confirms it rather than leaving it at an import check.
The five sources above, plus the two unit tests that belong to them
(`recorder_test.go`, `wildcard_registry_test.go`), compile as a standalone
module against controller-runtime, multicluster-runtime and `k8s.io/*` alone;
`go list -m all` shows **no Cluster API module anywhere in the transitive
graph**, and both test packages pass. So the files are free of Cluster API
*assumptions*, not merely of Cluster API *imports*.

One file does not come along, and finding it is why the spike was worth
running. `util/multicluster/fleet_test.go` imports
`sigs.k8s.io/cluster-api/util/controller` and drives the primitives through
`capicontrollerutil.NewMulticlusterControllerManagedBy` — the CAPI-coupled
builder. It is an integration test of L1 *through* L2, not an L1 unit test, so
it stays with the fork unless rewritten against a plain controller-runtime
builder. The extractable set is therefore **five sources and two unit tests**,
and L1 starts life with one less test than the drift record's `util/multicluster`
entries suggest.

`util/controller/builder_workspace.go` is the counter-example and marks the
boundary: it imports `cluster-api/feature`, `cluster-api/util/cache` and
`cluster-api/util/predicates`, and belongs where it is.

## What CAPX establishes

Read at `main` (`0b2405d`), before any port. These are code-reading findings:
anyone can check the cited lines. What they mean *under kcp* is a prediction
until a port exists.

### Two collisions of the class the dev provider already needed patching for

**VM identity is the Machine name, and the lookup is global.**
`vmName := rctx.Machine.Name` (`controllers/nutanixmachine_controller.go:325`)
— no namespace, no cluster, no workspace. `FindVMByName`
(`controllers/helpers.go:237`) resolves it by listing Prism Central with
`name eq '<vmName>'`, which spans the entire Prism Central, and **errors when
more than one matches**:

```go
if len(vms) > 1 {
    return nil, fmt.Errorf("error: found more than one (%v) vms with name %s", len(vms), vmName)
}
```

That is the path taken whenever the VM UUID is not yet recorded — the create
path. So two workspaces whose Machines share a name give each other a
reconcile error rather than a silent cross-mount. Louder than the dev
provider's collision, and still one tenant breaking another's reconcile.

**Category identity is the cluster name alone.**
`GetDefaultCAPICategoryIdentifiers(clusterName)` (`controllers/helpers.go:765`)
builds the single identifier `KubernetesClusterName: <clusterName>`. Two
workspaces each holding a cluster called `demo` address one Nutanix category —
the mechanism by which CAPX marks what it owns.

Teardown is where that matters. `deleteCategoryKeyValues`
(`controllers/helpers.go:811`) deletes the category on cluster deletion, and
what stops it destroying the other tenant's marker is Prism refusing to delete
a category that still has VMs attached. The provider does not recognise that
refusal — it swallows every delete error and returns `nil`, with an explicit
`TODO` to match the specific error:

```go
// NCN-101935: If the category value still has VMs assigned, do not delete the category key:value
// TODO:deepakmntnx Add a check for specific error mentioned in NCN-101935
return nil
```

Isolation here is a remote API's refusal, not a property of the provider.

**Both are arguably CAPX bugs today, independent of kcp.** Cluster and Machine
names are namespace-scoped in Cluster API, so two *namespaces* in one ordinary
management cluster collide the same way. That is worth recording because it
sets what kind of drift entry each becomes — the same distinction `DRIFT.md`
already draws for two of the dev provider's patches, which it marks "bug fix,
upstreamable".

**They are carried in a fork regardless.** Fixes go to
`jimmidyson/cluster-api-provider-nutanix`, cut per the L2 contract below, not
to `nutanix-cloud-native/cluster-api-provider-nutanix`. This project does not
plan its work around another repository's review queue, and ADR-0003 already
took that decision for the Cluster API wiring. Whether to *also* propose these
two upstream in CAPX is a separate call, deliberately not made here: being
upstreamable changes what a `DRIFT.md` entry says about itself, not where the
patch lives.

That classification is not optional, though, and this ADR being accepted does
not pre-authorise skipping it. Principle I gives a carried patch two lawful
states: **pending**, with a filing date no more than 90 days out, or
**deliberate**, carried under a decision that says so. Every entry in a CAPX
drift record picks one when it is written. What this ADR settles is only that
the patch lives in the fork either way; it does not make a CAPX record's
entries deliberate by default, and an undated pending entry is the same defect
there as here.

### Three structural facts that change this ADR's own conclusions

**CAPX is one Go module.** No `api/` or `test/` submodules. L2's "three
immutable tags per release" is therefore a Cluster API fact, not a general
one — CAPX needs one tag. The contract below is corrected to say so.

**Its reconcilers are public.** `controllers/` exports `NutanixClusterReconciler`,
`NutanixMachineReconciler` and their `SetupWithManager` methods. This is the
opposite of the constraint that forced this project's Cluster API fork off a
release tag and onto a `main` commit — the dev provider's reconcilers were
under `internal/`. A CAPX fork can be cut from a release tag.

**The version skew is real, and is the dependency-conflict argument made
concrete.** CAPX `main` against this project:

| | CAPX | kcp-cluster-api |
|---|---|---|
| `sigs.k8s.io/cluster-api` | v1.13.1 | v1.15 (forked) |
| `sigs.k8s.io/controller-runtime` | v0.23.3 | v0.24.1 |
| `k8s.io/api` | v0.35.5 | v0.36.3 |

A minor behind on all three, plus ten Nutanix SDK module requirements. Note
what this is and is not: Go's minimum version selection resolves such a graph
without complaint, picking the higher version throughout. The conflict is a
*compile* problem — CAPX would have to build against controller-runtime v0.24
and Cluster API v1.15 — and it is CAPX's fork that must absorb it, not this
repository's `go.mod`. That is precisely the argument for L3 being separate
modules: one module per provider means one version negotiation per provider,
each visible and each owned, instead of one graph in which the newest provider
silently forces the others forward.

## What the CAPX port settled

This section is later than the rest. It records what happened when the CAPX
fork was actually cut and wired, which is the event the triggers below were
waiting for.

### L1's trigger fired, and the layer was not needed

The trigger was "the CAPX fork needing any of the five sources". The fork was
cut, both reconcilers were given fleet-wide wiring, and CAPX imports **nothing**
from `util/multicluster`. It reaches the lifting machinery transitively,
through `util/controller` — the CAPI-coupled builder, which stays in the fork
by this ADR's own rule, and which CAPX already depends on because it depends on
Cluster API.

The prediction this ADR made was that every provider fork would need the five
sources and would reach them "by depending on this project's Cluster API fork —
which is a strange edge for a CAPA fork to have". The edge is not strange. It
is the dependency every Cluster API infrastructure provider already has, and
the five sources are an implementation detail of the fork's builder rather than
a library a provider imports.

So the architecture is **two layers, not three**: patch-carrier forks, and
per-provider integration modules. The shared plumbing stays where it is.

What would revive L1 is a consumer that needs those five files *without*
depending on Cluster API. No such consumer exists, and by construction an
infrastructure provider cannot be one.

### The other blocker was confirmed rather than dissolved

The two blockers were independent, and they did not fare the same way. CAPX's
`go.mod` had to restate all three `replace` directives at matching versions to
resolve the fork at all — which is the pin-propagation hazard, observed rather
than predicted, on the first consumer that was not this repository. That
finding stands and gets more expensive per fork, exactly as recorded.

### The forward-port cost, so far, is far below the prediction

The ADR predicted a real provider would cost "at least" the dev provider's 17
paths. The CAPX fork currently carries six: `go.mod` and `go.sum`, the two
files touched by removing a dead workload-cluster client cache, and two new
`*_workspace.go` files.

**This is a partial figure and must not be read as the final one.** The port is
not finished: the integration module is not built, and none of the three
name collisions is fixed. Each fix adds paths. What the figure does establish
is that the dependency skew — a minor behind on cluster-api, controller-runtime
and k8s.io alike — cost one dead-code deletion rather than a rewrite, because
`api/core/v1beta1`, `controllers/remote` and `util/deprecated/v1beta1` all
survive in v1.15.

### A third collision, latent rather than live

Alongside the two found by reading, the port found a third by compiling.
`pkg/context.RemoteClientCache` is a package-level map keyed on `ObjectKey`, so
two workspaces each holding a cluster of the same namespace and name would
share one workload-cluster client — one tenant handed a client for another
tenant's cluster.

It is **latent, not live**: `GetRemoteClient` is the only writer and has no
callers, so nothing populates the map. It was removed as dead code rather than
ported, which is also what fixed the one compile error the version bump caused.
Unlike the other two it is kcp-specific — `ObjectKey` carries a namespace, so
it does not collide within a single management cluster.

## The decision: three layers

**L1 — shared multicluster plumbing.** ~~A module of its own, depending on
controller-runtime and multicluster-runtime and *not* on Cluster API. The five
sources and two unit tests above, plus whichever parts of `internal/` prove to
have a second caller. Every fork and every integration imports it. This is the
layer that must not exist four times.~~

**Retired.** The CAPX port fired this layer's trigger and did not need it: a
provider fork reaches the plumbing through `util/controller`, which it already
depends on. The five sources stay in the fork as an implementation detail of
its builder. See [What the CAPX port settled](#what-the-capx-port-settled).
The two layers below are the architecture.

**L2 — one thin patch-carrier fork per upstream repository**, under this
project's own ownership: `jimmidyson/cluster-api` today,
`jimmidyson/cluster-api-provider-nutanix` for CAPX. A provider is consumed
from a fork this project controls, never pinned at an upstream repository —
the pin has to be a ref nobody else can move, and the patches have to land
without waiting on another project's review. Contract as today, with one
generalisation CAPX forces: one branch per release line cut
from the upstream commit built against, one commit per carried patch, and
**one signed and annotated tag per Go module in the repository** — three for
Cluster API, one for CAPX — and nothing else. The existing rule says "three",
which is a fact about Cluster API's `api/` and `test/` submodules rather than
about forks. The admission rule tightens: **a fork may carry in-place
modifications to upstream files, and code that cannot be expressed from outside
the module.** That rule is what keeps `git diff upstream..kcp/vX.Y` meaningful
when there are four of them.

~~Anything expressible as a new file belongs in L1.~~ Void with L1: a new file
that could only live in the fork — because it implements a seam against
upstream's own types — is carried on the same terms as a modification. The
`*_workspace.go` setups and the builder they call are exactly that, which is
why they stay.

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

## What was built, and what the triggers did

Constitution Principle VIII: abstraction layers must not be built ahead of a
concrete need, and the trigger is a second real caller, a measured constraint
or a stated requirement — not an anticipated one. When this ADR was written
there was no second caller, so nothing was built and the triggers were named
instead.

The table below is the amended state. Principle VIII is the reason L1 was never
extracted, and it is why that turned out to cost nothing: the layer was named,
deferred behind a falsifiable trigger, and retired when the trigger fired and
the need did not appear. Building it first would have produced a module with
no importer.

| Layer | State | Trigger to build |
|---|---|---|
| L1 | **retired** — trigger fired, layer not needed | would revive only for a consumer needing the five sources without depending on Cluster API; an infrastructure provider cannot be one |
| L2 | exists, two instances | the CAPX fork is cut at `kcp/v1.11` and carries the wiring |
| L3 | exists, one instance, not yet a module boundary | the CAPX integration, still unbuilt |

Naming CAPX rather than "a second provider" is the difference between a
deferral and a plan. It also made each trigger falsifiable — and one was
falsified: the CAPX port needed none of the five L1 sources, so L1 is not built
and this ADR was wrong about what is shared. That is the intended behaviour of
a named trigger, not a failure of it.

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

- ~~The fork's contract gains a rule it did not have: new files justify
  themselves against L1 before being carried.~~ **Void with L1.** A new file
  in a fork is judged on the L2 admission rule alone: it is carried if it
  cannot be expressed from outside the module.
- Adding a provider becomes: fork it into this project's ownership (L2), and
  integrate it as a module here (L3). There is no third step.
- The fork count tracks the provider count, and each fork is a repository this
  project maintains: a branch per release line, tags, and a drift record. That
  is the standing cost of the model, and it is the reason L2's admission rule
  is tight.
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
  CAPX was the test and it does need one: even before the collisions are
  fixed, it carries a dependency pin and a dead-code removal that cannot be
  expressed from outside the module.
- **Real providers costing far less than 17 paths.** Partly answered, and in
  the direction that would weaken this ADR: CAPX carries six paths so far
  against the dev provider's 17. If it finishes well below 17, "four forks is
  a thing worth designing for" is a weaker claim than it looked, and L3 alone
  carries more of the weight.

## What is not established

- **CAPX is forked and wired, not integrated and not run.** It builds against
  the fork's stack and its unit tests pass under envtest. Nothing has run it
  against kcp: the integration module is unbuilt, and the e2e suite needs a
  live Prism Central this project cannot reach.
- **The three collisions are unfixed**, and that they behave under kcp as
  described remains a prediction. Two are visible in CAPX's source and collide
  across plain namespaces without kcp; the third is dead code and so latent.
- Whether any of them is accepted upstream in CAPX is unknown, and nothing has
  been filed.
- **The six-path figure is partial.** Each collision fix and the integration
  module add to it. It is not comparable to the dev provider's 17 until the
  port is finished.
- Whether L3's modules can share a `Taskfile` and verification contract across
  differing dependency graphs is untested. It is now the main open risk in
  this ADR, since it is the only structural claim the CAPX port has not
  touched.
