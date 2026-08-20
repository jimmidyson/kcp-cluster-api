# What a ClusterClass based cluster costs

The sweep run behind the figures in
[Workspace resource usage](../../../docs/site/content/en/docs/design/workspace-resource-usage.md)
and [Usage](../../../docs/site/content/en/docs/user/usage.md), taken on the tree
this feature landed as.

## How to reproduce it

```sh
SWEEP_CORE_WORKSPACES=20 SWEEP_BOOTSTRAP_WORKSPACES=20 \
SWEEP_CONTROLPLANE_WORKSPACES=20 SWEEP_DEV_WORKSPACES=20 \
  task test:sweep
```

Twenty workspaces per deployment rather than the default three, so that the
per-deployment table is comparable with the one this page replaced — that one
was taken at twenty, and a slope fitted through three points is not the same
measurement as one fitted through twenty. The fleet shape keeps its default of
three: it stands a control plane up per workspace, and three is the smallest
count that gives two independent retention pairs.

`GOMAXPROCS=4`, Go 1.26.3, kcp v0.32.3.

## What it says

Per deployment, per workspace (`sweep-report-total.md`):

| Deployment | Goroutines/ws | Watch streams/ws | Discovery/ws | Requests/ws | Streams held | Retained/departure |
|---|--:|--:|--:|--:|--:|--:|
| core-manager | 2.0 | 0.00 | 3.0 | 7.0 | 8 | 0.0 |
| dev-infrastructure-manager | 2.0 | 0.00 | 3.0 | 8.0 | 6 | 0.0 |
| kubeadm-bootstrap-manager | 2.0 | 0.00 | 4.0 | 16.0 | 7 | 0.0 |
| kubeadm-control-plane-manager | 2.0 | 0.00 | 7.0 | 72.0 | 7 | -0.1 |
| **TOTAL** | **8.0** | **0.00** | **17.0** | **103.0** | **28** | **-0.1** |

**Wiring the four topology controllers changed one number.** The core
deployment holds eight streams on the shard where it held six — `ClusterClass`
and `MachinePool` — and every per-workspace column is what it was. That is the
fleet-wide wiring behaving as designed: a controller is registered once for the
shard, so adding one adds a fixed term and no per-workspace term.

The fleet shape, which is the one with clusters in it
(`sweep-report-fleet.md`), costs **57 goroutines and ~484 reconcile requests
per workspace**, against 45 and ~236 previously reported. Nothing per workspace
is retained on departure, and no watch stream is added per workspace.

## What this run does not establish

**That the managed topology accounts for the 45 → 57 difference.** Two things
changed between the two runs — the process wires four more controllers, *and*
the cluster it builds comes from a class rather than from six hand-written
objects. Isolating them needs a sweep of the hand-built shape under the new
wiring, and that has not been run.

**Anything about the heap or step-time columns of the four per-deployment
reports.** A short unrelated `go test` process overlapped part of that run on
the same machine. The counter columns — goroutines, streams, discovery,
requests, retention — are unaffected by CPU contention; the heap fit and the
step times are not, and should not be quoted from these files.
