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
export NUTANIX_SSH_AUTHORIZED_KEY="ssh-ed25519 AAAA... you@host"   # read by this script, not by CAREN
```

That last one is not what it looks like. `NUTANIX_SSH_AUTHORIZED_KEY` is CAPX's
variable, from CAPX's own cluster template, and CAREN's quick start never reads
it — every `${VARIABLE}` in that template is listed by `grep -oE '\$\{[A-Z_]+'`
and the key is not among them. A key exported for it was silently ignored, and
the first control plane node that failed cloud-init could not be logged into to
find out why. CAREN creates users through its `clusterConfig` variable's `users`
list, so `create` now writes the key there, as user `SSH_USER` (`capiuser`) with
passwordless sudo; `config` says whether a login will exist. Users are
cloud-init, so only machines built after the change carry the key: a node that
is already stuck stays locked, and the fix is to let the control plane replace
it (see "Debugging a control plane node that never joined").

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

## The compaction stall, and why the interval stays at five minutes

A 1500-cluster rung ended with the controller manager, scheduler and API server
on one control plane node all restarting inside two minutes, and this in that
node's etcd log:

```
15:05:05  scheduled compaction starts: 163,243 revisions, five minutes' worth at a converging fleet's write rate
15:05:09  every apply slow, reads included — the store's apply loop is blocked
15:05:43  kube-controller-manager's lease GET times out at 5 s; it exits
          the five slowest applies in the window: 58.5 to 59.0 seconds
15:06:46  "finished scheduled compaction" took=1m41s
15:06:49  the kubelet kills the API server for failing /livez
```

Same cluster, same 1.4 GB of live data, a warm page cache: compaction takes 4 to
5 seconds. The 101-second one ran while the API server was at its largest and
the controller manager leader was on the same node, and the kernel had evicted
etcd's database from the page cache to make room. Every page the compaction
touched was a random read from the vdisk at about 340 µs, and the arithmetic
over a third of the file's 594,730 pages is the minute and a half. The disk's
write path was never the problem: fsync tails on all three members are healthy
and agree.

**The obvious knob is the wrong one, and it was tried and reverted.** Shortening
the API server's `--etcd-compaction-interval` from 5m to 1m makes each
compaction a fifth the size, which bounds a cold-cache stall at seconds. It also
shrinks the history etcd keeps from five-to-ten minutes to one-to-two, because
the API server compacts to the revision it saw one tick earlier. Everything
that resumes from an old revision lives inside that window: an API server whose
own etcd watch was interrupted for longer re-lists every resource type, and a
paginated list whose `continue` token has aged out starts again, unpaginated.
At 16,000 Machines those re-lists are the most expensive event in the system,
and a shorter interval makes them five times easier to trigger. OpenShift does
not touch this interval, and a scale test whose settings would not be defensible
on a production cluster is measuring a cluster nobody would run. So it stays at
five minutes.

**What is defensible is what OpenShift does instead**: leave the store alone and
make everything around it tolerate a slow minute. Each of these is an OpenShift
production default, and each maps onto one link in the chain above:

| OpenShift | kubeadm | the link it breaks |
|---|---|---|
| API server liveness probe `/livez?exclude=etcd` | `/livez` including etcd | the kubelet killed the API server for a slow store |
| `--etcd-healthcheck-timeout=9s`, `--etcd-readycheck-timeout=9s` | 2 s | readiness on every API server went 500 during the stall |
| leader election 137 s lease, 107 s renew, 26 s retry on every controller, built for a 78 s API server outage | 15 s, 10 s, 2 s | the controller manager lost its lease to a 5 s GET |

`clusterclass` applies all three, as a second patch on the copy named
`controlPlaneTolerances`, with OpenShift's values as the defaults. Each knob
empty keeps kubeadm's default, and `config` prints which:

| knob | default | what it sets |
|---|---|---|
| `APISERVER_ETCD_CHECK_TIMEOUT` | `9s` | `--etcd-healthcheck-timeout` and `--etcd-readycheck-timeout` on the API server |
| `APISERVER_LIVEZ_EXCLUDE_ETCD` | `true` | the API server's liveness probe path, `/livez?exclude=etcd` |
| `LEADER_ELECT_LEASE_DURATION`, `LEADER_ELECT_RENEW_DEADLINE`, `LEADER_ELECT_RETRY_PERIOD` | `137s`, `107s`, `26s` | the three `--leader-elect-*` flags on kube-controller-manager and kube-scheduler, all or none |

The timeouts and the leader election flags are new argument names, so they
append through the ClusterClass copy exactly as the etcd quota does. The probe
path is not an argument: kubeadm generates the probes and exposes no knob for
them, but it does apply **patch files** to the static pod manifests it writes,
from the directory named by `initConfiguration.patches.directory` and
`joinConfiguration.patches.directory`. CAREN's class already names
`/etc/kubernetes/patches` in both and writes two `kubeletconfiguration` patches
there, so the liveness path is one more file appended to `files`:

```yaml
# /etc/kubernetes/patches/kube-apiserver1+strategic.yaml
spec:
  containers:
    - name: kube-apiserver
      livenessProbe:
        httpGet:
          path: /livez?exclude=etcd
```

A strategic merge patch against the Pod kubeadm generates: containers merge by
name, so only the path changes and the host, port and scheme stay as kubeadm
wrote them. The file name is what kubeadm matches on — target, an alphanumeric
suffix, the patch type — and files apply in alphanumeric order. No post-kubeadm
command: a `sed` over the manifest after kubeadm has written it restarts the API
server on every fresh node and is invisible to `kubeadm upgrade`, which
re-applies patches from the directory.

The directory is **read from the control plane template**, not assumed. Naming
a different one would silently drop every patch CAREN writes to its own, which
is how a kubelet comes up without the hardening it was given. Only a template
that names none gets `/etc/kubernetes/patches`, and then both `initConfiguration`
and `joinConfiguration` are told, since every control plane node after the
first joins.

The memory ceiling on the API server that would have kept the database in cache
is the root fix, and is a separate question.

Check all three took, on every node, because the roll picks them up one at a
time:

```sh
export KUBECONFIG=../../bin/capi-scale.kubeconfig
kubectl -n kube-system get pod -l tier=control-plane \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].livenessProbe.httpGet.path}{"\t"}{.spec.containers[0].command}{"\n"}{end}' \
  | tr ' ' '\n' | grep -E '^(kube-|etcd-)|livez|etcd-(health|ready)check|leader-elect'
```

**Why an append works when the profiling patch did not.** An argument
*appended* — `add` at index `-`, the one array index the patch validator
permits — under a name CAREN's class does not set is not caught by the
"extraArgs name must be unique" refusal. And a patch placed last in
`spec.patches` renders after CAREN's runtime extension and lands on the list
that extension produced instead of replacing it. All three of the profiling
attempts below were trying to *change* an argument CAREN already sets, which is
a different problem and is still not possible from here.

To see what a compaction costs on a given cluster, etcd logs every one with its
duration. Seconds is healthy; anything past ten is the cache going cold under
the API server, and the node's memory is the thing to look at:

```sh
kubectl -n kube-system logs etcd-<leader> | grep 'finished scheduled compaction' \
  | jq -r '"\(.ts)  took=\(.took)  in-use=\(."current-db-size-in-use")"' | tail
```

## etcd on a disk of its own

CAREN's template puts `/var/lib/etcd` on the root disk. So does the API
server's audit log, which CAREN turns on, and the container logs, and under a
burst of 500 clusters the audit log alone is tens of thousands of records on
the same vdisk etcd is fsyncing its WAL to. The leader measured during that
burst showed a clean fsync tail, so this is not the proven cause of any stall
here. It is etcd's own guidance, it is what every production control plane
does, and a store with its own disk is one fewer thing a rung can fail on for a
reason that is not the fleet.

`clusterclass` adds it as a third patch on the copy, `etcdDisk`. `ETCD_DISK_SIZE`
is the size, `32Gi` by default and empty to leave etcd on the root disk, which
is what the recorded runs used. The size is derived: the backend file at its
8 GiB quota, a second copy of it while `defrag` rewrites the file beside the
old one before swapping, the WAL segments and snapshots at under a gigabyte,
and room. Below about 17 GiB a defragmentation between rungs can fail with the
disk full, which would present as the quota. The vdisk is thin-provisioned, so
the figure is a ceiling rather than a cost. `ETCD_DISK_DEVICE` is the name the guest gives it, `/dev/sdb`; `ETCD_DISK_STORAGE_CONTAINER` is the container to create it in,
defaulting to `NUTANIX_STORAGE_CONTAINER_NAME` from the environment and empty
to leave the choice to Prism. `config` prints all of it.

Two halves, because the disk is attached by one provider and used by another:

- **CAPX attaches it**, as a `dataDisks` entry on the control plane's
  `NutanixMachineTemplate`: SCSI, device index 1, which is the slot after the
  system disk. This needs CAPX v1.5 or later.
- **cloud-init partitions, formats and mounts it before kubeadm runs**, through
  `diskSetup` and `mounts` on the kubeadm config: one GPT partition, ext4 with
  the label `etcd`, mounted by that label on `/var/lib/etcddisk`
  (`ETCD_DISK_MOUNT`). By label rather than by device, because nothing
  guarantees a device name, and `nofail` so a missing disk cannot hang the boot.
- **kubeadm is pointed at a subdirectory of the mount**, `etcd.local.dataDir:
  /var/lib/etcddisk/etcd`, rather than the disk being mounted on `/var/lib/etcd`
  itself. The first attempt did the latter and every new node failed preflight
  with `DirAvailable--var-lib-etcd: /var/lib/etcd is not empty`: `mke2fs` puts a
  `lost+found` directory on every filesystem it makes, and kubeadm refuses a
  data directory with anything in it. A subdirectory does not exist until
  kubeadm creates it, so the check passes and nothing has to be ignored. This is
  the arrangement Cluster API's Azure provider documents for its etcd disk.

`nofail` is what makes the third piece necessary. A node whose disk did not
mount would boot, run kubeadm, and put etcd on the root disk without a word,
and the run would measure a store it thought was on its own disk. So a
`preKubeadmCommands` guard checks the mount and refuses to run kubeadm without
it. The failure is then a Machine that never becomes ready, which is loud and
is the right way round.

Check it after the roll, on every control plane node:

```sh
export KUBECONFIG=../../bin/capi-scale.kubeconfig
for n in $(kubectl get nodes -l node-role.kubernetes.io/control-plane -o name); do
  kubectl debug "$n" -it --profile=sysadmin --image=busybox -- chroot /host sh -c \
    'hostname; findmnt /var/lib/etcddisk -o TARGET,SOURCE,FSTYPE,OPTIONS; lsblk -o NAME,SIZE,LABEL,MOUNTPOINT; ls /var/lib/etcddisk/etcd'
done
kubectl -n kube-system get pod -l component=etcd \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].command}{"\n"}{end}' | tr ' ' '\n' | grep -E 'etcd-|data-dir'
```

`SOURCE` should be the data disk's partition with `LABEL=etcd` on it, the
`etcd` directory under the mount should hold `member/`, and every member's
`--data-dir` should be `/var/lib/etcddisk/etcd`. If the mount is missing, the
guard did not fire and the manifest that reached the node is not the one this
step wrote.

## Debugging a control plane node that never joined

A ClusterClass change rolls the control plane, and a new machine that never
becomes Ready is the failure the etcd disk guard is designed to produce. Read it
from the outside first, from the bootstrap cluster:

```sh
export KUBECONFIG=../../bin/capi-scale-bootstrap.kubeconfig
kubectl get machines -o wide                                  # which one is stuck, and in which phase
kubectl describe machine <name> | sed -n '/Conditions/,/Events/p'
kubectl get kubeadmconfig -o wide                             # bootstrap data ready, or not
kubectl get nutanixmachine <name> -o jsonpath='{.status}' | jq   # the VM exists, and has its disks
```

A Machine in `Provisioned` with a `KubeadmConfig` whose data is ready and no
Node is a VM that booted and did not run kubeadm to completion. That is a
cloud-init question, and it needs a login on the node, which is what the SSH
key above is for. With one:

```sh
ssh capiuser@<node ip>
sudo cloud-init status --long                  # done, error, or still running, and which stage
sudo tail -50 /var/log/cloud-init-output.log   # the guard's own message is here if it fired
sudo grep -iE 'error|fail|warn' /var/log/cloud-init.log | tail -30
lsblk -o NAME,SIZE,TYPE,LABEL,MOUNTPOINT       # is the data disk there, and under which name
findmnt /var/lib/etcddisk -o TARGET,SOURCE     # is the disk mounted where kubeadm was told etcd lives
sudo journalctl -u kubelet --no-pager | tail -30
```

What the disk change can fail on, in the order to check:

- **The disk came up under a different name.** `lsblk` shows it; if it is not
  `/dev/sdb`, set `ETCD_DISK_DEVICE` and re-run `clusterclass`. The guard fired
  because `disk_setup` and `mounts` were told the wrong device, and its message
  is the last line of `cloud-init-output.log`.
- **The disk is not attached at all.** `nutanixmachine` status and the VM in
  Prism show one disk. CAPX before v1.5 ignores `dataDisks`; the bootstrap
  cluster's CAPX version is in `clusterctl describe cluster` and `config`.
- **cloud-init failed before the mount.** `cloud-init status --long` names the
  module; `disk_setup` refusing an already-partitioned device is the usual one
  on a reused disk, and `overwrite: false` is deliberate.
- **kubeadm's preflight refused the data directory.** `DirAvailable--var-lib-etcd:
  /var/lib/etcd is not empty` in `cloud-init-output.log`, with `lost+found` as
  the only thing in it, is a disk mounted on the data directory itself. The
  patch mounts one level up for exactly this reason; a class copy made before
  it did not, and needs `clusterclass` re-run.

A node without a login cannot be read this way, and the console in Prism has no
password to offer. Delete the stuck Machine on the bootstrap cluster and the
KubeadmControlPlane replaces it with one built from the current spec, key
included:

```sh
kubectl delete machine <name>
```

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

## One member reclaiming nothing is usually the gauge, not the store

A run reporting this is reporting a measurement defect, not a finding:

```
fw9n7 reclaimed 0 B (2.0 GiB to 2.0 GiB); nl882 reclaimed 850.3 MiB (2.0 GiB to
1.2 GiB); w4pp6 reclaimed 850.5 MiB (2.0 GiB to 1.2 GiB)
```

Three members of one raft cluster hold the same data and free the same pages
under the same compaction. One of them having nothing to reclaim while its peers
shed 850 MiB is not something the store can do — and `before` and `after` are
*exactly* equal, which a real defragmentation never leaves, because a rewritten
file differs by at least a page.

`etcd_mvcc_db_total_size_in_bytes` is refreshed when the backend commits, not
when a file is rewritten. A member that happens to be quiet in the moment after
its defragmentation keeps publishing its old size. Which member that catches is
timing, which is why it moved between members and between runs.

The reading is now waited on rather than taken once: after defragmenting, the
member is polled until its allocated size and its data have converged, or thirty
seconds pass. **Only the read is retried, never the defragmentation** — a second
rewrite of an already-compact file costs a stop-the-world pause for no gain, and
this runs between every pair of rungs.

Convergence rather than movement is the test on purpose. A member that restarted
and took a snapshot from the leader has a compact file already, so reclaiming
nothing from it is correct, and waiting for its number to move would wait
forever. When the file never converges the line says so, with the free space
that proves it:

```
fw9n7 reclaimed 0 B (2.0 GiB to 2.0 GiB) — **the size did not settle**: 850.3
MiB of the file is still free after defragmenting, so this reading is the gauge
lagging rather than the store refusing to shrink
```

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

### One ClusterClass per API server, which is not one per side

Stock puts a single ClusterClass in `capi-scale-blueprint` and every Cluster in
the fleet references it across namespaces — `Topology.ClassRef` carries a
`Namespace`, and no feature gate beyond `ClusterTopology` is needed for it. kcp
puts one in each workspace, because a workspace is an isolation boundary and a
class in one is not visible in another.

So at 3,000 clusters with `CLUSTERS_PER_NAMESPACE=10` the stock side has one
ClusterClass and the kcp side has three hundred.

**That asymmetry is the arrangement, not a flaw in it.** Both sides run what an
operator would actually run, and the duplication on the kcp side is what tenant
API isolation costs. Forcing an equal number of classes onto both — which the
harness used to do, on the mistaken belief that a Cluster could only name a class
in its own namespace — hid exactly the difference the comparison exists to find.

It also removes a race. The blueprint used to be created per namespace and the
Clusters immediately after, so the first Clusters in each of three hundred
namespaces were admitted before their class had been reconciled:

```
Cluster refers to ClusterClass capi-scale-0001/demo, but this ClusterClass hasn't
been successfully reconciled. Cluster topology has not been fully validated.
```

Nothing broke — the Cluster is admitted and reconciled again once the class is
ready — but those Clusters did less admission work than their neighbours, and
admission work is what this run now measures. `WaitForBlueprint` waits for
`status.observedGeneration` to catch up, and for `VariablesReady` when the class
reports it, before any Cluster is created. On the stock side that is one wait for
the whole fleet; on the kcp side one per workspace, which is the same
duplication in a different currency.

One shared class does mean a class that will not reconcile stops the whole fleet
rather than one tenant. That is the right way round: a run cannot measure
admission cost against a class the server will not validate against.

### The managers keep Cluster API's own client rate limit

`capiscale-prepare` defaults to `-kube-api-qps=100 -kube-api-burst=200`, which is
what Cluster API ships.

They were raised to 500/1000 for a while, on a principle that still holds: a
ceiling found at a client rate limit is a fact about the flag rather than about
the machine. What it produced was five times the write rate onto a store that
could not absorb it. etcd began timing out lease renewals, managers exited with
`leader election lost`, and no rung finished — so the run stopped measuring
anything at all, which is worse than measuring a throttled manager.

At 100/200 the same 500-cluster rung converged, in 10m52s against the 4m52s it
took before it fell over. Slower and finished beats faster and aborted.

Raise it once a run reaches a ceiling with the managers visibly throttling and
nothing else giving way, and record that the run was taken with it raised — that
is a different measurement and worth having, but it is the second one.

Note that raising it and the leader-election FlowSchema are a pair. The schema
keeps a manager's heartbeat out of the queue its own bulk traffic fills; it does
nothing about the volume of that traffic reaching etcd.

### The KubeadmControlPlane webhooks are the stock ceiling, and they kill their own manager

Measured on this cluster, from the API server's own admission metrics:

| KubeadmControlPlane UPDATE | calls | total | per call |
|---|--:|--:|--:|
| `default.kubeadmcontrolplane` | 58,889 | 4,163s | **70.7ms** |
| `validation.kubeadmcontrolplane` | 58,881 | 4,031s | **68.5ms** |

About **139ms of admission on every KubeadmControlPlane status update**, and
~187ms on a create. A webhook call should be single-digit milliseconds. Both are
served by the KCP manager itself, and both figures are lifetime means, so during
a failing rung they are worse than this.

That closes a loop which is structural in stock Cluster API rather than
particular to any cluster:

```
KCP controller probes N workload clusters for control-plane and etcd health
  → writes N KubeadmControlPlane status updates
    → each costs ~139ms through two webhooks served by the KCP manager
      → its webhook server queues
        → healthz, which is a TLS dial to that same server, misses its 1s timeout
          → three misses and the kubelet kills the manager
```

More clusters means more probes means more status writes means more admission
load on the process doing the probing. It is also why control-plane readiness
flaps: the same queue delays the status updates a rung is counting.

`capiscale-prepare` therefore gives the managers' health checks
`-probe-timeout-seconds=5` and `-probe-failure-threshold=5`. Only raised, never
lowered, and no probe is invented for a container that has none.

**This is not tuning the result.** The stock check is not a ping — Cluster API
wires healthz to controller-runtime's webhook `StartedChecker`, which opens a
TLS connection to the manager's own webhook server — so a one-second timeout
means a manager is killed for being busy with its own webhooks. Nothing here
lets the cluster hold more clusters; it lets a run reach a ceiling instead of
ending 81 seconds into a rung. The slow webhooks stay slow and stay measured.
Both sides of the comparison get it, or the side that keeps its managers is
being compared against the side that loses them.

To re-measure after a run:

```sh
kubectl get --raw /metrics |
  grep -E 'apiserver_admission_webhook_admission_duration_seconds_(sum|count)\{name="(default|validation)\.kubeadmcontrolplane'
```

`sum / count` per webhook. A mean that climbs between rungs is the ceiling
arriving.

### Lease renewals are given their own priority level

`capiscale-prepare` installs a `FlowSchema` named `capi-leader-election` that
routes the managers' `coordination.k8s.io/leases` writes to the API server's
built-in `leader-election` priority level.

Without it those renewals queue behind the same manager's Machine and DevMachine
writes. A renewal is a few hundred bytes every few seconds and it fails anyway,
because the queue it is in does not drain at a fleet of thousands. That is how a
manager comes to exit with `leader election lost` while nothing has run out.

The API server already ships the level for this. What it does not ship is a
schema pointing Cluster API at it: the built-in leader-election schemas name
specific system components and `kube-system` service accounts, and the managers
live in `capi-system`, `capd-system` and their siblings. Check where a renewal
actually lands — the API server names the schema and level in response headers:

```sh
kubectl -n capi-system get lease controller-leader-election-capi -v=8 2>&1 |
  grep -i 'X-Kubernetes-PF'
```

**This is not tuning the result.** Isolating a heartbeat does not let the cluster
hold more clusters; it stops a manager exiting for a reason unrelated to
capacity, which was aborting runs before they reached a ceiling. Giving the
managers' *bulk* traffic a level of its own would be different — that makes
Cluster API hold more by making it slower, and belongs in a separate experiment
rather than in the defaults. Both sides of the comparison get the schema, or the
side that keeps its leaders is being compared against a side that loses them.

The schema is applied after the client-limit patching, and the pairing is the
point: raising a manager's client rate lets it saturate the API server, and the
schema is what keeps its own heartbeat out of the queue it just filled.

If the run reports `shedding load: N request(s) rejected by priority and
fairness`, flow control is doing its job — a 429 with `Retry-After` is a client
backing off, which is a better outcome than the timeouts it replaces.

The managers' own leader-election window is the same one the ClusterClass gives
`kube-controller-manager` and `kube-scheduler`: `capiscale-prepare` defaults to
OpenShift's 137 s lease, 107 s renew deadline and 26 s retry period
(`-leader-elect-lease-duration`, `-leader-elect-renew-deadline`,
`-leader-elect-retry-period`). It used to give them a minute, 40 s and 5 s, and
the bootstrap manager lost its lease during a 40 s store stall at 2000 clusters
while the control plane's own components, on the longer window, did not. Every
leader-elected process on the cluster now tolerates the same pause, so a failure
line can be read without first asking which fuse was shortest.

### The managers are kept off the control plane nodes

A fresh cluster held 1000 clusters and lost the VIP at 1500. kube-vip on
`skv5v` could not renew its lease because that node's etcd member had stalled
for about eighty seconds on a compaction its two peers finished in three and
ten seconds; the next compaction on the same member took **1m11s** against 9 s
on the leader. Same disk type, same file, same minute. The difference was what
else was on the node: the DevCluster provider, a 24 GiB Guaranteed pod, sitting
beside a kube-apiserver already at 20 GiB resident on 32 GiB. With etcd,
Cilium and the kubelet, about 3 GiB was left for the page cache, and etcd's
2 GB backend file did not fit — so compaction read it from disk while the apply
loop waited, and every write through that member, kube-vip's lease renewal on
localhost included, timed out.

The provider was there for a reason the scheduler cannot see. clusterctl's
manifests tolerate `node-role.kubernetes.io/control-plane` so a provider can run
on a single-node bootstrap cluster, and kubeadm gives kube-apiserver a CPU
request and no memory request, so a control plane node holding a 20 GiB API
server looks like the emptiest node in the cluster.

`capiscale-prepare` now puts a required node affinity away from the control
plane label on all four managers (`KeepOffControlPlane`). Affinity rather than
removing the toleration, because the toleration only matters while the taint
is there and the affinity says what is meant either way. It is not tuning: a
management cluster's controllers do not belong on its control plane nodes, and
no production layout puts them there.

The report also warns when it finds one there anyway, with a
`managersOnControlPlane@` fact at every sample, because the samples always
carried the node name and nobody read `capi-scale-vtgjn-skv5v` as a control
plane node until the member on it had stalled twice.

### A control plane shedding itself is the ceiling, not a caveat

A rung at 1500 clusters ended with `kube-controller-manager` exiting 1, and the
report called it `restarted 1 time(s) — exited 1 of its own accord`. That reads
as a process blip. Its own log says what it was:

```
"Shutting down controller" controller="garbagecollector"
"Shutting down node controller"
"Shutting down controller" controller="deployment"
"Shutting down endpoint controller"
"Shutting down controller" controller="taint-eviction-controller"
"Shutting down persistent volume controller"
... twenty more
```

For as long as that lasts the cluster has no garbage collection, no node
lifecycle management, no deployment or endpoint reconciliation and no taint
eviction — with a fleet still running on it. That is a management cluster that
has stopped being able to manage itself, and it is a **stronger** result than a
convergence timeout, not a weaker one.

There was a wrong turn here worth recording. The Nutanix cloud controller
manager is excluded from failing a rung because it is an addon that happens to
run on control-plane nodes, and the first instinct was to extend that to
`kube-controller-manager` and `kube-scheduler` on the grounds that they too have
short fuses and are noticed first. They are not the same thing. The CCM runs
*beside* the control plane; these two **are** it.

Nor is a five-second lease timeout over-sensitivity. A lease that cannot be
renewed makes its holder step aside rather than act on state it can no longer
confirm — the mechanism firing is the mechanism working, and a cluster where it
fires repeatedly is one that is not safely operable however many Clusters are
Ready.

So `SteppedDown` classifies it as what it is, ahead of the generic restart line,
and names what the cluster went without. A Cluster API manager exiting 1 keeps
the ordinary wording: it is not a static pod, and losing it stops reconciliation
rather than the cluster's ability to run pods. An OOM kill still outranks
everything, because a component killed for memory wants a memory answer.

The mechanism joins up with the garbage collector. Cluster API's object graph
gives the GC an informer per resource and owner references across a hundred
thousand objects, and it loses write conflicts continuously against the
KubeadmControlPlanes that Cluster API is updating at ~139ms of admission apiece.
Cluster API's cost to `kube-controller-manager` is not incidental — it is the
mechanism by which the control plane fails.

### A manager that died in a previous run is not this run's ceiling

The control plane was given a restart baseline early, because a kubeadm static
pod restarted at any point in a node's life would otherwise fail the first rung.
The managers were left reading their raw restart count, and the same thing
happened to them a day later.

`capi-controller-manager` lost its leader election during a 2000-cluster rung.
From then on **every rung of every subsequent run aborted within seconds**,
naming that manager as having died — a restart from the previous day, presented
as this run's ceiling, complete with a throttling figure exonerating a process
that had never been in trouble:

```
This run measured nothing: the smallest fleet it tried did not converge
(capi-controller-manager restarted 1 time(s) — exited 1 of its own accord ... —
1.7% of CFS periods throttled, so it was not short of CPU)
```

Two runs measured nothing on that basis, and both were read as the cluster
getting worse. It gave itself away in a timestamp: `kubectl logs --previous` on
the manager returned a log from the day before, because the process had never
restarted again.

`ManagersSince` now diffs against a baseline taken before the climb, exactly as
`HealthSince` does for the control plane, and the baseline note says what the
managers had already been through. The restart count is not the only thing
carried forward — `OOMKilled` and the last termination travel with a restart
that has already been counted, so a manager killed for memory yesterday would
otherwise keep announcing it today.

A manager only restarts on its own account, so its baseline is nearly always
zero and this looks like it can never matter. It matters exactly once, and then
it matters for every run afterwards until somebody rolls the deployment.

### And neither is one the report *prints*, which took a third go to get right

The same bug had a third home, and it survived both fixes because it was in a
sentence rather than in a decision. `ControlPlaneReadout.Describe` had the
restart counts to hand — it holds a sample of every pod on the control plane's
nodes — and printed them under the heading `**restarted during this run**`. A
scrape only knows a pod's whole history, so the heading was never true.

The report of the run that reached 1000 clusters carried this at its
**baseline**, before a single cluster existed:

```
| controlPlane@baseline (no clusters) | ... — **restarted during this run**:
kube-apiserver-capi-scale-pddjz-fw9n7 x3, kube-apiserver-capi-scale-pddjz-w4pp6 x4, ...
```

Three of those were ours, done by hand the previous evening to measure a fresh
API server; the rest were older still. Nothing had restarted during that run.

The abort path was right the whole time, because it went through `HealthSince`.
Only the line a reader actually reads was wrong — which is the worse half to get
wrong, since nobody re-derives a ceiling from a table when a line above it has
already named the process that died.

The fix is the one the other two got. `RestartsSince` rebases a whole set of
samples against a baseline taken before the climb, keeping the samples (they
carry the run's memory figures) and zeroing the restart history of anything that
has not restarted since — the count, the `OOMKilled` flag and the last
termination together, because those travel with a restart that has already been
counted. `ManagersSince` is now that function narrowed to what restarted, and
`Restarted` writes the clause from the rebased samples in the runner, which is
the only place that knows what the run started from. The readout no longer
mentions restarts at all.

The rebased samples are what the report keeps, so its own
`A container restarted during this run` banner stops naming yesterday's
restarts too.

The general shape, now three for three: **on a long-lived cluster every counter
you did not baseline is mostly somebody else's run.** It applied to etcd's
counters, to the managers, to the control plane's pods, and to the sentence
describing them.

### Restart the API servers before a measured run, or the baseline is the last one

Measured on this cluster, both readings against an API with **no Clusters, no
Machines and no events left in it**:

| | served 1500 clusters yesterday | restarted | ratio |
|---|--:|--:|--:|
| `heap_alloc` (live) | 4.91 GiB | **216 MiB** | 23x |
| `heap_inuse` | 8.69 GiB | 256 MiB | 34x |
| `sys` | 26.1 GiB | 345 MiB | 78x |

Same cluster, same CRDs installed, same nothing to serve. So having Cluster
API's CRDs installed costs about **200 MiB**, not 5 GiB, and the rest was one
fleet's history — live heap, still referenced, after the objects were deleted
and their events expired.

That contaminated every absolute memory figure recorded before it. A run
reported the control plane "82% full at 1500 clusters" and the node was
concluded to be the binding constraint; most of that fullness was a fleet that
no longer existed. Slopes taken as differences between rungs probably survive.
Levels do not.

**`kubectl delete pod` does not restart a static pod.** It deletes the mirror
pod, and the kubelet re-creates the API object from the manifest on disk while
leaving the process alone — `wait --for=condition=Ready` then passes instantly
against something that never went away. `sys_bytes` byte-identical across a
"restart" is the tell. Kill the container instead and let the kubelet rebuild it
from the untouched manifest:

```sh
kubectl debug node/<control-plane-node> -it --profile=sysadmin --image=busybox -- \
  chroot /host sh -c 'crictl stop $(crictl ps --name kube-apiserver -q)'
```

One node at a time; the other two hold the VIP. Nothing under
`/etc/kubernetes/manifests/` is touched, so there is no way to leave a node
whose API server never comes back — which is the failure the ClusterClass
attempt hit.

Then confirm it is genuinely a new process before trusting anything:

```sh
kubectl -n kube-system get pod kube-apiserver-<node> \
  -o jsonpath='{.status.containerStatuses[0].state.running.startedAt}{"\n"}'
```

The run cannot do this for itself — restarting an API server needs the
container runtime on the node, which is not something a measurement should
reach for. What it does instead is refuse to present the number quietly:
`Inherited` reads `process_start_time_seconds` from every sampled process and
the baseline carries a warning naming any that were already running, with how
long for. See `internal/upstreamscale/inherited.go`.

### Live data or memory nobody handed back — the API server line says which

A run reported an API server at **23.5 GiB resident against 9.7 GiB of heap**,
on a 32 GiB node, holding 104,556 objects. That is about 97 KiB of heap per
stored object, against a Cluster API object that serialises to perhaps 5-15 KB.

The obvious question — how much of that is live data and how much is memory the
runtime has finished with and not returned — could not be answered from the
report. The heap figure is the lowest of several reads with no collection
forced, so it is an upper bound and nothing more, and profiling (which would
settle it) cannot be turned on here.

The Go runtime publishes the decomposition on `/metrics`, needing no profiling
and no forced collection, so the line now carries it:

| gauge | meaning |
|---|---|
| `go_memstats_heap_inuse_bytes` | spans holding at least one live object |
| `go_memstats_heap_idle_bytes` | spans holding nothing |
| `go_memstats_heap_released_bytes` | of those, already returned to the OS |
| `go_memstats_next_gc_bytes` | the heap size the next collection triggers at |

`idle − released` is memory the process is holding and not using. The two
readings want opposite responses:

- **Mostly in use** → the objects genuinely cost this much, and the answer is
  bigger control-plane nodes. If that holds, it is also the finding `sizing.md`
  asks for: an ordinary kube-apiserver serving Cluster API's CRDs as
  unstructured objects is expensive, and that applies to every management
  cluster anyone runs.
- **Mostly retained** → the runtime has no reason to be frugal, because kubeadm
  gives `kube-apiserver` a CPU request and nothing else: no memory limit, no
  cgroup ceiling, no `GOMEMLIMIT`. It has no idea it is on a 32 GiB node shared
  with etcd, the scheduler, the controller manager and Cilium.

For the second case, `GOMEMLIMIT` in the static pod's env is the lever — a soft
limit that makes the collector work harder as memory approaches it and returns
pages more eagerly, rather than the hard wall a cgroup limit gives. Two rules:
pair it with a cgroup memory limit set **above** it, never below (a cgroup limit
alone turns a slow collection into a `137`); and expect it in the CPU column,
because a GC-bound API server answers `/livez` more slowly, which is the failure
this whole exercise keeps meeting.

`next_gc_bytes` is there because it shows what GOGC resolves to on this process
rather than what somebody thinks it was set to.

Restarting the API servers is a **measurement** step, not an operational one: it
gives a clean baseline for a cluster that has been through days of runs. A
production API server needing periodic restarts would be a bug worth reporting,
not a runbook entry.

### Every rung's etcd line carries what changed, not just what is

A rung reports the store's state — size, keys, mean latencies, leader — and then
what happened to it since the defragmentation before that rung: leader changes,
slow applies, failed proposals, syncs past 128ms, and the commit and fsync means
*over the interval* rather than over the member's life.

Both halves, because the state half is not enough and a run proved it. A report
showing `wal fsync 1.7ms, commit 3.5ms` unchanged across 500, 1000, 1500 and
2000 clusters was read as "etcd was never the problem" — while the managers were
dying with:

```
"Failed to update lease optimistically, falling back to slow path"
  err="etcdserver: request timed out"
```

A failed proposal is etcd's own word for a write it could not commit. Over three
and a half million syncs a stall does not move a lifetime mean, so a line
carrying only the mean invites exactly that misreading. `EtcdSince` in
`internal/upstreamscale/etcdstrain.go` computes the differences; the rung line
appends them whenever there is something to say.

The baseline those differences are taken against is retaken after **every**
defragmentation, not once at the start. A defragmentation is strain of its own —
a rewrite of the backend file, during which the member answers nothing — and
the first version of this line charged it to the rung that followed: the 2500
rung's failure line carried slow applies and a leader change that the
2000-to-2500 defragmentation had caused, and read as though the climb had done
it. The defragmentation line itself now says how long each member took and
whether it was the raft leader at the time, because those two together are the
size of the perturbation. On a follower a two-minute rewrite is one member out
for two minutes; on the leader it is every write in the cluster waiting.

```
defragmented between rungs: etcd-...-fl8pl reclaimed 850.3 MiB (2.0 GiB to 1.2 GiB) in 4s;
etcd-...-thnhv reclaimed 850.5 MiB (2.0 GiB to 1.2 GiB) in 1m52s as the leader; ...
```

### Each rung records where the leader, the leases and the VIP sit

A control plane node died under the 2000-cluster fleet, and establishing what
had been on it took three commands by hand afterwards: it had held the raft
leader, `kube-controller-manager`'s lease and kube-vip's VIP at once, so one
node's loss took the API endpoint, the garbage collector and the store's leader
in a single event. By then everything had re-elected and the evidence was gone.

Every sample now carries a `placement@` fact:

```
etcd leader etcd-capi-scale-pddjz-thnhv on capi-scale-pddjz-thnhv;
kube-controller-manager lease on capi-scale-pddjz-thnhv;
kube-scheduler lease on capi-scale-pddjz-br7vn;
kube-vip (plndr-cp-lock) on capi-scale-pddjz-thnhv
— the raft leader, the controller manager and the VIP share capi-scale-pddjz-thnhv, ...
```

It is a record, not a verdict. Leader election puts these wherever it puts them
and a production cluster is no different; the point is that a rung which met a
node failure can say what that node was holding, rather than the next reader
having to guess. The etcd leader comes from `etcd_server_is_leader` on each
member, the rest from the leases in `kube-system`. kube-vip's is `plndr-cp-lock`,
the default name CAREN's template leaves it at.

### kcp cannot split its store, so a split stock store is a separate experiment

Moving leases and events to their own etcd is the obvious answer to a lease that
cannot be committed:

```
--etcd-servers-overrides=coordination.k8s.io/leases#https://lease-etcd:2379,/events#https://events-etcd:2379
```

kcp has no equivalent. Its entire storage surface is one flag:

```
$ kcp start --help | grep -i etcd
      --etcd-servers strings   List of etcd servers to connect with ...
```

No overrides, no per-resource routing — "storage" and "override" appear nowhere
else in its help.

kcp's own answer to store pressure is sharding: many shards, each with its own
store, rather than one store split by resource. That is a real answer, and it is
**not what this harness measures**. It runs one shard with three replicas
against one external etcd, deliberately, to mirror three kube-apiservers behind
a VIP — so today both sides put the whole fleet through a single store.

So a stock run with split stores is not comparable with the kcp runs as
configured, and must be recorded as its own result: *what a tuned control plane
holds*, beside *what a stock one holds*. Comparing split-store stock against
multi-shard kcp would be the fair version of that experiment, and it needs
multi-shard support here first.

### When the store is the ceiling, the managers report it as their own death

The manager log to recognise:

```
leaderelection.go:454 "Failed to update lease optimistically, falling back to
slow path" err="etcdserver: request timed out"
leaderelection.go:461 "Error retrieving lease lock" err="... context deadline exceeded"
leaderelection.go:304 "Failed to renew lease" err="context deadline exceeded"
main.go:415 "Problem running manager" err="leader election lost"
```

A leader lease is the smallest write a cluster makes. `etcdserver: request timed
out` on one is the store failing to commit, and the manager exiting is a
consequence rather than the fault. Raising `-leader-elect-renew-deadline`
further does not fix it — a manager that already tolerated half a minute of an
unavailable store and still lost the lease is not short of patience.

It is also why an API server gets killed by its own kubelet: `/livez` cannot be
answered while etcd is not committing. Both failures on one cluster, minutes
apart, are one failure.

The run now says so. `EtcdSince` in `internal/upstreamscale/etcdstrain.go`
diffs every member's counters against a baseline taken after the opening
defragmentation, and any rung failure carries the result: leader changes and
slow applies during the run, the backend-commit and WAL-fsync means over the run
rather than over the member's life, whether a member restarted, and whether the
backend is approaching the quota. No thresholds are invented — a leader change
and a slow apply are etcd's own judgements, and the latencies are printed beside
their baselines rather than being called bad.

If a run reports this, the fleet is not the thing to change. Check the disk
first: etcd's fsync latency is what `sizing.md` warns turns a scale test into a
latency test.

**Do not check it with the mean.** The member serving the cluster whose managers
were losing their leases reported this:

```
etcd_disk_wal_fsync_duration_seconds_sum        5647.228963687338
etcd_disk_wal_fsync_duration_seconds_count      3514308
etcd_disk_backend_commit_duration_seconds_sum   2604.4815442640124
etcd_disk_backend_commit_duration_seconds_count 906789
```

1.61ms and 2.87ms — a healthy disk. Both are lifetime figures over millions of
syncs and days of uptime, in which a minute of stalled writes does not reach the
third decimal place. They say the disk is not *chronically* slow and cannot say
whether it stalled, and a stall is what stops a fleet. The run reads the
histogram tail for that reason, and reports syncs past etcd's own 128ms bucket
boundary alongside `etcd_server_proposals_failed_total` — the direct counter for
what a client sees as `etcdserver: request timed out`.

By hand, per member, the same question:

```sh
curl -s localhost:2381/metrics | grep -E \
  'fsync_duration_seconds_bucket|commit_duration_seconds_bucket|proposals_(failed|pending)|leader_changes|slow_apply|peer_round_trip'
```

A commit timing out with a local disk this fast points away from the disk: at
another member's disk, at peer round-trip time between members, or at sheer
proposal volume. `etcd_network_peer_round_trip_time_seconds` is the one of those
the run does not sample, so check it here.

### A rung refused at its first Cluster is not a ceiling

The rejection to know by sight:

```
admission webhook "default.cluster.cluster.x-k8s.io" denied the request:
Internal error occurred: Cluster c0120 can't be defaulted. ClusterClass demo can
not be retrieved: Timeout: failed waiting for *v1beta2.ClusterClass Informer to
sync
```

That is Cluster API's own defaulting webhook saying its cache is not ready: the
manager could not list ClusterClasses inside the informer's sync timeout. It is
about the manager's readiness and not about how much the management cluster can
hold, so a rung abandoned on it records a ceiling that does not exist.

It shows up at the top of a rung because that is where the disturbance is. A
defragmentation runs between rungs and is a stop-the-world rewrite per member;
a member being rewritten can drop watches, the managers re-list, and the first
Cluster of the next rung arrives while they are still doing it. A manager that
restarted for any other reason produces the same thing.

The run now rides this out — `Transient` in `internal/upstreamscale/create.go`
separates a rejection about the server from a rejection about the object, and
creates are retried for two minutes against the first kind only. Invalid,
Forbidden and Conflict are still immediate failures, and a server that never
becomes ready still stops the climb, with the attempt count and the elapsed time
in the message. A creation failure also now carries whatever the managers and
the control plane were doing at the time, so "a manager restarted" is not left
to be inferred from an admission error.

If you see it anyway, the manager is the place to look, not the fleet:

```sh
kubectl -n capi-system get pods
kubectl -n capi-system logs deploy/capi-controller-manager --previous | tail -40
```

### A death is checked before the count is believed

The 2000-cluster rung was declared converged, and the kubeadm bootstrap manager
had died inside it — at 21:09:10, losing a 40 s lease renewal during a store
stall, eleven minutes before the count reached its target. The remaining
managers finished the fleet, the wait returned on the count without looking at
the pods, and the death surfaced as the **2500** rung's failure thirty-one
seconds in: a rung that had barely started took the blame, and the rung that
killed the process stood in the report as a success with a pace figure.

The poll now asks whether anything died *before* it believes a count that says
done. A fleet that arrived over a dead process did not converge; it was
finished by whatever was left, and that is the failure, at the rung it happened
in.

### A control plane counts only when every replica is ready

The 2000-cluster rung's readiness went backwards on most polls, and the first
explanation written into the harness — Cluster API's per-cluster health probes
timing out — was wrong. Twelve samples in a row of the control planes going
backwards showed the same thing:

```
2/1 Available=NotAvailable,EtcdClusterHealthy=NotHealthy
  ... Etcd member ... does not have a corresponding Machine
```

`KubeadmControlPlane` marks a one-member control plane Available the moment its
first member answers, then withdraws it while the second joins — for the length
of that join the etcd cluster is two members with one Machine. A count of
Available control planes therefore rose at 1 of 3, fell at 2 of 3 and rose again
at 3 of 3 for every cluster in the rung. That is not readiness failing to hold;
it is control planes that had not finished being built, counted too early.

`Converged` now counts a control plane when it is Available **and**
`status.controlPlane.readyReplicas` has reached `desiredReplicas`. A control
plane that reports no replica counts is judged on Available alone, since there
is nothing for it to be at full strength against. What can still register as a
fall is a control plane that had every replica and then lost Available — KCP
withdrawing it because a cluster's etcd or control plane stopped answering, or
during a remediation — and the `Steadiness` line now says that rather than
blaming probes.

### A fleet that all but arrived and stopped is stuck, and the report names what on

With the providers off the control plane nodes, the 2000 rung reached 1999 of
2000 control planes and 19999 of 20000 Machines and sat there for thirty
minutes with every component healthy and the store quiet. The verdict was
"reconciliation did not keep up". Reconciliation kept up for 1999 clusters; one
control plane replica never joined. And the run tore the fleet down at the end,
so which cluster, and what its KubeadmControlPlane said about it, went with it.

Two changes. Every poll now carries the first few unready Clusters and
Machines with Cluster API's own account of each — `Available=False
(NotAvailable: Etcd member 1 does not have a corresponding Machine)`, a
Machine's phase and its Ready condition — and a timeout puts them on the
failure line. And a fleet within half a percent of its target that has not
moved for eight polls is called **stuck** rather than slow, in the failure line
and in the log while the rung is still waiting, so a person can go and look at
the straggler before the step timeout takes it away. Run with `KEEP=true` when
chasing one, and the fleet stays up afterwards.

A stuck object is a different finding from a slow fleet, with a different next
step: not capacity, but one Cluster's conditions, and the question of whether a
control plane MachineHealthCheck, which a production cluster would have, would
have remediated it.

### The inherited-baseline check now covers the API servers

`Inherited` judges a process by its start time, and the control plane's samples
come from cAdvisor, which reports none. So a run whose API servers were never
restarted between runs took its baseline at 54.8 GiB of API server against an
empty API, and the check that exists for exactly this said nothing. An API
server serving nothing costs about half a gigabyte on this cluster; anything in
gigabytes at zero clusters is the fleet before, and `InheritedControlPlane`
now says so by size. The restart recipe is above.

### The soak holds the fleet it is labelled with

Rungs are cumulative: each keeps the fleet below it and adds to it. So when the
2500 rung failed, the cluster was holding 2500 clusters, and the soak that
followed labelled them 2000 — every drift figure and the readiness count at the
end were about a fleet nobody had measured, partly converged and a quarter
larger than the report said.

Before the soak begins the run now tears down the tenants the failed rung
added — the difference between what its `Create` returned and what the last
converged rung's did — waits for them to go, and records a `soakFleet` fact
saying so. If that teardown does not finish, the fact says that instead and the
soak still runs: a soak with a caveat on the line is worth more than none.

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
   side apply dry-run, not on the ClusterClass that caused it. Note what this
   does and does not rule out: the append itself landed, in the same list as
   CAREN's `profiling`, which is why the uniqueness rule saw it. Appending an
   argument CAREN does *not* set works, which is how the OpenShift tolerances
   above can be applied.
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
