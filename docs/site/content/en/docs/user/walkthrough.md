---
title: Walkthrough
description: Stand the whole thing up on your laptop and look at every part — workspaces, users, exports, bindings, claims, and one manager serving all of them.
weight: 7
---

[The demo](demo.md) brings clusters up in several kcp workspaces with one
command. This page runs the same thing and then stops to look at each piece,
because "it worked" and "I know what happened" are different states to be in.

**No kcp knowledge is assumed.** Every kcp concept is introduced where you
first meet it, and every command below is one you can paste.

By the end you will have:

- a kcp server running locally, with four `APIExport`s published in it
- two users, Alice and Bob, with a workspace each — **identically named**, in
  identically named workspaces, and neither able to see the other at all
- a Cluster API cluster in each of those workspaces
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
WORKSPACE                         LOGICAL CLUSTER   CLUSTER  PROVISIONED  READY  DETAIL
root:capi-demo:alice:capi-demo-1  2yqfrtuq4cjeh3n5  demo-00  yes          yes    cluster ready
root:capi-demo:bob:capi-demo-1    2pes8qc13ri2fa4y  demo-00  yes          yes    cluster ready
```

Leave that terminal alone and open a second one. Everything below runs there.

### Pointing kubectl at kcp

kcp is a Kubernetes API server, so `kubectl` talks to it. The one unusual part
is that *which workspace you are in* is a URL path, so you re-point `--server`
rather than switching context. Set this up once — take the port from your own
run, it is random:

```sh
cd /path/to/kcp-cluster-api
export KCP=https://localhost:33799          # yours will differ
export KUBECONFIG=$PWD/.demo/kcp/admin.kubeconfig
```

Every command below then spells `kubectl` out in full and passes
`--context base`. The `base` context is deliberately *cluster-unaware*: it
points at the server without choosing a workspace, so the `--server` you pass
decides. Check it works:

```sh
kubectl --context base --server $KCP/clusters/root get workspaces
```

```
NAME        TYPE        REGION   PHASE   URL
capi-demo   universal            Ready   https://localhost:33799/clusters/root:capi-demo
```

One workspace, not four. The others are *inside* it, which is the next
section's subject.

## 2. Workspaces, and what they actually are

A **workspace** is kcp's unit of isolation. Think of it as a whole Kubernetes
API server of your own — its own namespaces, its own objects, its own RBAC —
except that it is cheap enough to have thousands of, and they are arranged in a
tree. `root` is the top; `root:capi-demo:alice:capi-demo-1` is four levels down:

```
root                     the APIExports and the cluster-api WorkspaceType live here
└── capi-demo            the org workspace          universal
    ├── alice            alice's home               universal
    │   └── capi-demo-1  alice's workspace          cluster-api
    └── bob
        └── capi-demo-1  bob's, with the same name  cluster-api
```

The type in the right-hand column is not decoration. The two workspaces that
hold clusters are created with the **`cluster-api`** `WorkspaceType`, and that
is the whole of how they came to serve Cluster API — see
[section 6](#6-apibinding-a-workspace-opting-in). The ones above them are
kcp's ordinary `universal` type, because they hold no Cluster API objects and
need none of it.

Walk it a level at a time — a `Workspace` object lives in its **parent**
workspace, so a list returns that parent's direct children and nothing deeper:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo get workspaces
kubectl --context base --server $KCP/clusters/root:capi-demo:alice get workspaces
```

```
NAME    TYPE        REGION   PHASE   URL
alice   universal            Ready   https://localhost:33799/clusters/root:capi-demo:alice
bob     universal            Ready   https://localhost:33799/clusters/root:capi-demo:bob

NAME          TYPE          REGION   PHASE   URL
capi-demo-1   cluster-api            Ready   https://localhost:33799/clusters/root:capi-demo:alice:capi-demo-1
```

Who may run each of those is [section 9](#9-two-users-and-what-neither-can-see),
and it is the reason the tree has this shape.

Two names for the same thing appear everywhere, and mixing them up is the
first thing that confuses people:

| | Example | What it is |
|---|---|---|
| **Path** | `root:capi-demo:alice:capi-demo-1` | The human name, its position in the tree. Can be renamed and moved. |
| **Logical cluster** | `2yqfrtuq4cjeh3n5` | The identifier the server actually stores objects under. Never changes. |

Both work in a URL. These are the same workspace:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 get namespaces
kubectl --context base --server $KCP/clusters/2yqfrtuq4cjeh3n5 get namespaces
```

### What a logical cluster actually is

Creating a `Workspace` does two things. kcp allocates a **logical cluster** —
the partition on a shard that objects will really be stored in — and gives it an
opaque identifier. The `Workspace` object you created stays behind in the
*parent* workspace as the handle that names that partition and manages its
lifecycle. It is not the workspace's contents; nothing you create inside the
workspace is stored anywhere near it.

So the two rows above are different kinds of thing, not two spellings of one:

- The **path** is a lookup key. `root:capi-demo:alice:capi-demo-1` says where the
  handle sits in the tree, and kcp resolves it to a logical cluster on every
  request. Rename the workspace or move it under a different parent and the path
  changes with it.
- The **logical cluster** is what is being addressed. Its identifier is assigned
  once, when the workspace is created, and is never reused or changed — it
  survives any renaming above it. It is not the `Workspace` object's
  `metadata.uid`, which identifies the handle rather than the partition.

Inside every logical cluster sits exactly one object describing it — a
`LogicalCluster` named `cluster` — and every other object stored there carries
the identifier in a `kcp.io/cluster` annotation. That annotation is what the
fleet-wide query in [step 8](#8-the-multicluster-part) prints, and it is how a
manager knows which workspace an object it is reconciling came from.

Anything that has to survive a rename therefore names the logical cluster rather
than the path: those object annotations, the wildcard view, and flags such as
`--webhook-workspace-cluster-name` in [Installation](installation.md).

### Getting a workspace's logical cluster ID

Three ways, all returning the same string. Which one you want depends on what
you are holding: the parent, the workspace itself, or an object out of it.

**From the parent.** A `Workspace`'s `spec.cluster` is the logical cluster it
points at, so the parent lists the whole mapping:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice get workspaces \
  -o custom-columns='PATH:.metadata.name,LOGICAL CLUSTER:.spec.cluster,PHASE:.status.phase'
```

```
PATH          LOGICAL CLUSTER    PHASE
capi-demo-1   2yqfrtuq4cjeh3n5   Ready
```

That is the first identifier the status table in step 1 printed — the demo reads
it from the same field. Bob's `capi-demo-1` has its own, and you ask his home
for it: the mapping lives with the handle, so there is no one workspace that
holds them all. For a single workspace:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice \
  get workspace capi-demo-1 -o jsonpath='{.spec.cluster}{"\n"}'
```

**From inside the workspace**, where there is no parent to ask — the
`LogicalCluster` object is the partition describing itself, and its URL ends in
the identifier:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 get logicalcluster
```

```
NAME      PHASE   URL                                                 AGE
cluster   Ready   https://localhost:33799/clusters/2yqfrtuq4cjeh3n5   3m
```

Its annotations carry both names, so this is also how you go the other way —
from an identifier you found in a log line or a wildcard query back to a path a
human will recognise:

```sh
kubectl --context base --server $KCP/clusters/2yqfrtuq4cjeh3n5 \
  get logicalcluster cluster \
  -o jsonpath='{.metadata.annotations.kcp\.io/path}{"\n"}'
```

```
root:capi-demo:alice:capi-demo-1
```

**From any object in it**, which is the one that works when all you have is a
dumped object:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get namespace default \
  -o jsonpath='{.metadata.annotations.kcp\.io/cluster}{"\n"}'
```

`root` is the exception to the opaque identifiers: its own logical cluster is
called `root`, so there — and nowhere else — the path and the identifier are the
same string.

## 3. APIExport: publishing an API for others to use

A plain Kubernetes cluster gets a new API by installing a CRD, and every user
of that cluster then has it. kcp splits that in two:

- An **`APIExport`** publishes a set of API schemas *from* one workspace.
- An **`APIBinding`** consumes them *into* another.

Nothing is installed anywhere until a workspace asks. Look at what the demo
published:

```sh
kubectl --context base --server $KCP/clusters/root get apiexports
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
| `cluster-api-core` | `Cluster`, `ClusterClass`, `Machine`, `MachineSet`, `MachineDeployment`, `MachineHealthCheck`, `MachinePool` |
| `cluster-api-bootstrap-kubeadm` | `KubeadmConfig`, `KubeadmConfigTemplate` |
| `cluster-api-controlplane-kubeadm` | `KubeadmControlPlane`, `KubeadmControlPlaneTemplate` |
| `cluster-api-dev-infrastructure` | `DevCluster`, `DevClusterTemplate`, `DevMachine`, `DevMachineTemplate` |

## 4. Identity: why an API has a fingerprint

Every `APIExport` gets an **identity hash** when the server accepts it:

```sh
kubectl --context base --server $KCP/clusters/root \
  get apiexport cluster-api-core \
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
kubectl --context base --server $KCP/clusters/root \
  get apiexport cluster-api-core \
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
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 get apibindings
```

```
NAME                               AGE   READY
cluster-api-bootstrap-kubeadm      71s   True
cluster-api-controlplane-kubeadm   71s   True
cluster-api-core-3m4h3             83s   True
cluster-api-dev-infrastructure     72s   True
cluster-api-workspace-55amf        83s   True
tenancy.kcp.io-1gkz7               83s   True
topology.kcp.io-8ed5z              83s   True
```

`READY True` means the schemas are served *in this workspace* and the claims
were accepted. That is what makes the next command work at all — before the
binding, `kubectl get clusters` here would have said the type does not exist.

Two things in that list are worth stopping on.

**The names differ, and the difference says who made them.** The two with a
random suffix — `cluster-api-core-3m4h3` and `cluster-api-workspace-55amf` —
were created by **kcp**, not by anybody running `kubectl`. They come from the
`cluster-api` `WorkspaceType` this workspace was created with:

```sh
kubectl --context base --server $KCP/clusters/root get workspacetype cluster-api \
  -o jsonpath='{.spec.defaultAPIBindings}{"\n"}'
```

```
[{"export":"cluster-api-workspace","path":"root"},{"export":"cluster-api-core","path":"root"}]
```

That is what "creating the workspace is the whole of onboarding" means: nobody
wrote an `APIBinding` to get `Cluster` and `Machine` served here. The type also
carries `defaultAPIBindingLifecycle: Maintain`, which is the subject of the
next paragraph, and an initializer, which is [section 9](#9-two-users-and-what-neither-can-see)'s.

**The three without a suffix were created by Alice**, by name, after the
workspace existed:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get apibinding cluster-api-dev-infrastructure \
  -o jsonpath='{.spec.reference.export}{"\n"}'
```

```
{"name":"cluster-api-dev-infrastructure","path":"root"}
```

Which infrastructure, bootstrap and control plane provider a workspace uses is
the tenant's decision, so the `WorkspaceType` does not make it for them.
Enabling one is creating that object and nothing else.

### She is allowed to, and only just

Try it as Alice against an export nobody granted her — this project's own
onboarding export, which is not a provider:

```sh
kubectl --context shard-base --as alice \
  --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 create -f - <<'YAML'
apiVersion: apis.kcp.io/v1alpha2
kind: APIBinding
metadata:
  name: not-granted
spec:
  reference:
    export:
      path: root
      name: cluster-api-workspace
YAML
```

```
Error from server (Forbidden): error when creating "STDIN": apibindings.apis.kcp.io
"not-granted" is forbidden: unable to create APIBinding: no permission to bind to
export root:cluster-api-workspace
```

Alice can create an `APIBinding` in her own workspace — `cluster-api-admin`
grants her that. What she cannot do is bind *that* export, and the permission
deciding it does not live in her workspace at all:

```sh
kubectl --context base --server $KCP/clusters/root \
  get clusterrole cluster-api-provider-binder \
  -o jsonpath='{.rules[0].verbs}{" on "}{.rules[0].resources}{" named "}{.rules[0].resourceNames}{"\n"}'
```

```
["bind"] on ["apiexports"] named ["cluster-api-dev-infrastructure","cluster-api-bootstrap-kubeadm","cluster-api-controlplane-kubeadm"]
```

kcp authorises creating an `APIBinding` as the verb **`bind` on the
`APIExport`**, evaluated in the workspace the *export* lives in — `root` — not
where the binding is being created. So enabling a provider takes two grants in
two places: an operator says which providers you may turn on, and you decide
which of them you use. Granting `bind` in your own workspace does nothing at
all, which is a confusing thing to debug the first time.

(`--context shard-base` rather than `--context base` there, and it matters. kcp
*scopes* an impersonated user to the workspace the request addresses unless the
impersonator is privileged, and a scoped user is refused everywhere else
whatever RBAC says — including in `root`, where the `bind` check happens. From
`--context base` this same command fails with the same message for a reason
that has nothing to do with Alice's permissions. A real tenant authenticates as
themselves and is never scoped; this is an artefact of standing in for one.)

### The claims keep themselves current

Look at what Alice's core binding accepted:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get apibinding cluster-api-core-3m4h3 \
  -o jsonpath='{range .spec.permissionClaims[*]}{.resource}{" "}{end}{"\n"}'
```

```
configmaps secrets kubeadmconfigs kubeadmconfigtemplates kubeadmcontrolplanes kubeadmcontrolplanetemplates devclusters devclustertemplates devmachines devmachinetemplates
```

Alice accepted none of those. `Maintain` means kcp rebuilds that list from
core's `APIExport` whenever the export changes, and the export's own list is
maintained by a controller watching for providers — so installing a provider
makes its types reachable by core's controllers in every existing workspace,
with nobody accepting anything. [Onboarding a workspace](onboarding.md)
covers what that costs you, including the two things it takes away.

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusters,machines -A
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
describes. The point of the exercise is that it is unchanged.

The demo asked for a cluster by naming a **`ClusterClass`**, so the only object
it wrote is the `Cluster` itself:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo-1 -n default \
  get cluster demo-00 -o jsonpath='{.spec.topology}{"\n"}'
```

```json
{"classRef":{"name":"demo"},"controlPlane":{"replicas":1},"version":"v1.34.0",
 "workers":{"machineDeployments":[{"class":"default-worker","name":"md","replicas":1}]}}
```

The class and the five templates it refers to are in the workspace too, and
they are what everything under the `Cluster` was stamped from:

```sh
TEMPLATES=clusterclasses,devclustertemplates,devmachinetemplates
TEMPLATES=$TEMPLATES,kubeadmcontrolplanetemplates,kubeadmconfigtemplates

kubectl --context base --server $KCP/clusters/root:capi-demo-1 -n default \
  get $TEMPLATES
```

So the tree below was written by the core provider's topology controller, not
by the demo:

```
ClusterClass demo  ← Cluster demo-00 names it
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

Both workspaces hold a `ClusterClass` called `demo`, and they are two different
objects — the same point the two `demo-00` clusters make, one level further up.
Change one workspace's class and only that workspace's clusters roll.

### DevCluster and its backends

`DevCluster` is the infrastructure provider that Cluster API ships for
development and testing. It has two backends:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 -n default \
  get devcluster demo-00 \
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
`task demo --backend=docker` brings clusters and control planes up, and its
Nodes then stay `NotReady`: a kubeadm Node needs a CNI, and the demo command
does not install one. The container-runtime *test suite* does — it applies the
CNI that ships inside the kind node image — so this is a gap in the demo command
rather than in the wiring. See
[the conversion plan](https://github.com/jimmidyson/kcp-cluster-api/blob/main/docs/conversion-plan.md).
{{% /pageinfo %}}

## 8. The multicluster part

This is the piece the whole project exists for, and it is worth doing slowly.

**First: the two workspaces hold objects with the same name.**

```sh
for w in alice bob; do
  kubectl --context base --server $KCP/clusters/root:capi-demo:$w:capi-demo-1 -n default \
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
kubectl --context base --server $KCP/clusters/root \
  get apiexportendpointslice cluster-api-core \
  -o jsonpath='{.status.endpoints[0].url}{"\n"}'
```

```
https://localhost:33799/services/apiexport/root/cluster-api-core
```

Ask it for every `Cluster` on the shard, using `*` as the logical cluster:

```sh
export VW=$KCP/services/apiexport/root/cluster-api-core
kubectl --context base --server "$VW/clusters/*" get clusters -A \
  -o custom-columns='LOGICAL CLUSTER:.metadata.annotations.kcp\.io/cluster,NAMESPACE:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase'
```

```
LOGICAL CLUSTER    NAMESPACE   NAME      PHASE
2pes8qc13ri2fa4y   default     demo-00   Provisioned
2yqfrtuq4cjeh3n5   default     demo-00   Provisioned
```

Two clusters, same name, from two workspaces, in **one request over one watch
stream**. That is what the managers are doing: not a controller per workspace,
but one set of controllers watching this wildcard view, with each object
carrying the workspace it came from. It works for anything the export
publishes:

```sh
kubectl --context base --server "$VW/clusters/*" get machines -A \
  -o custom-columns='LOGICAL CLUSTER:.metadata.annotations.kcp\.io/cluster,NAMESPACE:.metadata.namespace,NAME:.metadata.name,PHASE:.status.phase'
```

**Nothing named either workspace.** The managers were started before the
workspaces existed. A workspace joins by creating an `APIBinding`; it leaves by
deleting one; the endpoint slice changes and the managers follow. Add a third
workspace while everything is still running and the managers will pick it up
without restarting.

What that costs, measured rather than asserted, is in
[Workspace resource usage](../design/workspace-resource-usage.md) — the short
version is 2 goroutines per workspace and no additional watch stream.

## 9. Two users, and what neither can see

Everything so far ran as the kcp admin, who can go anywhere. The two workspaces
belong to two people, and the last thing the demo prints is what kcp will let
each of them read of the other:

```
USER   WORKSPACE                         OWNER     RESOURCE    ALLOWED  DETAIL
alice  root                              everyone  workspaces  yes      1 workspace: capi-demo
alice  root:capi-demo                    nobody    workspaces  no       forbidden
alice  root:capi-demo:alice              alice     workspaces  yes      1 workspace: capi-demo-1
alice  root:capi-demo:alice:capi-demo-1  alice     clusters    yes      1 cluster: demo-00
alice  root:capi-demo:bob                bob       workspaces  no       forbidden
alice  root:capi-demo:bob:capi-demo-1    bob       clusters    no       forbidden
bob    ...
```

Run any of those yourself. `--as` asks the server to authorize the request as
somebody else, which is what the demo does — kcp evaluates its whole
authorization stack against the named user, so what comes back is Alice's
answer and not an imitation of it:

```sh
kubectl --context base --as alice \
  --server $KCP/clusters/root:capi-demo:alice get workspaces
```

```
NAME          TYPE          REGION   PHASE   URL
capi-demo-1   cluster-api            Ready   https://localhost:33799/clusters/root:capi-demo:alice:capi-demo-1
```

```sh
kubectl --context base --as alice \
  --server $KCP/clusters/root:capi-demo:bob get workspaces
```

```
Error from server (Forbidden): workspaces.tenancy.kcp.io is forbidden:
User "alice" cannot list resource "workspaces" in API group "tenancy.kcp.io"
at the cluster scope: access denied
```

The same holds for what is inside: `--as alice` against
`root:capi-demo:bob:capi-demo-1` is refused, and Bob is refused Alice's, so
neither can read a `Cluster`, a `Machine` or a kubeconfig `Secret` of the
other's.

### Where that comes from

Two `ClusterRoleBinding`s per user per workspace. In a home workspace the demo
makes both; in a workspace that holds clusters it makes only the bindings, and
the roles they name were written for it. Start with the home:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice \
  get clusterrolebindings -o custom-columns='NAME:.metadata.name,ROLE:.roleRef.name,SUBJECT:.subjects[*].name' \
  | grep -E 'NAME|alice'
```

```
NAME                                ROLE                          SUBJECT
demo-home-owner:alice               demo-home-owner               alice
system:kcp:workspace:access:alice   system:kcp:workspace:access   alice
```

The **first** is ordinary Kubernetes RBAC — a role the demo defines, granting
`get`/`list`/`watch` on `tenancy.kcp.io` `workspaces`.

In each workspace Alice *owns* it is a different role, and one the demo did not
write:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusterrolebindings -o custom-columns='NAME:.metadata.name,ROLE:.roleRef.name,SUBJECT:.subjects[*].name' \
  | grep -E 'NAME|alice'
```

```
NAME                                ROLE                          SUBJECT
cluster-api-admin:alice             cluster-api-admin             alice
system:kcp:workspace:access:alice   system:kcp:workspace:access   alice
```

`cluster-api-admin` was written by the `cluster-api` `WorkspaceType`'s
initializer, before kcp let the workspace become `Ready` — so a workspace you
can enter is one that already grants somebody the use of what it serves. The
demo only decided who holds it.

Its first rule is the one that moves:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusterrole cluster-api-admin -o jsonpath='{.rules[0].apiGroups}{"\n"}'
```

```
["bootstrap.cluster.x-k8s.io","cluster.x-k8s.io","controlplane.cluster.x-k8s.io","infrastructure.cluster.x-k8s.io"]
```

Nobody wrote that list either. It is derived from the `APIBinding`s this
workspace holds — the ones from [section 6](#6-apibinding-a-workspace-opting-in)
— and a controller rewrote the role when Alice's provider bindings appeared.
Had she enabled only the infrastructure provider, the list would be two groups
long. There is a `cluster-api-view` alongside it with the same groups and only
`get`/`list`/`watch`, for somebody who should see the clusters without being
able to read a kubeconfig `Secret`.

That first rule is **read only**, across every type those groups publish. What
Alice may *write* is one rule and one resource:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusterrole cluster-api-admin \
  -o jsonpath='{range .rules[?(@.verbs[0]=="create")]}{.apiGroups}{.resources}{"\n"}{end}'
```

```
["cluster.x-k8s.io"]["clusters"]
```

Write on one type is all a ClusterClass based cluster needs. Alice's `Cluster`
names a class and a shape, and everything under it — the `DevCluster`, the
`KubeadmControlPlane`, the worker `MachineDeployment` — is created by the
topology controller under the *manager's* identity, not hers. Scaling and
version bumps are edits to `spec.topology`, so they are edits to the `Cluster`.
The `ClusterClass` and its templates were seeded into her workspace by the demo
and she can read but not change them: choosing what a cluster here is made of
is the platform's answer, not a tenant's.

The one thing she may write that is not a `Cluster` is an `APIBinding` —
[section 6](#6-apibinding-a-workspace-opting-in)'s subject. Enabling a provider
is a tenant's decision, so the role carries it.

The **second** binding is kcp's. Before RBAC on the resource is consulted at all,
kcp's workspace content authorizer asks whether you may be in the workspace —
the verb `access` on the **non-resource URL** `/`, which is what
`system:kcp:workspace:access` grants. That role is kcp's own, bootstrapped into
its local admin logical cluster and resolvable from a binding in any workspace,
which is why the demo binds it by name rather than writing the rule out. Leave
this binding out and every request into the workspace is refused with `access
denied`, whatever the first role grants — including for types the workspace
plainly serves.

### Why `root:capi-demo` is in the way

The first row of the table is the interesting one: Alice **can** list `root`,
and sees that a workspace called `capi-demo` exists.

That is kcp's own policy — `root` binds `system:kcp:tenancy:reader` to
`system:authenticated`, so any authenticated user can list root's direct
children. And a `Workspace` list is neither recursive nor filtered by what the
caller can enter: it returns the workspaces stored in the one workspace you
addressed, all of them or none. There is no "list the workspaces I have access
to" — what a tenant has instead is the path to their own home.

Both facts together are why the homes sit under an org workspace rather than
under `root`. Directly under `root` their names would be listable by every
authenticated user on the shard, and Alice would at least learn that Bob
exists. One workspace in between, granting nobody anything, stops the walk one
level up: Alice can see `capi-demo` and gets `forbidden` the moment she asks
what is in it.

```sh
kubectl --context base --as alice --server $KCP/clusters/root get workspaces
kubectl --context base --as alice --server $KCP/clusters/root:capi-demo get workspaces
```

```
NAME        TYPE        REGION   PHASE   URL
capi-demo   universal            Ready   https://localhost:33799/clusters/root:capi-demo

Error from server (Forbidden): unknown
```

(`unknown` rather than a named resource: Alice cannot enter the workspace, so
the refusal comes before there is anything to name.)

None of this is authentication. There is no identity provider here and no
accounts — the users are names kcp authorizes as. A deployment's users arrive
through OIDC or a workspace authentication configuration; what this section
shows is the half that decides what they may then do.

## 10. Into a workload cluster

Each cluster's admin kubeconfig is a Secret in its own workspace, written by the
control plane provider:

```sh
kubectl --context base --server $KCP/clusters/root:capi-demo:alice:capi-demo-1 -n default \
  get secret demo-00-kubeconfig -o jsonpath='{.data.value}' | base64 -d > /tmp/alice.kubeconfig

kubectl --kubeconfig /tmp/alice.kubeconfig get nodes
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
each other. That command was the admin's; Bob is refused the Secret, so the
kubeconfig for Alice's cluster is hers alone.

## 11. The same thing, in a UI

Everything so far has been `kubectl` and a URL path. The same run is
browsable, and a UI shows one thing the commands cannot: **the UI itself
changes with the workspace**.

### The kubeconfig the run already wrote

Look in the demo's state directory:

```sh
kubectl --kubeconfig .demo/kcp/workspaces.kubeconfig config get-contexts -o name
```

```
alice@root:capi-demo:bob:capi-demo-1
root
root:capi-demo
root:capi-demo:alice
root:capi-demo:alice:capi-demo-1
root:capi-demo:bob
root:capi-demo:bob:capi-demo-1
```

One context per workspace, named after the workspace path. They differ only in
the `/clusters/<path>` on the end of the server URL — which, as
[section 2](#2-workspaces-and-what-they-actually-are) showed, is all a
workspace is. So a tool that picks a context picks a workspace, with nothing
taught to it about kcp.

Two details worth knowing before you open it:

- **A tenant's workspaces are browsed as that tenant.** The contexts under
  `root:capi-demo:alice` carry `as: alice`, so what the UI shows is Alice's
  authorization rather than an admin's rehearsal of it. The workspaces above
  them belong to nobody and use the demo's own credential — a tenant is
  refused there, and a UI showing an empty tree would say nothing about why.
- **`alice@root:capi-demo:bob:capi-demo-1` is refused on purpose.** It is
  Bob's workspace, browsed as Alice. Everything in it fails the way
  [section 9](#9-two-users-and-what-neither-can-see) showed at the command
  line — which is the point: a refusal you can click on.

The file carries the credentials it needs, including the shard admin's for the
impersonating contexts, and is written `0600`. It is for a demo on one machine.

### Two plugins

[Headlamp](https://headlamp.dev) reads that kubeconfig as one cluster per
workspace. Two plugins make the workspaces legible:

```sh
# The workspace navigator, and the sidebar that follows what a workspace serves
git clone https://github.com/jimmidyson/headlamp-kcp-plugin
cd headlamp-kcp-plugin && npm install && npm run build

# Cluster API, from the branch that detects it by discovery
git clone -b kcp/cluster-api-discovery https://github.com/jimmidyson/headlamp-plugins
cd headlamp-plugins/cluster-api && npm install && npm run build
```

A plugin is `dist/main.js` plus its `package.json`, in a directory named after
it. Headlamp desktop reads `~/.config/Headlamp/plugins`; a server build takes
`-plugins-dir`:

```sh
headlamp-server \
  -kubeconfig .demo/kcp/workspaces.kubeconfig \
  -plugins-dir <the directory holding both plugins>
```

The **stock** Cluster API plugin will not do here, and the reason is worth
understanding rather than working around: it decides whether Cluster API is
installed by reading the `CustomResourceDefinition` for
`clusters.cluster.x-k8s.io`. As [section 6](#6-apibinding-a-workspace-opting-in)
showed, these types arrive through an `APIBinding` — there is no CRD for them
in the workspace at all, so the plugin reports "Cluster API Not Detected" with
the objects sitting in front of it. The branch above asks discovery instead,
which has an answer in every workspace.

### What to look at

**The app bar** names the workspace you are in and the one above it. Click it
for the navigator.

**Move down the tree.** From `root:capi-demo`, Alice and Bob are one click
each; from Alice's home, `capi-demo-1` is one more. The navigator's **Plugins**
column says `Cluster-api` against `capi-demo-1` and nothing against the homes
— before you go there, because it reads each workspace's bindings.

**The sidebar changing.** In `capi-demo-1` there is a Cluster API section, with
the `demo-00` cluster, its `KubeadmControlPlane` and its `Machine`s. Go one
workspace up and the section is gone, along with its routes. Nothing is
configured per workspace: the section is tied to the `cluster.x-k8s.io` API
group, and the group is served there or it is not — which is the same fact
`kubectl api-resources` reported in section 6, wearing a different hat.

**What is missing everywhere.** No Pods, no Deployments, no Nodes. A workspace
serves what is bound into it, and none of those are. The sidebar offers what
the workspace answers for and nothing else.

**What each workspace binds.** The navigator's last section lists the
`APIBinding`s and the API groups each one serves — the join between "Alice
enabled a provider" in section 6 and "this section appeared" here.

**Being refused.** Switch to `alice@root:capi-demo:bob:capi-demo-1` and the
same UI, pointed at the same shard, shows nothing but errors. That is not the
UI failing; it is section 9's `Forbidden`, rendered.

[The demo in a UI](headlamp.md) has the same material without the walkthrough
around it.

## 12. Stopping, and what to change

`Ctrl-C` in the first terminal. The managers stop, the kcp server stops, and
with the in-memory backend the workload clusters go with them. State lives
under `.demo/`, which is gitignored — delete it to start clean.

Worth trying next:

```sh
task demo DEMO_FLAGS="--workspaces=6"                    # scale the fleet
task demo DEMO_FLAGS="--users=alice,bob,carol"           # more tenants
task demo DEMO_FLAGS="--users="                          # none: everything under root, admin only
task demo DEMO_FLAGS="--control-plane-machines=3"        # a three-replica control plane
task demo DEMO_FLAGS="--backend=docker"                  # real containers
task demo DEMO_FLAGS="--no-manager"                      # run the managers yourself
```

`--no-manager` is the interesting one if you are heading towards a real
deployment: the demo creates the workspaces and objects and then waits, leaving
you to start `core-manager` and friends the way you would run them for real. See
[Installation](installation.md).

## Where to go from here

- [Onboarding a workspace](onboarding.md) — getting one of your own, and turning on the providers you want
- [Usage](usage.md) — what differs day to day from upstream Cluster API, and what does not
- [Installation](installation.md) — running the managers against a kcp instance you already have
- [One APIExport per provider](../design/provider-exports.md) — why one export per provider and how the claims are derived
- [Workspace onboarding](../design/workspace-onboarding.md) — the WorkspaceType, the roles it writes, and the controllers that keep them right
- [The demo in a UI](headlamp.md) — browsing the same run in Headlamp, and what the UI shows that the tables cannot
- [The demo](../design/demo.md) — what the demo is for, and what it deliberately does not do
- [Workspace resource usage](../design/workspace-resource-usage.md) — what a workspace costs, measured
