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
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// Options configure the reconcilers.
type Options struct {
	// GenerateClusterSecrets writes each Cluster's CA certificates and
	// kubeconfig secret when they are absent, which is what lets Machines
	// reach Running with no control plane provider in the loop. It is safe to
	// leave on with one: what already exists is never replaced.
	GenerateClusterSecrets bool

	// MaxConcurrentReconciles is the worker count of each controller. Zero
	// means controller-runtime's default of one.
	MaxConcurrentReconciles int
}

// Setup wires the Cluster and Machine reconcilers onto a manager. It must be
// called before the manager starts.
func Setup(mgr ctrl.Manager, backend *Backend, opts Options) error {
	if backend == nil {
		return fmt.Errorf("a backend is required")
	}
	controllerOpts := controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("workloadsim-cluster").
		For(&clusterv1.Cluster{}).
		WithOptions(controllerOpts).
		Complete(&ClusterReconciler{
			Client:                 mgr.GetClient(),
			Backend:                backend,
			GenerateClusterSecrets: opts.GenerateClusterSecrets,
		}); err != nil {
		return fmt.Errorf("creating the Cluster controller: %w", err)
	}

	if err := ctrl.NewControllerManagedBy(mgr).
		Named("workloadsim-machine").
		For(&clusterv1.Machine{}).
		WithOptions(controllerOpts).
		Complete(&MachineReconciler{
			Client:  mgr.GetClient(),
			Backend: backend,
		}); err != nil {
		return fmt.Errorf("creating the Machine controller: %w", err)
	}
	return nil
}
