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

package coremanager

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ManagerGetter is the subset of mcmanager.Manager that WaitForManager needs.
// It exists so WaitForManager can be unit-tested with a fake, without having
// to implement the full mcmanager.Manager interface.
type ManagerGetter interface {
	GetManager(ctx context.Context, clusterName multicluster.ClusterName) (manager.Manager, error)
}

// WaitForManager polls mg.GetManager for clusterName until it succeeds, an
// unexpected error occurs, timeout elapses, or ctx is cancelled.
//
// The kcp APIExport provider engages a workspace asynchronously, some time
// after its APIBinding is observed to be Ready (see the "Chosen model"
// section of kcp/docs/conversion-plan.md); GetManager returns an error for
// any not-yet-engaged cluster name, so a caller that needs a specific,
// already-known workspace has to poll rather than call GetManager once.
func WaitForManager(ctx context.Context, mg ManagerGetter, clusterName multicluster.ClusterName, pollInterval, timeout time.Duration) (manager.Manager, error) {
	var result manager.Manager
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		mgr, err := mg.GetManager(ctx, clusterName)
		if err != nil {
			return false, nil //nolint:nilerr // not yet engaged; keep polling until timeout.
		}
		result = mgr
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for workspace %q to be engaged: %w", clusterName, err)
	}
	return result, nil
}
