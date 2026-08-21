---
title: Demo
description: Build a ready cluster in each of several kcp workspaces from one manager, in one command.
weight: 5
---

```sh
task demo
```

That starts a single-shard kcp server, publishes **one `APIExport` per
provider** out of its `root` workspace, gives **two users a workspace each**,
runs each provider's controllers — the same wiring each provider's own
deployment runs — and builds a cluster in each workspace **from a
`ClusterClass`**, printing what they are doing until they are all ready:

```
WORKSPACE                         LOGICAL CLUSTER   CLUSTER  PROVISIONED  READY  DETAIL
root:capi-demo:alice:capi-demo-1  1lmla01gpzr6iezu  demo-00  yes          yes    cluster ready
root:capi-demo:bob:capi-demo-1    2ech2ot4va8usjgo  demo-00  yes          yes    cluster ready

WORKSPACE                         CONTROL PLANE  INITIALIZED  READY  DETAIL
root:capi-demo:alice:capi-demo-1  demo-00-cp     yes          1/1    control plane ready
root:capi-demo:bob:capi-demo-1    demo-00-cp     yes          1/1    control plane ready

WORKSPACE                         MACHINE                 BOOTSTRAPPED  READY  DATA SECRET             PHASE    DETAIL
root:capi-demo:alice:capi-demo-1  demo-00-cp-x22l5        yes           yes    demo-00-cp-x22l5        Running  machine ready
root:capi-demo:alice:capi-demo-1  demo-00-md-fcr25-fxvl7  yes           yes    demo-00-md-fcr25-fxvl7  Running  machine ready
root:capi-demo:bob:capi-demo-1    demo-00-cp-c4bcf        yes           yes    demo-00-cp-c4bcf        Running  machine ready
root:capi-demo:bob:capi-demo-1    demo-00-md-mk8w2-rxcv5  yes           yes    demo-00-md-mk8w2-rxcv5  Running  machine ready
```

Each cluster gets a control plane machine and a worker by default, because a
cluster is what the demo is for. **Ready** is the Cluster's `Available`
condition and is what the run waits for; provisioned infrastructure is a
milestone on the way there, reported alongside rather than mistaken for the
destination. A control plane whose machines never go Ready is provisioned, and
is not a cluster.

Each workspace gets its own `ClusterClass` called `demo` and the five templates
it refers to, and the only object the demo writes per cluster is a `Cluster`
naming that class. The infrastructure cluster, the control plane, the worker
`MachineDeployment` and the per-cluster templates are all created by the core
provider's topology controller — which is why the names above are the names
they are: the class pins them, so that a `kubectl get` after the run is
predictable.

Alice's workspace and Bob's are both called `capi-demo-1`, both clusters are
called `demo-00`, and both classes are called `demo`, on purpose: identical
names are what makes a cross-workspace confusion visible rather than plausible.
One shard, one manager per provider, every workspace served by all of them —
and each workspace's objects stay its own.

## Two users, and what neither can see

The run finishes by asking kcp, as each user in turn, what it will let them
read of the other's:

```
USER   WORKSPACE                         OWNER     RESOURCE    ALLOWED  DETAIL
alice  root                              everyone  workspaces  yes      1 workspace: capi-demo
alice  root:capi-demo                    nobody    workspaces  no       forbidden
alice  root:capi-demo:alice              alice     workspaces  yes      1 workspace: capi-demo-1
alice  root:capi-demo:alice:capi-demo-1  alice     clusters    yes      1 cluster: demo-00
alice  root:capi-demo:bob                bob       workspaces  no       forbidden
alice  root:capi-demo:bob:capi-demo-1    bob       clusters    no       forbidden
bob    root                              everyone  workspaces  yes      1 workspace: capi-demo
bob    root:capi-demo                    nobody    workspaces  no       forbidden
bob    root:capi-demo:alice              alice     workspaces  no       forbidden
bob    root:capi-demo:alice:capi-demo-1  alice     clusters    no       forbidden
bob    root:capi-demo:bob                bob       workspaces  yes      1 workspace: capi-demo-1
bob    root:capi-demo:bob:capi-demo-1    bob       clusters    yes      1 cluster: demo-00
```

**ALLOWED is `yes` exactly where OWNER is you, or everyone.** Alice cannot list
Bob's workspaces, cannot list the Clusters inside them, and cannot list the
`root:capi-demo` workspace that holds both their homes — so she cannot discover
that Bob exists at all. Bob is in the same position looking the other way. A
run whose table does not have that shape says so in the DETAIL column and exits
non-zero: ready clusters in leaky workspaces are not a success.

The tree the demo builds is what produces that:

```
root                                  the APIExports live here
└── capi-demo                         the org workspace — nobody's, readable by nobody
    ├── alice                         alice's home — alice can list what is in it
    │   └── capi-demo-1               alice's workspace — her Cluster, her kubeconfig
    └── bob
        └── capi-demo-1
```

Each user is granted two things and nothing else: the right to be in their own
home and read the workspaces in it, and full use of the Cluster API types in
the workspaces they own. Nothing grants them anything in the org workspace,
which is why listing it is refused — and why the isolation is a property of
where the workspaces are rather than of anything the demo does at read time.

### The first row, and why the org workspace is in the way

Alice **can** list `root`, and does see that a workspace called `capi-demo`
exists. Two facts about kcp make that so, and both are worth knowing before
laying out a tree of your own:

- `root` binds `system:kcp:tenancy:reader` to `system:authenticated`, so any
  authenticated user can list its direct children. That is the shard's policy
  rather than this demo's, so the run **reports** that row rather than
  asserting it — a deployment that has narrowed it has broken nothing here.
- **There is no "list the workspaces I have access to".** A `Workspace` object
  lives in its parent workspace, and a list returns that parent's direct
  children — all of them or none, since RBAC cannot narrow a list by name, and
  never anything deeper. What a tenant has instead is the path to their own
  home.

Put the homes directly under `root` and those two together would leak the
tenant list: every authenticated user could read the names `alice` and `bob`,
while being able to enter neither. One workspace in between, granting nobody
anything, stops the walk a level higher — Alice sees `capi-demo` and is refused
the moment she asks what is inside it.

The demo authenticates to kcp as the admin and asks the server to evaluate each
request **as** the user, which is what `kubectl --as` does. kcp runs its whole
authorization stack against the impersonated user, so every `forbidden` above
is a real RBAC denial and not a simulation of one. The commands the demo prints
when it finishes are the ones that reproduce the table by hand:

```sh
kubectl --kubeconfig .demo/kcp/admin.kubeconfig --context base --as alice \
  --server $KCP/clusters/root:capi-demo:alice get workspaces

kubectl --kubeconfig .demo/kcp/admin.kubeconfig --context base --as alice \
  --server $KCP/clusters/root:capi-demo:bob get workspaces
```

```
Error from server (Forbidden): workspaces.tenancy.kcp.io is forbidden:
User "alice" cannot list resource "workspaces" in API group "tenancy.kcp.io"
at the cluster scope: access denied
```

Name the tenants yourself with `--users`, or turn the whole thing off with
`--users ""` — which puts every workspace directly under `root` and lets only
the demo's own credentials near them.

The demo runs those managers in a single process for convenience. A deployment
runs one process each; see
[One APIExport per provider](../design/provider-exports.md).

It takes about a minute, needs no container runtime, and pulls no images.

## Prerequisites

- Go — the version in [`go.mod`](https://github.com/jimmidyson/kcp-cluster-api/blob/main/go.mod)
- [task](https://taskfile.dev/): `go install github.com/go-task/task/v3/cmd/task@latest`

`task demo` downloads the pinned kcp server binary into `bin/` the first time,
and keeps the server's state and log under `.demo/`. Nothing is installed
outside the repository, and the server is stopped when the demo exits.

## Looking around while it runs

```sh
task demo DEMO_FLAGS="--wait"
```

`--wait` leaves the server and the manager running after every cluster is
ready. The demo prints a `kubectl` command per workspace as it finishes —
each one is the same kubeconfig with a different `--server`, because a kcp
workspace *is* a URL path:

```sh
kubectl --kubeconfig .demo/kcp/admin.kubeconfig --context base \
  --server https://localhost:35891/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusters,devclusters -A
```

Ctrl-C stops the manager and the server.

## Options

Pass flags through `DEMO_FLAGS`, or run `go run ./cmd/demo --help` for the full
list.

| Flag | Default | What it does |
|---|---|---|
| `--workspaces` | 2 | How many workspaces to create, bind and provision in, shared out between the users |
| `--users` | `alice,bob` | Tenants to share the workspaces out between, one home workspace each. `""` means none: every workspace goes directly under `--parent` and only admin credentials touch it |
| `--clusters` | 1 | Clusters per workspace |
| `--control-plane-machines` | 1 | Control plane machines per cluster, as a `KubeadmControlPlane`. At least one: the `ClusterClass` every demo cluster is built from always names a control plane |
| `--worker-machines` | 1 | Worker machines per cluster, as a `MachineDeployment` |
| `--backend` | `inmemory` | `inmemory` needs nothing; `docker` provisions real containers and pulls `kindest` images |
| `--wait` | false | Stay up after every cluster is ready |
| `--kcp-kubeconfig` | — | Run against a kcp server you already have, instead of starting one |
| `--no-manager` | false | Create the workspaces and objects only, against a `core-manager` you started yourself |
| `--nutanix-export` | false | Also publish and bind the Nutanix infrastructure provider's `APIExport`. See [The Nutanix provider](#the-nutanix-provider) |
| `--timeout` | 5m | How long to wait for every cluster to be ready |

Ten workspaces is as easy as two, and is the more interesting run:

```sh
task demo DEMO_FLAGS="--workspaces 10 --wait"
```

They are shared out between the users round-robin, so that is five workspaces
each for Alice and Bob. Give it more tenants instead, or different ones:

```sh
task demo DEMO_FLAGS="--workspaces 12 --users alice,bob,carol,dave"
```

There has to be at least one workspace per user — a tenant with nothing to own
has nothing to be isolated from, and the run refuses rather than printing a row
of nothing.

## Machines, and the providers behind them

```sh
task demo DEMO_FLAGS="--control-plane-machines 3 --worker-machines 2"
```

Each cluster gets a `KubeadmControlPlane` with that many replicas and a
`MachineDeployment` with that many workers, and the run publishes and wires the
kubeadm bootstrap and control plane providers alongside core and the dev
infrastructure provider. Those providers create the Machines, their
`KubeadmConfig`s and their `DevMachine`s themselves — the demo does not name
them, which is why the names in the first table are ones it never chose. The
`-cp-` machines are the control plane's; the `-md-` ones are the
`MachineDeployment`'s.

**Initialized** means the control plane can accept requests: there is a cluster
there. **Ready** is stricter, and is what the run waits for — every replica the
control plane was asked for has passed every health check the provider makes
against the workload cluster, and every Machine's Node is healthy. A control
plane sits at `INITIALIZED yes` with `READY 0/1` in between, which is a state
worth watching rather than a contradiction.

Each workspace's bootstrap data and cluster certificate authority are its own —
different bytes under identical names, which is what
`test/integration/bootstrap` asserts.

For the cheapest possible run — infrastructure only, no bootstrap or control
plane provider — ask for no machines:

```sh
task demo DEMO_FLAGS="--control-plane-machines 0"
```

That stops at provisioned infrastructure rather than at ready, because a
`Cluster` with no control plane has no readiness to reach: its `Available`
condition summarises a remote connection and a control plane it does not have.

## The Nutanix provider

```sh
task demo DEMO_FLAGS="--nutanix-export"
```

Adds a fifth `APIExport`, `cluster-api-nutanix-infrastructure`, and binds it
into every workspace alongside the other four. `kubectl get nutanixclusters`
then works in a workspace, and a `Cluster` can name a `NutanixCluster` as its
infrastructure.

**Nothing in the demo reconciles them.** The demo runs managers for the four
providers it starts; the Nutanix one is a separate binary, and running it needs
a Prism Central. So with this flag a `NutanixCluster` you create is stored and
left alone — the types are there, the controller is not.

To actually reconcile them, run the manager yourself against the same kcp:

```sh
cd providers/nutanix-infrastructure
go run ./cmd/nutanix-infrastructure-manager \
  --kubeconfig .demo/kcp/admin.kubeconfig \
  --endpoint-slice-name cluster-api-nutanix-infrastructure
```

It is a separate Go module because the Nutanix SDK is large and belongs in its
own dependency graph — see
[The Nutanix infrastructure provider](../../design/nutanix-provider/) for what
that costs and why.

### Credentials are per cluster

Every `NutanixCluster` must set `spec.prismCentral`, naming the Prism Central
to use and a `Secret` holding the credentials for it:

```yaml
spec:
  prismCentral:
    address: pc.example.com
    port: 9440
    credentialRef:
      kind: Secret
      name: nutanix-creds
```

A `NutanixCluster` that names none is an error here, and that differs from
CAPX running against an ordinary management cluster, where omitting it falls
back to credentials mounted into the manager's own pod. That fallback cannot
be offered to more than one tenant: it would let any workspace provision with
the operator's Prism Central account by leaving a field out. The
[design page](../../design/nutanix-provider/#credentials-are-per-cluster-or-absent)
has the detail.

## Against your own kcp

```sh
task demo DEMO_FLAGS="--kcp-kubeconfig ~/.kube/kcp.kubeconfig"
```

The kubeconfig context has to be a cluster-unaware one (`base` in a kcp admin
kubeconfig): the demo scopes it to each workspace itself. Everything it creates
is idempotent, so re-running it against the same server picks up where it left
off rather than failing.

## What it does and does not show

It shows what this project is for: unmodified upstream Cluster API reconcilers,
building clusters in many kcp workspaces at once and taking them to ready, from
one manager that was told about none of them — each workspace is engaged
because its `APIBinding` became ready, and nothing names a workspace in
configuration. It shows the other half of that too: the tenants those
workspaces belong to cannot see each other, and the fleet-wide manager serving
all of them is what makes that worth having.

What it does not show is a real identity provider. The users are names kcp
authorizes as, reached by impersonating from the demo's admin credentials;
there is no OIDC, no token file and no accounts. That is the right shape for
demonstrating *authorization*, which is where tenancy lives, and it is not a
statement about how a deployment authenticates.

It does not serve webhooks: those are single-workspace by construction until
the webhook dispatch layer (G4) lands, so every object the demo creates is
fully specified rather than defaulted. That is why its `ClusterClass` spells
out a rollout strategy for its worker class, and why the class declares no
variables — variable defaulting is a webhook's job.

The same run is an integration test — `test/integration/demo`, part of
`task verify` — which asserts both kinds of isolation: that each workspace sees
exactly its own cluster and that the status written into it was written for
that workspace, and that each user reads their own workspaces and is refused
every other user's.

## Going through it a piece at a time

[The walkthrough](walkthrough.md) runs this same demo and then stops at each
part — workspaces, exports, bindings, permission claims, and the virtual
workspace one manager watches — with the `kubectl` commands to see each of them
for yourself. It assumes no kcp knowledge.
