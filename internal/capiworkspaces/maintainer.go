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

package capiworkspaces

import (
	"context"
	"fmt"
	"time"

	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// MaintainerControllerName is what the role-maintaining controller is called
// in logs and metrics.
const MaintainerControllerName = "cluster-api-workspace-roles"

// RebindRetry is how long the maintainer waits before looking again at a
// workspace that serves no Cluster API type yet.
//
// It exists for one narrow case rather than as a resync. kcp labels an object
// with the permission claims its workspace had accepted at the moment the
// object was written, so an APIBinding created before this deployment's own
// binding was accepted carries no claim label and is invisible here until
// something rewrites it. DefaultExports orders the bindings so that cannot
// happen to the ones the WorkspaceType creates; this covers the workspace that
// was onboarded some other way, and it costs one reconcile a minute only for
// as long as a workspace has nothing bound.
const RebindRetry = time.Minute

// AddMaintainerToManager wires the role-maintaining controller onto mgr.
//
// One controller over the whole fleet, watching the resource that answers the
// question the roles are derived from: an APIBinding appearing in a workspace
// is a tenant enabling a provider, and it is the only event that can change
// what that workspace's roles should say.
//
// The manager must be built on the workspace onboarding APIExport
// (capiexports.Workspaces), whose claim on `apibindings` is what makes *every*
// binding in a workspace visible. Without it an APIExport's virtual workspace
// serves back only the one APIBinding that binds that export, and this
// controller would watch itself.
func AddMaintainerToManager(mgr mcmanager.Manager, skipNameValidation bool) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named(MaintainerControllerName).
		WithOptions(controller.TypedOptions[mcreconcile.Request]{SkipNameValidation: ptr.To(skipNameValidation)}).
		For(&apisv1alpha2.APIBinding{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			return maintainWorkspace(ctx, mgr, req)
		}))
}

// maintainWorkspace brings one workspace's roles in line with what it has
// bound.
//
// The reconcile is per-workspace even though the event is per-APIBinding: the
// roles are a function of every binding the workspace holds, so there is
// nothing useful to do with the one that changed. Which makes this
// idempotent - several bindings changing at once collapse into reconciles that
// each write the same thing, and only the first of them writes at all.
func maintainWorkspace(ctx context.Context, mgr mcmanager.Manager, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("workspace", req.ClusterName)

	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("getting the cluster for %s: %w", req.ClusterName, err)
	}

	state, err := ReconcileRoles(ctx, cl.GetClient())
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(state.Written) > 0 {
		logger.Info("Updated the workspace's Cluster API roles", "roles", state.Written, "apiGroups", state.Groups)
	}
	if len(state.Groups) == 0 {
		return reconcile.Result{RequeueAfter: RebindRetry}, nil
	}
	return reconcile.Result{}, nil
}
