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

package coremanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpconfig"
	"k8s.io/client-go/tools/record"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"

	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

// NewWorkspaceEventSink returns a record.EventSink that writes each event to the
// workspace it is marked for.
//
// # Where events have to go, and why it is here
//
// Into the workspace the object lives in, on the shard — measured, in
// test/integration/events: a kcp workspace does serve core v1 Events, so there
// is somewhere for a tenant's events to land and be read by whoever is watching
// that workspace.
//
// Not the APIExport's virtual workspace, which is where they were going and why
// none of them arrived. That endpoint serves what the export serves, and the
// manager addresses it at /clusters/*, which names no logical cluster to write
// to. Every event Cluster API emitted was rejected with "the server could not
// find the requested resource (post events)".
//
// This is the same split as the kubeconfig Secret reader: core types live on the
// shard, exported types on the virtual workspace, and knowing which is which is
// the one piece Cluster API is not asked to know.
//
// # What it costs
//
// One client per workspace, holding a transport rather than any object, cached
// and released by kcp's client cache. No goroutine and no queue per workspace:
// the broadcaster feeding this is one for the process, and its single watcher
// goroutine is what calls Create here — off the reconcile path, which is the
// property client-go's recorder has and this keeps.
func NewWorkspaceEventSink(shard *rest.Config) (record.EventSink, error) {
	if shard == nil {
		return nil, errors.New("a shard rest.Config is required: the virtual workspace does not serve Events")
	}

	// See NewWorkspaceSecretReader: the cache appends a cluster path, so the
	// config it is handed must not already carry one.
	shard = kcpconfig.Base(shard)

	httpClient, err := rest.HTTPClientFor(shard)
	if err != nil {
		return nil, fmt.Errorf("building the shard HTTP client: %w", err)
	}

	clients := kcpclient.NewCache(shard, httpClient, &kcpclient.Constructor[kubernetes.Interface]{
		NewForConfigAndClient: func(cfg *rest.Config, h *http.Client) (kubernetes.Interface, error) {
			return kubernetes.NewForConfigAndClient(cfg, h)
		},
	})

	return &workspaceEventSink{clients: clients}, nil
}

type workspaceEventSink struct {
	clients kcpclient.Cache[kubernetes.Interface]
}

func (s *workspaceEventSink) Create(event *corev1.Event) (*corev1.Event, error) {
	events, event, err := s.eventsFor(event)
	if err != nil {
		return nil, err
	}
	return events.Create(context.TODO(), event, metav1.CreateOptions{})
}

func (s *workspaceEventSink) Update(event *corev1.Event) (*corev1.Event, error) {
	events, event, err := s.eventsFor(event)
	if err != nil {
		return nil, err
	}
	return events.Update(context.TODO(), event, metav1.UpdateOptions{})
}

func (s *workspaceEventSink) Patch(event *corev1.Event, data []byte) (*corev1.Event, error) {
	events, event, err := s.eventsFor(event)
	if err != nil {
		return nil, err
	}
	return events.Patch(context.TODO(), event.Name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
}

// eventsFor resolves the workspace an event is marked for and strips the mark.
//
// The strip matters: the annotation is this project's routing decision, and
// storing it on the tenant's event would present it as something the event says
// about itself.
func (s *workspaceEventSink) eventsFor(event *corev1.Event) (typedcorev1.EventInterface, *corev1.Event, error) {
	if event == nil {
		return nil, nil, errors.New("nil event")
	}

	name := event.Annotations[capimulticluster.RecordInClusterAnnotation]
	if name == "" {
		// Undeliverable rather than sent somewhere. An event is a note about a
		// tenant's object, and a note whose tenant is unknown is not one to
		// leave on an arbitrary desk.
		return nil, nil, fmt.Errorf("event %s/%s carries no %s annotation, so there is no workspace to write it to",
			event.Namespace, event.Name, capimulticluster.RecordInClusterAnnotation)
	}

	out := event.DeepCopy()
	delete(out.Annotations, capimulticluster.RecordInClusterAnnotation)
	if len(out.Annotations) == 0 {
		out.Annotations = nil
	}

	client, err := s.clients.Cluster(logicalcluster.NewPath(name))
	if err != nil {
		return nil, nil, fmt.Errorf("building a client for workspace %q: %w", name, err)
	}
	return client.CoreV1().Events(out.Namespace), out, nil
}
