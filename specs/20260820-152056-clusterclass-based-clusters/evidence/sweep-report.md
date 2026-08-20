# Active workspace sweep (one controller, one type)

| Condition | Value |
|---|---|
| discoveryRequestsPerWorkspace | 0.0 |
| eventHandlersPerWorkspace | 1 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 12.0 |
| goroutinesReclaimedPerWorkspace | -18.7 |
| goroutinesRetainedPerDepartedWorkspace | 2.0 |
| heapBytesPerWorkspace | 105114 |
| objectsPerWorkspace | 5 |
| reconciledTypes | cluster.x-k8s.io/clusters |
| secondsPerWorkspaceEngagement | -0.6 |
| shape | one controller watching cluster.x-k8s.io/clusters |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 1 |
| workspaces | 4 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 18 | 19.3 MiB | 0 (0/0) | 0 | 3 | 3 | 0.0s |
| 1 bound, idle | 1 | 74 | 19.5 MiB | 3 (2/1) | 0 | 7 | 10 | 8.6s |
| 1 active | 1 | 74 | 19.5 MiB | 3 (2/1) | 0 | 7 | 10 | 4.3s |
| 2 bound, idle | 2 | 86 | 19.6 MiB | 3 (2/1) | 0 | 7 | 10 | 2.5s |
| 2 active | 2 | 86 | 19.6 MiB | 3 (2/1) | 0 | 7 | 10 | 2.5s |
| 3 bound, idle | 3 | 98 | 19.7 MiB | 3 (2/1) | 0 | 7 | 10 | 3.6s |
| 3 active | 3 | 98 | 19.7 MiB | 3 (2/1) | 0 | 7 | 10 | 2.5s |
| 4 bound, idle | 4 | 110 | 19.8 MiB | 3 (2/1) | 0 | 7 | 10 | 2.5s |
| 4 active | 4 | 110 | 19.8 MiB | 3 (2/1) | 0 | 7 | 10 | 2.3s |
| 3 left | 3 | 100 | 19.8 MiB | 3 (2/1) | 0 | 7 | 10 | 10.0s |
| 2 left | 2 | 90 | 19.7 MiB | 3 (2/1) | 0 | 7 | 10 | 10.0s |
| 1 left | 1 | 80 | 19.8 MiB | 3 (2/1) | 0 | 7 | 10 | 10.0s |
| 0 left | 0 | 41 | 19.6 MiB | 3 (2/1) | 0 | 7 | 10 | 10.0s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 1 |
