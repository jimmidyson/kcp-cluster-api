# Fleet target: 4 clusters over 4 workspaces, 3 nodes each

| Condition | Value |
|---|---|
| checkpointWallClock | 53s, 46s, 46s |
| clusterShape | ClusterClass based: one class per workspace, each Cluster naming it |
| clustersPerWorkspace | 1 |
| controlPlaneMachinesPerCluster | 1 |
| deployment | none — four deployments co-located, so one engagement per workspace rather than four |
| devClusterBackend | inMemory |
| endState | every control plane ready and every Machine Ready |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerClusterDerived | 69.00 |
| goroutinesPerWorkspace | 69.0 |
| heapBytesPerClusterDerived | 6354854 |
| heapBytesPerWorkspace | 6354854 |
| nodesPerCluster | 3 |
| outcome | pass |
| provisioningConcurrency | 8 |
| reachedClusters | 4 |
| reachedNodes | 12 |
| reachedWorkspaces | 4 |
| shape | every provider's controllers on one fleet: core, bootstrap, control plane, dev infrastructure |
| spread | 4x1 |
| stoppedBy | reached the requested target of 4 workspaces |
| targetClusters | 4 |
| targetNodes | 12 |
| targetWorkspaces | 4 |
| workerMachinesPerCluster | 2 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 40 | 12.4 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 workspaces, 1 clusters, 3 nodes | 1 | 678 | 25.2 MiB | 15 (14/1) | 30 | 24 | 970 | 56.4s |
| 2 workspaces, 2 clusters, 6 nodes | 2 | 747 | 26.8 MiB | 15 (14/1) | 60 | 31 | 1902 | 47.9s |
| 4 workspaces, 4 clusters, 12 nodes | 4 | 885 | 42.5 MiB | 15 (14/1) | 120 | 45 | 3780 | 48.5s |
