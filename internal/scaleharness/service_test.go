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

package scaleharness

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// configMapService is the second implementation of Service, and it exists for
// one reason: property S2 of contracts/service-characterisation.md.
//
// A seam with a single implementation is a claim. This one constructs
// ConfigMaps, has nothing to do with Cluster API, and drives the same sweep and
// departure point machinery — which is what turns "characterising the next controller will
// be cheap" from an intention into something demonstrated. It stands in for the
// VM or database services the appliance model anticipates, before they exist.
type configMapService struct{ prefix string }

func (s configMapService) Name() string { return "configmaps" }

func (s configMapService) WatchedTypes() []string { return []string{"v1/ConfigMap"} }

func (s configMapService) Populate(ctx context.Context, c client.Client, objects int) error {
	for i := range objects {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", s.prefix, i),
				Namespace: "default",
			},
			Data: map[string]string{"n": fmt.Sprint(i)},
		}
		if err := c.Create(ctx, cm); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func (s configMapService) Touch(ctx context.Context, c client.Client) error {
	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: "default", Name: s.prefix + "-0"}
	if err := c.Get(ctx, key, cm); err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["touched"] = "yes"
	return c.Update(ctx, cm)
}

var _ Service = configMapService{}

func TestServiceImplementationDrivesTheHarness(t *testing.T) {
	svc := configMapService{prefix: "probe"}
	c := fake.NewClientBuilder().Build()
	ctx := t.Context()

	if err := svc.Populate(ctx, c, 3); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if err := svc.Touch(ctx, c); err != nil {
		t.Fatalf("Touch: %v", err)
	}

	list := &corev1.ConfigMapList{}
	if err := c.List(ctx, list); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Items) != 3 {
		t.Errorf("populated %d objects, want 3", len(list.Items))
	}
	if got := len(svc.WatchedTypes()); got != 1 {
		t.Errorf("WatchedTypes has %d entries, want 1", got)
	}
}

// The watch set is what makes the listener term of the resource model
// per-service rather than a constant, so it has to be reported by the service
// and non-empty.
func TestServiceReportsANonEmptyWatchSet(t *testing.T) {
	for _, svc := range []Service{configMapService{prefix: "a"}} {
		if svc.Name() == "" {
			t.Error("service has no name; reports would be unattributable")
		}
		if len(svc.WatchedTypes()) == 0 {
			t.Errorf("%s reports no watched types; the listener term of the model would be zero for it", svc.Name())
		}
	}
}

func TestIdleAndActiveProfilesDiffer(t *testing.T) {
	idle, active := IdleHeavy(), ActiveHeavy()

	if idle.ObjectsPerWorkspace != 0 {
		t.Errorf("idle-heavy has %d objects per workspace, want 0 — the point of the profile is workspaces that hold nothing",
			idle.ObjectsPerWorkspace)
	}
	if active.ObjectsPerWorkspace == 0 {
		t.Error("active-heavy holds no objects; it would measure the same thing as idle-heavy")
	}
	if idle.Name == active.Name {
		t.Error("profiles share a name; capacity is stated per profile and would be ambiguous")
	}
}

// FR-036: the event rate is a declared parameter, never inferred. The
// event-dispatch term is quadratic in workspace count and highly sensitive to
// it, so a profile that leaves it implicit would silently encode a workload
// assumption the operator does not share.
func TestActiveProfileDeclaresItsEventRate(t *testing.T) {
	if got := ActiveHeavy().EventsPerWorkspacePerSecond; got <= 0 {
		t.Errorf("active-heavy declares an event rate of %v; it must be stated, not left to be inferred", got)
	}
}

func TestProfileValidationRejectsIncoherentShapes(t *testing.T) {
	for name, p := range map[string]Profile{
		"no name":          {ObjectsPerWorkspace: 1},
		"negative objects": {Name: "x", ObjectsPerWorkspace: -1},
		"negative rate":    {Name: "x", EventsPerWorkspacePerSecond: -1},
		"events without objects": {
			Name: "x", ObjectsPerWorkspace: 0, EventsPerWorkspacePerSecond: 5,
		},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted an incoherent profile", name)
		}
	}

	for name, p := range map[string]Profile{
		"idle":   IdleHeavy(),
		"active": ActiveHeavy(),
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: Validate rejected a built-in profile: %v", name, err)
		}
	}
}
