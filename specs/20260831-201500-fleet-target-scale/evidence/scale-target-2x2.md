# Fleet target: 4 clusters over 2 workspaces, 3 nodes each

| Condition | Value |
|---|---|
| checkpointWallClock | 53s, 46s |
| clusterShape | ClusterClass based: one class per workspace, each Cluster naming it |
| clustersPerWorkspace | 2 |
| controlPlaneMachinesPerCluster | 1 |
| deployment | none — four deployments co-located, so one engagement per workspace rather than four |
| devClusterBackend | inMemory |
| endState | every control plane ready and every Machine Ready |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerClusterDerived | 70.00 |
| goroutinesPerWorkspace | 140.0 |
| heapBytesPerClusterDerived | 8700964 |
| heapBytesPerWorkspace | 17401928 |
| nodesPerCluster | 3 |
| outcome | pass |
| provisioningConcurrency | 8 |
| reachedClusters | 4 |
| reachedNodes | 12 |
| reachedWorkspaces | 2 |
| shape | every provider's controllers on one fleet: core, bootstrap, control plane, dev infrastructure |
| spread | 2x2 |
| stoppedBy | reached the requested target of 2 workspaces |
| targetClusters | 4 |
| targetNodes | 12 |
| targetWorkspaces | 2 |
| workerMachinesPerCluster | 2 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 314 | 22.4 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 workspaces, 2 clusters, 6 nodes | 1 | 1019 | 33.8 MiB | 15 (14/1) | 56 | 24 | 1870 | 58.2s |
| 2 workspaces, 4 clusters, 12 nodes | 2 | 1159 | 50.4 MiB | 15 (14/1) | 116 | 31 | 3744 | 48.2s |
