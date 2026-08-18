//go:build integration

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

// Package events_test establishes where a Cluster API event can be recorded.
//
// Cluster API's reconcilers emit events, and under the fleet-wide wiring every
// one of them is rejected:
//
//	Server rejected event (will not retry!): the server could not find the
//	requested resource (post events)
//
// The recorder is the local manager's, and the local manager addresses the
// APIExport's virtual workspace at /clusters/* — an endpoint that serves what
// the export serves, and that names no logical cluster to write to anyway.
//
// Before building a recorder that writes somewhere else, this establishes that
// there *is* somewhere else: that a kcp workspace serves core v1 Events at all.
// A fix that assumed it does and was wrong would be a second silent failure in
// place of the first.
package events_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptesting "github.com/kcp-dev/sdk/testing"

	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/jimmidyson/kcp-cluster-api/internal/coremanager"
	kcpenvtest "github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

func TestWorkspaceServesEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, server := kcpenvtest.EnvironmentAndServer(t, "root")
	baseCfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("building scheme: %v", err)
	}

	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
	cfg := kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a client for workspace %s: %v", ws.Spec.Cluster, err)
	}

	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "probe.0001", Namespace: "default"},
		InvolvedObject: corev1.ObjectReference{
			Kind: "Cluster", Namespace: "default", Name: "probe", APIVersion: "cluster.x-k8s.io/v1beta2",
		},
		Reason:         "Probe",
		Message:        "does this workspace serve events",
		Type:           corev1.EventTypeNormal,
		FirstTimestamp: metav1.Now(),
		LastTimestamp:  metav1.Now(),
		Count:          1,
		Source:         corev1.EventSource{Component: "events-probe"},
	}
	if err := c.Create(ctx, event); err != nil {
		t.Fatalf("a kcp workspace does not accept a core v1 Event, so there is nowhere for a "+
			"workspace-scoped recorder to write: %v", err)
	}

	var got corev1.Event
	if err := c.Get(ctx, client.ObjectKeyFromObject(event), &got); err != nil {
		t.Fatalf("reading the event back from workspace %s: %v", ws.Spec.Cluster, err)
	}
	t.Logf("workspace %s serves events: created %s/%s reason=%s", ws.Spec.Cluster, got.Namespace, got.Name, got.Reason)
}

// TestRecordedEventsLandInTheirOwnWorkspace is the fix, end to end.
//
// One broadcaster and one sink serve every workspace, as they do in a
// deployment, and each event has to reach the workspace of the object it is
// about and no other. Two workspaces, because a routing bug that sends
// everything to one place passes any test that only has one.
func TestRecordedEventsLandInTheirOwnWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, server := kcpenvtest.EnvironmentAndServer(t, "root")
	baseCfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("building scheme: %v", err)
	}

	type workspace struct {
		name   string
		client client.Client
	}
	var workspaces []workspace
	for range 2 {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"))
		c, err := client.New(kcpclient.SetCluster(rest.CopyConfig(baseCfg), wsPath), client.Options{Scheme: scheme})
		if err != nil {
			t.Fatalf("building a client for workspace %s: %v", ws.Spec.Cluster, err)
		}
		workspaces = append(workspaces, workspace{name: ws.Spec.Cluster, client: c})
	}

	// The production wiring: one sink on the shard, one broadcaster, and a
	// recorder that marks each event with the cluster of its object.
	sink, err := coremanager.NewWorkspaceEventSink(baseCfg)
	if err != nil {
		t.Fatalf("building the workspace event sink: %v", err)
	}
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(sink)
	t.Cleanup(broadcaster.Shutdown)

	recorder := capimulticluster.NewClusterAwareRecorder(
		broadcaster.NewRecorder(scheme, corev1.EventSource{Component: "events-test"}),
		func(o client.Object) (mcmulticluster.ClusterName, bool) {
			name := logicalcluster.From(o)
			return mcmulticluster.ClusterName(name), name != ""
		},
	)

	// Distinct names, so that "it landed" cannot be satisfied by the other
	// workspace's event. Distinct UIDs for the same reason, and because
	// client-go's aggregation keys on them.
	for i, ws := range workspaces {
		obj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   "default",
				Name:        fmt.Sprintf("subject-%d", i),
				UID:         apitypes.UID(fmt.Sprintf("11111111-0000-0000-0000-00000000000%d", i)),
				Annotations: map[string]string{"kcp.io/cluster": ws.name},
			},
		}
		recorder.Eventf(obj, corev1.EventTypeNormal, "Recorded", "an event for %s", ws.name)
	}

	// Each workspace holds exactly its own event, and nothing of the other's.
	for i, ws := range workspaces {
		var found *corev1.Event
		wait := func() bool {
			var events corev1.EventList
			if err := ws.client.List(ctx, &events, client.InNamespace("default")); err != nil {
				t.Fatalf("listing events in workspace %s: %v", ws.name, err)
			}
			for j := range events.Items {
				if events.Items[j].Reason == "Recorded" {
					found = &events.Items[j]
					return true
				}
			}
			return false
		}
		deadline := time.Now().Add(90 * time.Second)
		for !wait() {
			if time.Now().After(deadline) {
				t.Fatalf("no recorded event reached workspace %s (%s) within 90s", ws.name, workspaces[i].name)
			}
			time.Sleep(250 * time.Millisecond)
		}

		if got, want := found.InvolvedObject.Name, fmt.Sprintf("subject-%d", i); got != want {
			t.Errorf("workspace %s holds an event about %s, want %s: an event reached the wrong tenant",
				ws.name, got, want)
		}
		if got := found.Message; got != fmt.Sprintf("an event for %s", ws.name) {
			t.Errorf("workspace %s holds message %q, want the one recorded for it", ws.name, got)
		}
		// The routing mark is this project's business, not the tenant's.
		if _, present := found.Annotations[capimulticluster.RecordInClusterAnnotation]; present {
			t.Errorf("workspace %s's event still carries %s: the routing decision was stored as data",
				ws.name, capimulticluster.RecordInClusterAnnotation)
		}
	}
}
