# Feature Specification: Measuring a fleet as it actually deploys

**Feature Branch**: `claude/scale-test-200-clusters-vfin19`

**Created**: 2026-08-31

**Status**: Built, never run — see "Where this stands"

**Input**: "wonder if we should run this in kind so we can separate out
resources per controller deployment to mimic a real deployment" — and "it would
also be great to run the test against a real kubernetes cluster so components
aren't necessarily on the same node."

## Where this stands

The harness is built and unit tested. **No deployed run has been taken**, and
none can be taken in an environment without a cluster and a container runtime.
Per Constitution Principle IX that is reported as not measured rather than
predicted, so this specification contains no figures.

What is built:

- `internal/managermetrics` — the Go runtime and process collectors on
  controller-runtime's registry, and a `--metrics-bind-address` flag on all
  four managers. Without this the whole feature is impossible: the registry is
  a bare `prometheus.NewRegistry()` carrying none of the collectors the default
  registerer has, so a manager served no `go_goroutines`, no `go_memstats_*`
  and no `process_resident_memory_bytes` at all, and a deployed run would have
  had nothing to reconcile against.
- `internal/deployedscale` — the manifests, the generated credentials, the
  metric parsing, the pod facts, the report and the reconciliation. Every part
  of it that does not need a cluster has tests, including the assertion that
  nothing in the manifests assumes one node.
- `test/integration/deployed` — the run itself, driven by
  `task test:scale:cluster`. Its "could not run" path is exercised; the rest
  has never executed.
- `task test:scale:images` — the four images, built with `ko`, which installs
  with the Go toolchain alone as the constitution requires. Verified by
  installing it, not assumed.

### What a run is likely to need

Measured on a 4 vCPU machine, in process, at 4 workspaces × 1 cluster × 3
machines — not a deployed run, but the two terms that will set its budget:

| | RSS |
|---|--:|
| kcp, started, no workspaces | ~360 MiB |
| kcp, 4 workspaces / 4 clusters / 12 machines | ~770 MiB, still climbing |
| all four providers co-located, plus the in-memory workload clusters and the driver | 62 MiB idle, 119 MiB at that point |

Two readings, both of which matter to this feature:

**kcp dominates and is not flat.** 360 → 770 MiB across four workspaces. Every
figure this repository has published measures the *managers* and is silent
about the server they talk to; on this evidence the server is the larger term.
RSS is returned lazily, so the marginal cost is an upper bound rather than a
slope — but it is not small, and a deployed run needs kcp sized as a
first-class component rather than as background.

**The heap-to-resident multiplier looks like 2.2–2.8×** — 42.5 MiB live heap
against 94–119 MiB resident. That is the first data point on the figure
`capacity.md` says it needs and never states. It is an upper bound, because the
process also held the driver and its fixture clients, and it is exactly what a
deployed run measures properly.

From those, a **prediction** for kind: about 3–4 GiB and 2–4 vCPU for an M1 or
M2 smoke run at 8 workspaces, 5–7 GiB at 32 workspaces with three machines
each, on top of ~1.5–2 GiB for the kind node itself. The 200 × 50 target is not
a kind-on-a-laptop proposition — extrapolating kcp alone gives 6–12 GiB, and
`dev-infrastructure-manager` would hold 200 in-memory API servers and 10,000
Nodes in one container, which is the term nothing has measured and the most
likely thing to be killed first.

**M1 is therefore ready to attempt and has not been attempted.** The first run
is `COMPONENTS=core-manager` against the committed core sweep; until those two
agree there is no reason to trust the deployed instrument with four.

## Purpose

Every resource figure this repository has ever published carries the same
asterisk, and it is written on each of them:

> **none — four deployments co-located, so one engagement per workspace rather
> than four**

That is the `deployment` fact on the fleet sweep's report and on the fleet
target's. It means a workspace in those runs pays **one** engagement where an
installation pays four, and shares **one** `ClusterCache` where an installation
has one per deployment. `capacity.md` names the same limitation as R17:
*"capacity is per deployment role, not one number… The single-process figures
below are the measurement, not the deployment shape."*

The asterisk cannot be removed by a better in-process measurement, because it is
not a measurement error. It is a property of measuring four deployments inside
one process. Removing it means running them as four deployments.

This feature does that: the four managers as four `Deployment`s in a Kubernetes
cluster, each in its own container with its own limits, measured per deployment.

## What this measures that nothing here can

| Quantity | Today | With this |
|---|---|---|
| Memory per deployment | one process's heap, split by arithmetic across four providers' wiring | each container's RSS, from the kubelet |
| CPU | **not modelled at all** (`capacity.md`, "What is not covered") | CPU seconds and throttling per container |
| Engagements per workspace | one | four — the real number |
| `ClusterCache` instances | one, shared | four, one per deployment |
| Heap → container limit | *"a separate step with its own stated multiplier"*, unmeasured | the multiplier itself, measured |
| Network between components | none: everything is in one address space | real hops, real TLS, real latency |
| Failure at capacity | a larger heap number | an OOMKill, which is a capacity finding |

The heap-to-RSS row is worth calling out. `capacity.md` reports live heap and
says converting it to a resident-size budget is a separate step with a stated
multiplier — and then never states one, because nothing could measure it. A
deployed run measures both quantities on the same process at the same moment,
which turns every existing figure in this repository into a container limit.

## The target is a kubeconfig, not kind

kind is how somebody gets a cluster on a laptop. It is **not** the thing being
targeted, and the distinction is a requirement rather than a preference:
anything that assumes kind assumes one node, and a single-node cluster cannot
show what this feature exists to show.

So the harness takes a **kubeconfig** and is indifferent to what produced it.
kind is one path, and the only one the repository automates; a real multi-node
cluster is the other, and it is where the figures are worth quoting from.

What follows from refusing kind-specific assumptions:

- **No `127.0.0.1` between components.** Every address one component uses to
  reach another must be routable from another node.
- **No `hostPath`, no host networking, no NodePort-on-localhost.**
- **kcp runs in the cluster** for the multi-node case. An external kcp on the
  operator's laptop is unreachable from a pod, so the in-cluster deployment is
  the shape, and a local kcp is at most a convenience for the single-node case.
- **Placement is data.** Which node each pod landed on is recorded with the
  figures, because a run where everything was co-scheduled is a different run
  from one where it was spread, and the two must not be compared silently.

## What already exists, and what has to be built

The deployed shape is not a new design. It is the shape the four `cmd/`
binaries were written for, and one of its hardest constraints is already
handled deliberately:

> The host is what the in-memory backend advertises its workload clusters at,
> and it is a parameter rather than upstream's `os.Getenv("POD_IP")` because an
> empty one does not fail […] **A deployment passes its pod IP**; a process
> serving itself passes `127.0.0.1`.
> — `coremanager.NewDevInfrastructure`

`cmd/dev-infrastructure-manager` already reads `POD_IP`, and the mux binds on
all interfaces, so the workload-cluster listeners are reachable from the
core-manager pod's `ClusterCache` without anything new. The same doc comment
states the other deployed constraint — the mux binds a fixed port range, so two
of them on one node collide — which makes the dev-infrastructure deployment
single-replica per node, or per-replica ranges.

What has to be built:

- **Container images for the four managers**, at the pinned version, built with
  the language toolchain rather than a system package manager (Constitution,
  Environment and Tooling Constraints).
- **Manifests this project owns** for kcp and the four deployments, with
  requests, limits and the `POD_IP` wiring.
- **A load driver** that creates the workspaces and clusters. `internal/scaletarget`
  already decides what to build and reads a run against it, and is reusable
  unchanged.
- **A metric collector**, because `runtime.MemStats` is unavailable across a
  process boundary. Two sources, deliberately:
  - each manager's own `/metrics` (`go_goroutines`, `go_memstats_*`), which is
    the **same quantity the in-process instrument reports** and is therefore
    what makes the two comparable;
  - the kubelet's container metrics (RSS, CPU seconds, throttling, restarts),
    which is what a limit is actually set against.

  The pair is the point. Either alone answers half the question.

## Two instruments, and the rule about that

This repository has already decided that two instruments measuring one process
is worse than one instrument being wrong, because a disagreement leaves neither
side obviously at fault. `internal/scaleharness` stopped measuring for exactly
that reason.

So the relationship is stated up front rather than discovered:

- **The in-process instruments remain the primary ones** for per-workspace cost.
  They are cheap, they run without a cluster, and almost nothing between the
  manager and kcp is unaccounted for in them.
- **The deployed instrument measures what they structurally cannot**: the split
  across deployments, CPU, the container multiplier, and the network.
- **They overlap at exactly one point, on purpose.** The same fleet shape run
  both ways must produce agreeing `go_goroutines` and `go_memstats_*`, because
  it is the same Go program doing the same work. That agreement is the
  deployed harness's own calibration, and a disagreement is a finding about one
  of them — which is the check that keeps a second instrument honest rather
  than merely additional.

## Out of Scope

- **A production deployment model.** This produces manifests for measurement.
  Whether they are also what an installation should use is a separate question
  with different concerns (upgrades, RBAC hardening, HA).
- **The docker backend.** The in-memory backend stays the workload for the same
  reason it does everywhere else here: the docker backend would measure Docker.
- **Replicas and sharding.** One replica per deployment. Measuring how
  workspaces spread across replicas is its own feature, and the existing spec
  already carries requirements for it.
- **Autoscaling, or deriving limits automatically.** This measures what a
  deployment costs. Choosing a limit from that is a decision, and remains one.
- **Retiring any existing figure.** Nothing measured in-process is withdrawn by
  this. Per Constitution Principle IX a figure is re-measured when the wiring it
  describes changes, and this changes no wiring.

## User Scenarios & Testing

### User Story 1 — An operator sizes one deployment's container (Priority: P1)

An operator has to put `resources.limits.memory` on `core-manager` for a shard
of a stated size. Today the only input is a live-heap figure and an unstated
multiplier.

**Acceptance:** a run at a stated fleet size reports core-manager's container
RSS and CPU alongside its own `go_memstats_*`, and the ratio between them is
recorded as the multiplier.

### User Story 2 — A regression is attributed to one provider (Priority: P1)

A change lands and the fleet gets more expensive. In a co-located measurement
the answer is "the fleet got worse", with nothing to point at.

**Acceptance:** each deployment's cost is reported separately, so a run before
and after names which container moved.

### User Story 3 — Components are not on the same node (Priority: P2)

The figures are quoted for a real cluster where the providers, kcp and the
workload clusters are on different machines and everything between them is a
network hop.

**Acceptance:** a run against a multi-node cluster completes, records which node
each pod ran on, and reports the same measurements. Any figure taken with pods
co-scheduled is labelled as such.

### User Story 4 — The deployed and in-process figures are reconciled (Priority: P2)

Somebody wants to know whether the cheap instrument can still be trusted.

**Acceptance:** the same fleet shape run both ways reports `go_goroutines`
within a stated tolerance, and a divergence beyond it fails the run rather than
being reported as two numbers.

## Requirements

### Functional Requirements

**The environment**

- **FR-001**: The harness MUST take a kubeconfig and MUST NOT require that the
  cluster was created by kind.
- **FR-002**: The harness MUST NOT assume components share a node, a network
  namespace, or a filesystem. No `127.0.0.1` between components, no `hostPath`,
  no host networking.
- **FR-003**: Every tool the harness needs MUST be installable at a pinned
  version using the language toolchain alone, into a location local to the
  repository (Constitution, Environment and Tooling Constraints).
- **FR-004**: kcp MUST be deployable into the cluster, at the same pinned
  version as `bin/kcp`, so the two paths cannot measure different servers.
- **FR-005**: The harness MUST report "could not run" — distinctly from failure
  — when no cluster is reachable, when the cluster cannot schedule the
  deployments, or when images cannot be built or pushed.

**The deployments**

- **FR-006**: Each of the four managers MUST run as its own `Deployment`, in its
  own container, with its own requests and limits.
- **FR-007**: The dev infrastructure deployment MUST advertise its pod IP as the
  in-memory backend's host, and its workload-cluster listeners MUST be reachable
  from the core-manager pod.
- **FR-008**: The dev infrastructure deployment MUST run one replica per node, or
  give each replica its own port range: the mux binds a fixed range and two on
  one node collide.
- **FR-009**: Each manager MUST expose its metrics endpoint to the harness, and
  that endpoint MUST NOT be reachable outside the measurement's namespace.

**The measurement**

- **FR-010**: Cost MUST be reported **per deployment**, never only as a total. A
  sum that hides which deployment moved is the failure this feature exists to
  fix.
- **FR-011**: Each sample MUST record both the process's own metrics
  (`go_goroutines`, `go_memstats_*`) and the container's (RSS, CPU seconds,
  throttling, restarts), taken at the same moment.
- **FR-012**: The heap-to-RSS ratio MUST be reported per deployment as a
  measured figure, and MUST be labelled with the fleet size it was taken at.
- **FR-013**: Each sample MUST record which node each pod was running on.
- **FR-014**: A container OOMKill or restart during a run MUST be reported as a
  capacity finding, and MUST NOT be silently retried into a pass.
- **FR-015**: The report MUST record whether the run was single-node or spread,
  and a single-node figure MUST NOT be presented as a multi-node one.
- **FR-016**: A deployed run MUST be reconcilable with an in-process run of the
  same shape on the quantities both measure, within a stated tolerance.

**What it is not allowed to become**

- **FR-017**: The deployed run MUST NOT be part of `verify` or `check`. It needs
  a cluster, and the done-condition must not depend on one — the same reason
  `test:scale` and `test:scale:local` sit outside it.
- **FR-018**: No figure this produces may be published without stating the
  cluster it came from: node count, node size, and whether pods were spread.

### Key Entities

- **Target cluster** — a Kubernetes cluster addressed by kubeconfig. Attributes:
  node count, node capacity, whether it was created by the harness.
- **Deployed component** — one manager, its container, its limits, and the node
  it landed on.
- **Deployed sample** — one moment, carrying both metric sources for every
  component plus placement.
- **Reconciliation** — the comparison between a deployed run and an in-process
  run of the same shape, on the quantities both measure.

## Success Criteria

- **SC-001**: A named operation runs a stated fleet against a cluster given only
  a kubeconfig, and reports per-deployment cost.
- **SC-002**: The report gives each of the four deployments its own memory and
  CPU figures, and no criterion here is satisfied by a total alone.
- **SC-003**: A measured heap-to-RSS multiplier is recorded for at least
  `core-manager`, at a stated fleet size.
- **SC-004**: The same fleet shape run in-process and deployed reports
  `go_goroutines` within a stated tolerance, and the run fails if it does not.
- **SC-005**: A run against a cluster of two or more nodes completes and records
  the placement of every pod.
- **SC-006**: A run whose container was OOMKilled reports that as its outcome
  rather than as a smaller measurement.
- **SC-007**: A run against no reachable cluster reports "could not run", and
  `verify` is unaffected by its absence.

## Risks and open questions

These are named rather than answered; each is research for the plan.

1. **How images get built.** `ko` is go-installable and needs no Dockerfile,
   which fits FR-003 well; a plain `docker build` needs a daemon this project
   already depends on elsewhere. Recommendation: `ko`, decided in the plan.
2. **How the harness reaches the metrics endpoints.** Port-forward per pod is
   simple and does not require the harness to run in-cluster, but it is a
   per-sample cost at fleet size. An in-cluster collector is the alternative.
3. **Whether the driver runs in or out of the cluster.** Out is simpler and
   reuses the existing code unchanged; it requires kcp to be reachable from
   outside, which is a real constraint on a managed cluster.
4. **Container metrics source.** The kubelet summary API needs no extra
   component; `metrics-server` is friendlier but is another thing to install
   and has a resolution floor that may be too coarse for a settling sample.
5. **What the workload clusters cost the dev-infrastructure pod.** Two hundred
   in-memory API servers in one container is the single most likely thing to hit
   a limit first, and it is a component of the measurement rather than of the
   system being measured.
6. **Whether the fleet target's 200×50 shape fits at all** in a container-limited
   deployment. It may not, and finding that out is a result.

## Milestones

Ordered so each is worth landing on its own, per AGENTS.md's preference for a
stack over one large change.

- **M1 — one deployment, calibrated.** kcp and `core-manager` in a cluster, the
  existing driver outside it, at a fleet size the in-process instrument has
  already measured. Deliverable: the heap-to-RSS multiplier, and the
  reconciliation of SC-004. This is the milestone that proves the instrument
  before it is trusted with anything new.
- **M2 — four deployments, split.** All four managers, per-deployment
  accounting, the fleet target's shapes. Deliverable: the first figures that do
  not carry the co-location asterisk.
- **M3 — spread.** A multi-node cluster, placement recorded, the network in the
  measurement. Deliverable: figures quotable for a real deployment.

## Verification

- `task test:unit` covers the manifest generation, the metric parsing, and the
  reconciliation tolerance — all of which are arithmetic and text, and none of
  which needs a cluster.
- The named operation reports "could not run" with no cluster reachable, which
  is checkable without one.
- M1's reconciliation against a committed in-process run is the harness's own
  correctness check.
- Per Constitution Principle IX, every figure this produces is committed under
  `evidence/` with the cluster it came from, and nothing is quoted that has not
  been run.
