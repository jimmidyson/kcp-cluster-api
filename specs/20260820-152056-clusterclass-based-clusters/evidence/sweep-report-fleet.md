# Active workspace sweep (every provider's controllers, one process)

| Condition | Value |
|---|---|
| clusterShape | ClusterClass based: one class per workspace, each Cluster naming it |
| deployment | none — four deployments co-located, so one engagement per workspace rather than four |
| devClusterBackend | inMemory |
| discoveryRequestsPerWorkspace | 7.0 |
| endState | control plane initialized |
| eventHandlersPerWorkspace | 41 |
| goMaxProcs | 4 |
| goVersion | go1.26.3 |
| goroutinesPerWorkspace | 57.0 |
| goroutinesReclaimedPerWorkspace | -183.5 |
| goroutinesRetainedPerDepartedWorkspace | 0.0 |
| heapBytesPerWorkspace | 843252 |
| objectsPerWorkspace | 1 |
| reconciledTypes | clusterclasses, clusters, machines, machinesets, machinedeployments, kubeadmconfigs, kubeadmcontrolplanes, devclusters, devmachines |
| secondsPerWorkspaceEngagement | 0.1 |
| shape | every provider's controllers on one fleet: core, bootstrap, control plane, dev infrastructure |
| watchStreamsPerWorkspace | 0.00 |
| watchedTypesPerWorkspace | 11 |
| workspaces | 3 |

| Step | Workspaces | Goroutines | Heap | Watch streams (wildcard/scoped) | Lists | Discovery | Requests | Step time |
|---|--:|--:|--:|--:|--:|--:|--:|--:|
| baseline (manager not started) | 0 | 44 | 16.7 MiB | 0 (0/0) | 0 | 7 | 7 | 0.0s |
| 1 bound, idle | 1 | 476 | 17.9 MiB | 12 (11/1) | 0 | 14 | 26 | 10.8s |
| 1 active | 1 | 624 | 27.2 MiB | 15 (14/1) | 0 | 23 | 472 | 44.4s |
| 2 bound, idle | 2 | 626 | 27.3 MiB | 15 (14/1) | 0 | 23 | 472 | 2.6s |
| 2 active | 2 | 681 | 28.8 MiB | 15 (14/1) | 0 | 30 | 967 | 44.6s |
| 3 bound, idle | 3 | 683 | 28.4 MiB | 15 (14/1) | 0 | 30 | 967 | 2.6s |
| 3 active | 3 | 738 | 28.9 MiB | 15 (14/1) | 0 | 37 | 1440 | 44.6s |
| 2 left | 2 | 681 | 28.5 MiB | 15 (14/1) | 0 | 37 | 1514 | 17.8s |
| 1 left | 1 | 624 | 27.9 MiB | 15 (14/1) | 0 | 37 | 1587 | 17.8s |
| 0 left | 0 | 314 | 25.9 MiB | 15 (14/1) | 0 | 37 | 1660 | 17.7s |

Every stream open at the widest point of the sweep:

| Verb | Logical cluster | Resource | Requests |
|---|---|---|--:|
| watch-list | * | /configmaps | 1 |
| watch-list | * | /secrets | 1 |
| watch-list | * | apis.kcp.io/apibindings | 1 |
| watch-list | root | apis.kcp.io/apiexportendpointslices | 1 |
| watch-list | * | bootstrap.cluster.x-k8s.io/kubeadmconfigs | 2 |
| watch-list | * | cluster.x-k8s.io/clusterclasses | 1 |
| watch-list | * | cluster.x-k8s.io/clusters | 2 |
| watch-list | * | cluster.x-k8s.io/machinedeployments | 1 |
| watch-list | * | cluster.x-k8s.io/machinehealthchecks | 1 |
| watch-list | * | cluster.x-k8s.io/machinepools | 1 |
| watch-list | * | cluster.x-k8s.io/machines | 2 |
| watch-list | * | cluster.x-k8s.io/machinesets | 1 |
| watch-list | * | controlplane.cluster.x-k8s.io/kubeadmcontrolplanes | 2 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devclusters | 2 |
| watch-list | * | infrastructure.cluster.x-k8s.io/devmachines | 2 |
