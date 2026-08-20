# Active workspace sweep (the kubeadm bootstrap provider's deployment)

| Condition | Value |
|---|---|
| deployment | kubeadm-bootstrap-manager, one of four provider deployments |
| deploymentName | kubeadm-bootstrap-manager |
| discoveryRequestsPerWorkspace | 4.0 |
| endState | bootstrap data secret written |
| eventHandlersPerWorkspace | 3 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| goroutinesReclaimedPerWorkspace | -2.9 |
| goroutinesRetainedPerDepartedWorkspace | 0.0 |
| heapBytesPerWorkspace | 1155611 |
| objectsPerWorkspace | 1 |
| reconciledTypes | bootstrap.cluster.x-k8s.io/kubeadmconfigs |
| secondsPerWorkspaceEngagement | -0.0 |
| shape | bootstrapmanager.SetupFleetControllers: KubeadmConfig — one controller for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 3 |
| workspaces | 20 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 13 | 18.4 MiB | 0 (0/0) | 0 | 3 | 3 | 0.0s |
| 1 bound, idle | 1 | 120 | 19.1 MiB | 5 (4/1) | 0 | 8 | 13 | 9.0s |
| 1 active | 1 | 138 | 19.7 MiB | 7 (6/1) | 0 | 13 | 32 | 5.8s |
| 2 bound, idle | 2 | 140 | 19.8 MiB | 7 (6/1) | 0 | 13 | 32 | 2.5s |
| 2 active | 2 | 140 | 19.9 MiB | 7 (6/1) | 0 | 17 | 48 | 7.9s |
| 3 bound, idle | 3 | 142 | 20.0 MiB | 7 (6/1) | 0 | 17 | 48 | 2.6s |
| 3 active | 3 | 142 | 20.2 MiB | 7 (6/1) | 0 | 21 | 64 | 4.8s |
| 4 bound, idle | 4 | 144 | 20.2 MiB | 7 (6/1) | 0 | 21 | 64 | 2.5s |
| 4 active | 4 | 144 | 20.4 MiB | 7 (6/1) | 0 | 25 | 80 | 4.8s |
| 5 bound, idle | 5 | 146 | 20.4 MiB | 7 (6/1) | 0 | 25 | 80 | 2.5s |
| 5 active | 5 | 146 | 20.6 MiB | 7 (6/1) | 0 | 29 | 96 | 4.8s |
| 6 bound, idle | 6 | 148 | 20.6 MiB | 7 (6/1) | 0 | 29 | 96 | 2.5s |
| 6 active | 6 | 148 | 20.8 MiB | 7 (6/1) | 0 | 33 | 112 | 4.8s |
| 7 bound, idle | 7 | 150 | 20.8 MiB | 7 (6/1) | 0 | 33 | 112 | 2.6s |
| 7 active | 7 | 150 | 21.0 MiB | 7 (6/1) | 0 | 37 | 128 | 5.4s |
| 8 bound, idle | 8 | 152 | 21.0 MiB | 7 (6/1) | 0 | 37 | 128 | 2.6s |
| 8 active | 8 | 152 | 21.2 MiB | 7 (6/1) | 0 | 41 | 144 | 4.8s |
| 9 bound, idle | 9 | 154 | 21.2 MiB | 7 (6/1) | 0 | 41 | 144 | 2.5s |
| 9 active | 9 | 154 | 21.4 MiB | 7 (6/1) | 0 | 45 | 160 | 5.4s |
| 10 bound, idle | 10 | 156 | 21.4 MiB | 7 (6/1) | 0 | 45 | 160 | 2.5s |
| 10 active | 10 | 156 | 21.6 MiB | 7 (6/1) | 0 | 49 | 176 | 4.8s |
| 11 bound, idle | 11 | 158 | 21.6 MiB | 7 (6/1) | 0 | 49 | 176 | 5.4s |
| 11 active | 11 | 158 | 21.8 MiB | 7 (6/1) | 0 | 53 | 192 | 4.8s |
| 12 bound, idle | 12 | 160 | 21.9 MiB | 7 (6/1) | 0 | 53 | 192 | 2.5s |
| 12 active | 12 | 160 | 34.0 MiB | 7 (6/1) | 0 | 57 | 208 | 5.4s |
| 13 bound, idle | 13 | 162 | 34.1 MiB | 7 (6/1) | 0 | 57 | 208 | 2.6s |
| 13 active | 13 | 162 | 34.2 MiB | 7 (6/1) | 0 | 61 | 224 | 4.8s |
| 14 bound, idle | 14 | 164 | 34.3 MiB | 7 (6/1) | 0 | 61 | 224 | 2.6s |
| 14 active | 14 | 164 | 34.4 MiB | 7 (6/1) | 0 | 65 | 240 | 6.9s |
| 15 bound, idle | 15 | 166 | 34.5 MiB | 7 (6/1) | 0 | 65 | 240 | 3.1s |
| 15 active | 15 | 166 | 34.6 MiB | 7 (6/1) | 0 | 69 | 256 | 4.9s |
| 16 bound, idle | 16 | 168 | 34.7 MiB | 7 (6/1) | 0 | 69 | 256 | 2.6s |
| 16 active | 16 | 168 | 34.9 MiB | 7 (6/1) | 0 | 73 | 272 | 4.8s |
| 17 bound, idle | 17 | 170 | 34.9 MiB | 7 (6/1) | 0 | 73 | 272 | 2.6s |
| 17 active | 17 | 170 | 35.1 MiB | 7 (6/1) | 0 | 77 | 288 | 5.4s |
| 18 bound, idle | 18 | 172 | 35.1 MiB | 7 (6/1) | 0 | 77 | 288 | 2.6s |
| 18 active | 18 | 172 | 35.3 MiB | 7 (6/1) | 0 | 81 | 304 | 4.8s |
| 19 bound, idle | 19 | 174 | 35.4 MiB | 7 (6/1) | 0 | 81 | 304 | 2.6s |
| 19 active | 19 | 174 | 35.5 MiB | 7 (6/1) | 0 | 85 | 320 | 4.8s |
| 20 bound, idle | 20 | 176 | 35.6 MiB | 7 (6/1) | 0 | 85 | 320 | 2.6s |
| 20 active | 20 | 176 | 35.7 MiB | 7 (6/1) | 0 | 89 | 336 | 5.4s |
| 19 left | 19 | 174 | 35.7 MiB | 7 (6/1) | 0 | 89 | 336 | 16.3s |
| 18 left | 18 | 172 | 35.6 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 17 left | 17 | 170 | 35.6 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 16 left | 16 | 168 | 35.5 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 15 left | 15 | 166 | 35.5 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 14 left | 14 | 164 | 35.5 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 13 left | 13 | 162 | 35.4 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 12 left | 12 | 160 | 35.4 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 11 left | 11 | 158 | 35.4 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 10 left | 10 | 156 | 35.3 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 9 left | 9 | 154 | 35.3 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 8 left | 8 | 152 | 35.3 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 7 left | 7 | 150 | 35.3 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 6 left | 6 | 148 | 35.2 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 5 left | 5 | 146 | 35.1 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 4 left | 4 | 144 | 35.1 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 3 left | 3 | 142 | 35.1 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 2 left | 2 | 140 | 35.0 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |
| 1 left | 1 | 138 | 35.0 MiB | 7 (6/1) | 0 | 89 | 336 | 10.3s |
| 0 left | 0 | 71 | 34.6 MiB | 7 (6/1) | 0 | 89 | 336 | 10.2s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | /configmaps | 1 |
| watch-list | * | /secrets | 1 |
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | bootstrap.cluster.x-k8s.io/kubeadmconfigs | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 1 |
