# The departure-phase hang: watches and reads are two different caches

The fleet sweep hangs. Sometimes a workspace never becomes active, sometimes
it never disengages; which workspace, and which phase, varies run to run.
This is what it is.

## The finding

**Events are delivered by one informer and read back through another.** A
reconcile woken by an event reads a version of the object older than the
event that woke it, takes the wrong branch, and returns with no requeue.
Because the event has been consumed and the second cache produces no event
of its own when it catches up, nothing ever wakes that reconciler again.

Two independent wildcard caches exist over the same `/clusters/*` endpoint:

| | Built by | Used for |
|---|---|---|
| **Provider's** | `watchedEndpoint` inside multicluster-provider, one per endpoint | every read a reconciler makes, via `mgr.GetCluster(...)` and so via the cluster-aware client |
| **Local manager's** | `mcmanager.New(localCfg, ...)` | every watch, because `SetupFleetControllers` passes `mgr.GetLocalManager().GetCache()` to `WithWildcard` |

Two informers, two watch streams, two independent lags. controller-runtime's
whole event model assumes watch-and-read are the same cache; this wiring
broke that assumption, and the sweep is what noticed.

## The evidence

From a run with routing logged at V(4) (`fleet-sweep11.log`), one workspace,
in order:

```
Routing event  cluster=2wcochnocile9gx5 type=*v1beta2.DevCluster resourceVersion=967
Reconciling    controller=devcluster    cluster=2wcochnocile9gx5
InMemoryCluster assigned controlPlane endpoint :20001          <-- ReconcileNormal
Reconcile successful  controller=devcluster
Cluster still has descendants - waiting for infrastructure cluster deletion
```

rv=967 is the version that carries the deletion timestamp. The reconcile it
woke ran `ReconcileNormal` — assigning an endpoint to a DevCluster that was
being deleted — which is only possible if the object it read had no deletion
timestamp. It then returned without a requeue, and the DevCluster controller
was never woken again. The Cluster reconciler waited on a DevCluster whose
finalizer nobody would now remove, so the APIBinding could not be deleted and
the workspace could not disengage.

The same shape produces the activation hang: the Cluster reconciler sets an
owner reference, the DevCluster controller is woken, reads a version without
it, logs `Waiting for Cluster Controller to set OwnerRef on DevCluster`, and
is never woken again.

## What was ruled out, and how

Each of these was a live hypothesis, and each was tested rather than argued
away.

**A stale read, measured at the timeout — inconclusive, not exculpatory.**
The first diagnostic compared all three views of the object when the wait
expired, and found them identical. That is what this fault predicts: the
lagging cache catches up within moments, long before a three-minute timeout.
The snapshot was taken too late to distinguish "never stale" from "stale and
then converged", which is why the routing log with resource versions was
needed.

**Events dropped by the resolver.** `WildcardSource` drops an event whose
object names no logical cluster. Logged; **zero** across a hanging run.

**Reconciles dropped by the cluster-not-found wrapper.** It turns
`ErrClusterNotFound` into a success with no requeue. Logged; **zero** across
a hanging run.

**The in-memory backend's process-global listener.** Upstream's test
infrastructure provider keys workload-cluster listeners by namespace and
name alone (`klog.KObj`), so every workspace's `default/sweep-00` shares one
listener on one port — a documented limitation of that provider. Tested with
`SWEEP_CORE_UNIQUE_NAMES=1`, which gives each workspace a distinctly named
Cluster: **the hang survives**. Not the cause.

**Store-key collision in the watch cache.** A plain controller-runtime cache
keys its store with `MetaNamespaceKeyFunc`, which has no room for a logical
cluster. The diagnostic counts entries per logical cluster; under unique
names it reported one per workspace. Not disproven for the identical-name
case, and it is a second reason not to watch a cache that is not kcp-aware.

## The fix, and why it is not a one-line change

Watches have to be registered on the cache the reads come from — the
provider's, which is also the only one of the two that is kcp-aware in its
keys and indexes.

That cache is not reachable through `apiexport.Provider`: it is created per
watched endpoint, and the aggregate the provider exposes is a `Lister`, not a
`cache.Cache`. The seam that does reach it is
`provider.Options.NewCluster`, in multicluster-provider's base package, which
is handed the `WildcardCache` for each engaged cluster —
`apiexport.New` does not forward that option, so the provider would have to
be built through `provider.NewProvider` directly.

Two complications, both real:

- **Ordering.** Controllers are wired before `mgr.Start`, and the provider
  does not build its cache until an endpoint appears. The builder would need
  a handle that resolves late — a façade whose `GetInformer` blocks until the
  cache exists.
- **More than one shard.** There is one cache per endpoint. A fleet spanning
  shards has several, and a watch registered on one sees only that shard.
  Registering on each as it appears is the general answer; failing loudly on
  the second is the honest interim one. Silently watching a fraction of the
  fleet is not an option — it is this same class of fault again.

Until then the wiring is **not correct under load**, and the goroutine and
watch-stream figures in `fleet-active-measured.md` should be read as measured
on a wiring with a known event-delivery fault, not as a green light.
