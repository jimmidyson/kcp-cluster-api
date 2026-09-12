/*
Copyright 2025 The Kubernetes Authors.

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

// Package workloadsim fakes the workload cluster behind any Cluster API
// infrastructure provider, so that a management cluster can be driven to
// Machines in the Running phase with no real nodes.
//
// It is the provider-agnostic half of what the docker/dev provider's in-memory
// backend does for DevMachine: an in-memory API server per Cluster, and a Node
// per Machine. It watches only the core types, and it takes what it needs from
// them — the endpoint from Cluster.spec.controlPlaneEndpoint, the providerID
// and addresses from the Machine once the infrastructure provider has reported
// them — so it works unchanged with any provider, and with a real provider
// pointed at a simulated backend (CAPX against ntnx-sim is the first).
//
// The contract with whatever creates the Clusters is one thing: the Cluster's
// control plane endpoint must name this process's host and a port in its
// range. The listener is opened at exactly that port when the Cluster reports
// it. Everything else is read from the management cluster.
//
// With no control plane provider present, nothing creates the cluster's CA or
// its kubeconfig secret, and without those the core Machine controller cannot
// reach the workload cluster to find its Node. Options.GenerateClusterSecrets
// makes this package generate them when they are absent, and is a no-op when a
// control plane provider has generated them already.
//
// What it deliberately does not fake: etcd members, the kube-apiserver Pods,
// kubeadm's ConfigMap, kube-proxy and CoreDNS. Those are what a
// KubeadmControlPlane inspects to report initialized, and are the next
// increment if a control plane provider is wanted in the loop.
package workloadsim
