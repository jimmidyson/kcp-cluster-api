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

// Package fleetfixture builds the whole-fleet measurement shape: every
// provider's controllers - core, kubeadm bootstrap, kubeadm control plane and
// dev infrastructure - wired onto one manager, against the dev provider's
// in-memory backend.
//
// It exists because two suites measure that shape and they must measure the
// same one. test/integration/sweep sweeps it to find what a workspace costs,
// and test/integration/scale drives it to a fixed target to find whether a
// fleet that size can be hosted at all. Two copies of the wiring would let the
// two disagree about the process they both claim to describe, and the
// disagreement would be invisible: both reports would look right.
//
// # Not a deployment
//
// Cluster API deploys one process per provider. This is four of them in one,
// which is why it is a bound rather than an installation's cost: it pays one
// engagement per workspace where four deployments pay four, and shares one
// ClusterCache where four have one each. Anything read off it inherits that
// caveat.
package fleetfixture

import (
	"context"
	"errors"
	"fmt"
	"net"

	"k8s.io/client-go/rest"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/bootstrapmanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/controlplanemanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	inmemoryserver "sigs.k8s.io/cluster-api/test/infrastructure/inmemory/pkg/server"
	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
)

// The types the fleet's controllers watch, published so that every one of them
// is served.
//
// Publishing a type nothing in a given run creates is not waste and not a
// matter of taste: controller-runtime blocks a controller's startup on every
// registered source's cache sync, including sources for kinds the API server
// does not serve. Leaving one out does not make a reconciler skip it, it makes
// the controller hang. See ADR-0001, Phase 1 results.
var (
	// CoreCRDs are the core provider's types, in the cluster-api module.
	CoreCRDs = []string{
		"core/config/crd/bases/cluster.x-k8s.io_clusters.yaml",
		// The ClusterClass controller is wired whenever ClusterTopology is on,
		// which is this project's default, and it watches this type.
		"core/config/crd/bases/cluster.x-k8s.io_clusterclasses.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machines.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinesets.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinedeployments.yaml",
		"core/config/crd/bases/cluster.x-k8s.io_machinehealthchecks.yaml",
		// Read by the topology reconciler on every reconcile of a managed
		// topology, whatever the MachinePool gate says. Published, not enabled.
		"core/config/crd/bases/cluster.x-k8s.io_machinepools.yaml",
	}

	// BootstrapCRDs are the kubeadm bootstrap provider's types, in the
	// cluster-api module.
	BootstrapCRDs = []string{
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigs.yaml",
		"bootstrap/kubeadm/config/crd/bases/bootstrap.cluster.x-k8s.io_kubeadmconfigtemplates.yaml",
	}

	// ControlPlaneCRDs are the kubeadm control plane provider's types, in the
	// cluster-api module.
	ControlPlaneCRDs = []string{
		"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanes.yaml",
		"controlplane/kubeadm/config/crd/bases/controlplane.cluster.x-k8s.io_kubeadmcontrolplanetemplates.yaml",
	}

	// DevCRDs are the dev infrastructure provider's types, in the cluster-api
	// test module rather than the main one.
	DevCRDs = []string{
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclusters.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachines.yaml",
		// Both templates, because a ClusterClass names one of each: nothing
		// watches or reconciles them, and the topology controller reads them to
		// stamp the DevCluster and each Machine's DevMachine.
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devclustertemplates.yaml",
		"infrastructure/docker/config/crd/bases/infrastructure.cluster.x-k8s.io_devmachinetemplates.yaml",
	}
)

// CoreModulePaths resolves manifest paths in the cluster-api module.
func CoreModulePaths(sets ...[]string) ([]string, error) {
	return kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPI, flatten(sets)...)
}

// DevModulePaths resolves manifest paths in the cluster-api test module, which
// is where the dev infrastructure provider lives.
func DevModulePaths(sets ...[]string) ([]string, error) {
	return kcpfixtures.MustManifestPaths(kcpfixtures.ModuleClusterAPITest, flatten(sets)...)
}

// CRDPaths resolves every manifest the wired fleet needs, from both modules.
func CRDPaths() ([]string, error) {
	core, err := CoreModulePaths(CoreCRDs, BootstrapCRDs, ControlPlaneCRDs)
	if err != nil {
		return nil, fmt.Errorf("resolving the cluster-api manifests: %w", err)
	}
	dev, err := DevModulePaths(DevCRDs)
	if err != nil {
		return nil, fmt.Errorf("resolving the dev infrastructure manifests: %w", err)
	}
	return append(core, dev...), nil
}

func flatten(sets [][]string) []string {
	var out []string
	for _, set := range sets {
		out = append(out, set...)
	}
	return out
}

// minimumMuxSpan is the smallest port range MuxPorts hands out.
//
// The in-memory backend takes one listener per workload cluster out of this
// range, so it bounds how many clusters a run can bring up at once. The floor
// is generous rather than tight because exhausting it produces a failure to
// create a listener rather than a diagnosis, and because the range costs
// nothing until a listener is opened in it.
const minimumMuxSpan = 500

// maxPort is the highest port there is, and the ceiling a claimed range must
// fit under.
const maxPort = 65535

// MuxPorts picks a range for the in-memory backend's workload-cluster
// listeners, wide enough for the given number of clusters, and a debug port
// just above it.
//
// One free ephemeral port is probed and the span above it is claimed. That is
// an assumption rather than a reservation, and it is the one the sweeps have
// always made; a run large enough to care should read a failure to bind as
// "something else holds a port in the range" rather than as a defect in the
// fleet.
//
// The debug port is taken from the top of that claim rather than probed
// separately, which is not tidiness: two independent ephemeral probes come
// from one range, so at a fleet-sized span the second lands *inside* the first
// one's range often rather than rarely — and a workload cluster contending
// with the mux's own debug endpoint for a port is a failure that looks like a
// flake. One contiguous claim has one assumption in it instead of two.
func MuxPorts(clusters int) (inmemoryserver.CustomPorts, error) {
	if clusters < 0 {
		return inmemoryserver.CustomPorts{}, fmt.Errorf("cluster count is %d", clusters)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return inmemoryserver.CustomPorts{}, fmt.Errorf("finding a free port: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		return inmemoryserver.CustomPorts{}, errors.New("a TCP listener reported a non-TCP address")
	}
	base := int32(addr.Port)
	if err := l.Close(); err != nil {
		return inmemoryserver.CustomPorts{}, fmt.Errorf("releasing the probe listener: %w", err)
	}

	span := int32(minimumMuxSpan)
	if int32(clusters) > span { //nolint:gosec // a cluster count from a flag, not arithmetic on untrusted input.
		span = int32(clusters) //nolint:gosec // as above.
	}
	if base+span+1 > maxPort {
		return inmemoryserver.CustomPorts{}, fmt.Errorf(
			"a range of %d ports from %d runs past port %d: the probe landed too high for a fleet this size, and a rerun will land elsewhere",
			span, base, maxPort)
	}

	return inmemoryserver.CustomPorts{MinPort: base, MaxPort: base + span, DebugPort: base + span + 1}, nil
}

// FleetOptions is what the wiring needs that it cannot work out for itself.
type FleetOptions struct {
	// ShardConfig addresses the shard rather than the APIExport's virtual
	// workspace. The two serve different API surfaces and some wiring needs
	// the one the manager is not built on - a kubeconfig Secret is not served
	// by the virtual workspace.
	ShardConfig *rest.Config

	// Ports is the in-memory backend's listener range. Take it from MuxPorts.
	Ports inmemoryserver.CustomPorts

	// Host is the address the in-memory workload clusters listen on. Empty
	// means 127.0.0.1.
	Host string

	// SkipControllerNameValidation allows a second shape in the same test
	// binary. controller-runtime's controller-name registry is process-global,
	// so two shapes built in one process collide on it without this.
	SkipControllerNameValidation bool

	// FleetMaxConcurrentReconciles is the worker count for each fleet-wide
	// controller. Zero means the deployment's own default.
	FleetMaxConcurrentReconciles int
}

// SetupFleet wires every provider's controllers onto one manager.
//
// The order matters and is the deployments' own: process globals first,
// because the core Machine reconciler resolves a bootstrap ref through the
// contract-metadata registry and fails without it; then the in-memory backend;
// then the fleet the four provider setups share.
func SetupFleet(ctx context.Context, mgr mcmanager.Manager, registry *capicontrollerutil.WildcardRegistry, opts FleetOptions) error {
	if mgr == nil {
		return errors.New("a multi-cluster manager is required")
	}

	coremanager.SetupProcessGlobals()

	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}

	dev, err := coremanager.NewDevInfrastructure(ctx, host, opts.Ports)
	if err != nil {
		return fmt.Errorf("building the dev infrastructure provider: %w", err)
	}

	fleet, err := coremanager.NewFleet(ctx, mgr, registry, coremanager.SetupOptions{
		ShardConfig:                  opts.ShardConfig,
		SkipControllerNameValidation: opts.SkipControllerNameValidation,
		FleetMaxConcurrentReconciles: opts.FleetMaxConcurrentReconciles,
	})
	if err != nil {
		return fmt.Errorf("building the fleet: %w", err)
	}

	if err := coremanager.SetupCoreControllers(ctx, mgr, fleet, dev); err != nil {
		return fmt.Errorf("wiring the core controllers: %w", err)
	}
	if err := bootstrapmanager.SetupFleetControllers(ctx, mgr, fleet, bootstrapmanager.Options{}); err != nil {
		return fmt.Errorf("wiring the bootstrap controllers: %w", err)
	}
	if err := controlplanemanager.SetupFleetControllers(ctx, mgr, fleet, controlplanemanager.Options{}); err != nil {
		return fmt.Errorf("wiring the control plane controllers: %w", err)
	}
	return nil
}
