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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
)

// clusterRequeue is how long a Machine waits between looks for its Cluster's
// fake API server, which the ClusterReconciler brings up independently.
const clusterRequeue = 2 * time.Second

// MachineReconciler gives each Machine a Node in its Cluster's fake API
// server, carrying the providerID the infrastructure provider reported. That
// is what the core Machine controller matches on to set the Machine's NodeRef
// and move it to Running.
type MachineReconciler struct {
	client.Client
	Backend *Backend

	// nodes remembers which resource group each Machine's Node was written
	// to, so the Node can be removed once the Machine is gone and can no
	// longer say which Cluster it belonged to. In-process, like the Nodes.
	nodes sync.Map
}

// Reconcile writes a Machine's Node once the Machine has a providerID, and
// removes it once the Machine is gone.
func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	machine := &clusterv1.Machine{}
	if err := r.Get(ctx, req.NamespacedName, machine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.deleteNode(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, err
	}

	if machine.Spec.ProviderID == "" {
		log.V(4).Info("Waiting for the Machine's providerID")
		return ctrl.Result{}, nil
	}
	if machine.Spec.ClusterName == "" {
		log.V(4).Info("Waiting for the Machine to name its Cluster")
		return ctrl.Result{}, nil
	}

	clusterKey := types.NamespacedName{Namespace: machine.Namespace, Name: machine.Spec.ClusterName}.String()
	if _, err := r.Backend.Mux.ResourceGroupByWorkloadCluster(clusterKey); err != nil {
		log.V(4).Info("Waiting for the Cluster's API server", "cluster", clusterKey)
		return ctrl.Result{RequeueAfter: clusterRequeue}, nil
	}

	inmemoryClient := r.Backend.Manager.GetResourceGroup(clusterKey).GetClient()
	node := newNode(machine)
	if err := inmemoryClient.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("reading Node %s: %w", node.Name, err)
		}
		if err := inmemoryClient.Create(ctx, node); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating Node %s: %w", node.Name, err)
		}
		log.Info("Created the Machine's Node", "cluster", clusterKey, "providerID", machine.Spec.ProviderID)
	}
	r.nodes.Store(req.NamespacedName, clusterKey)
	return ctrl.Result{}, nil
}

// deleteNode removes the Node of a Machine that no longer exists, if this
// process wrote one. The core Machine controller usually deletes it first,
// through the fake API server, which is why NotFound is not an error here.
func (r *MachineReconciler) deleteNode(ctx context.Context, machine types.NamespacedName) error {
	clusterKey, ok := r.nodes.LoadAndDelete(machine)
	if !ok {
		return nil
	}
	inmemoryClient := r.Backend.Manager.GetResourceGroup(clusterKey.(string)).GetClient() //nolint:errcheck,forcetypeassert // Store only ever writes a string.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: machine.Name}}
	if err := inmemoryClient.Delete(ctx, node); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Node %s: %w", node.Name, err)
	}
	return nil
}

// newNode is the Node a Machine gets: named after it, carrying its providerID
// and addresses, Ready, and labelled and tainted as a control plane node when
// the Machine is one. The shape is the docker/dev provider's in-memory Node,
// with the providerID taken from the Machine rather than invented.
func newNode(machine *clusterv1.Machine) *corev1.Node {
	now := metav1.Now()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: machine.Name,
		},
		Spec: corev1.NodeSpec{
			ProviderID: machine.Spec.ProviderID,
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: machine.Spec.Version,
			},
			Addresses: nodeAddresses(machine.Status.Addresses),
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady", LastTransitionTime: now},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse, Reason: "KubeletHasSufficientMemory", LastTransitionTime: now},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse, Reason: "KubeletHasNoDiskPressure", LastTransitionTime: now},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse, Reason: "KubeletHasSufficientPID", LastTransitionTime: now},
			},
		},
	}
	if util.IsControlPlaneMachine(machine) {
		node.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
		node.Spec.Taints = []corev1.Taint{{
			Key:    "node-role.kubernetes.io/control-plane",
			Effect: corev1.TaintEffectNoSchedule,
		}}
	}
	return node
}

// nodeAddresses carries a Machine's addresses over to its Node. The two
// enumerations name the same five kinds.
func nodeAddresses(addresses clusterv1.MachineAddresses) []corev1.NodeAddress {
	out := make([]corev1.NodeAddress, 0, len(addresses))
	for _, a := range addresses {
		out = append(out, corev1.NodeAddress{Type: corev1.NodeAddressType(a.Type), Address: a.Address})
	}
	return out
}
