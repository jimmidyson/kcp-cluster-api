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

`install` needs `CLUSTER_TOPOLOGY=true` for the same reason on a different
cluster: every cluster the scale test creates is built from a ClusterClass, and
a provider installed without the gate refuses them at admission with `spec:
Forbidden: can be set only if the ClusterTopology feature flag is enabled` — a
message that names the object rather than the installation. It does **not** need
`EXP_RUNTIME_SDK`: no runtime extension runs there, since CAREN stays on the
bootstrap cluster.

`capiscale-prepare` **sets** the gate rather than reporting it missing, on the
two controllers that read it — core, whose topology controller does the work, and
the DevCluster provider, whose template webhooks refuse the objects without it.
The two kubeadm providers accept the flag and nothing reads it.

Setting rather than reporting, because `clusterctl init` will not revisit a
provider it has already installed: a cluster whose providers arrived before the
`install` step set `CLUSTER_TOPOLOGY` would otherwise need a reinstall to become
measurable, and re-running `install` then `test:capi:cluster` is enough.

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

Worth checking on a cluster that already exists rather than assuming, because a
cluster provisioned before the patch went in, or one whose ClusterClass edit did
not roll the control plane, comes up on the default and says nothing about it
until a rung reaches 2 GiB — at which point etcd raises a NOSPACE alarm, the
store goes read-only, and every controller in the fleet stops at once. That
failure is indistinguishable from a management cluster that ran out of capacity,
and it is not one:

```sh
kubectl -n kube-system get pod -l component=etcd \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].command}{"\n"}{end}' \
  | tr ' ' '\n' | grep -E 'etcd-|quota-backend'
```

All three members, because a quota is per member and a rolled control plane
picks them up one at a time.

## Changing the ClusterClass on a cluster that already exists

The etcd patches live in the ClusterClass copy, and the Cluster names that copy.
So changing either patch is a ClusterClass change, and Cluster API rolls the
control plane to apply it — the kubeadm config of every control plane machine
changes, so every control plane machine is replaced, one at a time.

To do it in place:

```sh
./scale-cluster.sh clusterclass          # rewrite the copy with the new patches
kubectl --kubeconfig ../../bin/capi-scale-bootstrap.kubeconfig \
  -n default get kcp -o yaml | grep -A5 listen-metrics   # watch it propagate
```

The rollout takes as long as three control plane machines take to build. Nothing
needs to be told to start it: the topology controller notices the class changed.

**When to recreate instead.** If the machine *sizes* are also wrong — anything
built before the sizing fix came up at CAREN's 2 vCPU and 4 GiB — then every
machine is being replaced either way, and `down` followed by `create` is fewer
moving parts than two rolling updates and a hand-patched topology. It is also
the only path that is known to work end to end, because it is the one the script
does.

## etcd is defragmented between rungs

Compaction frees pages inside etcd's backend file and returns none of them, and
the quota counts the file. A converging Cluster API fleet churns, so a climb can
reach the quota with most of the file free — at which point etcd goes read-only
and the run has recorded a ceiling about accumulated free pages rather than
about how much state the store can hold.

So each rung starts from a defragmented store, and the report says what each
defragmentation reclaimed. Two rules that matter as much as doing it at all:

- **Never inside a rung.** A defrag is a stop-the-world rewrite on the member it
  runs against — writes block, latencies spike, a leader change is possible. All
  three in the middle of a measurement, none of them about the fleet.
- **Never during the soak.** The soak asks what a held fleet does when nothing
  is being asked of the cluster. A defrag is something being asked of the
  cluster.

Members go one at a time: three at once on a three-member cluster is an outage
rather than a maintenance window. A member that will not defragment is reported
and does not abandon the climb — it is simply the one whose file reaches the
quota first.

The report also carries the gap either way, so a run that hits the quota can say
whether defragmenting would have bought room or whether the store is genuinely
full.

## Teardown deletes Clusters first, and namespaces only once they are gone

The first run deleted its namespaces and left every one of them Terminating,
with the DevCluster provider logging, forever:

```
"Connect failed" err="error creating REST config: error getting kubeconfig
secret: Secret \"c0001-kubeconfig\" not found" controller="clustercache"
```

The Secret is a symptom. Deleting a namespace stamps every object in it at once,
with no order, and stock Cluster API cannot finish from there:

- A `Secret` has no finalizer, so the kubeconfig goes at once, and the cluster
  cache logs the line above for as long as the `Cluster` — which does have one —
  is still there.
- A deleting `DevCluster` removes its finalizer immediately, taking the in-memory
  resource group and listener every `DevMachine` would clean up with it.
- A deleting `DevMachine` whose `DevCluster` has gone logs `DevCluster is not
  available yet` and returns without releasing its finalizer. Its `Machine`
  waits for it, the `Cluster` waits for its Machines, and the namespace waits
  for the `Cluster`. Nothing times out.

This repository's fork carries a fix for both halves — a deleting DevCluster
waits for its DevMachines, and a deleting DevMachine whose DevCluster has gone
releases itself; see `DRIFT.md` — because deleting a kcp `APIBinding` removes
every bound object at once exactly as a namespace does. The cluster under test
runs stock Cluster API **on purpose**, so it does not have them, and the harness
keeps the order itself: `upstreamscale.Teardown` deletes every `Cluster`, waits
until none remain, and only then deletes the namespaces and waits for those.
Deleting the `Cluster` lets the Cluster controller order its own descendants —
workers, control plane, infrastructure, then the Cluster and the Secrets it owns
— which is the order upstream's own tests rely on.

`TEARDOWN_TIMEOUT` (30m) is how long that wait may take. A teardown that runs
out reports what it was still waiting for, by name and with the Cluster's own
`Deleting` condition message, and **leaves the namespaces alone**: deleting them
anyway is the failure above. It fails the run, after the report is written,
because whatever it leaves behind is what the next run would take as its
baseline.

### Recovering a fleet left Terminating

A namespace stuck this way stays stuck: nothing in stock Cluster API will ever
release those DevMachines. Their in-memory state went with the DevCluster's
resource group, so releasing them by hand loses nothing.

Start by finding out whether anything is still working on it. A run interrupted
at scale can leave the provider OOM killed, in which case no finalizer is coming
off on its own:

```sh
export KUBECONFIG=../../bin/capi-scale.kubeconfig
kubectl -n capd-system get pods
kubectl get ns -o name | grep -c '^namespace/capi-scale-'
kubectl get devmachines -A --no-headers --chunk-size=500 | wc -l
```

Then release the DevMachines. `kubectl patch` takes no `--all` — that flag is
`delete`, `label` and `annotate` only — so each object is named, and at a fleet
of thousands that has to be done in parallel rather than one at a time:

```sh
kubectl get devmachines -A --chunk-size=500 --no-headers \
    -o custom-columns=NS:.metadata.namespace,N:.metadata.name \
  | awk '$1 ~ /^capi-scale-/ {print $1, $2}' \
  | xargs -P 32 -n 2 sh -c 'kubectl -n "$1" patch devmachine "$2" \
      --type=merge -p "{\"metadata\":{\"finalizers\":null}}"' _

kubectl get namespaces | grep capi-scale    # until none remain
```

The `awk` filter is not decoration: without it this reaches every DevMachine on
the cluster rather than the ones this harness created.

Once the DevMachines go, every Machine finishes, then every Cluster, then the
namespace. If something is still there after that, run the same pipeline for
`devclusters`, then `machines`, then `clusters` — in that order, so that each
one is only stripped if the layer below it did not free it. Check the list is
empty before the next run: a run that starts over a terminating fleet measures
both.

### Restart the managers between runs

A manager keeps the high-water mark of every fleet it has held. Go returns
memory to the operating system lazily, the settle before the baseline waits for
goroutine counts rather than for memory, and a process that has been up across
several runs starts the next one carrying all of them — which lands directly on
the intercept every per-cluster figure is measured against.

`capiscale-prepare` rolls the deployments itself whenever it changes a flag, so
running it is usually enough. When it reports everything already prepared,
restart them by name:

```sh
export KUBECONFIG=../../bin/capi-scale.kubeconfig
for ns in capi-system capi-kubeadm-bootstrap-system \
          capi-kubeadm-control-plane-system capd-system; do
  for d in $(kubectl -n "$ns" get deployments -o name); do
    kubectl -n "$ns" rollout restart "$d"
    kubectl -n "$ns" rollout status "$d" --timeout=5m
  done
done
```

By name because `rollout restart` and `rollout status` take a name or a
selector and not `--all` — that flag belongs to `delete`, `label` and
`annotate`, which is the same trap the DevMachine recovery above documents.

The API server's own allocator high-water mark survives this and everything
else short of rolling the control plane. It does not matter for a run read as
slopes between rungs; it does for one whose absolutes are meant to be quoted.

### Do not delete the namespaces to tear a fleet down

It is the fastest-looking way to clean up and it is what produces the state
above. A namespace deletion stamps every object in it at once, with no order,
and the failure is the one this section exists to recover from.

Delete the Clusters and let the Cluster controller order its own descendants,
which is what `upstreamscale.Teardown` does and what upstream's own tests rely
on:

```sh
kubectl delete clusters -A --all --wait=false
# wait until none remain, and only then remove the namespaces
```

### A container that exited 137 was not necessarily OOM killed

`Exit Code: 137` is SIGKILL and reads like a memory kill. It often is not, and
the two send you to opposite places.

The kubelet records `Reason: OOMKilled` when the kernel kills a container for
memory. A last state of `Exit Code: 137, Reason: Error` is therefore the
kubelet's own kill: SIGTERM first, then SIGKILL when the process did not finish
shutting down inside its termination grace period. On a kubeadm control plane
the usual cause is a liveness probe that stopped being answered — `/livez` at a
15s timeout, 10s period and a failure threshold of 8, so about eighty seconds of
an API server too busy to reply.

Two things confirm it rather than leaving it a guess. `kube-apiserver` has no
memory limit under kubeadm — only a CPU request — so a cgroup OOM was not
available to it at all:

```sh
kubectl -n kube-system get pod "kube-apiserver-$node" \
  -o jsonpath='{.spec.containers[0].resources}{"\n"}'
```

And an OOM kill leaves no shutdown in the log, because SIGKILL cannot be caught.
Lines like `client-ca controller shutting down` in the previous container's log
mean the process was asked to stop and was trying to:

```sh
kubectl -n kube-system logs "kube-apiserver-$node" --previous | tail -40
```

On a scale run this distinction is the result rather than an obstacle to it. A
control plane killed for memory says buy memory. A control plane killed by its
own kubelet for not answering says the fleet outran it, which is what the run is
measuring. The report says which: `PodFacts.WhyItDied` reads the exit code, the
OOM flag and the memory limit together, so a failed rung is classified as one or
the other instead of as `(Error)`.

## The API server's profiling cannot be turned on here, and trying broke a node

CAREN's ClusterClass sets `--profiling=false` on the API server — CIS benchmark
1.2.18, and right for a cluster anyone depends on. It also means the harness
cannot force a collection before reading the API server's heap, so that figure
is a point on an allocator sawtooth rather than the retained set. The report
says so on every line.

**Three attempts to patch it, and the conclusion is that it cannot be done from
a ClusterClass patch on a CAREN class.** Recorded because each failure looked
like a fixable mistake and the third was not:

1. **Append a second entry.** Refused: extraArgs is validated
   `self.all(x, self.exists_one(y, x.name == y.name))`, *"extraArgs name must be
   unique"*. A repeated flag would take the last value on a command line, and
   never gets one, because the object is rejected at admission. The refusal
   surfaces on the `KubeadmControlPlane`, as the topology controller's server
   side apply dry-run, not on the ClusterClass that caused it.
2. **Replace that entry in place.** Refused:
   `core/webhooks/admission/patch_validation.go` permits an array index of only
   `0` or `-` on `add`, and forbids any index at all on `replace` and `remove` —
   *"elements in arrays can not be accessed in a replace operation"*.
3. **Replace the whole list**, snapshotted from the control plane template with
   `profiling` flipped. Accepted by every validator, and **it broke the control
   plane**: the new machine never came up.

The third is the one worth understanding. CAREN is a *runtime extension*, so its
patches are `external:` entries in `spec.patches` with no `definitions` — they
write the API server's configuration at render time, from code, and there is
nothing in the ClusterClass to read. A whole-list replace appended last renders
last and discards everything that extension contributed, leaving a
`ClusterConfiguration` the node cannot come up on.

That is not fixable by reordering. Put the patch first and CAREN's extension
renders after it and wins, so profiling stays off; put it last and it wins and
takes the rest of the API server's configuration with it. There is no position
that changes one argument and keeps the others.

If a control plane is stuck this way, remove the patch and let it roll back:

```sh
./scale-cluster.sh clusterclass     # this step no longer emits an apiServer patch
```

### What the harness does instead

It reads the API server's heap **five times, two seconds apart, and keeps the
lowest** — the sawtooth's floor, which is the closest thing to the live set
available without asking the process for one. It is an upper bound and the
report labels it as one:

```
apiserver@400 clusters: ... 6.6 GiB heap ... (heap is the lowest of 5 reads: no
collection could be forced, so this is the sawtooth's floor and an upper bound
on the retained set)
```

`APISERVER_HEAP_SAMPLES` sets the count. This needs nothing from the cluster,
which is the point: the figure it replaces cost a control plane to chase.

The API server's **resident** memory and **goroutine** count were never affected
by any of this — both are monotonic and reproducible, and they are what the
result rests on.

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
