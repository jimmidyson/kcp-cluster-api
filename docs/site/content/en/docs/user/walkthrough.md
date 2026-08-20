---
title: Walkthrough
description: Stand the whole thing up on your laptop and look at every part — workspaces, exports, bindings, claims, and one manager serving all of them.
weight: 7
---

[The demo](demo.md) brings clusters up in several kcp workspaces with one
command. This page runs the same thing and then stops to look at each piece,
because "it worked" and "I know what happened" are different states to be in.

**No kcp knowledge is assumed.** Every kcp concept is introduced where you
first meet it, and every command below is one you can paste.

By the end you will have:

- a kcp server running locally, with four `APIExport`s published in it
- two workspaces, each holding a Cluster API cluster whose objects have
  **identical names** and are nonetheless completely separate
- four manager processes — the same wiring each provider deploys — reconciling
  **both** workspaces without either being named in any configuration
- a running workload cluster in each workspace that you can `kubectl` into

## Before you start

- Go, the version in the repository's root `go.mod`
- [task](https://taskfile.dev/):
  `go install github.com/go-task/task/v3/cmd/task@latest`
- `kubectl` — any recent version
- **No container runtime and no Kubernetes cluster.** The default backend runs
  the workload clusters in-process. `task` downloads the pinned `kcp` binary
  into `bin/` for you.

## 1. Start it, and leave it running

```sh
task demo DEMO_FLAGS=--wait
```

`--wait` is the difference from the demo page: instead of tearing everything
down once the clusters are ready, it stays up so you can look around. `Ctrl-C`
stops it and takes the kcp server with it.

About a minute later you get the status tables, and then the server's address:

```
WORKSPACE         LOGICAL CLUSTER   CLUSTER  PROVISIONED  READY  DETAIL
root:capi-demo-1  2yqfrtuq4cjeh3n5  demo-00  yes          yes    cluster ready
root:capi-demo-2  2pes8qc13ri2fa4y  demo-00  yes          yes    cluster ready
```

Leave that terminal alone and open a second one. Everything below runs there.

### A shorthand for the rest of the page

kcp is a Kubernetes API server, so `kubectl` talks to it. The one unusual part
is that *which workspace you are in* is a URL path, so you re-point `--server`
rather than switching context. Set this up once — take the port from your own
run, it is random:

```sh
cd /path/to/kcp-cluster-api
export KCP=https://localhost:33799          # yours will differ
alias k='kubectl --kubeconfig .demo/kcp/admin.kubeconfig --context base'
```

The `base` context is deliberately *cluster-unaware*: it points at the server
without choosing a workspace, so the `--server` you pass decides. Check it
works:

```sh
k --server $KCP/clusters/root get workspaces
```

```
NAME          TYPE        REGION   PHASE   URL
capi-demo-1   universal            Ready   https://localhost:33799/clusters/root:capi-demo-1
capi-demo-2   universal            Ready   https://localhost:33799/clusters/root:capi-demo-2
```

## 2. Workspaces, and what they actually are

A **workspace** is kcp's unit of isolation. Think of it as a whole Kubernetes
API server of your own — its own namespaces, its own objects, its own RBAC —
except that it is cheap enough to have thousands of, and they are arranged in a
tree. `root` is the top; `root:capi-demo-1` is a child.

Two names for the same thing appear everywhere, and mixing them up is the
first thing that confuses people:

| | Example | What it is |
|---|---|---|
| **Path** | `root:capi-demo-1` | The human name, its position in the tree. Can be renamed and moved. |
| **Logical cluster** | `2yqfrtuq4cjeh3n5` | The identifier the server actually stores objects under. Never changes. |

Both work in a URL. These are the same workspace:

```sh
k --server $KCP/clusters/root:capi-demo-1 get namespaces
k --server $KCP/clusters/2yqfrtuq4cjeh3n5 get namespaces
```

Objects carry the logical cluster, not the path — which is why the status
table prints both.

## 3. APIExport: publishing an API for others to use

A plain Kubernetes cluster gets a new API by installing a CRD, and every user
of that cluster then has it. kcp splits that in two:

- An **`APIExport`** publishes a set of API schemas *from* one workspace.
- An **`APIBinding`** consumes them *into* another.

Nothing is installed anywhere until a workspace asks. Look at what the demo
published:

```sh
k --server $KCP/clusters/root get apiexports
```

```
NAME                               AGE
cluster-api-bootstrap-kubeadm      71s
cluster-api-controlplane-kubeadm   71s
cluster-api-core                   72s
cluster-api-dev-infrastructure     72s
cache.kcp.io                       83s
...
```

The four `cluster-api-*` exports are this project's. The rest are kcp's own.

**One export per provider, not one for Cluster API.** That mirrors how Cluster
API is deployed normally — core, bootstrap, control plane and infrastructure
are separate deployments with separate RBAC — and it means a workspace can bind
the infrastructure provider it wants without binding all of them. The reasoning
is in [One APIExport per provider](../design/provider-exports.md).

Each export publishes the CRDs for its own types:

| Export | Publishes |
|---|---|
| `cluster-api-core` | `Cluster`, `Machine`, `MachineSet`, `MachineDeployment`, `MachineHealthCheck` |
| `cluster-api-bootstrap-kubeadm` | `KubeadmConfig`, `KubeadmConfigTemplate` |
| `cluster-api-controlplane-kubeadm` | `KubeadmControlPlane`, `KubeadmControlPlaneTemplate` |
| `cluster-api-dev-infrastructure` | `DevCluster`, `DevMachine`, `DevMachineTemplate` |

## 4. Identity: why an API has a fingerprint

Every `APIExport` gets an **identity hash** when the server accepts it:

```sh
k --server $KCP/clusters/root get apiexport cluster-api-core \
  -o jsonpath='{.status.identityHash}{"\n"}'
```

```
c7edf866ce8eda64e179601f0e01ae09b8c82c66eb3cd53e465614c04c29cc95
```

This exists because group/resource names are not unique in kcp. Anyone can
publish something called `clusters.cluster.x-k8s.io`. The identity hash is what
distinguishes *this* `Cluster` API from an impostor with the same name, and it
is why nothing in this project can be configured with a hash baked in: it is
different on every kcp instance, so it is resolved at run time.

## 5. Permission claims: how providers reach each other's types

Here is the problem kcp has to solve. The core provider's `Cluster` reconciler
deletes the `DevCluster` that backs it — but `DevCluster` belongs to a
*different* export. Publishing an API grants you nothing over anyone else's.

A **permission claim** is an export declaring what it needs from elsewhere, and
a binding *accepting* those claims is the workspace agreeing. Look at what core
asks for:

```sh
k --server $KCP/clusters/root get apiexport cluster-api-core \
  -o jsonpath='{range .spec.permissionClaims[*]}{.group}/{.resource}  verbs={.verbs}  identity={.identityHash}{"\n"}{end}'
```

```
/configmaps  verbs=["get","list","watch"]  identity=
/secrets  verbs=["get","list","watch","create","update","patch"]  identity=
bootstrap.cluster.x-k8s.io/kubeadmconfigs  verbs=[...,"delete"]  identity=ee58998b...
controlplane.cluster.x-k8s.io/kubeadmcontrolplanes  verbs=[...,"delete"]  identity=434fb0ee...
infrastructure.cluster.x-k8s.io/devclusters  verbs=[...,"delete"]  identity=5ac40b2124...
```

Three things worth noticing:

- **The identity hash is the "whose".** Those long hashes are the *other*
  exports' identities, resolved when the demo published them. A claim without
  one — Secrets, ConfigMaps — means a core Kubernetes type, which kcp serves in
  every workspace and which belongs to no export.
- **Verbs are narrowed.** Core gets `delete` on `DevCluster` because it really
  does delete them, but only `get/list/watch` on ConfigMaps. The infrastructure
  provider gets no `create` on Secrets at all — it reads a workload cluster's
  kubeconfig, it never writes one.
- **Claims are the most common thing to get wrong.** A missing claim is not a
  startup error. The manager comes up perfectly and then a write is refused deep
  inside a reconcile, minutes later, in a log line that does not mention
  bindings at all.

## 6. APIBinding: a workspace opting in

Now look at the other side:

```sh
k --server $KCP/clusters/root:capi-demo-1 get apibindings
```

```
NAME                               AGE   READY
cluster-api-bootstrap-kubeadm      80s   True
cluster-api-controlplane-kubeadm   80s   True
cluster-api-core                   87s   True
cluster-api-dev-infrastructure     81s   True
```

`READY True` means the schemas are served *in this workspace* and the claims
were accepted. That is what makes the next command work at all — before the
binding, `kubectl get clusters` here would have said the type does not exist:

```sh
k --server $KCP/clusters/root:capi-demo-1 get clusters,machines -A
```

```
NAMESPACE   NAME      AVAILABLE   PHASE         AGE
default     demo-00   True        Provisioned   74s

NAMESPACE   NAME                     CLUSTER   PHASE     VERSION
default     demo-00-cp-vzm47         demo-00   Running   v1.34.0
default     demo-00-md-vg6q5-4cfjr   demo-00   Running   v1.34.0
```

(Columns are trimmed to fit here; the real output is wider.)

## 7. What the cluster is made of

Nothing here is kcp-specific — this is ordinary Cluster API, and it behaves as
the [upstream documentation](https://cluster-api.sigs.k8s.io/user/quick-start)
describes. The point of the exercise is that it is unchanged:

```
Cluster demo-00
├── infrastructureRef  → DevCluster demo-00           (dev infrastructure provider)
└── controlPlaneRef    → KubeadmControlPlane demo-00-cp  (control plane provider)
                          └── creates Machine demo-00-cp-vzm47   (core provider)
                              ├── bootstrapRef      → KubeadmConfig    (bootstrap provider)
                              └── infrastructureRef → DevMachine       (dev infrastructure)
MachineDeployment demo-00-md → worker Machine demo-00-md-vg6q5-4cfjr
```

Four providers, each watching its own types and writing to the others' through
the claims from step 5.

### DevCluster and its backends

`DevCluster` is the infrastructure provider that Cluster API ships for
development and testing. It has two backends:

```sh
k --server $KCP/clusters/root:capi-demo-1 -n default get devcluster demo-00 \
  -o jsonpath='{.spec}{"\n"}'
```

```json
{"backend":{"inMemory":{}},"controlPlaneEndpoint":{"host":"127.0.0.1","port":36969}}
```

| Backend | What a "machine" is | Needs |
|---|---|---|
| `inMemory` (default) | A fake API server in the manager's own process | nothing |
| `docker` | A real container running real kubeadm | a container runtime, and pulls `kindest` images |

In-memory is the default here because it needs nothing installed and takes a
minute rather than twenty. Run the same thing on real containers with
`task demo DEMO_FLAGS="--backend=docker"`.

{{% pageinfo color="info" %}}
The docker backend brings clusters and control planes up, but its Nodes stay
`NotReady` because nothing in this repository installs a CNI. See
[the conversion plan](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md)
for where that stands.
{{% /pageinfo %}}

## 8. The multicluster part

This is the piece the whole project exists for, and it is worth doing slowly.

**First: the two workspaces hold objects with the same name.**

```sh
for w in 1 2; do
  k --server $KCP/clusters/root:capi-demo-$w -n default \
    get cluster demo-00 -o jsonpath='{.metadata.uid}{"\n"}'
done
```

```
dae1e493-7858-4917-bd9e-90536b29143d
cf5b8fa5-c19c-4f7d-bc72-d831e7d0c68e
```

Same namespace, same name, different objects. That is not a coincidence — the
demo names them identically on purpose, because a leak between workspaces
cannot hide behind names that happen not to collide.

**Second: one request can see across all of them.** Each export has an
`APIExportEndpointSlice` carrying the URL of a *virtual workspace* — a view
that serves every workspace bound to that export:

```sh
k --server $KCP/clusters/root get apiexportendpointslice cluster-api-core \
  -o jsonpath='{.status.endpoints[0].url}{"\n"}'
```

```
https://localhost:33799/services/apiexport/root/cluster-api-core
```

Ask it for every `Cluster` on the shard, using `*` as the logical cluster:

```sh
export VW=$KCP/services/apiexport/root/cluster-api-core
k --server "$VW/clusters/*" get clusters -A \
  -o custom-columns='LOGICAL CLUSTER:.metadata.annotations.kcp\.io/cluster,NAME:.metadata.name,PHASE:.status.phase'
```

```
LOGICAL CLUSTER    NAME      PHASE
2pes8qc13ri2fa4y   demo-00   Provisioned
2yqfrtuq4cjeh3n5   demo-00   Provisioned
```

Two clusters, same name, from two workspaces, in **one request over one watch
stream**. That is what the managers are doing: not a controller per workspace,
but one set of controllers watching this wildcard view, with each object
carrying the workspace it came from. It works for anything the export
publishes:

```sh
k --server "$VW/clusters/*" get machines -A \
  -o custom-columns='LOGICAL CLUSTER:.metadata.annotations.kcp\.io/cluster,NAME:.metadata.name,PHASE:.status.phase'
```

**Nothing named either workspace.** The managers were started before the
workspaces existed. A workspace joins by creating an `APIBinding`; it leaves by
deleting one; the endpoint slice changes and the managers follow. Add a third
workspace while everything is still running and the managers will pick it up
without restarting.

What that costs, measured rather than asserted, is in
[Workspace resource usage](../design/workspace-resource-usage.md) — the short
version is 2 goroutines per workspace and no additional watch stream.

## 9. Into a workload cluster

Each cluster's admin kubeconfig is a Secret in its own workspace, written by the
control plane provider:

```sh
k --server $KCP/clusters/root:capi-demo-1 -n default \
  get secret demo-00-kubeconfig -o jsonpath='{.data.value}' | base64 -d > /tmp/demo-00.kubeconfig

kubectl --kubeconfig /tmp/demo-00.kubeconfig get nodes
```

```
NAME                     AGE
demo-00-cp-vzm47         75s
demo-00-md-vg6q5-4cfjr   48s
```

Those Nodes are how the Machines reached `Ready`: the Machine reconciler finds
each one by provider ID and records it. Fetch the *other* workspace's secret
and you get that cluster's nodes instead — two separate workload clusters,
reached through two separate kubeconfigs, from two workspaces that never see
each other.

## 10. Stopping, and what to change

`Ctrl-C` in the first terminal. The managers stop, the kcp server stops, and
with the in-memory backend the workload clusters go with them. State lives
under `.demo/`, which is gitignored — delete it to start clean.

Worth trying next:

```sh
task demo DEMO_FLAGS="--workspaces=5"              # scale the fleet
task demo DEMO_FLAGS="--control-plane-machines=3"  # a three-replica control plane
task demo DEMO_FLAGS="--backend=docker"            # real containers
task demo DEMO_FLAGS="--no-manager"                # run the managers yourself
```

`--no-manager` is the interesting one if you are heading towards a real
deployment: the demo creates the workspaces and objects and then waits, leaving
you to start `core-manager` and friends the way you would run them for real. See
[Installation](installation.md).

## Where to go from here

- [Usage](usage.md) — what differs day to day from upstream Cluster API, and what does not
- [Installation](installation.md) — running the managers against a kcp instance you already have
- [One APIExport per provider](../design/provider-exports.md) — why four exports and how the claims are derived
- [The demo](../design/demo.md) — what the demo is for, and what it deliberately does not do
- [Workspace resource usage](../design/workspace-resource-usage.md) — what a workspace costs, measured
