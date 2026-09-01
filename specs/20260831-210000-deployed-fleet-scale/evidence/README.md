# What the deployed instrument costs, and whether it can be believed

`deployed-core-8x1.json` is the run that calibrates this instrument against the
in-process one. It is the first deployed measurement in this repository, and it
is here because a second instrument nobody has checked is worth less than the
one it was meant to corroborate.

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

## What this is not

- **Not multi-node.** Every component ran on the kind control-plane node, so
  this says nothing about a deployment whose managers sit on different
  machines. `SPREAD=true` with `WORKERS` is how that gets measured.
- **Not a fleet-size claim.** Eight clusters, one node each. Nothing here
  supports a statement about 200 clusters; extrapolating a slope 25x beyond its
  data is a prediction, and this repository labels those as predictions.
- **One quantity reconciled.** Goroutines per workspace is checked against the
  reference. The resident-bytes slope is reported and is not checked against
  anything.

## Provenance

The `source` path in the JSON was rewritten from the absolute path the run
recorded to the repository-relative one, so the reference it names resolves for
anyone who checks it out. No measured value was altered.
