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
	"maps"
	"reflect"
	"slices"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// ReconcileRoles brings the Cluster API ClusterRoles in the workspace cl is
// scoped to in line with the APIBindings that workspace holds, and reports the
// roles it wrote.
//
// Both callers - the WorkspaceType's initializer and the fleet-wide
// maintainer - run exactly this, so the roles a workspace is created with and
// the roles it is kept at cannot drift apart.
//
// Quiet when nothing changed. The maintainer runs on every APIBinding event in
// every workspace of the fleet, so a reconcile that finds the roles already
// right must not write them: an update per event, per workspace, is a load
// this project measures and would have to answer for.
func ReconcileRoles(ctx context.Context, cl client.Client) (RoleState, error) {
	bindings := &apisv1alpha2.APIBindingList{}
	if err := cl.List(ctx, bindings); err != nil {
		return RoleState{}, fmt.Errorf("listing APIBindings: %w", err)
	}

	state := RoleState{Groups: APIGroups(bindings.Items)}
	for _, want := range Roles(bindings.Items) {
		changed, err := applyRole(ctx, cl, want)
		if err != nil {
			return state, err
		}
		if changed {
			state.Written = append(state.Written, want.Name)
		}
	}
	return state, nil
}

// RoleState is what one reconcile of a workspace found and did: the Cluster
// API groups the workspace serves, and the roles that had to be written to
// match. Written is empty on a reconcile that found nothing to change, which
// is most of them.
type RoleState struct {
	Groups  []string
	Written []string
}

// applyRole creates want, or updates an existing role to match it.
//
// An existing role that this project does not manage is left alone and
// reported as an error rather than overwritten. A name collision here is
// somebody's deliberate role being silently replaced by a generated one, which
// is the kind of thing a controller should refuse to do quietly.
func applyRole(ctx context.Context, cl client.Client, want *rbacv1.ClusterRole) (bool, error) {
	got := &rbacv1.ClusterRole{}
	err := cl.Get(ctx, client.ObjectKeyFromObject(want), got)
	switch {
	case apierrors.IsNotFound(err):
		if err := cl.Create(ctx, want.DeepCopy()); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Lost a race with another reconcile of the same workspace.
				// The other one wrote what this one would have.
				return false, nil
			}
			return false, fmt.Errorf("creating ClusterRole %s: %w", want.Name, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading ClusterRole %s: %w", want.Name, err)
	}

	if got.Labels[ManagedByLabel] != ManagedByValue {
		return false, fmt.Errorf("ClusterRole %s exists and is not managed by %s: refusing to overwrite it",
			want.Name, ManagedByValue)
	}
	if slices.EqualFunc(got.Rules, want.Rules, sameRule) {
		return false, nil
	}

	got.Rules = want.Rules
	if got.Labels == nil {
		got.Labels = map[string]string{}
	}
	maps.Copy(got.Labels, want.Labels)
	if err := cl.Update(ctx, got); err != nil {
		return false, fmt.Errorf("updating ClusterRole %s: %w", want.Name, err)
	}
	return true, nil
}

func sameRule(a, b rbacv1.PolicyRule) bool {
	return reflect.DeepEqual(a, b)
}
