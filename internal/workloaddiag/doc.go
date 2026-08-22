/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package workloaddiag reports why a workload cluster is not ready, from
// inside the workload cluster.
//
// Everything else this project reports is the management side: the Cluster,
// the KubeadmControlPlane, the Machine, and the conditions Cluster API sets on
// them. That is the right altitude until the answer is "the Node is NotReady",
// at which point the management side has said everything it knows and the
// reason is in the workload cluster — a CNI DaemonSet whose pod never ran, a
// container restarting, a kubelet that never saw a network configuration.
//
// It exists because a run that ends there leaves nothing behind. The suite
// that hit it in CI reported a Node stuck at "cni plugin not initialized"
// after its CNI DaemonSet had reported its pods ready, and the clusters were
// torn down with the log, the pod and the node's own filesystem unread — so
// the failure could be described and not diagnosed.
//
// Collection is best effort: a failure to read one part is recorded on the
// report and the rest is still returned. A partial account of a cluster that
// is about to be torn down is worth more than an error about the part that
// was missing.
package workloaddiag
