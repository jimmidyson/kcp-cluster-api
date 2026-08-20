# What an installation pays per workspace

One process per provider, so a workspace is engaged by each of them and the
cost of serving it is the sum. Each row is one deployment's own sweep; the
total is what a workspace costs the installation.

| Deployment | Workspaces swept | Goroutines/ws | Watch streams/ws | Discovery/ws | Requests/ws | Heap/ws | Streams held | Retained/departure |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| core-manager | 20 | 2.0 | 0.00 | 3.0 | 7.0 | 513 KiB | 8 | 0.0 |
| dev-infrastructure-manager | 20 | 2.0 | 0.00 | 3.0 | 8.0 | 995 KiB | 6 | 0.0 |
| kubeadm-bootstrap-manager | 20 | 2.0 | 0.00 | 4.0 | 16.0 | 1129 KiB | 7 | 0.0 |
| kubeadm-control-plane-manager | 20 | 2.0 | 0.00 | 7.0 | 72.0 | 1010 KiB | 7 | -0.1 |
| TOTAL | — | 8.0 | 0.00 | 17.0 | 103.0 | 3647 KiB | 28 | -0.1 |

**Streams held** is per deployment rather than per workspace: it is what that
process holds open on the shard whether it serves one workspace or twenty, so
the total is what the shard sees from the installation at rest.

**Heap/ws** is the one column to read with care. It is a least-squares fit, and
a process whose live heap grows in steps reports a slope that is the step
divided by the swept range rather than a per-workspace cost. Read a heap figure
against its own report's step table before quoting it.

| Condition | Value |
|---|---|
| goMaxProcs | 4 |
| goVersion | go1.26.3 |

Measured by:

- `sweep-report-core.json` — Active workspace sweep (the core provider's deployment) (2026-08-20T17:09:58Z)
- `sweep-report-dev.json` — Active workspace sweep (the dev infrastructure provider's deployment) (2026-08-20T17:16:57Z)
- `sweep-report-bootstrap.json` — Active workspace sweep (the kubeadm bootstrap provider's deployment) (2026-08-20T16:53:29Z)
- `sweep-report-controlplane.json` — Active workspace sweep (the kubeadm control plane provider's deployment) (2026-08-20T17:02:42Z)
