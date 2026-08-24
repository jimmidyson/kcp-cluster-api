# Feature Specification: Running the demo on Kubernetes

**Feature Branch**: `claude/demo-kubernetes-deployment-cget4y`

**Created**: 2026-08-24

**Status**: Draft

**Input**: User description: "i'd like to turn this demo into something that can actually run on kubernetes rather than local processes"

## Purpose

`task demo` starts a kcp server and every provider's controllers in one
process. That is the right shape for showing what this project does and the
wrong shape for believing it. The managers share a process they would never
share in an installation; the shard is a child process that dies with the
terminal; and a green run says nothing about whether the wiring survives being
split across pods that reach kcp over a network with credentials of their own.

The topology this project describes everywhere else - one deployment per
provider, each discovering workspaces through its own `APIExport` - has never
actually been deployed. This feature deploys it.

## Scope

**In scope**

1. Container images for the binaries this repository builds - one per binary,
   built with ko - carrying the CRD manifests an `APIExport` is published from.
   The shard is upstream's image, not one of them.
2. Kubernetes objects for an installation, built in Go from the provider list:
   a kcp shard, one deployment per provider, the workspace manager, and the
   credentials they share.
3. The credentials themselves: a serving certificate naming the shard's
   Service, a client CA, and client certificates reproducing the two
   identities kcp mints for itself.
4. A command that applies them, waits, runs the demo as a `Job`, and reports
   what the Job printed - or prints the objects instead, for an installation
   that applies its own YAML.
5. Whatever the managers need in order to start in an order nobody controls.
6. Named task targets, including one that creates a kind cluster to run in.
7. User and design documentation.

**Out of scope**

- Publishing an image anywhere. They are built locally and loaded into a
  cluster; building to a registry is a flag, not a pipeline.
- More than one shard, or more than one replica of any manager. Both need
  work that is not this feature's: leader election for the second, kcp
  `Partition`s for the first (plan item D6).
- Helm, kustomize, or an operator. The objects are built in Go and can be
  printed as YAML.
- The docker backend. The dev infrastructure provider's pod has no container
  runtime, so the deployed demo runs the in-memory backend.
- Webhooks, which are single-workspace until the dispatch layer lands.
- Measuring what the split topology costs per workspace.

## User scenarios

### Somebody who wants to see it run for real

**Given** a machine with a container runtime, **when** they run
`task demo:kubernetes:kind`, **then** a kind cluster is created, the image is
built and loaded, the shard and five managers are deployed, and the demo's
tables are printed from a `Job` - **and** `kubectl get pods` shows six pods
rather than one process.

### Somebody with a cluster of their own

**Given** a Kubernetes cluster their kubectl is pointed at and an image its
nodes can pull, **when** they run `task demo:kubernetes`, **then** the same
thing happens in that cluster.

### Somebody who applies their own manifests

**Given** they want to see or keep what would be applied, **when** they run
`go run ./cmd/deploy --output yaml`, **then** they get every object, including
the generated credentials, and nothing is applied.

### A manager that starts before its workspaces exist

**Given** every object is applied at once, **when** a provider manager starts
before any workspace has bound its export, **then** it waits rather than
exiting, and reconciles from the moment the first workspace binds.

### Taking it away

**Given** an installation, **when** they run `task demo:kubernetes:clean`,
**then** the namespace and everything in it is gone, the shard's volume
included.

## Functional requirements

- **FR-001** One image per binary, named after it, each carrying its own
  entrypoint. A deployment names an image and its arguments, and overrides
  nothing the build decided.
- **FR-002** The shard runs upstream's kcp image at the version `task tools`
  installs, and is not built here. The two pins on that version are held
  together by a test.
- **FR-003** The demo's image carries the CRD manifests of the pinned modules,
  copied in the build that compiled the binary, and the binary resolves them
  from there without being configured to. Publishing an `APIExport` needs them
  and a container has no Go toolchain to resolve them with.
- **FR-004** The shard is served with a certificate naming its Service, in
  every form Kubernetes resolves it, and `localhost` - the last so that one
  kubeconfig works through a port-forward. kcp has no flag that would add a
  name to the certificate it generates for itself.
- **FR-005** Client credentials are issued before the shard starts, and
  authenticate as the two identities kcp mints for itself: `kcp-admin` in
  `system:kcp:admin`, and `shard-admin` in `system:masters`. The second is
  what impersonating a tenant requires.
- **FR-006** Nothing reads anything back out of the shard's pod. No sidecar,
  no shared volume.
- **FR-007** Each manager gets a kubeconfig whose current context is the one
  it can use: scoped to the provider workspace for a provider manager,
  cluster-unaware for the workspace manager.
- **FR-008** A manager waits, bounded and configurable, for the things it
  cannot create for itself: its export's virtual workspace endpoint, and for
  the workspace manager the `WorkspaceType`. A failure that will not resolve
  by waiting - a forbidden read - is reported at once.
- **FR-009** A container that is waiting is not killed for waiting: the
  startup probe's budget covers the startup timeout.
- **FR-009a** A probe that a deployment makes is answered. A manager that
  binds a health address serves the endpoints the probes ask for.
- **FR-010** Every manager's metrics endpoint is configurable. Several
  managers on one machine cannot all take controller-runtime's default port.
- **FR-011** One replica per manager, and `Recreate` rather than
  `RollingUpdate`: these controllers hold no lease, so two of them would
  reconcile every workspace twice.
- **FR-012** A provider with no manager in the image fails the build of an
  installation, rather than producing a shard that serves types nothing
  reconciles.
- **FR-013** The demo run in the cluster is the same code as `task demo`,
  with its manager half switched off. Nothing describes a demo twice.
- **FR-014** The objects can be printed instead of applied.
- **FR-015** An installation is removed by removing its namespace.

## Success criteria

- **SC-001** `task demo:kubernetes:kind` reaches ready clusters in every
  workspace, and the isolation table has the shape `task demo` produces.
- **SC-002** The managers are separate pods, holding only their own
  credentials, and no workspace is named in any of their configuration.
- **SC-003** Applying everything at once converges without a manager
  crash-looping.
- **SC-004** A kubeconfig written by the run reaches the deployed shard
  through a port-forward with no further arguments.

## Verification

Unit tests cover the credentials and the objects: that the serving
certificate verifies for every name a client uses, that the client
certificates carry the identities kcp authenticates, that a kubeconfig built
from them authenticates against a TLS server configured the way kcp is
configured here, and that each object carries the arguments, mounts and
environment its component needs.

The identities themselves were established against a running kcp with a
`SelfSubjectReview`, and the certificate arrangement - a supplied serving
certificate and client CA, authenticating as those identities - was confirmed
against one before it was written down.

The end-to-end claim is `task demo:kubernetes:kind`, which needs a container
runtime and a Kubernetes cluster and so is not part of `task verify`, for the
same reason `task demo` is not.

Everything about that claim except Kubernetes itself was established without
one, because the environment this was built in could not pull an image: the
credentials, both kubeconfigs, each manager as its own process with the flags
and the kubeconfig its `Deployment` gives it, and the demo with `--no-manager`.
That run reaches ready clusters in both workspaces with the isolation table
intact, and it is what found the two faults recorded in
[`evidence/`](evidence/README.md). What it does not cover is the pod layer -
the image build, the mounts, the probes, the scheduling - which has not been
run.
