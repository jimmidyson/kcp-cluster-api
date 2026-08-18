# Active workspace sweep (core-manager reconciler set, in-memory backend)

| Condition | Value |
|---|---|
| devClusterBackend | inMemory |
| discoveryRequestsPerWorkspace | 4.0 |
| eventHandlersPerWorkspace | 15 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| goroutinesReclaimedPerWorkspace | -10.8 |
| goroutinesRetainedPerDepartedWorkspace | 0.0 |
| heapBytesPerWorkspace | 1096900 |
| objectsPerWorkspace | 1 |
| reconciledTypes | cluster.x-k8s.io/clusters + infrastructure.cluster.x-k8s.io/devclusters |
| secondsPerWorkspaceEngagement | -0.2 |
| shape | coremanager.SetupFleetControllers: ClusterCache, Cluster, Machine, DevCluster, DevMachine — one controller each for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 6 |
| workspaces | 8 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 19 | 12.1 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 bound, idle | 1 | 223 | 13.1 MiB | 8 (7/1) | 0 | 12 | 20 | 8.8s |
| 1 active | 1 | 243 | 13.6 MiB | 8 (7/1) | 0 | 16 | 48 | 10.1s |
| 2 bound, idle | 2 | 245 | 13.7 MiB | 8 (7/1) | 0 | 16 | 48 | 2.5s |
| 2 active | 2 | 245 | 13.9 MiB | 8 (7/1) | 0 | 20 | 75 | 8.4s |
| 3 bound, idle | 3 | 247 | 19.9 MiB | 8 (7/1) | 0 | 20 | 75 | 2.5s |
| 3 active | 3 | 247 | 20.0 MiB | 8 (7/1) | 0 | 24 | 102 | 7.6s |
| 4 bound, idle | 4 | 249 | 20.1 MiB | 8 (7/1) | 0 | 24 | 102 | 2.5s |
| 4 active | 4 | 249 | 20.2 MiB | 8 (7/1) | 0 | 28 | 129 | 9.3s |
| 5 bound, idle | 5 | 251 | 20.3 MiB | 8 (7/1) | 0 | 28 | 129 | 3.7s |
| 5 active | 5 | 251 | 20.4 MiB | 8 (7/1) | 0 | 32 | 156 | 9.4s |
| 6 bound, idle | 6 | 253 | 20.5 MiB | 8 (7/1) | 0 | 32 | 156 | 4.6s |
| 6 active | 6 | 253 | 20.6 MiB | 8 (7/1) | 0 | 36 | 183 | 9.3s |
| 7 bound, idle | 7 | 255 | 20.7 MiB | 8 (7/1) | 0 | 36 | 183 | 2.5s |
| 7 active | 7 | 255 | 20.8 MiB | 8 (7/1) | 0 | 40 | 210 | 7.3s |
| 8 bound, idle | 8 | 257 | 20.8 MiB | 8 (7/1) | 0 | 40 | 210 | 2.7s |
| 8 active | 8 | 257 | 21.0 MiB | 8 (7/1) | 0 | 44 | 237 | 7.3s |
| 7 left | 7 | 255 | 20.9 MiB | 8 (7/1) | 0 | 44 | 248 | 3.5s |
| 6 left | 6 | 253 | 20.9 MiB | 8 (7/1) | 0 | 44 | 259 | 7.0s |
| 5 left | 5 | 251 | 20.9 MiB | 8 (7/1) | 0 | 44 | 270 | 5.3s |
| 4 left | 4 | 249 | 20.8 MiB | 8 (7/1) | 0 | 44 | 281 | 5.3s |
| 3 left | 3 | 247 | 20.8 MiB | 8 (7/1) | 0 | 44 | 292 | 5.3s |
| 2 left | 2 | 245 | 20.8 MiB | 8 (7/1) | 0 | 44 | 303 | 6.3s |
| 1 left | 1 | 243 | 20.7 MiB | 8 (7/1) | 0 | 44 | 314 | 5.3s |
| 0 left | 0 | 136 | 20.2 MiB | 8 (7/1) | 0 | 44 | 325 | 5.3s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
| watch-list | * | cluster.x-k8s.io/machinedeployments | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 1 |
| watch-list | * | cluster.x-k8s.io/machinesets | 1 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devclusters | 2 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devmachines | 1 |
