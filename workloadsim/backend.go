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

package workloadsim

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	cloudv1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/cloud/api/v1alpha1"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
)

// inmemoryScheme is the scheme of the fake workload clusters: what their API
// servers can hold. It is the set the docker/dev provider's in-memory backend
// registers, so anything Cluster API's own controllers write to a workload
// cluster (Nodes, kubeadm's ConfigMap, kube-proxy, CoreDNS) has a home.
var inmemoryScheme = runtime.NewScheme()

func init() {
	_ = cloudv1.AddToScheme(inmemoryScheme)
	_ = corev1.AddToScheme(inmemoryScheme)
	_ = appsv1.AddToScheme(inmemoryScheme)
	_ = rbacv1.AddToScheme(inmemoryScheme)
	_ = storagev1.AddToScheme(inmemoryScheme)
	_ = apiextensionsv1.AddToScheme(inmemoryScheme)
	_ = policyv1.AddToScheme(inmemoryScheme)
}

// Ports is where the fake API servers listen.
type Ports struct {
	// Min and Max bound the port range workload cluster listeners are opened
	// in. A Cluster's endpoint port must fall inside it. Zero means the
	// upstream defaults, 20000 to 30000.
	Min, Max int32
	// Debug is the port of the mux's debug endpoint, which lists the
	// listeners and the objects behind each. Zero means the upstream default,
	// 19000.
	Debug int32
}

// Backend is the in-memory workload cluster runtime shared by every Cluster
// this process serves: one store of fake objects, partitioned per Cluster into
// resource groups, and one mux that answers each Cluster's endpoint from its
// group.
type Backend struct {
	// Host is the address the listeners are advertised at, and the address a
	// Cluster's control plane endpoint must name.
	Host string

	Manager inmemoryruntime.Manager
	Mux     *inmemoryserver.WorkloadClustersMux
}

// NewBackend starts the in-memory runtime and its mux. The mux binds its
// debug port immediately; workload cluster listeners bind as Clusters ask for
// them.
func NewBackend(ctx context.Context, host string, ports Ports) (*Backend, error) {
	if host == "" {
		return nil, fmt.Errorf("a host is required")
	}

	manager := inmemoryruntime.NewManager(inmemoryScheme)
	if err := manager.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting the in-memory runtime: %w", err)
	}

	opts := inmemoryserver.CustomPorts{
		MinPort:   inmemoryserver.DefaultMinPort,
		MaxPort:   inmemoryserver.DefaultMaxPort,
		DebugPort: inmemoryserver.DefaultDebugPort,
	}
	if ports.Min != 0 {
		opts.MinPort = ports.Min
	}
	if ports.Max != 0 {
		opts.MaxPort = ports.Max
	}
	if ports.Debug != 0 {
		opts.DebugPort = ports.Debug
	}

	mux, err := inmemoryserver.NewWorkloadClustersMux(manager, host, opts)
	if err != nil {
		return nil, fmt.Errorf("creating the workload cluster mux: %w", err)
	}

	return &Backend{Host: host, Manager: manager, Mux: mux}, nil
}

// Shutdown stops every listener and the debug endpoint.
func (b *Backend) Shutdown(ctx context.Context) error {
	return b.Mux.Shutdown(ctx)
}
