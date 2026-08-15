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

// Package providerwiring is the seam between this project and the provider
// binaries built on it: the conversion plan's G2, "per-workspace glue".
//
// Everything either side of this package is somebody else's interface. Below
// it, sigs.k8s.io/multicluster-runtime and github.com/kcp-dev/multicluster-provider
// discover workspaces and cache them. Above it, unmodified upstream Cluster
// API reconcilers are wired onto a manager. This package is the only part
// that is bespoke to this project, and SetupFunc is the shape a provider
// binary implements to plug into it.
//
// # What is pinned here, and why separately from the implementation
//
// docs/conversion-plan.md asks for the Phase 2 seams to be expressed as Go
// signatures before Phase 3's tracks (the bootstrap, control-plane and
// docker-infrastructure provider ports) are fanned out, so that those tracks
// code against one shape rather than each inventing their own. SetupFunc and
// ManagerGetter are that shape. A track can be written and reviewed against
// this file alone.
//
// # The lifecycle contract
//
// Each rule below is a property of a dependency, established by reading that
// dependency's source rather than its documentation, as Constitution
// Principle V requires. Each is also silent when violated, which is why they
// are stated as a contract rather than left to the implementation.
//
//  1. Wiring must be registered with the manager before the manager is
//     started. sigs.k8s.io/multicluster-runtime's coordinator fans Engage out
//     to the components registered at the moment of the call
//     (pkg/manager/coordinator/basic), and never replays earlier
//     engagements. A component registered afterwards is not an error and
//     logs nothing; it simply never hears about the workspaces that engaged
//     before it.
//
//  2. Runnables registered against a workspace's manager must be bound to
//     that workspace's lifetime. multicluster-runtime's per-workspace
//     manager delegates Add to the host manager (pkg/manager.scopedManager),
//     so a runnable added the obvious way outlives the workspace it belongs
//     to: the context Engage supplies is cancelled on disengage, and nothing
//     is listening to it.
//
//  3. Controller name validation must be disabled for per-workspace
//     controllers. sigs.k8s.io/controller-runtime records controller names in
//     a process-global set that is never emptied (pkg/controller/name.go), so
//     the second workspace to wire a controller named "cluster" fails, as
//     does the second engagement of any one workspace.
//
//  4. Webhooks are not part of a SetupFunc. controller-runtime's webhook
//     builder skips a path that is already registered (pkg/builder/webhook.go,
//     isAlreadyHandled) rather than rejecting it, so per-workspace
//     registration does not fail — it leaves the first workspace's handlers,
//     holding the first workspace's client, serving every workspace. Routing
//     admission requests to the right workspace is the conversion plan's G4,
//     which is unbuilt and carries a required human review checkpoint.
//
// # Not pinned here
//
// Discovery (G1) has no interface in this package. The seam is
// multicluster.Provider and mcmanager.Manager, which are already interfaces
// owned by the libraries that implement them; wrapping them would add a layer
// with one implementation and one caller, which Constitution Principle VIII
// prohibits building ahead of a second one. Revisit if a second provider is
// adopted (the path-aware provider alongside the apiexport one), or if the
// conversion plan's hand-rolled fallback becomes necessary.
//
// The workspace-scoped rest.Config builder (G3) is deferred for the same
// reason: it has no caller. Its trigger is clusterctl workspace-awareness
// (P5), or anything else that has to talk to a specific workspace from
// outside the pool of engaged ones.
package providerwiring

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// SetupFunc registers one workspace's controllers.
//
// It is called once for each workspace the provider engages, with a manager
// scoped to that workspace: its client, cache, API reader and field indexer
// all read and write that workspace alone. A SetupFunc is the whole of what a
// provider binary contributes — each of the four Cluster API provider
// binaries supplies its own, wiring its own upstream reconcilers, and nothing
// else about them differs.
//
// The context is cancelled when the workspace is disengaged. Anything the
// function registers with mgr is stopped at that point, so a SetupFunc does
// not need to arrange its own shutdown; it does need to avoid storing
// workspace-scoped values (a client, a cache, a reconciler) anywhere
// process-global, because the next workspace's call would overwrite them and
// nothing would report it.
//
// Returning an error abandons that one workspace, leaving the others
// unaffected. It must be safe to call for a workspace that was engaged,
// disengaged, and engaged again.
type SetupFunc func(ctx context.Context, workspace multicluster.ClusterName, mgr manager.Manager) error

// ManagerGetter yields the manager scoped to one workspace.
//
// It is the subset of sigs.k8s.io/multicluster-runtime's mcmanager.Manager
// that this package needs, declared here so the wiring can be exercised
// against a fake rather than a real multi-cluster manager, and so a caller
// can see what this package is entitled to do to the manager it is given.
type ManagerGetter interface {
	GetManager(ctx context.Context, workspace multicluster.ClusterName) (manager.Manager, error)
}

// ErrStarted is returned when wiring is registered after the manager it
// belongs to has already started.
//
// This is rule 1 of the lifecycle contract made loud. The underlying
// behaviour is silent: registering late is accepted, and the workspaces that
// engaged in the meantime are simply never set up.
var ErrStarted = errors.New("per-workspace wiring must be registered before the manager is started")

// ErrWebhooksAlreadyWired is returned when webhook wiring is requested for a
// second workspace.
//
// Serving webhooks for more than one workspace requires resolving each
// admission request to its source workspace, which is the conversion plan's
// G4 and is not built. Until it is, the supported configurations are one
// workspace's webhooks or none. This error exists because the alternative —
// what controller-runtime does on its own — is to keep serving every
// workspace's admission requests with the first workspace's client and say
// nothing.
var ErrWebhooksAlreadyWired = errors.New(
	"webhooks are already wired for another workspace: serving more than one workspace requires " +
		"resolving each admission request to its workspace (G4, unbuilt)")
