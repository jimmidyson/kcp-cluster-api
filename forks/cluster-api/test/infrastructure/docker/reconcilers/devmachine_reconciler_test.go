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

package reconcilers

import (
	"context"
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
)

// A DevMachine needs its Machine, its Cluster and its DevCluster to reconcile,
// and each of those is normally deleted after it. Normally: Cluster API's
// teardown is a sequence, and something that removes objects in a different
// order leaves this reconcile with nothing to work from. Deleting a kcp
// APIBinding is exactly that — every bound object goes at once.
//
// Each of these cases used to be a deadlock rather than a slow path. The
// reconcile returned without requeueing, or errored forever, so the finalizer
// stayed on a DevMachine that was already deleted; that held its Machine, which
// held the control plane, which held the Cluster, which held the APIBinding.
// A workspace in that state can never finish unbinding.
func TestDevMachineReleasesWhenItsPrerequisitesAreAlreadyGone(t *testing.T) {
	deletedDevMachine := func() *infrav1.DevMachine {
		return &infrav1.DevMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-dev-machine",
				Namespace:         "default",
				DeletionTimestamp: ptr.To(metav1.Now()),
				Finalizers:        []string{infrav1.MachineFinalizer},
				Labels:            map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: clusterv1.GroupVersion.String(),
					Kind:       "Machine",
					Name:       "test-machine",
					UID:        "test-machine-uid",
				}},
			},
			Spec: infrav1.DevMachineSpec{
				Backend: infrav1.DevMachineBackendSpec{InMemory: &infrav1.InMemoryMachineBackendSpec{}},
			},
		}
	}
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-machine",
			Namespace: "default",
			UID:       "test-machine-uid",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
		},
		Spec: clusterv1.MachineSpec{ClusterName: "test-cluster"},
	}
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevCluster",
				Name:     "test-cluster",
			},
		},
	}

	for _, tc := range []struct {
		name    string
		objects []client.Object
	}{
		{
			// The safeguard that was already here, kept honest through the
			// refactor that gave the other two the same exit.
			name:    "its owner Machine is gone",
			objects: []client.Object{deletedDevMachine()},
		},
		{
			name:    "its Cluster is gone",
			objects: []client.Object{deletedDevMachine(), machine},
		},
		{
			name:    "its DevCluster is gone",
			objects: []client.Object{deletedDevMachine(), machine, cluster},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			c := fake.NewClientBuilder().
				WithScheme(scheme.Scheme).
				WithObjects(tc.objects...).
				WithStatusSubresource(&infrav1.DevMachine{}).
				Build()
			r := &DevMachine{Client: c}

			// Twice: the first reconcile of a DevMachine that has never carried
			// a Paused condition writes one and requeues, which is upstream
			// behaviour and not what this test is about.
			for range 2 {
				_, err := r.Reconcile(context.Background(), ctrl.Request{
					NamespacedName: types.NamespacedName{Name: "test-dev-machine", Namespace: "default"},
				})
				g.Expect(err).ToNot(HaveOccurred())
			}

			// Gone entirely: the fake client, like the API server, removes an
			// object whose deletion timestamp is set once the last finalizer
			// goes.
			var got infrav1.DevMachine
			err := c.Get(context.Background(), types.NamespacedName{Name: "test-dev-machine", Namespace: "default"}, &got)
			g.Expect(err).To(HaveOccurred(), "the DevMachine still holds its finalizer, so nothing that owns it can finish deleting either")
			g.Expect(client.IgnoreNotFound(err)).ToNot(HaveOccurred())
		})
	}
}

// The same missing DevCluster on a DevMachine that is *not* being deleted is
// not a deadlock but a wait: the DevCluster is on its way, the DevMachine
// controller watches for it, and dropping the finalizer here would orphan
// backend state that is about to exist.
func TestDevMachineWaitsForADevClusterThatIsStillComing(t *testing.T) {
	g := NewWithT(t)

	devMachine := &infrav1.DevMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-dev-machine",
			Namespace:  "default",
			Finalizers: []string{infrav1.MachineFinalizer},
			Labels:     map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       "test-machine",
				UID:        "test-machine-uid",
			}},
		},
		Spec: infrav1.DevMachineSpec{
			Backend: infrav1.DevMachineBackendSpec{InMemory: &infrav1.InMemoryMachineBackendSpec{}},
		},
	}
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-machine",
			Namespace: "default",
			UID:       "test-machine-uid",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "test-cluster"},
		},
		Spec: clusterv1.MachineSpec{ClusterName: "test-cluster"},
	}
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "default"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group,
				Kind:     "DevCluster",
				Name:     "test-cluster",
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(devMachine, machine, cluster).
		WithStatusSubresource(&infrav1.DevMachine{}).
		Build()
	r := &DevMachine{Client: c}

	for range 2 {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "test-dev-machine", Namespace: "default"},
		})
		g.Expect(err).ToNot(HaveOccurred())
	}

	var got infrav1.DevMachine
	g.Expect(c.Get(context.Background(), types.NamespacedName{Name: "test-dev-machine", Namespace: "default"}, &got)).To(Succeed())
	g.Expect(got.Finalizers).To(ContainElement(infrav1.MachineFinalizer),
		"a DevMachine that is not being deleted must keep its finalizer while it waits for its DevCluster")
}
