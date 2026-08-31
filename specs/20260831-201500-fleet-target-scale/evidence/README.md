# Evidence

**There is no measured figure for the 200-cluster target here, and the two runs
in this directory are not one.** They are four clusters each. Read the next
section before quoting anything from them.

## What these runs are

Two shapes of a toy fleet, taken on one developer machine at `GOMAXPROCS=4`,
with one control plane machine and two workers per cluster:

| File | Shape | Clusters | Nodes |
|---|---|--:|--:|
| `scale-target-4x1.{md,json}` | 4 workspaces × 1 cluster | 4 | 12 |
| `scale-target-2x2.{md,json}` | 2 workspaces × 2 clusters | 4 | 12 |

They are committed for one reason: they are the evidence that the **instrument
works**, which is the only claim this feature makes. They show it reaching a
stated end state, reporting a curve rather than a point, and — the part worth
keeping — producing a pair that can be read against each other:

| Shape | Goroutines per workspace | Clusters per workspace | Derived per cluster |
|---|--:|--:|--:|
| `4x1` | 69.0 | 1 | 69.0 |
| `2x2` | 140.0 | 2 | 70.0 |

The per-workspace figures differ by a factor of two and the per-cluster figures
agree to within 1.5%. That is the pair of spreads doing the job it exists for:
separating the term that scales with workspaces from the term that scales with
clusters. At this size it says the cost is dominated by the cluster rather than
by the engagement.

Watch streams held at 15 (14 wildcard, 1 scoped) across every checkpoint of
both runs, which is the same flat line the sweeps report and the conversion
plan's central claim.

## What they are not

- **Not a capacity figure, and not a sizing input.** Four clusters is three
  orders of magnitude below anything worth planning against, and two of the
  runs' three data points is not a curve anybody should extrapolate from. The
  per-workspace figures are least-squares fits over two and three active
  samples respectively.
- **Not the target this feature was built for.** That is 200 clusters of 50
  nodes at `200x1` and `20x10`, and it has **not been run**. Per AGENTS.md a
  measurement that was not taken is reported as not taken, never rounded to the
  nearest available number — which is what quoting these as though they
  answered the capacity question would be.
- **Not comparable with the sweep's fleet figures.** These clusters carry three
  machines each where the fleet sweep's carry one, so a per-workspace number
  here is a different workload's.

## Taking the real run

```sh
task test:scale:target
```

Hours of wall clock and considerably more memory than a normal test run. When
it has been taken, its reports belong in this directory and the "no figure yet"
notes in `docs/conversion-plan.md` and the spec come out.
