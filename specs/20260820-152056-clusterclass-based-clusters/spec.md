# Feature Specification: ClusterClass based clusters

**Feature Branch**: `claude/clusterclass-based-clusters-uketqw`

**Created**: 2026-08-20

**Status**: Draft — implementation complete, pending the fork tag (see Success Criteria)

**Input**: "We should base everything on ClusterClass based clusters."

## Purpose

Until now a cluster in this project was six objects a tenant wrote out by hand
and had to keep in agreement: a `Cluster`, a `DevCluster`, a
`KubeadmControlPlane`, a `DevMachineTemplate`, a `KubeadmConfigTemplate` and a
`MachineDeployment`. That was the shape of the demo, of the integration tests
and of the documentation, because it was the shape the core provider could
serve — `ClusterClass` and the managed topology were the part of upstream's
core set this project had not wired.

This feature makes a cluster **a `Cluster` that names a `ClusterClass`**, and
makes that the shape everything here is built and measured on.

Two reasons, and the second is the one that matters to this project.

**It is what a tenant of a fleet is given.** A workspace is a tenant. Handing
a tenant a class means handing them eight lines to get a cluster instead of six
objects that have to agree with each other, and it means a fix or a version
bump is made once, in the class, for every cluster built from it. A
multi-tenant Cluster API that cannot do that is not offering the thing its
users came for.

**It is the harder case to serve, and this project exists to serve the hard
case.** A managed topology adds four reconcilers to the core provider, a
server-side apply of every object under a `Cluster` on every reconcile, and a
cross-object read — `Cluster` to `ClusterClass` to five templates — that has to
resolve inside one workspace and must never resolve across two. Everything
this project is about is in that last sentence.

## Out of Scope

- **Variables and patches.** A `ClusterClass` may declare variables and JSON
  patches over its templates. This feature wires the machinery that would run
  them and defines a class that uses neither. Variable *defaulting* is done by
  the `Cluster` admission webhook, and webhooks serve one workspace or none
  until the conversion plan's G4 lands — so a class with defaulted variables
  would work in one workspace and silently not in the rest. That is the wrong
  thing to demonstrate, and the honest order is G4 first.
- **Runtime SDK / runtime extensions.** `ExtensionConfig`, external patches
  and the upgrade-plan extension stay behind the `RuntimeSDK` gate, which stays
  off. Nothing here publishes `runtime.cluster.x-k8s.io`.
- **`MachinePool` classes.** `MachinePool` is outside ADR-0001's D3 scope and
  its CRD is not published, so a class naming a machine pool class could not be
  served.
- **Upgrades through the class.** Changing a class's version and watching every
  cluster roll is the thing a class is for, and it is a separate feature with
  its own measurements. This one takes clusters to ready and asserts tenancy.
- **`clusterctl` templates.** Still the conversion plan's P5.

## What changes

### The fork gains four fleet-wide setups

`SetupWithMulticlusterManager` for `clusterclass`, `topology/cluster`,
`topology/machinedeployment` and `topology/machineset` — the same For, watches,
map functions, predicates and gates as each reconciler's single-cluster twin,
on the builder that keys the queue on a request carrying the cluster. Recorded
in [`DRIFT.md`](../../DRIFT.md), carried deliberately per ADR-0003, as every
other provider's setup is.

Two watches are gated where upstream registers them unconditionally:
`topology/cluster`'s on `MachinePool`, and `clusterclass`'s on
`ExtensionConfig`. A fleet-wide watch is one registration against a shared
cache, and controller-runtime blocks a controller's startup on every registered
source's cache sync — including for a kind the server does not serve. An
unserved watched type does not skip the watch, it hangs the controller. The
first of the two is also where upstream disagrees with itself: the core
`Cluster` reconciler's own `MachinePool` watch is already behind that gate.

One latent fault in this project's own patches is fixed on the way:
`MulticlusterBuilder.For` took opaque builder options and dropped them in
wildcard mode, because that mode never builds the multicluster builder. No
caller had passed any. All three topology controllers do, and what they pass is
the filter that makes them cheap — only `Cluster`s with a topology, only the
`MachineDeployment`s and `MachineSet`s a topology owns.

### The core provider wires them, behind a gate that is now on by default

`coremanager.SetupCoreControllers` wires the four when `ClusterTopology` is
enabled, and this project enables it. `coremanager.SetFeatureGateDefaults` is
where that is stated, called before flag parsing so `--feature-gates` still
overrides it.

The same function turns `MachinePool` **off**, which upstream defaults on. That
was previously done by each caller that got far enough to notice — the demo,
and every integration test that starts a manager — and by no deployment at all,
which made a documented operator responsibility out of a defect: a
`core-manager` started with upstream's defaults against this project's exports
hangs on a `MachinePool` cache sync that never completes.

### The exports publish what a class refers to

`clusterclasses` joins the core export; `devclustertemplates` joins the
infrastructure export beside `devmachinetemplates`. Core's claims on the
infrastructure export widen to include `devclustertemplates`, because the
topology controller does not merely dereference a template — it creates the
object stamped from it, and Cluster API patches owner references onto templates
so they are garbage-collected with the cluster.

### Indexes reach the caches the reconcilers read through

The topology reconciler maps a `ClusterClass` event to the `Cluster`s using it
by listing with a field selector on `spec.topology.classRef`. A
controller-runtime cache fails such a List rather than falling back to a scan,
so the index has to exist on the cache the reconciler reads through — which
here is the provider's per-shard kcp-aware cache, not the manager's.

Upstream registers its indexes against a manager, which works where those are
the same cache. `coremanager.FleetCacheIndexes` declares them as data instead
and `providerwiring.WithCacheIndexes` replays them onto each shard's cache as
it appears, before that shard's watches are registered — the same shape, and
the same reason, as the wildcard watch registry.

### The demo, the tests and the docs are built on a class

`internal/demo` creates one `ClusterClass` and five templates per workspace and
then a `Cluster` per cluster that names it. The class pins the names of what it
creates — `demo-00`, `demo-00-cp`, `demo-00-md` — with naming templates, so
that a walkthrough can print a `kubectl get` and a person can predict it.
Nothing depends on the pinning; a class that omits it works as well, and is
what a real tenant would write.

The sweep shape that runs every provider together builds ClusterClass based
clusters, because it is the one that measures what an installation pays. The
single-provider sweeps keep hand-built stand-in objects: each of them
deliberately excludes the core provider, and the topology controllers are the
core provider's.

## Requirements

- **FR-001** A `Cluster` naming a `ClusterClass`, in a workspace bound to the
  exports, reaches `Available` with its control plane replicas and workers
  Ready — reconciled by managers that were told about no workspace.
- **FR-002** Every object under such a `Cluster` — the infrastructure cluster,
  the control plane, the worker `MachineDeployment` and the templates each is
  stamped from — is created by the topology controller, in the workspace the
  `Cluster` lives in, and in no other.
- **FR-003** Two workspaces each holding a `ClusterClass` called `demo` and a
  `Cluster` called `demo-00` are served without either one's class, templates
  or objects being visible to the other.
- **FR-004** A deployment started with this project's own defaults wires the
  topology controllers and serves them; an operator can turn the gate off and
  gets the previous behaviour.
- **FR-005** A `ClusterClass` change reaches every `Cluster` using it. This is
  the requirement the field index exists for, and it fails silently without it.
- **FR-006** What a workspace costs is re-measured on this shape rather than
  carried over: the wiring the published figures describe has changed.

## Success Criteria

- `task verify` passes, with `test/integration/demo` asserting FR-001 to
  FR-003 on ClusterClass based clusters. **Met**, with one step reported as
  *could not run* — see below.
- `task test:sweep` reports the per-workspace cost of the fleet shape with a
  ClusterClass based cluster in every workspace, and the figures in
  [Workspace resource usage](../../docs/site/content/en/docs/design/workspace-resource-usage.md)
  and [Usage](../../docs/site/content/en/docs/user/usage.md) are the figures
  from that run. **Met** — the run is in [`evidence/`](evidence/README.md).
- `task drift` passes against a signed tag of the fork carrying the four
  setups. **Not met**, and it is the one thing here that cannot be finished
  from a session: the pin is a pseudo-version of the fork branch until somebody
  cuts `v1.15.0-kcp.12` with a key. `DRIFT.md` says what has to happen; the
  check passes against the branch (`go run ./cmd/drift -ref <branch>`) and
  reports *divergence matches DRIFT.md exactly*.

## Where this landed

- **FR-001 to FR-003**: `test/integration/demo` takes two workspaces to ready
  ClusterClass based clusters in about 90 seconds and asserts the isolation.
  Both workspaces hold a class called `demo` and a `Cluster` called `demo-00`,
  and they are distinct objects.
- **FR-004**: `coremanager.SetFeatureGateDefaults`, called before flag parsing
  in all four managers, so `--feature-gates=ClusterTopology=false` still wins.
- **FR-005**: `coremanager.FleetCacheIndexes` and
  `providerwiring.WithCacheIndexes`.
- **FR-006**: measured, in [`evidence/`](evidence/README.md). Wiring the four
  topology controllers moved exactly one number in the per-deployment table —
  the core deployment holds two more watch streams on the shard — and moved no
  per-workspace column at all.

Three things the implementation found that the specification did not predict,
all now in [The demo](../../docs/site/content/en/docs/design/demo.md):

1. A feature gate guards watches and reconcilers, not reads. The topology
   reconciler lists `MachinePool`s on every reconcile whatever the gate says,
   so the type has to be published even though nothing here reconciles one.
2. The topology reconciler needs `delete` on `Secret`s, for the cluster shim it
   owns its work through. Without it a cluster comes up completely and then
   reports `TopologyReconciled=False` forever — a permission failure wearing
   the costume of a reconcile bug.
3. A `Cluster` being deleted was blocked forever by a `ClusterClass` that had
   already gone, which is what a deleted `APIBinding` produces when it removes
   every bound object at once. Fixed in the fork, as an upstreamable bug: a
   deleted namespace tears a class down alongside its clusters the same way.
