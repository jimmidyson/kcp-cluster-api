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

package upstreamscale

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
)

// WorkspaceTenancy is Tenancy against a real kcp: a Workspace in :root, a
// client rebased onto the logical cluster it became, and an APIBinding per
// provider.
//
// # Why every export, with each provider's own claims
//
// A provider reads and writes across exports — core's Clusters, the bootstrap
// provider's Secrets — so a binding granting only what one export needs leaves
// those reads refused. The manager then engages the workspace and reconciles
// nothing, which is a run that measures an idle fleet and reports it as an
// active one. That is the failure this repeats the deployed harness's shape
// for rather than simplifying.
type WorkspaceTenancy struct {
	// Root addresses :root, where the Workspaces and the exports live.
	Root client.Client
	// Base is the config a workspace's own client is derived from: the shard,
	// with no logical cluster on it.
	Base   *rest.Config
	Scheme *runtime.Scheme

	// Providers and Discovery are what each workspace binds. Discovery comes
	// from capiexports.Publish, which the driver runs before the climb.
	Providers []capiexports.Provider
	Discovery capiexports.Discovery

	// Timeout bounds one workspace becoming ready and one binding being
	// accepted. Zero is two minutes, which is what the deployed harness waits.
	Timeout time.Duration
}

var _ Tenancy = (*WorkspaceTenancy)(nil)

func (w *WorkspaceTenancy) timeout() time.Duration {
	if w.Timeout <= 0 {
		return 2 * time.Minute
	}
	return w.Timeout
}

// Preflight checks the shard serves what the run is about to create.
//
// The kcp analogue of asking a cluster whether the CRDs are installed, and it
// catches the same class of failure one workspace earlier: an export that was
// never published fails every binding in every workspace, and the first one to
// report it does so a rung into a climb with a message about a binding rather
// than about the installation.
func (w *WorkspaceTenancy) Preflight(ctx context.Context) error {
	if w.Root == nil || w.Base == nil || w.Scheme == nil {
		return errors.New("no shard: the kcp side needs a client for :root and a config to derive " +
			"workspace clients from")
	}
	if len(w.Providers) == 0 {
		return errors.New("no providers: a workspace binding nothing serves none of the objects the " +
			"run creates")
	}

	var missing []string
	for _, provider := range w.Providers {
		var export apisv1alpha2.APIExport
		if err := w.Root.Get(ctx, client.ObjectKey{Name: provider.Export}, &export); err != nil {
			if apierrors.IsNotFound(err) {
				missing = append(missing, provider.Export)
				continue
			}
			return fmt.Errorf("reading the %s export: %w", provider.Export, err)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("these APIExports are not published in %s: %v — every workspace this run "+
			"creates would bind nothing and every manager would engage a workspace holding no Cluster API "+
			"types at all", deployedscale.RootWorkspace, missing)
	}
	return nil
}

// Ensure makes the workspace, waits for it, and binds every export.
func (w *WorkspaceTenancy) Ensure(ctx context.Context, name string) (client.Client, error) {
	logical, err := kcpfixtures.EnsureWorkspace(ctx, w.Root, name, w.timeout())
	if err != nil {
		return nil, fmt.Errorf("creating workspace %s: %w", name, err)
	}

	cl, err := client.New(deployedscale.WorkspaceConfig(w.Base, logical), client.Options{Scheme: w.Scheme})
	if err != nil {
		return nil, fmt.Errorf("client for workspace %s: %w", name, err)
	}

	for _, provider := range w.Providers {
		if err := kcpfixtures.BindExport(ctx, cl, kcpfixtures.BindExportOptions{
			BindingName:      provider.Export,
			ExportPath:       deployedscale.RootWorkspace,
			ExportName:       provider.Export,
			PermissionClaims: provider.Claims(w.Discovery.Identities(), w.Discovery),
			ReadyTimeout:     w.timeout(),
		}); err != nil {
			return nil, fmt.Errorf("binding %s in %s: %w", provider.Export, name, err)
		}
	}
	return cl, nil
}

// Remove deletes the workspace, which deletes everything in it.
func (w *WorkspaceTenancy) Remove(ctx context.Context, name string) error {
	ws := &tenancyv1alpha1.Workspace{}
	ws.Name = name
	if err := w.Root.Delete(ctx, ws); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting workspace %s: %w", name, err)
	}
	return nil
}

// Gone reports whether the workspace has finished going.
func (w *WorkspaceTenancy) Gone(ctx context.Context, name string) (bool, error) {
	var ws tenancyv1alpha1.Workspace
	err := w.Root.Get(ctx, client.ObjectKey{Name: name}, &ws)
	switch {
	case apierrors.IsNotFound(err):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading workspace %s: %w", name, err)
	}
	return false, nil
}
