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

// Package capiservice is the Cluster API implementation of the scale harness's
// service seam.
//
// It is a package of its own rather than a file in internal/scaleharness for a
// structural reason: the harness asserts, by parsing its own imports, that the
// sweep, fit and departure point machinery knows nothing about any particular service. A
// Cluster API implementation living beside that machinery would defeat the
// assertion the moment it compiled. Keeping it here means the property is
// enforced by the package boundary rather than by everyone remembering.
package capiservice

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/jimmidyson/kcp-cluster-api/internal/scaleharness"
)

// Namespace is where the harness's objects are created. A single namespace
// keeps what is being measured attributable: object count per workspace, not
// namespace fan-out.
const Namespace = "default"

// Service populates workspaces with Cluster objects and mutates them to
// generate events.
//
// Cluster is the right type to measure with because it is what the wiring
// under test actually watches: the core reconcilers watch it directly, and the
// dynamic watches added for infrastructureRef hang off it. An object nothing
// watches would measure storage rather than the wiring.
type Service struct {
	// Prefix distinguishes one run's objects from another's.
	Prefix string
}

var _ scaleharness.Service = Service{}

// Name identifies this service in reports and published figures.
func (s Service) Name() string { return "cluster-api-core" }

// WatchedTypes reports what the wired controllers watch.
//
// This drives the listener term of the resource model, so it is the wired set
// rather than the reconciled set: the core Cluster and Machine reconcilers
// watch several types they never reconcile, and each of those is a
// registration whose cost scales with workspace count.
func (s Service) WatchedTypes() []string {
	return []string{
		"cluster.x-k8s.io/v1beta2/Cluster",
		"cluster.x-k8s.io/v1beta2/Machine",
		"cluster.x-k8s.io/v1beta2/MachineSet",
		"cluster.x-k8s.io/v1beta2/MachineDeployment",
	}
}

// Populate ensures the workspace holds the requested number of Clusters.
//
// Idempotent, as the seam requires: a sweep accumulates workspaces, so this is
// called again for every workspace at every later point, and an object that
// already exists means the workspace is already in the wanted shape.
func (s Service) Populate(ctx context.Context, c client.Client, objects int) error {
	for i := range objects {
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.objectName(i),
				Namespace: Namespace,
				Labels:    map[string]string{"scaleharness": s.Prefix},
			},
			// Paused is set explicitly, and not for its meaning. ClusterSpec is
			// tagged omitzero, so an entirely zero spec is omitted from the
			// serialised object and the server rejects it with "spec: Required
			// value". Setting any field makes the spec non-zero.
			//
			// This is the synthetic-load hazard in miniature, and it is why
			// ModeSynthetic figures carry their label: an object generated from
			// a type's Go shape can be rejected by the server that type came
			// from, and a sweep that measured rejections would report load it
			// never applied. Only a real server finds this.
			Spec: clusterv1.ClusterSpec{Paused: ptr.To(false)},
		}
		if err := c.Create(ctx, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Cluster %s: %w", cluster.Name, err)
		}
	}
	return nil
}

// Touch generates one event by relabelling the first Cluster.
//
// A label change is deliberate: it is a real update that every watcher sees,
// without depending on a controller having progressed the object's status,
// which would make the harness's event rate a function of reconcile throughput
// rather than an input to it.
func (s Service) Touch(ctx context.Context, c client.Client) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{Namespace: Namespace, Name: s.objectName(0)}
	if err := c.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("getting Cluster %s: %w", key.Name, err)
	}
	if cluster.Labels == nil {
		cluster.Labels = map[string]string{}
	}
	// A monotonically changing value, so consecutive touches are distinct
	// updates rather than no-ops the apiserver may discard.
	cluster.Labels["scaleharness-touch"] = fmt.Sprint(cluster.Generation)
	if err := c.Update(ctx, cluster); err != nil {
		return fmt.Errorf("updating Cluster %s: %w", key.Name, err)
	}
	return nil
}

func (s Service) objectName(i int) string { return fmt.Sprintf("%s-%d", s.Prefix, i) }
