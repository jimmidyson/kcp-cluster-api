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

package demo

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpconfig"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ping reports whether a kcp server is serving the root workspace and the
// tenancy API the demo's first real step uses.
//
// Both, and not /readyz, because each of the three goes green at a different
// time. readyz is first, and the root workspace does not exist yet when it
// does. The root workspace's LogicalCluster is next, and tenancy.kcp.io is
// still absent from that workspace's discovery when it appears - which does
// not fail loudly: a controller-runtime client caches discovery on first use
// and rate-limits reloading it, so a client built in that window fails its
// first Workspace call with "no matches for kind Workspace", a message that
// reads like a scheme bug rather than a server that was not ready.
func ping(ctx context.Context, base *rest.Config) error {
	scheme, err := fixtureScheme()
	if err != nil {
		return err
	}
	cfg := kcpconfig.ForCluster(base, "root")
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("building a client for root: %w", err)
	}

	lc := &corev1alpha1.LogicalCluster{}
	if err := cl.Get(ctx, client.ObjectKey{Name: corev1alpha1.LogicalClusterName}, lc); err != nil {
		return err
	}

	workspaces := &tenancyv1alpha1.WorkspaceList{}
	return cl.List(ctx, workspaces)
}
