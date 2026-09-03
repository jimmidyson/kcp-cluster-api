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

package upstreamscale

import "k8s.io/apimachinery/pkg/api/resource"

// Controller is one clusterctl-installed deployment: where it lives, and what
// a scale run gives it.
//
// One list, used by the tool that prepares the cluster and by the sampler that
// measures it. Two lists would drift, and the failure would be a run that
// carefully sized a controller it then did not sample.
type Controller struct {
	// Name is what the report attributes cost to.
	Name string
	// Namespace and Deployment are where clusterctl put it.
	Namespace  string
	Deployment string
	// CPU and Memory are what it is guaranteed. See sizing.md for where each
	// number comes from; raise one and re-run when a rung is OOM killed.
	CPU    string
	Memory string
	// DevCluster marks the one provider that ships expecting a Docker socket
	// it does not need for the in-memory backend.
	DevCluster bool
	// TopologyGate marks the controllers that read the ClusterTopology feature
	// gate: core, whose topology controller does the work, and the DevCluster
	// provider, whose template webhooks refuse the objects without it. The two
	// kubeadm providers accept the flag and nothing reads it.
	TopologyGate bool
}

// Quantities parses the resources, so a bad flag fails before the cluster is
// touched rather than half way through patching it.
func (c Controller) Quantities() (cpu, memory resource.Quantity, err error) {
	if cpu, err = resource.ParseQuantity(c.CPU); err != nil {
		return cpu, memory, err
	}
	memory, err = resource.ParseQuantity(c.Memory)
	return cpu, memory, err
}

// Controllers is the stock Cluster API a scale run installs, with the
// namespaces and deployment names clusterctl gives them.
//
// The DevCluster provider is the Docker provider: DevCluster chooses a backend
// and the in-memory one is a mode of it, which is why it is capd-system rather
// than anything named for in-memory, and why it is the one that arrives wanting
// a Docker socket.
func Controllers() []Controller {
	return []Controller{
		{Name: "core", Namespace: "capi-system", Deployment: "capi-controller-manager",
			CPU: "4", Memory: "8Gi", TopologyGate: true},
		{Name: "kubeadm-bootstrap", Namespace: "capi-kubeadm-bootstrap-system",
			Deployment: "capi-kubeadm-bootstrap-controller-manager", CPU: "2", Memory: "4Gi"},
		{Name: "kubeadm-control-plane", Namespace: "capi-kubeadm-control-plane-system",
			Deployment: "capi-kubeadm-control-plane-controller-manager", CPU: "4", Memory: "6Gi"},
		{Name: "devcluster", Namespace: "capd-system", Deployment: "capd-controller-manager",
			CPU: "6", Memory: "24Gi", DevCluster: true, TopologyGate: true},
	}
}
