# Provisioning the management cluster for the stock Cluster API scale test

`scale-cluster.sh` builds the cluster the scale test measures. It is deliberately
several steps rather than one: the middle one wants reading before it is applied,
and the last one is the one you will re-run.

```sh
export CLUSTER_TEMPLATE=/path/to/your/caren-cluster-template.yaml

./scale-cluster.sh bootstrap      # a local kind cluster, with CAPX and CAREN on it
./scale-cluster.sh clusterclass   # copy CAREN's ClusterClass, add the etcd quota
./scale-cluster.sh create         # create the cluster, wait for it
./scale-cluster.sh kubeconfig     # write bin/capi-scale.kubeconfig
./scale-cluster.sh install        # stock Cluster API on it, prepared for measuring
```

## What goes where, and why

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

## etcd's backend quota

CAREN has no variable for it, so `clusterclass` copies CAREN's ClusterClass
under a new name and adds a patch that sets `quota-backend-bytes` on the local
etcd. A copy rather than an edit: the CAREN-supplied ClusterClass is managed by
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
