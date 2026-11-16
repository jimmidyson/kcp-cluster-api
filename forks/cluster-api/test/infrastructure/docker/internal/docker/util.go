/*
Copyright 2018 The Kubernetes Authors.

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
	"strings"

	pkgerrors "github.com/pkg/errors"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
	"sigs.k8s.io/cluster-api/test/infrastructure/docker/internal/docker/types"
)

const (
	clusterLabelKey  = "io.x-k8s.kind.cluster"
	nodeRoleLabelKey = "io.x-k8s.kind.role"
	filterLabel      = "label"
	filterName       = "name"

	failureDomainLabelKey = "io.x-k8s.cluster.failureDomain"

	// logicalClusterLabelKey records which kcp logical cluster a container's
	// Cluster was read from, so that two clusters sharing a name do not share
	// containers.
	//
	// # Why a cluster name is not enough here
	//
	// Every lookup in this package selects containers by clusterLabelKey, whose
	// value is the Cluster's name. That is sufficient where Cluster API
	// normally runs, because one management cluster's docker daemon serves one
	// set of names. It is not sufficient under kcp: a workspace is a whole API
	// server, two workspaces routinely hold a Cluster with the same name, and
	// both are served by controllers sharing one daemon.
	//
	// Unqualified, the daemon cannot tell them apart, and the failure is worse
	// than a name clash. The load balancer collects its backend servers by this
	// label, so one workspace's load balancer is configured with another
	// workspace's control plane and fronts a cluster it does not belong to.
	// What that looks like from the outside is a TLS failure - kubeadm names
	// every cluster's CA "kubernetes", so reaching the wrong cluster is
	// "certificate signed by unknown authority" against a CA with the right
	// name and the wrong key, which reads as a certificate bug rather than as
	// the routing one it is.
	logicalClusterLabelKey = "kcp.io/logical-cluster"

	// logicalClusterAnnotation is where kcp records the logical cluster an
	// object was read from. It is on the object rather than in configuration,
	// which is why every helper here takes the Cluster and derives the scope
	// from it rather than being told.
	logicalClusterAnnotation = "kcp.io/cluster"
)

// logicalClusterOf returns the kcp logical cluster the Cluster was read from,
// or the empty string when it was not read from kcp.
//
// Empty is the ordinary Cluster API case and everything below treats it as
// "do not qualify", so a fork carrying this behaves exactly as upstream does
// wherever kcp is not involved.
func logicalClusterOf(cluster *clusterv1.Cluster) string {
	if cluster == nil {
		return ""
	}
	return cluster.Annotations[logicalClusterAnnotation]
}

// scopedContainerName qualifies a container name with its logical cluster.
//
// Only names that must be unique on the daemon need this: container names are
// unique per daemon rather than per label, so two workspaces asking for the
// same one is a hard collision that no filter can resolve afterwards.
func scopedContainerName(logicalCluster, name string) string {
	if logicalCluster == "" {
		return name
	}
	return fmt.Sprintf("%s-%s", logicalCluster, name)
}

// clusterContainerFilters selects the containers belonging to one cluster, in
// one logical cluster.
func clusterContainerFilters(clusterName, logicalCluster string) container.FilterBuilder {
	filters := container.FilterBuilder{}
	filters.AddKeyNameValue(filterLabel, clusterLabelKey, clusterName)
	if logicalCluster != "" {
		filters.AddKeyNameValue(filterLabel, logicalClusterLabelKey, logicalCluster)
	}
	return filters
}

// withLogicalClusterLabel returns labels with the logical cluster recorded, so
// that the container can be found by [clusterContainerFilters] later.
func withLogicalClusterLabel(labels map[string]string, logicalCluster string) map[string]string {
	if logicalCluster == "" {
		return labels
	}
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[logicalClusterLabelKey] = logicalCluster
	return out
}

// FailureDomainLabel returns a map with the docker label for the given failure domain.
func FailureDomainLabel(failureDomain string) map[string]string {
	if failureDomain != "" {
		return map[string]string{failureDomainLabelKey: failureDomain}
	}
	return nil
}

// MachineContainerName computes the name of a container for a given machine.
func MachineContainerName(cluster, machine string) string {
	if strings.HasPrefix(machine, cluster) {
		return machine
	}
	return fmt.Sprintf("%s-%s", cluster, machine)
}

func machineFromContainerName(cluster, containerName string) string {
	machine := strings.TrimPrefix(containerName, cluster)
	return strings.TrimPrefix(machine, "-")
}

// listContainers returns the list of docker containers matching filters.
func listContainers(ctx context.Context, filters container.FilterBuilder) ([]*types.Node, error) {
	n, err := List(ctx, filters)
	if err != nil {
		return nil, pkgerrors.Wrapf(err, "failed to list containers")
	}
	return n, nil
}

// getContainer returns the docker container matching filters.
func getContainer(ctx context.Context, filters container.FilterBuilder) (*types.Node, error) {
	n, err := listContainers(ctx, filters)
	if err != nil {
		return nil, err
	}

	switch len(n) {
	case 0:
		return nil, nil
	case 1:
		return n[0], nil
	default:
		return nil, pkgerrors.Errorf("expected 0 or 1 container, got %d", len(n))
	}
}

// List returns the list of container IDs for the kind "nodes", optionally
// filtered by docker ps filters
// https://docs.docker.com/engine/reference/commandline/ps/#filtering
func List(ctx context.Context, filters container.FilterBuilder) ([]*types.Node, error) {
	res := []*types.Node{}
	visit := func(_ context.Context, _ string, node *types.Node) {
		res = append(res, node)
	}
	return res, list(ctx, visit, filters)
}

func list(ctx context.Context, visit func(context.Context, string, *types.Node), filters container.FilterBuilder) error {
	containerRuntime, err := container.RuntimeFrom(ctx)
	if err != nil {
		return pkgerrors.Wrap(err, "failed to connect to container runtime")
	}

	// We also need our cluster label key to the list of filter
	filters.AddKeyValue("label", clusterLabelKey)

	containers, err := containerRuntime.ListContainers(ctx, filters)
	if err != nil {
		return pkgerrors.Wrap(err, "failed to list containers")
	}

	for _, cntr := range containers {
		name := cntr.Name
		cluster := clusterLabelKey
		image := cntr.Image
		status := cntr.Status

		visit(ctx, cluster, types.NewNode(name, image, "undetermined").WithStatus(status))
	}

	return nil
}
