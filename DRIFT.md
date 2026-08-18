# Drift record

The complete set of changes this project carries against upstream Cluster
API. Everything here is a liability: each entry makes the next upstream
adoption more expensive.

`task drift` checks this file against reality and fails on any path that
diverges without an entry here.

## Two kinds of entry

Most entries are **temporary**: they exist to be deleted once an upstream
proposal is accepted, and Constitution Principle I gives them a filing
deadline. The contract-metadata patch is one of these.

Some are **carried deliberately**, with no proposal intended.
[ADR-0003](docs/adr-0003-workspace-aware-cluster-api.md) took that decision
for the workspace-aware wiring: the changes are made in the fork and not
proposed upstream, so their "Upstream proposal" cell reads *None — carried
deliberately* rather than a date.

The distinction is worth drawing in this file rather than leaving it to the
reader, because the two carry opposite obligations. A temporary entry is
overdue if nothing has been filed by its date. A deliberate one is never
overdue, and the corresponding cost is that it must be rebased forever — so
the thing to watch is not a deadline but how much upstream code each entry
touches. A new file rebases cleanly; a modified one does not. Of the
thirty-three deliberate entries below, thirteen are new files and twenty modify
existing ones — and the twenty are the number that matters.

## Where the check runs, and why not on every pull request

The check runs on a daily schedule, on demand, and on pull requests that
touch the pin, this record, or the checker itself — not on every pull
request, and not in the fork.

**Not on every pull request here**, because the thing being measured lives
in another repository. Gating every change on it means an unrelated pull
request goes red because somebody pushed to the fork: a failure its author
can neither fix nor merge past. The scheduled run surfaces the same problem
within a day, without holding anyone's work hostage. The path filter keeps
it blocking exactly where a pull request in this repository *is* the right
place to fix it — when the pinned version or this record changes.

**Not in the fork**, though that is where drift is introduced, because the
checker would become drift. The fork's contract is "upstream at base commit
plus recorded patches, nothing else", which is what makes
`git diff upstream..kcp/v1.15` mean something without mental subtraction.
Adding a workflow and a copy of this record there would add two more
differing paths and require the check to exempt its own infrastructure. It
would also need a second implementation: the checker is Go in this module,
and the fork is the Cluster API module, which cannot import it without a
cycle.

If push-time rejection on the fork is wanted, branch protection on `kcp/*`
is the honest mechanism, not a workflow the fork has to carry.

Fork: [`github.com/jimmidyson/cluster-api`](https://github.com/jimmidyson/cluster-api), branch `kcp/v1.15`, tag `v1.15.0-kcp.2`

Base: `281e4e3ed2af1d6852651d69e1207a3073b478c2`

The base is an upstream `main` commit, not a release tag, and that is
forced rather than chosen: at `v1.14.0` the docker provider's reconcilers
and admission webhooks were under `internal/`, which an external module
cannot import. They became public on `main` afterwards, and this project
cannot build without them. See the feature's research notes (R2).

## Carried patches

| Path | Rationale | Upstream proposal |
|---|---|---|
| `internal/contract/version.go` | Factors the contract-metadata resolver into an overridable package variable. Every contract-version lookup funnels through it, so one seam covers `GetObjectFromContractVersionedRef`, `GetContractVersion` and `GetAPIVersion` uniformly. Default behaviour is unchanged. | **Pending**, due **2026-11-13** |
| `controllers/external/metadata.go` | Exposes `SetGKMetadataGetter` and `GetAPIVersion` publicly, so a module outside `sigs.k8s.io/cluster-api/` can supply its own resolver. Mirrors the existing `conversion.SetAPIVersionGetter` escape hatch. | **Pending**, due **2026-11-13** |
| `util/multicluster/lift.go` | New file. Adapts a single-cluster event handler for a fleet-wide controller, putting the cluster in both the requests it enqueues and the context it runs in. multicluster-runtime does only the former. | None — carried deliberately, ADR-0003 |
| `util/multicluster/client.go` | New file. A `client.Client` that resolves per call to the cluster named in the call's context. What lets one controller serve many clusters with no reconciler changing. | None — carried deliberately, ADR-0003 |
| `util/multicluster/wildcard.go` | New file. Registers one event handler per type against a fleet-spanning cache and demultiplexes per event, in place of one registration per cluster per type. Measured at 45 of the 51.7 goroutines a workspace cost before it. | None — carried deliberately, ADR-0003 |
| `util/multicluster/fleet_test.go` | New file. Envtest for the three above, over multicluster-runtime's namespace provider: that one controller keeps two clusters' work apart, and that a request naming a cluster the provider does not have is dropped rather than retried. | None — carried deliberately, ADR-0003 |
| `util/controller/builder_workspace.go` | New file. `MulticlusterBuilder`: the same controller wiring as `Builder`, keyed on a request that carries the cluster. | None — carried deliberately, ADR-0003 |
| `util/controller/wildcard_registry.go` | New file. Joins fleet-wide controllers to the caches their watches go on, in either order. Controllers are wired before the manager starts; a provider builds its caches after. It also fans each watch out across a fleet spanning shards, which is one cache each. | None — carried deliberately, ADR-0003 |
| `util/controller/wildcard_registry_test.go` | New file. Both arrival orders, fan-out across several caches, idempotence when one cache is offered once per workspace, and which controller and cache a failed registration names. | None — carried deliberately, ADR-0003 |
| `util/controller/controller.go` | **Modified.** The reconciler and controller wrappers take the request type as a parameter, so both builders share one implementation of rate limiting, deferral and the reconcile cache rather than two that can drift. | None — carried deliberately, ADR-0003 |
| `util/controller/builder.go` | **Modified.** `Controller` becomes a generic alias `ControllerFor[reconcile.Request]`, so every existing declaration keeps compiling. | None — carried deliberately, ADR-0003 |
| `util/controller/controller_test.go` | **Modified.** The wrappers are built as struct literals here, so the new fields have to be supplied. | None — carried deliberately, ADR-0003 |
| `util/controller/builder_test.go` | **Modified.** As above. | None — carried deliberately, ADR-0003 |
| `controllers/external/tracker.go` | **Modified.** `ObjectTracker` gains an optional `MultiClusterController`, registering runtime watches fleet-wide. A separate type was tried and does not work: the reconcilers hold the tracker as a concrete field and read `PredicateLogger` off it from the reconcile path. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_cache_workspace.go` | New file. `SetupWithMulticlusterManager`, `GetMulticlusterClusterSource` and the accessor key that carries the logical cluster. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_cache.go` | **Modified.** Accessors and last-event times key on the logical cluster as well as the ObjectKey, resolved from the context. `clusterSource.ch` becomes a send function so one cache can feed both shapes of consumer. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_accessor.go` | **Modified.** Carries the logical cluster, for metric labels. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/metrics.go` | **Modified.** Adds a `logical_cluster` label; without it two workspaces' identically named Clusters share a time series. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_cache_workspace_test.go` | New file. Covers the one behaviour a fleet-wide ClusterCache has that a per-workspace one cannot: a Cluster whose logical cluster has stopped being served is disconnected rather than polled forever. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_cache_fake.go` | **Modified.** Keys the fake's accessor map the same way. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_cache_test.go` | **Modified.** Same keying; the event-fan-out test substitutes the send rather than reading a channel. | None — carried deliberately, ADR-0003 |
| `controllers/clustercache/cluster_accessor_test.go` | **Modified.** Same keying. | None — carried deliberately, ADR-0003 |
| `test/infrastructure/docker/reconcilers/devcluster_reconciler_workspace.go` | New file. `SetupWithMulticlusterManager` for DevCluster. | None — carried deliberately, ADR-0003 |
| `test/infrastructure/docker/reconcilers/devmachine_reconciler_workspace.go` | New file. `SetupWithMulticlusterManager` for DevMachine. | None — carried deliberately, ADR-0003 |
| `test/infrastructure/docker/reconcilers/devmachine_reconciler.go` | **Modified.** The `controller` field narrows to the one method the reconcile path calls. | None — carried deliberately, ADR-0003 |
| `test/infrastructure/docker/reconcilers/backends/docker/taskmanager.go` | **Modified.** Tasks key on the logical cluster; progress events carry it; `GetSource` gains a fleet-wide counterpart. | None — carried deliberately, ADR-0003 |
| `test/infrastructure/docker/reconcilers/backends/docker/dockermachine_backend.go` | **Modified.** Passes the context to the task manager calls that now need it. | None — carried deliberately, ADR-0003 |
| `test/go.mod` | **Modified.** go directive raised to match the root. | None — carried deliberately, ADR-0003 |
| `test/go.sum` | **Modified.** As above. | None — carried deliberately, ADR-0003 |
| `core/reconcilers/cluster/cluster_controller_workspace.go` | New file. `SetupWithMulticlusterManager` for the Cluster reconciler. | None — carried deliberately, ADR-0003 |
| `core/reconcilers/machine/machine_controller_workspace.go` | New file. `SetupWithMulticlusterManager` for the Machine reconciler. | None — carried deliberately, ADR-0003 |
| `core/reconcilers/machine/machine_controller.go` | **Modified.** `watchClusterNodes` builds its watch through a `nodeWatcherFunc` each setup installs, because `clustercache.NewWatcher` is keyed on the controller's request type and the fleet-wide watch additionally needs the management cluster from the context. The `controller` field narrows to the one method the reconcile path calls. | None — carried deliberately, ADR-0003 |
| `core/reconcilers/machine/machine_controller_test.go` | **Modified.** One test reaches `watchClusterNodes` through a struct literal and has to install the watcher the seam expects. | None — carried deliberately, ADR-0003 |
| `go.mod` | **Modified.** Adds `sigs.k8s.io/multicluster-runtime`. | None — carried deliberately, ADR-0003 |
| `go.sum` | **Modified.** As above. | None — carried deliberately, ADR-0003 |

The first two paths belong to a single patch, carried as one commit in the
fork. The rest are the workspace-aware wiring, and they are listed
per-path rather than as one entry because the check is per-path.

**Not yet on `kcp/v1.15`.** The wiring lives on the fork's
`claude/workspace-aware-wiring` branch. Until it merges, `task drift` will
report these as *missing* — recorded but not diverging — which is reported
and does not fail. That is the correct state to be in while the branch is
open, and it flips to silence when the branch lands.

### What to watch on the deliberate entries

Thirteen are new files and rebase for free. Twenty modify upstream files, and
those are the real recurring cost:

- `util/controller/controller.go` and `builder.go` — the largest, and the
  one that would conflict with any upstream change to the builder.
- `controllers/clustercache/*` — the second largest, and the one this
  project would most like upstream to take, because a cluster-blind accessor
  map is a latent fault for anyone running Cluster API over more than one
  logical cluster.
- `controllers/external/tracker.go` — one field and one branch.
- `core/reconcilers/machine/machine_controller.go` — one seam, one field
  type. This is the only reconciler touched, and its *reconcile logic* is
  unchanged: `watchClusterNodes` names the same watch with the same
  arguments and no longer decides how it is built.
- the three test files and `go.mod`/`go.sum` — mechanical.

ADR-0003's premise is "unmodified upstream reconcile logic", not
"unmodified upstream files", and this table is what that distinction costs
in practice.

### Why the contract-metadata patch exists

Resolving a contract-versioned reference — `spec.infrastructureRef`,
`spec.bootstrap.configRef`, `spec.controlPlaneRef` — reads contract-version
labels off the referenced type's `CustomResourceDefinition` object. That
assumes a CRD object is the source of truth for every type a cluster serves.

Under kcp it is not: a workspace consuming a type through an `APIBinding`
has no such object, because the CRD-shaped source of truth is an
`APIResourceSchema` in the *exporting* workspace. Every reconcile that
resolves a cross-reference fails with an internal error — which is not a
corner case, since `infrastructureRef` is how every infrastructure provider
integrates with core Cluster API.

## Pending proposals

Constitution Principle I permits carrying a patch before its proposal is
filed, but only as an explicitly pending state with a filing date no more
than 90 days out. Once that date passes without a filed proposal, the
carried patch is a defect in this project — the response is to file, remove
the patch, or amend the constitution, never to extend the date quietly.

| Patch | Landed | Proposal due |
|---|---|---|
| Pluggable contract-metadata resolution | 2026-08-15 | 2026-11-13 |
