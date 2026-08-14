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

package main

import (
	"context"
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"sigs.k8s.io/cluster-api/controllers/clustercache"
	"sigs.k8s.io/cluster-api/core/reconcilers/cluster"
	"sigs.k8s.io/cluster-api/core/reconcilers/machine"
	coreadmission "sigs.k8s.io/cluster-api/core/webhooks/admission"
	"sigs.k8s.io/cluster-api/core/webhooks/conversion"
	"sigs.k8s.io/cluster-api/internal/contract"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/reconcilers"
	infrawebhooks "sigs.k8s.io/cluster-api/test/infrastructure/docker/webhooks/admission"
	cloudv1 "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/cloud/api/v1alpha1"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
)

// inmemoryScheme is the scheme for the docker/dev infrastructure provider's
// in-memory workload-cluster backend (its own apiserver-like resources), kept
// separate from the main scheme per test/infrastructure/docker/main.go.
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

// setupReconcilers wires the walking skeleton's reconciler set onto mgr: the
// core Cluster/Machine reconcilers and the docker/dev infrastructure
// provider's DevCluster/DevMachine reconcilers, all unmodified upstream
// exported types, per ADR-0001's D3 scope. Everything else core/main.go and
// test/infrastructure/docker/main.go wire up (ClusterClass/topology,
// RuntimeSDK, MachineSet/MachineDeployment/MachinePool, ClusterResourceSet,
// MachineHealthCheck, CRD migration) is intentionally out of scope: Phase 1's
// job is to prove the KCP-workspace-aware mechanism holds for a real
// Cluster->Machine loop, not to reach feature parity with core/main.go
// (that's Phase 3).
//
// CRDMigrator is skipped entirely and deliberately, not just deferred: it
// operates on CustomResourceDefinition objects directly, but a workspace
// consuming a bound API via APIBinding has no such object to migrate - the
// CRD-shaped source of truth (the APIResourceSchema) lives in the exporting
// workspace instead. Running it here would be reconciling a concept that
// doesn't apply under kcp's APIBinding model.
func setupReconcilers(ctx context.Context, mgr ctrl.Manager) error {
	secretCachingClient, err := client.New(mgr.GetConfig(), client.Options{
		HTTPClient: mgr.GetHTTPClient(),
		Cache:      &client.CacheOptions{Reader: mgr.GetCache()},
	})
	if err != nil {
		return fmt.Errorf("creating secret caching client: %w", err)
	}

	clusterCache, err := clustercache.SetupWithManager(ctx, mgr, clustercache.Options{
		SecretClient: secretCachingClient,
	}, concurrency(10))
	if err != nil {
		return fmt.Errorf("creating ClusterCache: %w", err)
	}

	if err := (&cluster.Reconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		ClusterCache: clusterCache,
	}).SetupWithManager(ctx, mgr, concurrency(10)); err != nil {
		return fmt.Errorf("creating Cluster controller: %w", err)
	}

	if err := (&machine.Reconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		ClusterCache: clusterCache,
	}).SetupWithManager(ctx, mgr, concurrency(10)); err != nil {
		return fmt.Errorf("creating Machine controller: %w", err)
	}

	runtimeClient, err := container.NewDockerClient()
	if err != nil {
		return fmt.Errorf("establishing container runtime connection: %w", err)
	}

	inMemoryManager := inmemoryruntime.NewManager(inmemoryScheme)
	if err := inMemoryManager.Start(ctx); err != nil {
		return fmt.Errorf("starting in-memory manager: %w", err)
	}

	apiServerMux, err := inmemoryserver.NewWorkloadClustersMux(inMemoryManager, os.Getenv("POD_IP"))
	if err != nil {
		return fmt.Errorf("creating workload clusters mux: %w", err)
	}

	if err := (&reconcilers.DevCluster{
		Client:           mgr.GetClient(),
		ContainerRuntime: runtimeClient,
		InMemoryManager:  inMemoryManager,
		APIServerMux:     apiServerMux,
	}).SetupWithManager(ctx, mgr, concurrency(10)); err != nil {
		return fmt.Errorf("creating DevCluster controller: %w", err)
	}

	if err := (&reconcilers.DevMachine{
		Client:           mgr.GetClient(),
		ContainerRuntime: runtimeClient,
		ClusterCache:     clusterCache,
		InMemoryManager:  inMemoryManager,
		APIServerMux:     apiServerMux,
	}).SetupWithManager(ctx, mgr, concurrency(10)); err != nil {
		return fmt.Errorf("creating DevMachine controller: %w", err)
	}

	return nil
}

// setupWebhooks wires the core Cluster/Machine admission webhooks and the
// docker/dev infrastructure provider's DevCluster/DevMachine admission
// webhooks onto mgr, which also registers the shared "/convert" endpoint
// (see sigs.k8s.io/controller-runtime/pkg/builder's webhook builder) that
// serves the core Cluster v1beta1<->v1beta2 conversion webhook - satisfying
// Phase 1's "at least one admission webhook and the conversion webhook"
// exit criterion without any extra wiring of our own.
func setupWebhooks(mgr ctrl.Manager) error {
	conversion.SetAPIVersionGetter(func(ctx context.Context, gk schema.GroupKind) (string, error) {
		return contract.GetAPIVersion(ctx, mgr.GetClient(), gk)
	})

	if err := (&coreadmission.Cluster{Client: mgr.GetClient()}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating Cluster webhook: %w", err)
	}
	if err := (&coreadmission.Machine{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating Machine webhook: %w", err)
	}
	if err := (&infrawebhooks.DevCluster{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating DevCluster webhook: %w", err)
	}
	if err := (&infrawebhooks.DevMachine{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("creating DevMachine webhook: %w", err)
	}

	return nil
}

func concurrency(c int) controller.Options {
	return controller.Options{MaxConcurrentReconciles: c}
}
