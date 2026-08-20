# Active workspace sweep (the core provider's deployment)

| Condition | Value |
|---|---|
| deployment | core-manager, one of four provider deployments |
| deploymentName | core-manager |
| discoveryRequestsPerWorkspace | 3.0 |
| eventHandlersPerWorkspace | 28 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| goroutinesReclaimedPerWorkspace | -3.7 |
| goroutinesRetainedPerDepartedWorkspace | 0.0 |
| heapBytesPerWorkspace | 525182 |
| objectsPerWorkspace | 1 |
| reconciledTypes | cluster.x-k8s.io/clusters |
| secondsPerWorkspaceEngagement | 0.1 |
| shape | coremanager.SetupCoreControllers: ClusterCache, Cluster, Machine, MachineSet, MachineDeployment, ClusterClass and the three topology controllers — one controller each for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 6 |
| workspaces | 20 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 29 | 22.0 MiB | 0 (0/0) | 0 | 6 | 6 | 0.0s |
| 1 bound, idle | 1 | 329 | 23.1 MiB | 8 (7/1) | 0 | 10 | 18 | 9.0s |
| 1 active | 1 | 329 | 23.3 MiB | 8 (7/1) | 0 | 13 | 25 | 2.9s |
| 2 bound, idle | 2 | 331 | 23.3 MiB | 8 (7/1) | 0 | 13 | 25 | 2.6s |
| 2 active | 2 | 331 | 23.5 MiB | 8 (7/1) | 0 | 16 | 32 | 4.1s |
| 3 bound, idle | 3 | 333 | 23.5 MiB | 8 (7/1) | 0 | 16 | 32 | 2.6s |
| 3 active | 3 | 333 | 23.7 MiB | 8 (7/1) | 0 | 19 | 39 | 2.5s |
| 4 bound, idle | 4 | 335 | 23.7 MiB | 8 (7/1) | 0 | 19 | 39 | 4.7s |
| 4 active | 4 | 335 | 23.8 MiB | 8 (7/1) | 0 | 22 | 46 | 2.5s |
| 5 bound, idle | 5 | 337 | 23.9 MiB | 8 (7/1) | 0 | 22 | 46 | 2.6s |
| 5 active | 5 | 337 | 24.0 MiB | 8 (7/1) | 0 | 25 | 53 | 2.5s |
| 6 bound, idle | 6 | 339 | 24.0 MiB | 8 (7/1) | 0 | 25 | 53 | 2.6s |
| 6 active | 6 | 339 | 24.2 MiB | 8 (7/1) | 0 | 28 | 60 | 4.0s |
| 7 bound, idle | 7 | 341 | 24.2 MiB | 8 (7/1) | 0 | 28 | 60 | 2.6s |
| 7 active | 7 | 341 | 24.3 MiB | 8 (7/1) | 0 | 31 | 67 | 3.7s |
| 8 bound, idle | 8 | 343 | 24.4 MiB | 8 (7/1) | 0 | 31 | 67 | 2.6s |
| 8 active | 8 | 343 | 24.5 MiB | 8 (7/1) | 0 | 34 | 74 | 4.2s |
| 9 bound, idle | 9 | 345 | 24.6 MiB | 8 (7/1) | 0 | 34 | 74 | 2.6s |
| 9 active | 9 | 345 | 24.7 MiB | 8 (7/1) | 0 | 37 | 81 | 5.6s |
| 10 bound, idle | 10 | 347 | 24.7 MiB | 8 (7/1) | 0 | 37 | 81 | 2.6s |
| 10 active | 10 | 347 | 24.9 MiB | 8 (7/1) | 0 | 40 | 88 | 2.5s |
| 11 bound, idle | 11 | 349 | 24.9 MiB | 8 (7/1) | 0 | 40 | 88 | 2.6s |
| 11 active | 11 | 349 | 25.0 MiB | 8 (7/1) | 0 | 43 | 95 | 4.0s |
| 12 bound, idle | 12 | 351 | 25.1 MiB | 8 (7/1) | 0 | 43 | 95 | 2.6s |
| 12 active | 12 | 351 | 25.2 MiB | 8 (7/1) | 0 | 46 | 102 | 5.2s |
| 13 bound, idle | 13 | 353 | 25.3 MiB | 8 (7/1) | 0 | 46 | 102 | 2.6s |
| 13 active | 13 | 353 | 25.4 MiB | 8 (7/1) | 0 | 49 | 109 | 3.9s |
| 14 bound, idle | 14 | 355 | 25.4 MiB | 8 (7/1) | 0 | 49 | 109 | 2.6s |
| 14 active | 14 | 355 | 25.6 MiB | 8 (7/1) | 0 | 52 | 116 | 3.8s |
| 15 bound, idle | 15 | 357 | 25.6 MiB | 8 (7/1) | 0 | 52 | 116 | 2.6s |
| 15 active | 15 | 357 | 25.7 MiB | 8 (7/1) | 0 | 55 | 123 | 2.6s |
| 16 bound, idle | 16 | 359 | 25.8 MiB | 8 (7/1) | 0 | 55 | 123 | 2.6s |
| 16 active | 16 | 359 | 25.9 MiB | 8 (7/1) | 0 | 58 | 130 | 2.5s |
| 17 bound, idle | 17 | 361 | 26.0 MiB | 8 (7/1) | 0 | 58 | 130 | 2.6s |
| 17 active | 17 | 361 | 26.1 MiB | 8 (7/1) | 0 | 61 | 137 | 4.0s |
| 18 bound, idle | 18 | 363 | 26.2 MiB | 8 (7/1) | 0 | 61 | 137 | 2.8s |
| 18 active | 18 | 363 | 26.3 MiB | 8 (7/1) | 0 | 64 | 144 | 4.5s |
| 19 bound, idle | 19 | 365 | 38.3 MiB | 8 (7/1) | 0 | 64 | 144 | 2.6s |
| 19 active | 19 | 365 | 38.5 MiB | 8 (7/1) | 0 | 67 | 151 | 4.4s |
| 20 bound, idle | 20 | 367 | 38.6 MiB | 8 (7/1) | 0 | 67 | 151 | 2.6s |
| 20 active | 20 | 367 | 38.7 MiB | 8 (7/1) | 0 | 70 | 158 | 4.3s |
| 19 left | 19 | 365 | 38.6 MiB | 8 (7/1) | 0 | 70 | 161 | 11.3s |
| 18 left | 18 | 363 | 38.6 MiB | 8 (7/1) | 0 | 70 | 164 | 13.0s |
| 17 left | 17 | 361 | 38.6 MiB | 8 (7/1) | 0 | 70 | 167 | 13.1s |
| 16 left | 16 | 359 | 38.6 MiB | 8 (7/1) | 0 | 70 | 170 | 13.0s |
| 15 left | 15 | 357 | 38.6 MiB | 8 (7/1) | 0 | 70 | 173 | 13.1s |
| 14 left | 14 | 355 | 38.6 MiB | 8 (7/1) | 0 | 70 | 176 | 13.0s |
| 13 left | 13 | 353 | 38.6 MiB | 8 (7/1) | 0 | 70 | 179 | 13.0s |
| 12 left | 12 | 351 | 38.6 MiB | 8 (7/1) | 0 | 70 | 182 | 13.2s |
| 11 left | 11 | 349 | 38.6 MiB | 8 (7/1) | 0 | 70 | 185 | 13.0s |
| 10 left | 10 | 347 | 38.6 MiB | 8 (7/1) | 0 | 70 | 188 | 13.0s |
| 9 left | 9 | 345 | 38.5 MiB | 8 (7/1) | 0 | 70 | 191 | 13.0s |
| 8 left | 8 | 343 | 38.5 MiB | 8 (7/1) | 0 | 70 | 194 | 13.0s |
| 7 left | 7 | 341 | 38.5 MiB | 8 (7/1) | 0 | 70 | 197 | 13.0s |
| 6 left | 6 | 339 | 38.5 MiB | 8 (7/1) | 0 | 70 | 200 | 13.1s |
| 5 left | 5 | 337 | 38.5 MiB | 8 (7/1) | 0 | 70 | 203 | 13.1s |
| 4 left | 4 | 335 | 38.5 MiB | 8 (7/1) | 0 | 70 | 207 | 13.0s |
| 3 left | 3 | 333 | 38.5 MiB | 8 (7/1) | 0 | 70 | 212 | 15.5s |
| 2 left | 2 | 331 | 38.5 MiB | 8 (7/1) | 0 | 70 | 215 | 13.0s |
| 1 left | 1 | 329 | 38.5 MiB | 8 (7/1) | 0 | 70 | 218 | 13.0s |
| 0 left | 0 | 209 | 37.8 MiB | 8 (7/1) | 0 | 70 | 221 | 10.2s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | cluster.x-k8s.io/clusterclasses | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
| watch-list | * | cluster.x-k8s.io/machinedeployments | 1 |
| watch-list | * | cluster.x-k8s.io/machinepools | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 1 |
| watch-list | * | cluster.x-k8s.io/machinesets | 1 |
