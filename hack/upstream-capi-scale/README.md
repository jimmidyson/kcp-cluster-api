# Provisioning the management cluster for the stock Cluster API scale test

`scale-cluster.sh` builds the cluster the scale test measures. It is deliberately
several steps rather than one: the middle one wants reading before it is applied,
and the last one is the one you will re-run.

First, the pinned tools:

```sh
task tools:capi     # kind, into bin/ (clusterctl is fetched per version, below)
```

The script puts `bin/` first on its PATH, and fetches a **clusterctl per Cluster
API version** into `bin/clusterctl-<version>`. The two clusters it builds run
different Cluster APIs, and clusterctl checks the contract version of what it is
asked to install against the one it was built for — a question with a real
answer and no reason to depend on it. A tool that matches what it installs
cannot be the thing that fails.

## Why the bootstrap cluster is pinned and the cluster under test is not

CAREN v0.50.0's runtime extension strict-decodes CAPX's `NutanixClusterTemplate`
against the types it was compiled with, and a newer Cluster API topology
controller writes a `spec.template.metadata` those types do not have. The
cluster then never gets built:

```
failed to generate patches for patch "cluster-config": failed to call extension
handler "nutanixclusterv6configpatch-gp...": failed to convert unstructured
object (infrastructure.cluster.x-k8s.io/v1beta1, Kind=NutanixClusterTemplate) to
typed object: strict decoding error: unknown field "spec.template.metadata"
```

So the **bootstrap** cluster runs the Cluster API CAREN was built against —
`BOOTSTRAP_CAPI_VERSION`, v1.12.5. That is a constraint on the machinery that
builds the test cluster and not on what the test measures.

The **cluster under test** runs `CAPI_VERSION`, the latest release, on a
different cluster entirely. Measuring stock Cluster API at a version chosen to
appease a runtime extension on another cluster would be measuring the wrong
thing for no reason.

`config` prints both. Both serve `v1beta2` for every kind this harness creates,
including `DevCluster`, so the preflight's expectations hold either way.

Nutanix credentials must be in the environment before the first step —
`clusterctl init` reads them, and so does `clusterctl generate cluster` later:

```sh
export NUTANIX_ENDPOINT=... NUTANIX_PORT=9440 NUTANIX_USER=... NUTANIX_PASSWORD=...
export NUTANIX_INSECURE=false
export NUTANIX_SUBNET_NAME=... NUTANIX_PRISM_ELEMENT_CLUSTER_NAME=...
export NUTANIX_MACHINE_TEMPLATE_BASE_OS=... NUTANIX_MACHINE_TEMPLATE_LOOKUP_FORMAT=...
export NUTANIX_STORAGE_CONTAINER_NAME=...
export CONTROL_PLANE_ENDPOINT_IP=... KUBERNETES_SERVICE_LOAD_BALANCER_IP=...
export KUBERNETES_VERSION=v1.32.0
export DOCKER_HUB_USERNAME=... DOCKER_HUB_PASSWORD=...
```

```sh
./scale-cluster.sh config         # resolve and print every input, touching nothing
./scale-cluster.sh bootstrap      # a local kind cluster, with CAPX and CAREN on it
./scale-cluster.sh clusterclass   # copy CAREN's ClusterClass, add the etcd quota
./scale-cluster.sh create         # create the cluster, wait for it
./scale-cluster.sh kubeconfig     # write bin/capi-scale.kubeconfig
./scale-cluster.sh install        # stock Cluster API on it, prepared for measuring
```

Then, before anything creates a fleet:

```sh
KUBECONFIG=../../bin/capi-scale.kubeconfig \
  go run ../../cmd/capiscale-prepare --only=preflight
```

This touches nothing and answers the largest open question in the whole
exercise: the objects a run creates are built against this repository's fork of
Cluster API, off the v1.15 line, and the CRDs come from the stock release
clusterctl installed. It says whether those agree — by kind and by version, and
naming the provider to install for anything missing — before there is a fleet
to be confused by it.

The cluster template defaults to CAREN's own published quick start —
`examples/capi-quick-start/nutanix-cluster-cilium-helm-addon.yaml` from the
release named by `CAREN_VERSION` — so there is nothing to author. Set
`CLUSTER_TEMPLATE` to use a different one. You will need the usual Nutanix
substitutions in your environment (`NUTANIX_ENDPOINT`, `NUTANIX_USER`,
`NUTANIX_PASSWORD`, `NUTANIX_SUBNET_NAME`, `NUTANIX_PRISM_ELEMENT_CLUSTER_NAME`,
`NUTANIX_MACHINE_TEMPLATE_BASE_OS`, `CONTROL_PLANE_ENDPOINT_IP`,
`KUBERNETES_VERSION`, and a `KUBERNETES_SERVICE_LOAD_BALANCER_IP`), which is
what `clusterctl generate cluster` reads.

## The generated manifest is trimmed before it is applied

CAREN's example is a fine cluster and a poor scale-test cluster, in two ways
that are easy to miss:

**Its worker pool has no replica count.** The size is a pair of
cluster-autoscaler annotations, so the pool is whatever the autoscaler decides.
A scale test cannot have its own management cluster resizing underneath it —
and with the autoscaler addon removed, nothing would size the pool at all. So
the annotations come off and an explicit count goes on. Both: Cluster API
refuses a topology that sets replicas while the annotations are still there.

**Its nodes are built for a quick start.** Every one is 2 vCPU and 4 GiB — a
sixth of the memory `sizing.md` asks the control plane for. Nothing in a run
reports that as wrong: the cluster comes up, the controllers schedule, and the
ceiling the ladder finds is the box rather than Cluster API. The trimmer sizes
both pools, and `config` prints what a run will ask for.

Override with `CONTROL_PLANE_VCPUS`, `CONTROL_PLANE_MEMORY`,
`CONTROL_PLANE_DISK`, `WORKER_VCPUS`, `WORKER_MEMORY`, `WORKER_DISK`. Disks are
larger than the example's 40 GiB because etcd's revisions between compactions
are what a climbing fleet fills a disk with. Only the socket count moves for
vCPUs — cores per socket stay as the template had them, since two numbers
multiply to make a vCPU count and changing both invites four times what anyone
asked for.

**It turns on addons this measurement does not use** — CSI, COSI, the
autoscaler, a MetalLB service load balancer and node feature discovery. Nothing
here asks for a PersistentVolume, and every addon left on is another controller
reconciling against the API server whose cost is the subject of the run.

The CNI and the cloud provider stay. Without the CNI nothing networks; without
the cloud provider new nodes keep the `uninitialized` taint and never become
schedulable, which would present as a scale test that cannot place its own
controllers.

`cmd/capiscale-template` does this, on the manifest clusterctl generated rather
than on the template — the template's `${VARIABLE}` placeholders sit in fields
that are numbers once substituted, and a round trip through a YAML parser would
quote them.

## Kubeconfigs

Two, both in `bin/`, and neither is `~/.kube/config`:

| | |
|---|---|
| `bin/capi-scale-bootstrap.kubeconfig` | the kind management cluster |
| `bin/capi-scale.kubeconfig` | the cluster under test, written by `kubeconfig` |

kind's default is to merge its context into whatever kubeconfig is in play and
make it current, which leaves your shell pointing at a throwaway cluster after a
step you ran for another reason. This repository already refuses the mirror
image of that — its scale tasks name a context rather than taking whatever is
current, so a run meant for a local cluster cannot create a fleet somewhere else
— and the argument runs the same way here. So the bootstrap cluster gets a file
of its own, every command names it, and nothing outside `bin/` changes.

`./scale-cluster.sh config` prints both paths.

## What goes where, and why

`bootstrap` is one `clusterctl init`. CAREN is a clusterctl provider given
somewhere to find it, so the script writes a clusterctl config into `bin/`
naming CAREN's release and passes it with `--config` — rather than editing
`~/.config/cluster-api/clusterctl.yaml`, which the rest of your work depends on.

Four things that line needs, each of which fails in a way that does not name
itself:

- `CLUSTER_TOPOLOGY=true` — the templates are ClusterClass based, and without
  the gate the topology controller does not run at all.
- `EXP_RUNTIME_SDK=true` — CAREN is a runtime extension; without the gate its
  hooks are never called and the cluster comes up unpatched.
- `--addon helm` — CAREN's templates deploy the CNI and the cloud provider with
  `strategy: HelmAddon`, so without the Helm addon *provider* there is no CNI
  and no node ever becomes Ready. This is a provider, not the `helm` CLI, which
  nothing here needs.
- the Nutanix credentials, which clusterctl reads at init time.

CAPX is unpinned — clusterctl takes its latest. To pin one:

```sh
CAPX_VERSION=v1.10.3 ./scale-cluster.sh bootstrap
```

Either way `config` records which version a run used. If a CAPX turns out not to
work here, the first thing to check is whether CAREN's ClusterClass still
resolves against its types: the chart gates that class on
`infrastructure.cluster.x-k8s.io/v1beta1/NutanixClusterTemplate` being present,
so a CAPX that moves that API leaves you with the empty ClusterClass list
again.

It then applies CAREN's default Nutanix ClusterClass, **which clusterctl does
not install**. CAREN's providers artifact carries the runtime extension and
nothing else; the default ClusterClasses live in its Helm chart, gated on
`deployDefaultClusterClasses` and on CAPX being present. So a clusterctl-only
install leaves the extension running and no class for a `Cluster` to name, which
presents as an empty ClusterClass list and nothing to say why.

The chart includes that file verbatim — `.Files.Get`, no Helm templating — so
applying it directly is exactly what a Helm install would have done. The `{{ }}`
inside it are cloud-init and kube-vip templating, not Helm's.

**CAPX and CAREN go on the kind cluster only.** They build the workload cluster
and have no part in what it measures. Installing them on the cluster under test
would put two more controller sets and their CRDs on the API server whose cost
is the point of the exercise.

**Nothing is pivoted.** The kind cluster stays the management cluster for the
life of the experiment. A self-managed cluster would have CAPX and CAREN
reconciling against the same API server the measurement is reading.

**The workload cluster gets four providers**: core, kubeadm bootstrap, kubeadm
control plane, and docker. The docker provider is what serves `DevCluster` —
the in-memory backend is a mode of it, not a provider of its own — which is also
why its deployment arrives wanting a Docker socket.

**No CSI.** Nothing in this test asks for a PersistentVolume. etcd writes to the
control plane nodes' own disks through kubeadm.

## The two things that need patching, and they are different things

*Every* controller is patched, not just the DevCluster one:

| | core | kubeadm bootstrap | kubeadm control plane | DevCluster |
|---|:-:|:-:|:-:|:-:|
| Guaranteed resources | yes | yes | yes | yes |
| GOMEMLIMIT below the limit | yes | yes | yes | yes |
| `--profiler-address` | yes | yes | yes | yes |
| Docker socket removed | — | — | — | **yes** |

The first three are how the run is measured at all and how its numbers stay
comparable between rungs. The fourth is one provider's problem: it ships as the
Docker provider, mounting `/var/run/docker.sock` from the host and running
privileged, and there is no such socket on a containerd node.

That work is `cmd/capiscale-prepare`, which calls the same functions
`internal/upstreamscale` unit tests. It is idempotent, so re-running it after
raising a limit changes only what you raised — and restarting a controller you
did not mean to restart would reset the process metrics the measurement is made
of.

Raising a limit after an OOM kill is the loop this is built for:

```sh
./scale-cluster.sh install --devcluster-memory 40Gi
```

## etcd's backend quota, and its metrics port

CAREN has a variable for neither, so `clusterclass` copies CAREN's ClusterClass
under a new name and adds a patch that sets both on the local etcd.

The quota because the 2 GiB default is a cliff. The metrics port because kubeadm
points `--listen-metrics-urls` at 127.0.0.1, so nothing outside the node can
scrape it — and this run expects the store to be what runs out, which is a hard
thing to establish about a store you cannot see. `:2381` serves etcd's
`/metrics` and not its client API: no data, no authentication, which is a fair
trade on a throwaway scale cluster and would not be on anything else. A copy rather than an edit: the CAREN-supplied ClusterClass is managed by
whatever installed it, and an edit is liable to be reverted underneath a running
experiment — which would look like a cluster that got slower halfway up the
ladder.

The default 2 GiB quota is a cliff. The database itself is not large at these
object counts; the revisions between compactions during a climb are what fill
it.

## Node labels have to be in a domain Cluster API propagates

Cluster API copies a Machine label to its Node only if it has
`node-role.kubernetes.io` as a prefix, or belongs to the
`node-restriction.kubernetes.io` or `node.cluster.x-k8s.io` domains — or matches
a regex given to the core manager's `--additional-sync-machine-labels`.

A pool label like `scale-role=devcluster` therefore reaches the Machine and
stops there. The Node never gets it, a node selector against it never matches,
and the pod sits Pending with every manifest looking correct. Use
`scale-role.node.cluster.x-k8s.io/...` or `node-role.kubernetes.io/...`.

Nothing is pinned by default, so this only matters if you choose to pin — see
`../../specs/20260903-140000-upstream-capi-scale/sizing.md`.

## What is not verified here

The CAREN-specific inputs — the ClusterClass name, the cluster template, and
whether CAREN installs as a clusterctl provider in your distribution of it — are
inputs rather than assumptions, because they differ between CAREN versions and
none of them could be checked while this was written. `clusterclass` prints the
ClusterClasses it can see when the name it was given is not one of them.

The `extraArgs` form in the etcd patch *was* checked: it is a list of
`{name, value}` in the v1beta2 kubeadm API that Cluster API v1.14 uses, not the
map it used to be.
