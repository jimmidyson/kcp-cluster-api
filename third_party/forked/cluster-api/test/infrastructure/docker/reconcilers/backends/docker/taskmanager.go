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

package docker

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// NewTaskManager create a new TaskManager that can be used for running dockerMachines provisioning tasks async of the reconcile loop.
func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks:        make(map[string]*TaskState),
		progressChan: make(chan taskProgress, 100),
	}
}

// TaskManager is responsible for running dockerMachines provisioning tasks async of the reconcile loop.
type TaskManager struct {
	mu           sync.RWMutex
	tasks        map[string]*TaskState
	progressChan chan taskProgress
}

// taskProgress is a task's progress event together with the logical cluster the
// DockerMachine lives in.
//
// The cluster travels alongside the object because the object does not name it,
// and a fleet-wide controller's queue is keyed on a request that does. It is
// empty when the controller serves one cluster.
type taskProgress struct {
	cluster multicluster.ClusterName
	event   event.GenericEvent
}

// TaskState represent the state of a task.
type TaskState struct {
	// cluster is the logical cluster the DockerMachine lives in, empty when the
	// controller serves one. Unexported: it is the TaskManager's bookkeeping,
	// not part of the state a caller reads.
	cluster                     multicluster.ClusterName
	DockerMachineKey            client.ObjectKey
	ID                          string
	Completed                   bool
	CurrentOperationDescription string
	CurrentOperation            int
	TotalOperations             int
	Cancel                      context.CancelFunc
	Err                         error
}

func (s *TaskState) toEvent() event.GenericEvent {
	return event.GenericEvent{
		Object: &infrav1.DevMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.DockerMachineKey.Name,
				Namespace: s.DockerMachineKey.Namespace,
			},
		},
	}
}

func (s *TaskState) String() string {
	if s.Completed {
		return ""
	}
	if s.Err != nil {
		return fmt.Sprintf("%s failed: %s", s.CurrentOperationDescription, s.Err.Error())
	}
	return fmt.Sprintf("%s (%d of %d)", s.CurrentOperationDescription, s.CurrentOperation, s.TotalOperations)
}

// Operation defines an operation in a task, e.g. one of the preKubeadmCommand.
type Operation struct {
	Description string
	F           func(ctx context.Context) error
}

// RegisterTask start a task for a dockerMachine.
func (m *TaskManager) RegisterTask(ctx context.Context, dockerMachine client.Object, id string, operations []Operation, timeout time.Duration) (*TaskState, error) {
	m.mu.Lock()
	if _, exists := m.tasks[taskUID(ctx, dockerMachine, id)]; exists {
		m.mu.Unlock()
		return nil, fmt.Errorf("task for %s, ID %s already exist", klog.KObj(dockerMachine), id)
	}

	log := ctrl.LoggerFrom(ctx).WithValues("reconcileMode", "asyncTask")

	containerRuntime, err := container.RuntimeFrom(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to container runtime: %v", err)
	}

	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), timeout)
	ctxWithTimeout = ctrl.LoggerInto(ctxWithTimeout, log)
	ctxWithTimeout = container.RuntimeInto(ctxWithTimeout, containerRuntime)

	cluster, _ := mccontext.ClusterFrom(ctx)
	state := &TaskState{
		cluster:                     cluster,
		DockerMachineKey:            client.ObjectKeyFromObject(dockerMachine),
		ID:                          id,
		Completed:                   false,
		CurrentOperation:            0,
		CurrentOperationDescription: "Starting",
		TotalOperations:             len(operations),
		Cancel:                      cancel,
	}
	m.tasks[taskUID(ctx, dockerMachine, id)] = state
	m.mu.Unlock()

	go m.runTask(ctxWithTimeout, state, operations)

	snapshot := *state
	return new(snapshot), nil
}

func (m *TaskManager) runTask(ctx context.Context, state *TaskState, operations []Operation) {
	select {
	case <-ctx.Done():
		m.mu.Lock()
		state.Err = ctx.Err()
		m.mu.Unlock()
		m.progressChan <- taskProgress{cluster: state.cluster, event: state.toEvent()}
		return
	default:
		for i, op := range operations {
			// Report error if the context has been canceled
			if ctx.Err() != nil {
				m.mu.Lock()
				state.Err = ctx.Err()
				m.mu.Unlock()
				m.progressChan <- taskProgress{cluster: state.cluster, event: state.toEvent()}
				return
			}

			// Start the next operation
			m.mu.Lock()
			state.CurrentOperationDescription = op.Description
			state.CurrentOperation = i + 1
			m.mu.Unlock()
			m.progressChan <- taskProgress{cluster: state.cluster, event: state.toEvent()}

			if err := op.F(ctx); err != nil {
				// Report error if the operation fails
				m.mu.Lock()
				state.Err = err
				m.mu.Unlock()
				m.progressChan <- taskProgress{cluster: state.cluster, event: state.toEvent()}
				return
			}
		}

		// Report all the operations have been completed
		m.mu.Lock()
		state.Completed = true
		m.mu.Unlock()
		m.progressChan <- taskProgress{cluster: state.cluster, event: state.toEvent()}
	}
}

// GetStatus return state of a task.
func (m *TaskManager) GetStatus(ctx context.Context, dockerMachine client.Object, id string) *TaskState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, exists := m.tasks[taskUID(ctx, dockerMachine, id)]
	if !exists {
		return nil
	}

	snapshot := *state
	return new(snapshot)
}

// ResetStatus the status of a task for a dockerMachine.
func (m *TaskManager) ResetStatus(ctx context.Context, dockerMachine client.Object, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.tasks, taskUID(ctx, dockerMachine, id))
}

// Cancel cancels all the tasks for a dockerMachine.
func (m *TaskManager) Cancel(ctx context.Context, dockerMachine client.Object) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cluster, _ := mccontext.ClusterFrom(ctx)
	for id, state := range m.tasks {
		if state.DockerMachineKey != client.ObjectKeyFromObject(dockerMachine) || state.cluster != cluster {
			continue
		}

		state.Cancel()
		delete(m.tasks, id)
	}
}

// GetSource return a controller runtime source that can be used to get notifications when an operation for
// a dockerMachine is completed.
func (m *TaskManager) GetSource() source.Source {
	return source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[ctrl.Request]) error {
		go m.forward(ctx, func(p taskProgress) {
			q.Add(ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p.event.Object)})
		})
		return nil
	})
}

// GetMulticlusterSource is GetSource for a controller that serves every cluster.
//
// It cannot go through source.Channel for the same reason the ClusterCache's
// cannot: the channel carries a client.Object, and the object does not name the
// logical cluster the request has to be keyed on.
func (m *TaskManager) GetMulticlusterSource() source.TypedSource[mcreconcile.Request] {
	return source.TypedFunc[mcreconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		go m.forward(ctx, func(p taskProgress) {
			q.Add(mcreconcile.Request{
				Request:     ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p.event.Object)},
				ClusterName: p.cluster,
			})
		})
		return nil
	})
}

func (m *TaskManager) forward(ctx context.Context, enqueue func(taskProgress)) {
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-m.progressChan:
			enqueue(p)
		}
	}
}

// taskUID identifies a task by the DockerMachine it belongs to, the logical
// cluster that DockerMachine lives in, and the task's own ID.
//
// The logical cluster is part of the key because one TaskManager now serves
// every cluster: without it, two tenants' identically named DockerMachines share
// a task, so one tenant's provisioning cancels or reports on the other's. It is
// empty when the controller serves one cluster, which keys every task the same
// way it did before.
func taskUID(ctx context.Context, dockerMachine client.Object, id string) string {
	cluster, _ := mccontext.ClusterFrom(ctx)
	return fmt.Sprintf("%s/%s/%s", cluster, client.ObjectKeyFromObject(dockerMachine), id)
}
