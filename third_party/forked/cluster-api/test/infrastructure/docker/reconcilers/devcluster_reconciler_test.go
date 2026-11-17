/*
Copyright 2024 The Kubernetes Authors.

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

package reconcilers

import (
	"context"
	"net"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
	inmemoryruntime "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/runtime"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
)

func TestDevCluster_ExternallyManaged(t *testing.T) {
	g := NewWithT(t)

	devCluster := &infrav1.DevCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-dev-cluster",
			Namespace: "default",
			Annotations: map[string]string{
				clusterv1.ManagedByAnnotation: "external-controller",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(devCluster).
		Build()

	r := &DevCluster{
		Client: c,
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      devCluster.Name,
			Namespace: devCluster.Namespace,
		},
	})

	// Should return early without error
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify finalizer was not added (early return before EnsureFinalizer)
	updatedCluster := &infrav1.DevCluster{}
	err = c.Get(context.Background(), types.NamespacedName{
		Name:      devCluster.Name,
		Namespace: devCluster.Namespace,
	}, updatedCluster)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(updatedCluster.Finalizers).To(BeEmpty())
}

// A DevCluster must outlive the DevMachines that belong to it. They cannot
// clean themselves up without it — the docker backend deletes only the load
// balancer with the DevCluster and leaves each machine's container to that
// machine's own reconcile — so a DevCluster that went first would leak every
// container in the cluster.
//
// Cluster API's own teardown deletes Machines before the infrastructure
// cluster and never gets this wrong. Nothing here relies on that: deleting a
// kcp APIBinding removes every bound object at once, and a person can always
// delete a DevCluster by hand.
func TestDevClusterWaitsForItsDevMachines(t *testing.T) {
	newDevCluster := func() *infrav1.DevCluster {
		return &infrav1.DevCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-cluster",
				Namespace:         "default",
				DeletionTimestamp: ptr.To(metav1.Now()),
				Finalizers:        []string{infrav1.ClusterFinalizer},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Cluster",
					Name:       "test-cluster",
					UID:        "test-cluster-uid",
				}},
			},
			Spec: infrav1.DevClusterSpec{
				Backend: infrav1.DevClusterBackendSpec{InMemory: &infrav1.InMemoryClusterBackendSpec{}},
			},
		}
	}
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default", UID: "test-cluster-uid"},
	}
	devMachine := &infrav1.DevMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-machine",
			Namespace: "default",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
		},
	}

	t.Run("waits while one remains", func(t *testing.T) {
		g := NewWithT(t)

		devCluster := newDevCluster()
		c := fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(devCluster, cluster, devMachine).
			WithStatusSubresource(&infrav1.DevCluster{}).
			Build()
		r := &DevCluster{Client: c}

		for range 2 {
			result, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "default"},
			})
			g.Expect(err).ToNot(HaveOccurred())
			_ = result
		}

		var got infrav1.DevCluster
		g.Expect(c.Get(context.Background(), types.NamespacedName{Name: "test-cluster", Namespace: "default"}, &got)).To(Succeed())
		g.Expect(got.Finalizers).To(ContainElement(infrav1.ClusterFinalizer),
			"the DevCluster gave up its finalizer while a DevMachine still needed it to clean up")
	})

	t.Run("goes once they have", func(t *testing.T) {
		g := NewWithT(t)

		devCluster := newDevCluster()
		c := fake.NewClientBuilder().
			WithScheme(scheme.Scheme).
			WithObjects(devCluster, cluster).
			WithStatusSubresource(&infrav1.DevCluster{}).
			Build()
		// A real in-memory backend, because this case runs the delete rather
		// than stopping short of it: the reconcile tears the workload cluster's
		// listener down, and a nil mux panics rather than reporting anything.
		inMemoryManager := inmemoryruntime.NewManager(scheme.Scheme)
		g.Expect(inMemoryManager.Start(context.Background())).To(Succeed())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		g.Expect(err).ToNot(HaveOccurred())
		port := int32(listener.Addr().(*net.TCPAddr).Port) //nolint:forcetypeassert // a TCP listener has a TCP address.
		g.Expect(listener.Close()).To(Succeed())
		mux, err := inmemoryserver.NewWorkloadClustersMux(inMemoryManager, "127.0.0.1",
			inmemoryserver.CustomPorts{MinPort: port + 1, MaxPort: port + 10, DebugPort: port})
		g.Expect(err).ToNot(HaveOccurred())

		r := &DevCluster{Client: c, InMemoryManager: inMemoryManager, APIServerMux: mux}

		for range 2 {
			_, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "test-cluster", Namespace: "default"},
			})
			g.Expect(err).ToNot(HaveOccurred())
		}

		var got infrav1.DevCluster
		err = c.Get(context.Background(), types.NamespacedName{Name: "test-cluster", Namespace: "default"}, &got)
		g.Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())
		g.Expect(err).To(HaveOccurred(), "with no DevMachines left the DevCluster should have finished deleting")
	})
}
