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

package inmemory

import (
	"context"

	"k8s.io/klog/v2"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// workloadClusterKey names one cluster's state in the in-memory backend: its
// resource group, and the listener serving its API server and etcd members.
//
// It was the Cluster's namespace and name, which is unique in one management
// cluster and not in a fleet of them. Two clusters called default/demo-00 in
// different management clusters would share one resource group and one
// listener - so one tenant's control plane would serve the other's, on the same
// port, with the same objects. That is not a collision that fails; it is one
// that works, which is worse.
//
// The management cluster comes from the reconcile context, where the fleet-wide
// wiring puts it. A context without one is a single-cluster deployment, whose
// key is what it always was.
func workloadClusterKey(ctx context.Context, cluster *clusterv1.Cluster) string {
	name, ok := mccontext.ClusterFrom(ctx)
	if !ok || name == "" {
		return klog.KObj(cluster).String()
	}
	return string(name) + "|" + klog.KObj(cluster).String()
}
