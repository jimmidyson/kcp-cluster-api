---
title: On Kubernetes
description: Run the demo as pods - a kcp shard and one deployment per provider - instead of as processes on your machine.
weight: 7
---

{{% pageinfo color="info" %}}
**New, and exercised in two halves rather than end to end.** Everything below
except the pod layer itself — the credentials, the kubeconfigs, each manager as
its own process with the flags and the kubeconfig its `Deployment` gives it,
and the demo with its manager half switched off — was run and reaches ready
clusters in every workspace; that run is under the feature's
[`evidence/`](https://github.com/jimmidyson/kcp-cluster-api/tree/main/specs/20260824-071500-kubernetes-deployment/evidence).
The image build, the mounts, the probes and the scheduling have not been run,
because the environment this was built in could not pull a container image.
What was checked of the images is their contents' behaviour: the demo binary
finds the CRD manifests through `KO_DATA_PATH` with no Go toolchain on `PATH`,
which is the container's situation.
Report what you find.
{{% /pageinfo %}}

```sh
task demo:kubernetes:kind
```

That creates a kind cluster, builds this repository's images straight into it
with [ko](https://ko.build), and deploys a kcp shard and **one deployment per
Cluster API provider** —
then runs the demo as a Job against them and prints what it prints. The tables
are [the demo's](demo.md); what is different is where everything ran.

`kubectl -n kcp-demo get pods` afterwards lists seven: `kcp-0`, a
`cluster-api-core`, `cluster-api-bootstrap-kubeadm`,
`cluster-api-controlplane-kubeadm` and `cluster-api-dev-infrastructure` pod,
the `cluster-api-workspace-manager`, and the completed `capi-demo` run. Six
running pods rather than one process: each manager holds its own credentials,
reaches kcp over the network, and knows about no workspace until one binds its
`APIExport` — which is the topology an installation has, and the one thing
`task demo` cannot show, because there everything shares a process.

## The images

ko builds one image per binary — `core-manager`,
`kubeadm-bootstrap-manager`, `kubeadm-control-plane-manager`,
`dev-infrastructure-manager`, `workspace-manager` and `demo` — with no
Dockerfile and no build context. The shard is not built here at all: it is
upstream's `ghcr.io/kcp-dev/kcp`, pinned to the version `task tools` installs
for a local run.

The images have to be reachable from the cluster's nodes, and where they go is
`KO_REPO`:

```sh
task image                                   # ko.local: the local Docker daemon
task image KO_REPO=kind.local KIND_CLUSTER_NAME=my-cluster
task image KO_REPO=registry.example.com/capi  # builds and pushes
```

Each is named `<repo>/<binary>:<tag>`, which is what `ko build -B` produces
and what `--image-repo` tells the deployment to expect:

```sh
task demo:kubernetes DEPLOY_FLAGS="--image-repo registry.example.com/capi --image-pull-policy Always"
```

## Against a cluster you already have

```sh
task image                      # into the local Docker daemon
task demo:kubernetes            # deploy into whatever kubectl is pointed at
```

That works where the cluster's nodes are the local daemon. Anywhere else,
build to a registry they can pull from, as above.

Everything lands in one namespace, `kcp-demo` by default, and

```sh
task demo:kubernetes:clean
```

takes it away again — the namespace and everything in it, the shard's volume
included. After a kind run, `task demo:kubernetes:kind:clean` takes the whole
cluster instead, which is the thing that run created.

## Talking to the deployed shard

The run writes `.demo/kubernetes/kcp.kubeconfig`, and the shard's certificate
names `localhost` as well as its Service, so that one file works through a
port-forward:

```sh
kubectl -n kcp-demo port-forward svc/kcp 6443:6443 &
kubectl --kubeconfig .demo/kubernetes/kcp.kubeconfig get workspaces
kubectl --kubeconfig .demo/kubernetes/kcp.kubeconfig --context base \
  --server https://localhost:6443/clusters/root:capi-demo:alice:capi-demo-1 \
  get clusters,machines -A
```

It is the same kubeconfig the pods hold, with a different address. The
contexts are the ones a kcp admin kubeconfig has — `base` for the
cluster-unaware endpoint, `root` scoped to the workspace the exports live in,
`shard-base` for the privileged credential — because everything that reads one
of these already knows those names.

## Talking to a workload cluster

The clusters the run builds are not in the cluster you deployed into, and they
are not anywhere else either. The dev infrastructure provider's in-memory
backend **serves each workload cluster's API server from inside its own pod**,
on a port it allocates as the cluster is created. There are no nodes, no VMs
and no containers: a `kubectl get nodes` against one of them is answered by
that pod out of memory.

Two lookups get you in, both inside the workspace that owns the cluster:

```sh
kubectl -n kcp-demo port-forward svc/kcp 6443:6443 &

KUBECONFIG_FILE=.demo/kubernetes/kcp.kubeconfig     # written by the deploy run
WORKSPACE=https://localhost:6443/clusters/root:capi-demo:alice:capi-demo-1

# the port that provider chose for this cluster
kubectl --kubeconfig "$KUBECONFIG_FILE" --context base --server "$WORKSPACE" \
  -n default get cluster demo-00 -o jsonpath='{.spec.controlPlaneEndpoint}'

# the kubeconfig the control plane provider wrote for it
kubectl --kubeconfig "$KUBECONFIG_FILE" --context base --server "$WORKSPACE" \
  -n default get secret demo-00-kubeconfig -o jsonpath='{.data.value}' \
  | base64 -d > /tmp/demo-00.kubeconfig
```

**From inside the cluster**, that kubeconfig works unchanged: it names the
provider's pod IP, which any pod can reach.

**From outside**, the pod IP is not routable, so forward the port and point the
same kubeconfig at localhost:

```sh
kubectl -n kcp-demo port-forward deploy/cluster-api-dev-infrastructure 20000:20000 &
kubectl --kubeconfig /tmp/demo-00.kubeconfig --server https://localhost:20000 get nodes
```

Ports are allocated from 20000 upwards as clusters are created, one per
cluster, so a two-workspace run serves 20000 and 20001 — one process, both
tenants' clusters, which is the same fact the rest of this page is about.

Nothing has to be skipped to make that verify. The backend's listener binds
every interface, and the serving certificate it issues names `localhost` and
`127.0.0.1` alongside the pod's address — so the forwarded connection is
verified against the cluster's own CA, exactly as the in-cluster one is.

Every listener and its port, when you would rather ask than look them up one
cluster at a time:

```sh
kubectl -n kcp-demo port-forward deploy/cluster-api-dev-infrastructure 19000:19000 &
curl -s http://localhost:19000/listeners
```

**They are in memory, and that is the whole of their durability.** Restart the
dev infrastructure pod and every workload cluster it was serving is gone, its
port is gone, and the pod IP in every kubeconfig it wrote is wrong. Cluster API
notices and reports the clusters as unreachable; nothing rebuilds them. That is
a property of this backend rather than of the deployment — the docker backend
provisions real containers and needs a container runtime the pod does not have.

## What it deploys

| Object | What it is |
|---|---|
| `StatefulSet/kcp` | The shard. One replica, one volume: the volume is etcd, so a second replica against the same data would corrupt it |
| `Service/kcp` | How everything else addresses it, and what its certificate names |
| `Secret/kcp-serving-cert`, `Secret/kcp-client-ca` | What the shard serves with, and what it authenticates clients against |
| `Secret/kcp-kubeconfig` | Two kubeconfigs, one per kind of manager — see below |
| `Deployment/cluster-api-*` | One per provider: core, kubeadm bootstrap, kubeadm control plane, dev infrastructure |
| `Deployment/cluster-api-workspace-manager` | The controller behind the `cluster-api` `WorkspaceType` |
| `Job/capi-demo` | The demo run, with its manager half switched off |

Print them instead of applying them, for an installation that applies its own
YAML:

```sh
go run ./cmd/deploy --output yaml > install.yaml
```

The output holds the generated private keys, so it is a secret. Regenerating
it produces a new set: nothing here is a credential to keep.

## Three things this needed that a laptop run does not

**The shard's certificate has to name its Service.** kcp generates a serving
certificate for `localhost` and a placeholder address when it is given none,
and has no flag that adds a name to it — so a client inside the cluster, which
reaches kcp as `kcp.kcp-demo.svc.cluster.local`, would refuse it. The
deployment issues the certificate itself and hands it to kcp with
`--tls-cert-file`.

**The credentials have to exist before the shard does.** kcp mints its own
admin tokens into its state directory, inside its own pod, where nothing else
can read them without a sidecar or a shared volume. So the deployment issues a
client CA instead and gives kcp `--client-ca-file`: client certificates
carrying `kcp-admin` and `shard-admin` authenticate as exactly the two
identities kcp mints for itself, and every kubeconfig is known before the first
pod starts.

**A manager has to wait for what it cannot create.** kcp gives an `APIExport`
an endpoint only once a workspace has bound it, and the workspaces are created
by the demo run — which Kubernetes starts alongside the managers rather than
before them. `--startup-timeout` (30 minutes here, a minute by default) is how
long a manager waits for that rather than exiting; a manager that exited would
back off exponentially and turn a wait of seconds into minutes of
`CrashLoopBackOff`.

[The design page](../design/kubernetes-deployment.md) has the rest, including
what this deployment deliberately does not do.

## Options

`DEPLOY_FLAGS` reaches `cmd/deploy`; `go run ./cmd/deploy --help` lists all of
them. The ones worth knowing:

| Flag | Default | What it does |
|---|---|---|
| `--namespace` | `kcp-demo` | Where the installation goes |
| `--image-repo`, `--image-tag` | `ko.local`, `latest` | Where this repository's images are. One per binary, named after it |
| `--kcp-image` | upstream's, at the pinned version | The shard's image. Not built here |
| `--demo` | true | Run the demo Job. `--demo=false` deploys the shard and the managers and creates nothing in them |
| `--workspaces`, `--users`, `--clusters`, `--control-plane-machines`, `--worker-machines` | as `task demo` | Passed through to the demo run |
| `--storage-size`, `--storage-class` | `2Gi`, the cluster's default | The shard's volume |
| `--demo-args` | — | Extra flags for the demo run, for anything with no flag here — e.g. `--nutanix-export` |
| `--output yaml` | — | Print the objects instead of applying them |
| `--delete` | — | Remove the installation |

The demo uses the in-memory backend and nothing else is offered: the docker
backend provisions real containers through a container runtime, and the pod
running the dev infrastructure provider has no socket to one. What that would
take is a provider deployment with a runtime of its own, which is a different
thing to show.

## Your own workspaces, no demo

```sh
task demo:kubernetes DEPLOY_FLAGS="--demo=false"
```

That leaves a shard and five managers with nothing in them. Nothing is
published yet either — the exports and the `WorkspaceType` are created by the
demo run — so the managers will sit waiting for their endpoints until
something publishes them. [Onboarding a workspace](onboarding.md) is what
happens next, and publishing the exports without the demo is not yet a command
of its own.
