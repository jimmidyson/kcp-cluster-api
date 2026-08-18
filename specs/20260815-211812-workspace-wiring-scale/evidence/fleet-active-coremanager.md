# Active workspace sweep (core-manager reconciler set, in-memory backend)

| Condition | Value |
|---|---|
| devClusterBackend | inMemory |
| discoveryRequestsPerWorkspace | 4.0 |
| eventHandlersPerWorkspace | 15 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| heapBytesPerWorkspace | 683606 |
| objectsPerWorkspace | 1 |
| reconciledTypes | cluster.x-k8s.io/clusters + infrastructure.cluster.x-k8s.io/devclusters |
| secondsPerWorkspaceEngagement | -0.0 |
| shape | coremanager.SetupFleetControllers: ClusterCache, Cluster, Machine, DevCluster, DevMachine — one controller each for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 6 |
| workspaces | 8 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 19 | 12.1 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 bound, idle | 1 | 223 | 13.0 MiB | 8 (7/1) | 18 | 10 | 54 | 13.4s |
| 1 active | 1 | 297 | 13.8 MiB | 8 (7/1) | 18 | 16 | 90 | 9.4s |
| 2 bound, idle | 2 | 299 | 13.8 MiB | 8 (7/1) | 18 | 16 | 90 | 4.0s |
| 2 active | 2 | 299 | 19.9 MiB | 8 (7/1) | 18 | 20 | 117 | 9.4s |
| 3 bound, idle | 3 | 301 | 20.0 MiB | 8 (7/1) | 18 | 20 | 117 | 2.5s |
| 3 active | 3 | 301 | 20.1 MiB | 8 (7/1) | 18 | 24 | 144 | 9.4s |
| 4 bound, idle | 4 | 303 | 20.1 MiB | 8 (7/1) | 18 | 24 | 144 | 4.5s |
| 4 active | 4 | 303 | 20.2 MiB | 8 (7/1) | 18 | 28 | 171 | 9.4s |
| 5 bound, idle | 5 | 305 | 20.3 MiB | 8 (7/1) | 18 | 28 | 171 | 2.5s |
| 5 active | 5 | 305 | 20.4 MiB | 8 (7/1) | 18 | 32 | 198 | 9.4s |
| 6 bound, idle | 6 | 307 | 20.4 MiB | 8 (7/1) | 18 | 32 | 198 | 2.5s |
| 6 active | 6 | 307 | 20.5 MiB | 8 (7/1) | 18 | 36 | 225 | 9.4s |
| 7 bound, idle | 7 | 309 | 20.6 MiB | 8 (7/1) | 18 | 36 | 225 | 2.5s |
| 7 active | 7 | 309 | 20.7 MiB | 8 (7/1) | 18 | 40 | 252 | 8.7s |
| 8 bound, idle | 8 | 311 | 20.7 MiB | 8 (7/1) | 18 | 40 | 252 | 3.0s |
| 8 active | 8 | 311 | 20.8 MiB | 8 (7/1) | 18 | 44 | 279 | 9.4s |
| 7 left | 7 | 309 | 20.7 MiB | 8 (7/1) | 18 | 44 | 290 | 6.8s |
| 6 left | 6 | 307 | 20.7 MiB | 8 (7/1) | 18 | 44 | 299 | 7.3s |
