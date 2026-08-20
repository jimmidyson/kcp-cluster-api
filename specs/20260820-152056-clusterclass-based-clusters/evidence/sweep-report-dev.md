# Active workspace sweep (the dev infrastructure provider's deployment)

| Condition | Value |
|---|---|
| deployment | dev-infrastructure-manager, one of four provider deployments |
| deploymentName | dev-infrastructure-manager |
| devClusterBackend | inMemory |
| discoveryRequestsPerWorkspace | 3.0 |
| endState | infrastructure provisioned |
| eventHandlersPerWorkspace | 6 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| goroutinesReclaimedPerWorkspace | -2.9 |
| goroutinesRetainedPerDepartedWorkspace | 0.0 |
| heapBytesPerWorkspace | 1019119 |
| objectsPerWorkspace | 1 |
| reconciledTypes | infrastructure.cluster.x-k8s.io/devclusters |
| secondsPerWorkspaceEngagement | -0.0 |
| shape | coremanager.SetupDevInfrastructureControllers: DevCluster, DevMachine — one controller each for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 4 |
| workspaces | 20 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 23 | 23.4 MiB | 0 (0/0) | 0 | 6 | 6 | 0.0s |
| 1 bound, idle | 1 | 163 | 23.8 MiB | 6 (5/1) | 0 | 11 | 17 | 9.3s |
| 1 active | 1 | 163 | 24.0 MiB | 6 (5/1) | 0 | 14 | 25 | 4.7s |
| 2 bound, idle | 2 | 165 | 24.0 MiB | 6 (5/1) | 0 | 14 | 25 | 2.7s |
| 2 active | 2 | 165 | 24.1 MiB | 6 (5/1) | 0 | 17 | 33 | 6.7s |
| 3 bound, idle | 3 | 167 | 24.2 MiB | 6 (5/1) | 0 | 17 | 33 | 2.6s |
| 3 active | 3 | 167 | 24.3 MiB | 6 (5/1) | 0 | 20 | 41 | 4.6s |
| 4 bound, idle | 4 | 169 | 24.3 MiB | 6 (5/1) | 0 | 20 | 41 | 4.2s |
| 4 active | 4 | 169 | 24.4 MiB | 6 (5/1) | 0 | 23 | 49 | 4.3s |
| 5 bound, idle | 5 | 171 | 24.5 MiB | 6 (5/1) | 0 | 23 | 49 | 2.6s |
| 5 active | 5 | 171 | 24.6 MiB | 6 (5/1) | 0 | 26 | 57 | 5.4s |
| 6 bound, idle | 6 | 173 | 24.6 MiB | 6 (5/1) | 0 | 26 | 57 | 2.6s |
| 6 active | 6 | 173 | 24.7 MiB | 6 (5/1) | 0 | 29 | 65 | 4.3s |
| 7 bound, idle | 7 | 175 | 24.8 MiB | 6 (5/1) | 0 | 29 | 65 | 2.6s |
| 7 active | 7 | 175 | 24.9 MiB | 6 (5/1) | 0 | 32 | 73 | 6.4s |
| 8 bound, idle | 8 | 177 | 24.9 MiB | 6 (5/1) | 0 | 32 | 73 | 2.5s |
| 8 active | 8 | 177 | 25.0 MiB | 6 (5/1) | 0 | 35 | 81 | 4.3s |
| 9 bound, idle | 9 | 179 | 25.1 MiB | 6 (5/1) | 0 | 35 | 81 | 2.6s |
| 9 active | 9 | 179 | 25.2 MiB | 6 (5/1) | 0 | 38 | 89 | 4.3s |
| 10 bound, idle | 10 | 181 | 25.2 MiB | 6 (5/1) | 0 | 38 | 89 | 2.6s |
| 10 active | 10 | 181 | 25.3 MiB | 6 (5/1) | 0 | 41 | 97 | 5.4s |
| 11 bound, idle | 11 | 183 | 25.4 MiB | 6 (5/1) | 0 | 41 | 97 | 4.3s |
| 11 active | 11 | 183 | 25.4 MiB | 6 (5/1) | 0 | 44 | 105 | 4.3s |
| 12 bound, idle | 12 | 185 | 25.5 MiB | 6 (5/1) | 0 | 44 | 105 | 2.6s |
| 12 active | 12 | 185 | 25.6 MiB | 6 (5/1) | 0 | 47 | 113 | 4.3s |
| 13 bound, idle | 13 | 187 | 25.7 MiB | 6 (5/1) | 0 | 47 | 113 | 2.6s |
| 13 active | 13 | 187 | 25.7 MiB | 6 (5/1) | 0 | 50 | 121 | 4.3s |
| 14 bound, idle | 14 | 189 | 37.8 MiB | 6 (5/1) | 0 | 50 | 121 | 3.3s |
| 14 active | 14 | 189 | 37.9 MiB | 6 (5/1) | 0 | 53 | 129 | 4.3s |
| 15 bound, idle | 15 | 191 | 37.9 MiB | 6 (5/1) | 0 | 53 | 129 | 2.6s |
| 15 active | 15 | 191 | 38.1 MiB | 6 (5/1) | 0 | 56 | 137 | 6.1s |
| 16 bound, idle | 16 | 193 | 38.1 MiB | 6 (5/1) | 0 | 56 | 137 | 2.6s |
| 16 active | 16 | 193 | 38.2 MiB | 6 (5/1) | 0 | 59 | 145 | 4.3s |
| 17 bound, idle | 17 | 195 | 38.3 MiB | 6 (5/1) | 0 | 59 | 145 | 2.6s |
| 17 active | 17 | 195 | 38.4 MiB | 6 (5/1) | 0 | 62 | 153 | 4.3s |
| 18 bound, idle | 18 | 197 | 38.4 MiB | 6 (5/1) | 0 | 62 | 153 | 4.4s |
| 18 active | 18 | 197 | 38.5 MiB | 6 (5/1) | 0 | 65 | 161 | 4.3s |
| 19 bound, idle | 19 | 199 | 38.6 MiB | 6 (5/1) | 0 | 65 | 161 | 2.6s |
| 19 active | 19 | 199 | 38.7 MiB | 6 (5/1) | 0 | 68 | 169 | 4.3s |
| 20 bound, idle | 20 | 201 | 38.7 MiB | 6 (5/1) | 0 | 68 | 169 | 2.6s |
| 20 active | 20 | 201 | 38.8 MiB | 6 (5/1) | 0 | 71 | 177 | 4.3s |
| 19 left | 19 | 199 | 38.8 MiB | 6 (5/1) | 0 | 71 | 179 | 17.5s |
| 18 left | 18 | 197 | 38.8 MiB | 6 (5/1) | 0 | 71 | 181 | 10.6s |
| 17 left | 17 | 195 | 38.8 MiB | 6 (5/1) | 0 | 71 | 183 | 10.5s |
| 16 left | 16 | 193 | 38.8 MiB | 6 (5/1) | 0 | 71 | 185 | 10.5s |
| 15 left | 15 | 191 | 38.8 MiB | 6 (5/1) | 0 | 71 | 187 | 10.6s |
| 14 left | 14 | 189 | 38.8 MiB | 6 (5/1) | 0 | 71 | 189 | 10.6s |
| 13 left | 13 | 187 | 38.8 MiB | 6 (5/1) | 0 | 71 | 191 | 10.5s |
| 12 left | 12 | 185 | 38.8 MiB | 6 (5/1) | 0 | 71 | 193 | 10.5s |
| 11 left | 11 | 183 | 38.8 MiB | 6 (5/1) | 0 | 71 | 195 | 10.5s |
| 10 left | 10 | 181 | 38.8 MiB | 6 (5/1) | 0 | 71 | 197 | 10.3s |
| 9 left | 9 | 179 | 38.8 MiB | 6 (5/1) | 0 | 71 | 199 | 10.3s |
| 8 left | 8 | 177 | 38.8 MiB | 6 (5/1) | 0 | 71 | 201 | 10.5s |
| 7 left | 7 | 175 | 38.8 MiB | 6 (5/1) | 0 | 71 | 203 | 10.5s |
| 6 left | 6 | 173 | 38.8 MiB | 6 (5/1) | 0 | 71 | 205 | 10.5s |
| 5 left | 5 | 171 | 38.8 MiB | 6 (5/1) | 0 | 71 | 207 | 10.5s |
| 4 left | 4 | 169 | 38.8 MiB | 6 (5/1) | 0 | 71 | 209 | 10.5s |
| 3 left | 3 | 167 | 38.8 MiB | 6 (5/1) | 0 | 71 | 211 | 10.5s |
| 2 left | 2 | 165 | 38.8 MiB | 6 (5/1) | 0 | 71 | 213 | 10.5s |
| 1 left | 1 | 163 | 38.8 MiB | 6 (5/1) | 0 | 71 | 215 | 10.3s |
| 0 left | 0 | 99 | 38.6 MiB | 6 (5/1) | 0 | 71 | 217 | 10.5s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 1 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devclusters | 1 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devmachines | 1 |
