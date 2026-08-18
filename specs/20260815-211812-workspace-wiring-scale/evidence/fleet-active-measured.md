# The fleet wiring under load, measured

The first measurement of the fleet-wide wiring with workspaces doing work.
Everything before it — 2.00 goroutines and ~113 KiB per workspace to a
hundred workspaces (`fleet-wide-measured.md`) — was measured on **idle**
workspaces, and that gap was flagged at the time rather than papered over.
This closes it, and in closing it found three defects that no idle
measurement could have found.

Instrument: `test/integration/sweep`, `TestCoreReconcilerWorkspaceSweep`,
against a real kcp. Raw output: `fleet-active-coremanager.{md,json}`.

Shape: `coremanager.SetupFleetControllers` — ClusterCache, Cluster, Machine,
DevCluster and DevMachine, one controller each for the whole shard, over the
dev provider's in-memory backend. Each workspace holds one Cluster and one
DevCluster, both named `default/sweep-00` — deliberately the same name in
every workspace, because identical names are how a cross-workspace confusion
becomes visible rather than plausible. Eight workspaces, `GOMAXPROCS=4`.

## What it costs

| Quantity | Per workspace |
|---|--:|
| Goroutines | **2.0** |
| Watch streams | **0.00** |
| Discovery requests | 4.0 |
| Heap | see below — the fit is not usable |

**Goroutines: 2.0, and the same 2.0 as idle.** 297 at one active workspace,
311 at eight: fourteen goroutines for seven workspaces, exactly. Putting a
Cluster and a DevCluster in each workspace and reconciling them to
provisioned adds nothing per workspace that survives the reconcile. That is
the claim this run was written to test, and it holds.

**Watch streams: zero per workspace.** Eight streams at one workspace and
eight at eight — seven against `/clusters/*` and one scoped, the scoped one
being the APIExportEndpointSlice the provider itself watches in `root`. The
sweep additionally asserts that no watch is addressed to a *tenant's* logical
cluster, and that assertion passed. This is the wildcard registration doing
what it was built for, now confirmed with the tenants active rather than
merely bound.

**Discovery: 4 requests per workspace.** Small and not free. It is the
provider building a scoped cluster as each workspace engages, so it is paid
once per engagement rather than continuously.

**Heap: not usable from this run.** The harness reports 683 KiB per
workspace, and that figure should not be quoted. The table shows why: heap
is flat at ~13.8 MiB through two workspaces, steps once to ~19.9 MiB, and is
then flat again to eight — 0.1 MiB per workspace across the whole upper
range. A straight line through a single step is arithmetic, not a
measurement. The idle sweep's ~113 KiB is the better-supported figure, and it
is an upper bound; this run neither confirms nor contradicts it.

## What it found

Three defects, all of them invisible to an idle sweep.

**1. The ClusterCache could not connect to any workload cluster.** Its
`SecretClient` was the cluster-aware client, which reads through the
APIExport's virtual workspace. That endpoint serves what the export serves,
and a core `v1.Secret` is not part of it, so every connection attempt failed
at the RESTMapper:

```
error getting kubeconfig secret: failed to get informer for *v1.Secret:
failed to get REST mapping: no matches for kind "Secret" in version "v1"
```

An idle workspace holds no Cluster, so nothing ever asked. Fixed by
`coremanager.NewWorkspaceSecretReader`, which addresses the shard and
resolves the workspace from the call's context. The same run showed
`cmd/core-manager` had the mirror-image fault — it built its local manager
from the shard config rather than from the virtual workspace, so its
RESTMapper described the exporting workspace, which does not bind what it
exports. `providerwiring.VirtualWorkspaceConfig` existed for exactly this and
had no caller.

**2. A workspace that unbinds went on costing reconciles forever.** After the
departure phase the log is nothing but

```
Requeuing after 10s (error getting Cluster object): failed to get cluster
"2t2ylktol6oe105o": cluster not found
```

for workspaces that had already disengaged. A fleet-wide ClusterCache learns
about Clusters through a cache spanning every workspace, so an unbinding
workspace leaves its Clusters in the queue; `ErrClusterNotFound` fell through
to the generic read-error branch and was requeued rather than treated as
"gone". It leaks work, not goroutines — the sweep confirms two goroutines are
reclaimed per departing workspace — but it grows with tenant churn and it
directly contradicts user story 2's "stops costing anything". Fixed in the
fork.

**3. Every event Cluster API emits is dropped.** The recorder posts through
the manager's client, so it posts to the virtual workspace:

```
Server rejected event (will not retry!): the server could not find the
requested resource (post events)
```

Not fixed here, and recorded rather than left implied: it is the same class
of fault as the Secret one — a core type addressed at an endpoint that serves
only exported types — and the fix has the same shape.

## What this run does not establish

The run **failed**, and the failure is part of the record.

It completed the whole ramp-up: all eight workspaces bound, engaged,
activated and reached `InfrastructureProvisioned`, and every measurement
above is from that half. It then failed in the departure phase, timing out
after three minutes waiting for the third workspace to disengage. Deleting an
APIBinding deletes the bound objects, and the Cluster reconciler was still
reporting `Cluster still has descendants - waiting for infrastructure cluster
deletion`. Whether that is teardown genuinely stuck or merely slower than the
three-minute bound on four CPUs is **not established by this run**, and the
two departure samples it did take (`7 left`, `6 left`) are too few to say
anything about reclamation beyond the two goroutines each.

Two earlier attempts failed in the *activation* phase instead — one at the
eighth workspace, one at the first — with the chain deadlocked on a DevCluster
waiting for an owner reference the Cluster reconciler had not set. That did
not recur once the Secret fix landed, but "did not recur in one run" is not
"fixed", and it is recorded here so that a future occurrence is a second
sighting rather than a first.

Not measured: anything above eight active workspaces, more than one object
per workspace, or the parity controller set.
