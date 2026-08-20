# Active workspace sweep (the kubeadm control plane provider's deployment)

| Condition | Value |
|---|---|
| deployment | kubeadm-control-plane-manager, one of four provider deployments |
| deploymentName | kubeadm-control-plane-manager |
| discoveryRequestsPerWorkspace | 7.0 |
| endState | certificates written and the first replica's Machine created |
| eventHandlersPerWorkspace | 3 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 2.0 |
| goroutinesReclaimedPerWorkspace | -2.9 |
| goroutinesRetainedPerDepartedWorkspace | -0.1 |
| heapBytesPerWorkspace | 1034595 |
| objectsPerWorkspace | 1 |
| reconciledTypes | controlplane.cluster.x-k8s.io/kubeadmcontrolplanes |
| secondsPerWorkspaceEngagement | -0.1 |
| shape | controlplanemanager.SetupFleetControllers: KubeadmControlPlane — one controller for the whole shard |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 3 |
| workspaces | 20 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 15 | 21.7 MiB | 0 (0/0) | 0 | 3 | 3 | 0.0s |
| 1 bound, idle | 1 | 122 | 22.0 MiB | 5 (4/1) | 0 | 8 | 13 | 10.3s |
| 1 active | 1 | 149 | 22.6 MiB | 7 (6/1) | 0 | 21 | 71 | 10.7s |
| 2 bound, idle | 2 | 151 | 22.7 MiB | 7 (6/1) | 0 | 21 | 77 | 2.6s |
| 2 active | 2 | 151 | 22.9 MiB | 7 (6/1) | 0 | 28 | 126 | 7.6s |
| 3 bound, idle | 3 | 153 | 23.0 MiB | 7 (6/1) | 0 | 28 | 132 | 2.6s |
| 3 active | 3 | 153 | 23.2 MiB | 7 (6/1) | 0 | 35 | 196 | 10.6s |
| 4 bound, idle | 4 | 155 | 23.3 MiB | 7 (6/1) | 0 | 35 | 211 | 6.1s |
| 4 active | 4 | 155 | 23.5 MiB | 7 (6/1) | 0 | 42 | 269 | 10.6s |
| 5 bound, idle | 5 | 157 | 23.5 MiB | 7 (6/1) | 0 | 42 | 275 | 2.5s |
| 5 active | 5 | 158 | 23.9 MiB | 7 (6/1) | 0 | 49 | 337 | 11.0s |
| 6 bound, idle | 6 | 159 | 23.8 MiB | 7 (6/1) | 0 | 49 | 345 | 2.6s |
| 6 active | 6 | 159 | 36.1 MiB | 7 (6/1) | 0 | 56 | 394 | 7.1s |
| 7 bound, idle | 7 | 161 | 36.1 MiB | 7 (6/1) | 0 | 56 | 412 | 4.3s |
| 7 active | 7 | 161 | 36.4 MiB | 7 (6/1) | 0 | 63 | 479 | 12.4s |
| 8 bound, idle | 8 | 163 | 36.4 MiB | 7 (6/1) | 0 | 63 | 485 | 2.5s |
| 8 active | 8 | 163 | 36.7 MiB | 7 (6/1) | 0 | 70 | 540 | 8.4s |
| 9 bound, idle | 9 | 165 | 36.7 MiB | 7 (6/1) | 0 | 70 | 555 | 3.4s |
| 9 active | 9 | 165 | 37.0 MiB | 7 (6/1) | 0 | 77 | 631 | 13.2s |
| 10 bound, idle | 10 | 167 | 37.1 MiB | 7 (6/1) | 0 | 77 | 634 | 2.6s |
| 10 active | 10 | 167 | 37.3 MiB | 7 (6/1) | 0 | 84 | 683 | 6.4s |
| 11 bound, idle | 11 | 169 | 37.4 MiB | 7 (6/1) | 0 | 84 | 692 | 2.6s |
| 11 active | 11 | 169 | 37.6 MiB | 7 (6/1) | 0 | 91 | 771 | 11.9s |
| 12 bound, idle | 12 | 171 | 37.7 MiB | 7 (6/1) | 0 | 91 | 774 | 2.6s |
| 12 active | 12 | 171 | 37.9 MiB | 7 (6/1) | 0 | 98 | 823 | 6.5s |
| 13 bound, idle | 13 | 173 | 38.0 MiB | 7 (6/1) | 0 | 98 | 835 | 2.6s |
| 13 active | 13 | 173 | 38.2 MiB | 7 (6/1) | 0 | 105 | 908 | 10.6s |
| 14 bound, idle | 14 | 175 | 38.3 MiB | 7 (6/1) | 0 | 105 | 923 | 4.6s |
| 14 active | 14 | 175 | 38.5 MiB | 7 (6/1) | 0 | 112 | 972 | 8.1s |
| 15 bound, idle | 15 | 177 | 38.6 MiB | 7 (6/1) | 0 | 112 | 993 | 3.7s |
| 15 active | 15 | 177 | 38.8 MiB | 7 (6/1) | 0 | 119 | 1042 | 6.4s |
| 16 bound, idle | 16 | 179 | 38.9 MiB | 7 (6/1) | 0 | 119 | 1057 | 3.1s |
| 16 active | 16 | 179 | 39.2 MiB | 7 (6/1) | 0 | 126 | 1124 | 8.7s |
| 17 bound, idle | 17 | 181 | 39.3 MiB | 7 (6/1) | 0 | 126 | 1157 | 11.2s |
| 17 active | 17 | 181 | 39.5 MiB | 7 (6/1) | 0 | 133 | 1212 | 9.7s |
| 18 bound, idle | 18 | 183 | 39.6 MiB | 7 (6/1) | 0 | 133 | 1221 | 2.6s |
| 18 active | 18 | 183 | 39.9 MiB | 7 (6/1) | 0 | 140 | 1279 | 7.4s |
| 19 bound, idle | 19 | 185 | 39.9 MiB | 7 (6/1) | 0 | 140 | 1291 | 3.3s |
| 19 active | 19 | 185 | 40.2 MiB | 7 (6/1) | 0 | 147 | 1361 | 10.6s |
| 20 bound, idle | 20 | 187 | 40.3 MiB | 7 (6/1) | 0 | 147 | 1385 | 7.2s |
| 20 active | 20 | 187 | 40.5 MiB | 7 (6/1) | 0 | 154 | 1431 | 6.9s |
| 19 left | 19 | 185 | 40.4 MiB | 7 (6/1) | 0 | 154 | 1484 | 20.9s |
| 18 left | 18 | 183 | 40.3 MiB | 7 (6/1) | 0 | 154 | 1504 | 11.5s |
| 17 left | 17 | 181 | 40.2 MiB | 7 (6/1) | 0 | 154 | 1518 | 11.6s |
| 16 left | 16 | 179 | 40.2 MiB | 7 (6/1) | 0 | 154 | 1536 | 11.6s |
| 15 left | 15 | 177 | 40.1 MiB | 7 (6/1) | 0 | 154 | 1550 | 11.5s |
| 14 left | 14 | 175 | 40.0 MiB | 7 (6/1) | 0 | 154 | 1564 | 11.6s |
| 13 left | 13 | 173 | 39.9 MiB | 7 (6/1) | 0 | 154 | 1575 | 11.5s |
| 12 left | 12 | 171 | 39.8 MiB | 7 (6/1) | 0 | 154 | 1589 | 11.5s |
| 11 left | 11 | 169 | 39.8 MiB | 7 (6/1) | 0 | 154 | 1601 | 11.5s |
| 10 left | 10 | 167 | 39.6 MiB | 7 (6/1) | 0 | 154 | 1613 | 11.5s |
| 9 left | 9 | 165 | 39.6 MiB | 7 (6/1) | 0 | 154 | 1622 | 11.5s |
| 8 left | 8 | 163 | 39.5 MiB | 7 (6/1) | 0 | 154 | 1634 | 11.6s |
| 7 left | 7 | 161 | 39.4 MiB | 7 (6/1) | 0 | 154 | 1649 | 11.5s |
| 6 left | 6 | 159 | 39.3 MiB | 7 (6/1) | 0 | 154 | 1657 | 11.5s |
| 5 left | 5 | 157 | 39.3 MiB | 7 (6/1) | 0 | 154 | 1669 | 11.5s |
| 4 left | 4 | 155 | 39.2 MiB | 7 (6/1) | 0 | 154 | 1677 | 11.6s |
| 3 left | 3 | 153 | 39.1 MiB | 7 (6/1) | 0 | 154 | 1686 | 11.6s |
| 2 left | 2 | 151 | 39.0 MiB | 7 (6/1) | 0 | 154 | 1694 | 11.6s |
| 1 left | 1 | 149 | 38.9 MiB | 7 (6/1) | 0 | 154 | 1702 | 11.6s |
| 0 left | 0 | 82 | 38.4 MiB | 7 (6/1) | 0 | 154 | 1710 | 11.5s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | /secrets | 1 |
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | bootstrap.cluster.x-k8s.io/kubeadmconfigs | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 2 |
| watch-list | * | controlplane.cluster.x-k8s.io/kubeadmcontrolplanes | 1 |
