# Retrospective: what got the stock scale test here, what it found, and what next

**Written**: 2026-09-07, against branch `claude/scale-test-200-clusters-vfin19`
at `8e1a394`.

**What is being evaluated**: seventy-eight commits over five days, from a
harness built blind to one that has taken stock Cluster API v1.14.1 to 1600
clusters and 16,000 Machines on one management cluster and held them, and has
then been pushed to 2000 clusters, where the store stopped committing lease
renewals. The committed evidence stops at 1600 (`evidence/README.md`); the
runs above it are recorded in commit messages and in
`hack/upstream-capi-scale/README.md`, not as evidence files.

Three parts. The changes, grouped by what each was answering. An evaluation of
the instrument and its results, including the defects it still carries. And
the changes worth making to a stock Cluster API management cluster, now that
the measurements say where it gives out.

## Part 1: the changes

### Phase 0: what the kcp runs taught, before a line of this was written

Four findings from `specs/20260831-201500-fleet-target-scale` and
`specs/20260831-210000-deployed-fleet-scale` shaped the design:

| Finding | Commit | What it became here |
|---|---|---|
| The store binds before the controllers. The kcp shard was OOM killed against 4 GiB at 200 clusters of fifty nodes while the four managers sat at a fifth of their limits. | `cf5eda8` | The API server and etcd are measured as first-class components, and the ceiling was expected there. |
| An OOM kill without `GOMEMLIMIT` measures the collector. kcp died holding 1.63 GiB of live heap because the runtime had grown the heap to 3 GiB with no ceiling. | `03b01dc` | Every controller gets `GOMEMLIMIT` below its limit. |
| A heap figure without a forced collection is noise. Three runs disagreed by a factor of four; collected, they agreed to 3.4%. | `daf9536`, `47eb8ab` | Every controller sample goes through `/debug/pprof/heap?gc=1`. |
| The memory is the unstructured representation, at roughly 200 KB of retained heap per stored object. | `65ea369` | S3: does an ordinary kube-apiserver cost the same? |

### Phase 1: the harness, built without a cluster (`67fa889` to `9d3c61f`)

- Two blockers found by reading source rather than by failing: stock managers
  serve no Go runtime metrics, so sampling had to be pprof; the released
  DevCluster provider is the Docker provider and cannot schedule on a
  containerd node until its socket mount and privilege are stripped.
- Guaranteed QoS on every component with the cost stated: a CPU limit means
  CFS throttling, and throttling counterfeits "the fleet did not arrive and
  nothing died", so throttling became a measured quantity.
- The rung classification: OOM, restart, or did not converge. Three answers
  that send an operator to three different places.
- Fleet planning, a preflight that checks the CRDs serve what the fork's types
  will create, and convergence counted against what was asked for.

### Phase 2: provisioning against CAREN (`2aecc66` to `6758e16`)

Eight commits, most of them replacing a guess with a fact read from CAREN's
docs or source: the ClusterClass lives in the Helm chart and clusterctl does
not install it; the templates need `CLUSTER_TOPOLOGY`, `EXP_RUNTIME_SDK`, the
Helm addon provider and credentials at init time; the quick-start template has
no worker replica count and five addons the measurement does not want. The
`config` step, added after a `set -u` ordering bug, has caught two more since.

### Phase 3: three projects' versions (`1fb1f4a` to `34b5de8`)

CAREN v0.50.0 strict-decodes CAPX types compiled against an older Cluster API,
so the bootstrap cluster was pinned to v1.12.5 and the cluster under test was
split off to run the latest, with a clusterctl fetched per version. The
quietest bug in the exercise was here: CAREN's example builds every node at 2
vCPU and 4 GiB, and nothing in a run would have reported that the ceiling was
the box.

### Phase 4: measuring the control plane (`470799e` to `bf1b162`)

Preflight went green against the real cluster. The API server and etcd
samplers were built, etcd's metrics port opened through a ClusterClass patch,
and defragmentation put between rungs. Then `bf1b162` found nothing had ever
pointed the Cluster at the patched ClusterClass copy, so every cluster built
until then had the stock 2 GiB quota.

### Phase 5: first contact, and the ladder (`8efcfb6` to `a9e0478`)

The loop landed and the first two smoke runs found five bugs, each fixed with
the check that would have caught it:

- the driver's scheme was missing four API groups (`1bd4b27`);
- `CLUSTER_TOPOLOGY` was unset on the cluster under test, and then set only
  on the providers clusterctl reinstalled (`3f49911`, `d9f57c8`);
- deleting namespaces left every one Terminating, because stock Cluster API
  cannot finish deleting a fleet whose objects were all stamped at once. The
  fork already carries the DevCluster/DevMachine ordering fix. Teardown now
  deletes Clusters first and waits;
- the sampler read a container named after the deployment, and clusterctl
  names every container `manager`, so every controller sampled as zero
  restarts with no limit, which `Classify` reads as "nothing died";
- the baseline was not cold: the managers take a minute or two after start
  to open their caches, so the harness now waits for two consecutive samples
  to agree within 2%.

Then the instrument defects the sweep and ladder exposed: rung timing split
into creation and convergence; resident memory and CPU from cAdvisor rather
than a metrics-server the cluster did not have; defragmentation before the
baseline; stored objects split into Cluster API groups, events and the whole;
the driver's own client-go rate limit of 5 QPS, which was over two minutes of
every 400-cluster rung (`0de7804`); and a soak drift check that compared
endpoints only and missed core's heap doubling and coming back (`a9afefd`).

**The results.** Five rungs at ten nodes each converged, then 800 and 1600.
The cost model fitted on 25 to 400 clusters predicted 400 to 1600 to within
2%. 1600 clusters and 16,000 Machines held for 32 minutes with nothing killed
and nothing restarted. Detail is in `evidence/README.md`.

### Phase 6: pushing past the floor, where the resilience findings are (`cfe6c19` to `8e1a394`)

The runs above 1600 are not in the evidence directory. What they found is in
the commit messages, and every one of them is a change to how the harness
reads a failure rather than to what the cluster can hold:

| What happened | Finding | Harness change |
|---|---|---|
| The API server sample was one arbitrary instance behind the VIP, and the pod proxy strips credentials so per-instance `/metrics` is refused. | Every control-plane figure before this was one process of three. | Scrape every control plane node's cAdvisor, which carries the caller's identity and reports every process on the node, including the controller manager's garbage collector holding an informer per CRD (`cfe6c19`). |
| cAdvisor puts a timestamp after each value and the parser skipped every line. | Every committed run's manager resident and CPU figures were zero for this reason, not the container name (`e842326`). | Parse the timestamp. |
| A 1000-cluster rung ended with core exiting `leader election lost` after 1.25 s of client-side throttling at Cluster API's default 100 QPS. | A ceiling that is a flag is not a ceiling. | Raise client limits and widen the leader-election window as flags (`46f4d6c`). |
| At 500/1000 QPS the store could not commit lease renewals, managers exited, no rung finished. | Five times the write rate onto etcd is worse than a throttled manager. | Default back to 100/200; the 500-cluster rung converged in 10m52s (`d50d4c6`). |
| A 1000-cluster rung ended 81 s in with the KubeadmControlPlane manager SIGKILLed, nowhere near its limit, unthrottled. | Its liveness probe is a TLS dial to its own webhook server, and KubeadmControlPlane status updates cost 139 ms through two webhooks served by that process. It was killed for being busy with its own admission. | Probe timeout 5 s, threshold 5, raised only (`5f525b3`). |
| A manager exited `leader election lost` while saturating the API server with its own Machine writes. | Its lease renewal was queued behind its own bulk traffic. | A FlowSchema routing the managers' lease writes to the built-in `leader-election` priority level (`799217f`). |
| A 1000-rung was abandoned two seconds in on a webhook returning "ClusterClass Informer failed to sync" after the between-rung defrag. | A warming manager's cache is not a ceiling. | Retry transient rejections for two minutes; return Invalid, Forbidden, Conflict immediately (`003e698`). |
| A kube-apiserver died `Exit Code 137, Reason: Error`. | kubeadm gives it no memory limit, so this was the kubelet's own kill after `/livez` went unanswered, not an OOM. | `WhyItDied` reads exit code, OOM flag and limit together (`160d748`). |
| Managers died with `etcdserver: request timed out` while etcd's lifetime fsync mean read 1.6 ms. | Over 3.5 million syncs a minute of stalls does not move a mean. | Read the histogram tail past 128 ms, failed and pending proposals, and diff every counter against a baseline (`5a43775`, `b49ee2c`, `8d0d353`). |
| The Nutanix cloud controller manager lost its lease on a 5 s PUT timeout and ended a 1000-cluster rung. | The most impatient neighbour is the first to notice a slow API server, and it is not the system under test. | Static pods decide a rung's failure; everything else on the node is reported beside it (`83aed1c`). |
| Core lost its leader election during a 2000-cluster rung and every rung of every run afterwards aborted within seconds, naming that day-old restart. | Restart counts are cumulative. | Managers, control plane and etcd are all diffed against a baseline taken before the climb (`2eceb03`, `8e1a394`). |
| An API server that had served 1500 clusters the day before held 4.91 GiB of live heap against an empty API; restarted, 216 MiB. | Over nine tenths of every absolute memory figure was the previous fleet. The "82% full at 1500 clusters" conclusion was mostly history. | `Inherited` warns on every process that predates the run; the README carries the procedure, and the fact that deleting a static pod does not restart it (`a511ccd`). |
| 23.5 GiB resident against 9.7 GiB heap. | Whether that is live data or memory the runtime never returned decides whether the fix is bigger nodes or `GOMEMLIMIT`. | Parse heap in use, idle, released and next GC (`f9e12e3`). |
| One etcd member always reported reclaiming nothing while its peers shed 850 MiB. | The size gauge refreshes on commit, not on rewrite. | Poll until file and data converge; never re-defragment (`78abed2`). |

### Phase 7: one runner, two control planes (`197ad42` to `4ac1bf4`)

The driver was refactored into a `Runner` with a `Target` interface, so the
same ladder, settle, sampling, defragmentation, soak and report drive either a
Kubernetes API server or a kcp shard. The kcp side gained three shard
replicas over an external three-member etcd with the same quota, a test that
three replicas serve one store, and the managers gained `--profiler-address`
so both sides are read with the same instrument. Specified in
`specs/20260904-090000-comparable-kcp-stock-scale`. Built, not yet run.

## Part 2: evaluation

### What was measured

| Fleet | Result |
|---|---|
| 400 clusters, 4000 Machines | converged in 15.8 min, soaked 30 min, nothing drifted |
| 1600 clusters, 16,000 Machines | converged, held 32 min, nothing killed, nothing restarted |
| cost model, four controllers | 58.75 goroutines and 2.84 MB live heap per cluster, R² ≥ 0.9999, predicting 1600 from 400 to within 2% |
| API server | goroutines flat across a sixteenfold change in fleet; cost is memory, about 6.7 MB resident per cluster |
| etcd | 82.7 keys per cluster at ten nodes; a held fleet turns over roughly a gigabyte of reclaimable pages an hour |
| 2000 clusters | the store stopped committing lease renewals; managers exited `leader election lost` |

The last row is the one that matters and it is the one without an evidence
file.

### What is good

**The discipline about what may be called a measurement survived contact
with real failures.** A floor is reported as a floor. Every figure carries
whether it was post-collection. A missing series is an error, not zero. A
wrong prediction, twice, was corrected in the file that made it with the
reason it was wrong. That last habit is rarer than it should be.

**Every false ceiling became a diagnosis rather than a threshold.** The
harness went through seven ways a rung can end that are not about capacity:
a flag, a warming cache, an impatient neighbour, a probe that dials a busy
webhook server, a lease queued behind its own writes, a restart from
yesterday, an allocator high-water mark from yesterday. Each is now
classified from evidence the run already holds, with no invented thresholds:
etcd's own 128 ms bucket, the kernel's own OOM flag, the kubelet's own mirror
annotation.

**The instrument has been paid for by what it found about stock Cluster
API.** Three findings that apply to every management cluster anyone runs:
the KubeadmControlPlane webhook loop, the lease renewal starvation, and the
API server's retained memory. Part 3 is built on them.

**The comparison is now structurally honest.** One runner, one cluster, two
targets. Anything the two sides differ in is on one interface, and the fleet
they build is checked to be the same by a test.

### What is still wrong

Ordered by how much it would distort the next run.

**1. The convergence poll still forces a garbage collection in every
controller every fifteen seconds.** `Runner.wait` calls `died` each poll,
`died` calls `Sampler.Sample`, and `Sample` reads every controller's heap
through `/debug/pprof/heap?gc=1`. At 1600 clusters that is a full collection
of a multi-gigabyte heap in four processes, four times a minute, for the
length of a convergence, charged to the rung whose verdict is "reconciliation
did not keep up". The kcp runs measured forced collections as CPU precisely
because it was not negligible. `died` needs only pod status, which
`PodFactsFrom` already provides without touching pprof.

**2. ~~The soak still holds the failed rung's fleet.~~ Fixed.** Rungs are
cumulative and nothing removed the rung that failed before the soak began, so a
climb that failed at 2500 soaked 2500 partially converged clusters under a
label of 2000. The run now tears down the failed rung's own tenants before the
soak and records a `soakFleet` fact. Three more harness faults the 2000-cluster
runs exposed were fixed at the same time: the wait believed a converged count
before checking whether a process had died inside the rung (the bootstrap
manager's death at 21:09 was charged to the next rung); the etcd strain baseline
was taken once rather than after every defragmentation, so a rung's line
carried the defragmentation before it; and a control plane was counted ready on
`Available` alone, which KubeadmControlPlane grants at one member and withdraws
while the second joins, so every rung's readiness appeared to flap. A control
plane now counts only at full replicas, and each sample records where the etcd
leader, the controller manager's lease and kube-vip's VIP sit.

**3. The poll lists every Machine, unpaginated, as full objects.** At 16,000
Machines that is tens of megabytes decoded into typed structs every fifteen
seconds, and it is load on the API server under test that is not Cluster
API's. Metadata-only paginated lists, or one informer held for the run.

**4. The runs above 1600 are not evidence.** The 500, 1000, 1500 and 2000
cluster runs produced the most important findings on the branch and exist
only as prose. Whatever their reports were, commit them, with the caveats the
README already states about their baselines. A finding without its evidence
file is what `AGENTS.md` calls a stale status.

**5. The prepare tool now changes the system under test, and the report
does not say so.** Probe patience, leader election deadlines of a minute, a
FlowSchema for leases, and client limits are all applied by
`capiscale-prepare`. Each is argued as "not tuning the result" and the
argument holds. But a report from a prepared cluster should carry those
settings as facts beside the provider versions, or a reader comparing against
a stock installation cannot know the probes were not stock.

**6. The controllers' own metrics are still not read.** Workqueue depth, queue
latency, reconcile errors and client rate-limiter latency are on the
diagnostics endpoint the harness already reaches. The 1000-cluster lease loss
was diagnosed from a log line about client-side throttling; the rate-limiter
histogram would have said it in the report.

**7. Webhook latency is measured by hand.** The KubeadmControlPlane admission
figures that explain the stock ceiling came from a `grep` in the README. The
API server exposition the harness fetches every rung carries
`apiserver_admission_webhook_admission_duration_seconds`; parse it per
webhook and put the top few on the rung line.

**8. Smaller.** `GOMEMLIMIT` headroom is capped at 512 MiB, which is 2% of
the provider's 24 GiB. The package is still named `upstreamscale` while
holding the shared runner, which the refactor commit already calls wrong.
The spec's status line says "Draft" with a measured result under it.

## Part 3: what to change on a stock Cluster API management cluster

The measurements say where a stock installation gives out, in order: the
KubeadmControlPlane manager's webhook path, then the store's ability to commit
small writes under the managers' bulk traffic, with the API server's memory
further out than first thought once the previous fleet's history is
subtracted. The recommendations follow that order. Each is something an
operator can do without a fork, and each is also a measurement to take.

### The KubeadmControlPlane webhook loop, which is structural

The KCP controller probes every workload cluster's control plane and etcd,
writes a status update per cluster per probe, and every update passes through
a defaulting and a validating webhook served by the same process, at about 139
ms per update on this cluster. More clusters mean more probes mean more
admission load on the process doing the probing, its health check is a dial to
that same server, and readiness flaps for the same reason.

- **Run more replicas of the KubeadmControlPlane manager.** Leader election
  means one replica reconciles, but every replica serves webhooks behind the
  Service. Three replicas divide admission load by three without changing what
  is reconciled. This is stock-compatible and is the cheapest experiment on
  this list.
- **Give that manager CPU, not memory.** Its 6 GiB was 63% used at 1600
  clusters. Its webhooks are JSON decoding and validation, which is CPU, and
  it had 4 cores. The next rung should raise CPU first.
- **Upstream: decouple the health check from the webhook server.** A liveness
  probe that fails when the process is busy serving admission kills the
  process under exactly the load it was built for. controller-runtime's
  `StartedChecker` is the wrong thing to wire to `healthz` for a manager that
  serves heavy webhooks. This is a proposal, and `AGENTS.md` says where it
  goes.
- **Upstream: probe less, write less.** A status write per cluster per health
  probe is the write volume that reaches etcd. Writing only on change, or
  batching condition updates, reduces both the admission load and the store
  load below.

### The store, which is where 2000 clusters stopped

At 2000 clusters etcd timed out lease renewals with fsync means unchanged, so
the disk was not chronically slow. A 1500-cluster rung on 2026-09-07 found
the mechanism: the API server's five-minute compaction walked 163,000
revisions on a page cache the API server's heap had evicted, took 1m41s, and
blocked the store's apply loop for 59 seconds, which is longer than every
stock lease on the control plane. Fsync tails on all three members were
healthy, the vdisk's random reads were fast, and the host was not contended.
The store did not fail; it was starved of memory by the process in front of
it. Everything here reduces proposals, shortens what one compaction can hold,
or isolates the small writes.

- **Do not shorten the compaction interval.** It was tried at one minute and
  reverted: it bounds a cold-cache stall, but it also cuts the history etcd
  keeps to one or two minutes, and at this scale that turns any etcd
  interruption into a full re-list by every API server. OpenShift leaves it
  at five minutes, and a setting that is not defensible in production has no
  place in the test.
- **Make the control plane tolerate a slow minute, as OpenShift does.** A
  liveness probe of `/livez?exclude=etcd` so the kubelet does not kill an API
  server for a slow store, 9-second etcd health and ready check timeouts, and
  137/107/26-second leader election on the controller manager and scheduler,
  built for a 78-second API server outage. The provisioning script now
  applies all three through the ClusterClass copy: the arguments as appends,
  the probe as a kubeadm patch file in the directory CAREN's class already
  uses. See `hack/upstream-capi-scale/README.md`.
- **Keep etcd's database in the page cache**, which is the memory ceiling on
  the API server from the section below, and keep the etcd leader off the
  node that holds the controller-manager leader.

- **Move leases and events to their own etcd.** The kube-apiserver flag
  `--etcd-servers-overrides` routes a resource to a separate store. Leases are
  the smallest write in the cluster and the one whose failure kills a manager;
  events are the highest-churn, lowest-value writes and most of the store's
  key count at small fleets. kcp has no equivalent, so this is its own
  experiment, not a comparison.
- **Keep the FlowSchema for leases, and consider one for the managers' bulk
  traffic.** The lease schema stops a heartbeat queueing behind its owner's
  writes. A separate priority level for the four provider service accounts
  with bounded concurrency shares would make Cluster API slower and let it
  hold more, which is the trade an operator may want and the harness
  deliberately does not default to.
- **Do not raise client QPS past what the store can commit.** 500/1000 was
  five times the write rate onto the store and no rung finished. The client
  limit is a back-pressure mechanism; the right value is the one the store
  absorbs, and 100/200 is close to it on three-node kubeadm etcd.
- **Sample peer round-trip time.** It is the one etcd signal the run does not
  read and the one that distinguishes a slow peer from a slow disk.
- **A disk of its own for etcd's data directory**, which the provisioning
  script now attaches through the ClusterClass copy, mounted one level above
  the data directory so kubeadm's preflight does not trip on `lost+found`. On CAREN's template etcd shared
  the root disk with the API server's audit log, tens of thousands of records
  per burst on the vdisk the WAL fsyncs to. The 8 GiB quota stays as
  sizing.md has it. A held fleet turns over a gigabyte of reclaimable pages an
  hour, so a defragmentation schedule is part of operating this, one member at
  a time.

### The API server's memory, which is smaller than it looked

A restarted API server holding Cluster API's CRDs and nothing else costs
about 200 MiB. One that had served 1500 clusters the day before held 4.9 GiB
of live heap against the same empty API. Whether a loaded API server's
resident set is live data or memory the runtime never handed back is now
readable from the heap decomposition on the rung line, and the answer decides
the fix.

- **If mostly retained: `GOMEMLIMIT` on the kube-apiserver static pod**, with
  a cgroup memory limit set above it. kubeadm gives the API server a CPU
  request and nothing else, so the runtime has no idea it shares a 32 GiB node
  with etcd. Expect the cost in CPU, and therefore in `/livez` latency, which
  is the failure mode this exercise keeps meeting.
- **If mostly in use: bigger control plane nodes**, and the finding that an
  ordinary kube-apiserver serving CRDs as unstructured objects costs on the
  order of the kcp shard's 200 KB per object, which the spec's S3 asked.
- **Either way: stream lists and size the watch cache.** An API server
  restart at this scale was observed with its watch cache 150,000 revisions
  behind on the way back. Every restarting controller lists everything, and
  the API server materialises each list in memory per request. The
  `WatchList` server gate with `WatchListClient` on the managers removes that
  buffer; `--watch-cache-sizes` keeps a brief disconnect from becoming a
  re-list. Check the gate state for the Kubernetes version in use, since both
  have moved between beta and off across releases.

### Resilience, as distinct from capacity

- **Nothing but the control plane on the control plane nodes.** The first
  fresh-cluster run put the DevCluster provider beside a 20 GiB API server on
  a 32 GiB control plane node, because clusterctl's manifests tolerate the
  taint and kubeadm's API server has no memory request. The etcd member on
  that node lost its page cache, its compactions went from seconds to over a
  minute, and kube-vip's lease through it timed out. The prepare tool now
  pins every manager away from the control plane label; a production
  installation should do the same for everything it lets tolerate that taint.
- **Every neighbour with a short lease is a fuse.** The cloud controller
  manager's 5 s PUT timeout made it the first process to give up on a slow API
  server. Audit lease and probe timeouts on everything that tolerates the
  control-plane taint, and lengthen the ones that would take a management
  cluster down for being busy.
- **Leader election for a busy API server.** OpenShift's 137 s lease, 107 s
  renew deadline and 26 s retry, which the ClusterClass gives the control
  plane's own components and the prepare tool now gives the four managers too,
  and the lease FlowSchema. A manager that has already tolerated well over a
  minute of an unavailable store and still lost is not short of patience; past
  that, the store is the fix.
- **Restart cost as a measured number.** The most dangerous event on a
  management cluster at scale is a controller or API server restart and the
  re-list that follows. Restart the core manager during the soak and time the
  return to every Machine reconciled. That is the recovery time objective and
  nobody knows it until it is measured.
- **Priority classes and disruption budgets** on the four managers, so node
  pressure evicts the workload before the thing managing it, and a drain
  cannot take a single-replica manager down mid-reconcile.
- **Bounded remediation.** A `MachineHealthCheck` with `maxUnhealthy` set is
  what stops a transient API server outage, which looks to the health check
  like every node going NotReady at once, from becoming a fleet-wide
  replacement.
- **Deletion ordering is a stock bug with a fix in the fork.** A namespace
  deleted over its Clusters leaves every DevMachine Terminating for ever. The
  fork's DevCluster/DevMachine ordering fix belongs upstream, and until it
  lands, delete Clusters and wait.

### Sharding, and where the store's limit sends the design

Watch-filter sharding gives each provider manager its own caches, rate
limiter and lease against one API server, and it doubles the in-memory
provider's port-range ceiling per pod. It reduces nothing at the store. The
2000-cluster result says the store's commit path is what gives out first, and
the only things that move that are fewer writes per cluster, a split store, or
more stores. The last is what kcp does with shards and what the comparable
run exists to price.

### What to run next, in order

1. Fix items 1 to 3 above, commit the runs above 1600 as evidence, then take
   the 3200 rung with core and the KubeadmControlPlane manager's limits raised,
   so the wall reached is Cluster API's rather than sizing.md's.
2. Three replicas of the KubeadmControlPlane manager at the same shape. If
   the webhook means drop and the rung converges faster, the stock ceiling is
   admission and the fix is a deployment setting.
3. Leases and events on their own etcd, recorded as a tuned-control-plane
   result beside the stock one.
4. The kcp side at the same shape, from the comparable spec, so that the
   control-plane figures are finally subtractable.
5. The restart-during-soak measurement on whichever side is being quoted.
