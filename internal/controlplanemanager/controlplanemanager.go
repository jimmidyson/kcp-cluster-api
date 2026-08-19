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

// Package controlplanemanager wires the kubeadm control plane provider as a
// controller that serves every workspace bound to its APIExport - the
// conversion plan's P2.
//
// # What this provider adds that the others do not
//
// It is the first one that talks to the workload clusters it creates. The
// others read and write objects in the management workspace; this one connects
// to each cluster's API server and to its etcd members, through the
// ClusterCache and a client certificate it mints from the cluster's CA. That
// is why it needs a grace period rather than a timeout: the ClusterCache drops
// a connection on repeated probe failure, and the reconciler must not conclude
// a control plane is unreachable before that has happened.
//
// It is also the first that *creates* objects another provider publishes. A
// KubeadmControlPlane creates Machines (core's export) and KubeadmConfigs (the
// bootstrap provider's), so its export claims both for writing - see
// internal/capiexports. Where the other providers' claims are mostly "let me
// watch this", this provider's are "let me author this", which is the case
// that most repays scoping claims by verb once they are expressed in kcp's
// v1alpha2 shape.
package controlplanemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	"sigs.k8s.io/cluster-api/controlplane/kubeadm/reconcilers/kubeadmcontrolplane"
)

// Defaults taken from upstream's controlplane/kubeadm/main.go, restated here
// because this binary does not expose its full flag surface.
//
// RemoteConditionsGracePeriod has a floor the reconciler enforces rather than a
// preference: the ClusterCache can take 75 seconds to drop a connection under
// its own health checking, so anything under two minutes lets the provider
// decide a control plane is unreachable while the cache still believes in it.
const (
	DefaultEtcdDialTimeout             = 10 * time.Second
	DefaultEtcdCallTimeout             = 15 * time.Second
	DefaultRemoteConditionsGracePeriod = 5 * time.Minute
)

// Options configures what SetupFleetControllers wires.
type Options struct {
	// EtcdDialTimeout and EtcdCallTimeout bound connections to a workload
	// cluster's etcd members. Zero means the defaults above.
	EtcdDialTimeout time.Duration
	EtcdCallTimeout time.Duration

	// RemoteConditionsGracePeriod is how long the provider waits before
	// reporting a control plane it cannot reach as unhealthy. Zero means the
	// default; anything under two minutes is rejected by the reconciler.
	RemoteConditionsGracePeriod time.Duration
}

func (o Options) etcdDialTimeout() time.Duration {
	if o.EtcdDialTimeout <= 0 {
		return DefaultEtcdDialTimeout
	}
	return o.EtcdDialTimeout
}

func (o Options) etcdCallTimeout() time.Duration {
	if o.EtcdCallTimeout <= 0 {
		return DefaultEtcdCallTimeout
	}
	return o.EtcdCallTimeout
}

func (o Options) remoteConditionsGracePeriod() time.Duration {
	if o.RemoteConditionsGracePeriod <= 0 {
		return DefaultRemoteConditionsGracePeriod
	}
	return o.RemoteConditionsGracePeriod
}

// SetupFleetControllers wires the KubeadmControlPlane reconciler onto an
// already-built Fleet, as one controller serving every engaged workspace.
//
// It MUST be called before mgr.Start, and exactly once.
func SetupFleetControllers(ctx context.Context, mgr mcmanager.Manager, fleet *coremanager.Fleet, opts Options) error {
	if mgr == nil {
		return errors.New("a multi-cluster manager is required")
	}
	if fleet == nil {
		return errors.New("a fleet is required: it carries the clients, the ClusterCache and the wildcard registration this controller shares with every other provider in the process")
	}

	// Client and SecretCachingClient are the same value, as for the bootstrap
	// provider: both resolve the workspace from the context, and the
	// label-filtered Secret cache upstream separates them for is not built
	// here. See internal/bootstrapmanager for what that costs.
	if err := (&kubeadmcontrolplane.Reconciler{
		Client:                      fleet.Client,
		APIReader:                   fleet.APIReader,
		SecretCachingClient:         fleet.Client,
		ClusterCache:                fleet.ClusterCache,
		EtcdDialTimeout:             opts.etcdDialTimeout(),
		EtcdCallTimeout:             opts.etcdCallTimeout(),
		RemoteConditionsGracePeriod: opts.remoteConditionsGracePeriod(),
	}).SetupWithMulticlusterManager(ctx, mgr, fleet.Options, fleet.ClusterSource, fleet.BuilderOptions()...); err != nil {
		return fmt.Errorf("creating fleet-wide KubeadmControlPlane controller: %w", err)
	}

	return nil
}
