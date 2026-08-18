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
| heapBytesPerWorkspace | 725095 |
| objectsPerWorkspace | 1 |
| reconciledTypes | cluster.x-k8s.io/clusters + infrastructure.cluster.x-k8s.io/devclusters |
| secondsPerWorkspaceEngagement | 0.4 |
| shape | coremanager.SetupFleetControllers: ClusterCache, Cluster, Machine, DevCluster, DevMachine — one controller each for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 6 |
| workspaces | 8 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 22 | 12.1 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 bound, idle | 1 | 226 | 13.1 MiB | 8 (7/1) | 0 | 12 | 20 | 9.8s |
| 1 active | 1 | 237 | 13.5 MiB | 8 (7/1) | 0 | 16 | 46 | 8.4s |
| 2 bound, idle | 2 | 239 | 19.6 MiB | 8 (7/1) | 0 | 16 | 46 | 2.5s |
| 2 active | 2 | 239 | 19.7 MiB | 8 (7/1) | 0 | 20 | 71 | 7.4s |
| 3 bound, idle | 3 | 241 | 19.7 MiB | 8 (7/1) | 0 | 20 | 71 | 2.5s |
| 3 active | 3 | 241 | 19.9 MiB | 8 (7/1) | 0 | 24 | 96 | 10.3s |
| 4 bound, idle | 4 | 243 | 20.0 MiB | 8 (7/1) | 0 | 24 | 96 | 2.5s |
| 4 active | 4 | 243 | 20.1 MiB | 8 (7/1) | 0 | 28 | 121 | 8.4s |
| 5 bound, idle | 5 | 245 | 20.1 MiB | 8 (7/1) | 0 | 28 | 121 | 2.5s |
| 5 active | 5 | 245 | 20.3 MiB | 8 (7/1) | 0 | 32 | 146 | 8.4s |
| 6 bound, idle | 6 | 247 | 20.3 MiB | 8 (7/1) | 0 | 32 | 146 | 2.5s |
| 6 active | 6 | 247 | 20.5 MiB | 8 (7/1) | 0 | 36 | 171 | 9.3s |
| 7 bound, idle | 7 | 249 | 20.5 MiB | 8 (7/1) | 0 | 36 | 171 | 2.9s |
| 7 active | 7 | 249 | 20.7 MiB | 8 (7/1) | 0 | 40 | 196 | 9.3s |
| 8 bound, idle | 8 | 251 | 20.7 MiB | 8 (7/1) | 0 | 40 | 196 | 6.2s |
| 8 active | 8 | 251 | 20.9 MiB | 8 (7/1) | 0 | 44 | 221 | 11.8s |
| 7 left | 7 | 249 | 20.8 MiB | 8 (7/1) | 0 | 44 | 230 | 3.5s |
| 6 left | 6 | 247 | 20.7 MiB | 8 (7/1) | 0 | 44 | 239 | 6.9s |
| 5 left | 5 | 245 | 20.7 MiB | 8 (7/1) | 0 | 44 | 248 | 5.3s |
| 4 left | 4 | 244 | 20.6 MiB | 8 (7/1) | 0 | 44 | 257 | 5.3s |
| 3 left | 3 | 241 | 20.5 MiB | 8 (7/1) | 0 | 44 | 266 | 5.3s |
| 2 left | 2 | 239 | 20.5 MiB | 8 (7/1) | 0 | 44 | 275 | 5.6s |
| 1 left | 1 | 237 | 20.4 MiB | 8 (7/1) | 0 | 44 | 284 | 5.3s |
| 0 left | 0 | 130 | 19.9 MiB | 8 (7/1) | 0 | 44 | 293 | 5.3s |

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
