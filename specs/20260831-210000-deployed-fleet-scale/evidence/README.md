# What the deployed instrument costs, and whether it can be believed

Three runs:

| File | What it is for |
|---|---|
| `deployed-core-8x1.json` | the calibration: one manager, at the reference's end state |
| `deployed-all-25x1.json` | all four managers, 25 clusters, one per workspace |
| `deployed-all-50x1.json` | the same at 50, to see whether cost stays linear |

`deployed-core-8x1.json` comes first because a second instrument nobody has
checked is worth less than the one it was meant to corroborate.

## How to reproduce it

```sh
task test:scale:kind COMPONENTS=core-manager CLUSTERS=8
```

One manager rather than four, deliberately. The in-process sweeps stop at
engagement — every workspace bound and holding its objects — and a run of all
four providers goes past that to take every cluster to Ready. Only the
one-manager run measures the same work the reference did, so only it can check
the two against each other. See `deployedscale.EndStateEngaged`.

kind (single node), kcp v0.32.3, `IfNotPresent`, in-memory dev backend.

## What it says

Core-manager, per workspace, deployed as its own Deployment:

| Workspaces | Goroutines | Heap | Resident | CPU |
|--:|--:|--:|--:|--:|
| 2 | 329 | 11.1 MiB | 70.2 MiB | 0.4s |
| 4 | 331 | 10.8 MiB | 73.4 MiB | 0.7s |
| 8 | 339 | 15.1 MiB | 76.7 MiB | 1.2s |

- **goroutines per workspace: 1.7**
- resident bytes per workspace: 1.0 MiB

## The finding

| Quantity | Deployed | In process | Ratio | Within 20% |
|---|--:|--:|--:|---|
| goroutinesPerWorkspace | 1.7 | 2.0 | 0.86x | yes |

**The two instruments agree.** The same controllers, measured by two rigs that
share no code path — one sweeping an in-process manager, one scraping a pod's
metrics endpoint through a Kubernetes API server — land 14% apart on the
quantity the whole cost model turns on.

That is what makes the deployed figures worth quoting. It is also what makes
the *disagreement* at the other end state worth reading as a finding rather
than a fault: see below.

## Why a run of all four providers reports ten times this

A run with every provider deployed measures core-manager at 17.0 goroutines per
workspace, not 1.7 — reproducibly, from clean linear fits at 2/4/8 and at
3/5/10 workspaces. Both numbers are right. They differ because a complete
provider set takes every cluster to Ready, and a ready cluster costs the core
manager a live ClusterCache — a connection to the workload cluster, its
informers, and their goroutines — that a run stopping at engagement never
opens.

So roughly 15 goroutines per connected workload cluster, on top of 1.7 per
workspace engaged. That number is **not** measured here: this run has no ready
clusters in it, and the run that does has no evidence file committed. It is
recorded as the explanation for a gap, not as a figure to quote.

## What all four managers cost, and whether it stays linear

`deployed-all-25x1.json` and `deployed-all-50x1.json`, fitted against cluster
count:

| Deployment | 25 clusters | 50 clusters | fixed cost, 25 | fixed cost, 50 |
|---|--:|--:|--:|--:|
| core-manager | 17.0 | 17.0 | 344 | 344 |
| kubeadm-bootstrap-manager | 14.0 | 14.0 | 149 | 149 |
| kubeadm-control-plane-manager | 46.0 | 47.0 | 174 | 153 |
| dev-infrastructure-manager | 29.0 | 30.1 | 194 | 171 |
| **TOTAL** | **105.9** | **108.0** | | |

Goroutines per cluster, and the intercept each fit implies.

**It stays linear, and the numbers repeat.** Two runs taken minutes apart over
different ranges — 7/13/25 and 13/25/50 clusters — agree to 2% in total. Core
and bootstrap agree exactly, on both slope and intercept, with a maximum
residual of 0.0 goroutines: their three points are collinear to the integer.

That is worth more than either run alone. A slope that reproduces across a
doubled fleet, from a fresh set of pods, is a property of the software rather
than of the afternoon.

### What it does not yet establish

The two runs above both place one cluster in each workspace, so they cannot
separate what a cluster costs from what a workspace costs — the two rise
together. Runs that pack several clusters into a workspace do separate them and
suggest core-manager pays about 15 per cluster and 2 per workspace, the latter
matching the calibration above exactly. Those runs are not committed here, so
that decomposition is a reading rather than a result.

## What this is not

- **Not multi-node.** Every component ran on the kind control-plane node, so
  this says nothing about a deployment whose managers sit on different
  machines. `SPREAD=true` with `WORKERS` is how that gets measured.
- **Not a 200-cluster figure.** The largest run is fifty. Carrying these slopes
  to 200 predicts roughly 22,400 goroutines across the four managers; that is a
  prediction from a 4x extrapolation and is labelled as one wherever it appears.
  It is better supported than it would have been an hour ago — linearity now
  holds over 7 to 50 clusters — and it is still not a measurement.
- **Not a statement about nodes per cluster.** Every run here is one node per
  cluster. The 200x50 target is 10,000 Machines and nothing here has been near
  that.
- **One quantity reconciled.** Goroutines per workspace is checked against the
  reference. The resident-bytes slope is reported and is not checked against
  anything.

## Provenance

The `source` path in the JSON was rewritten from the absolute path the run
recorded to the repository-relative one, so the reference it names resolves for
anyone who checks it out. No measured value was altered.
