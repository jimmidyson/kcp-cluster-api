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

package multicluster

import (
	"maps"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RecordInClusterAnnotation names the cluster an event is to be written to.
//
// # Why an annotation and not an argument
//
// record.EventRecorder takes no context, so the cluster cannot travel the way it
// does for clients — the reconciler calls r.recorder.Eventf(obj, ...) and there
// is nowhere to put it. What there *is* is the event itself, which the recorder
// builds and the sink writes, so the cluster travels on that.
//
// It is stripped before the event is written. A sink that left it in place would
// be storing this project's routing decision as though it were data about the
// event.
//
// Not kcp's own kcp.io/cluster: that one is set by the API server on stored
// objects, and writing it would be claiming something this is not entitled to
// claim. This says only where the event is going.
const RecordInClusterAnnotation = "multicluster.cluster.x-k8s.io/record-in-cluster"

// NewClusterAwareRecorder returns a record.EventRecorder that marks each event
// with the cluster of the object it is about.
//
// # What it is for
//
// A fleet-wide controller reconciles objects in many clusters and holds one
// recorder. Without this, every event it emits goes wherever that recorder was
// built to write — for this project, the APIExport's virtual workspace, which
// serves no core v1.Event and names no logical cluster to write to, so every
// event Cluster API emitted was rejected:
//
//	Server rejected event (will not retry!): the server could not find the
//	requested resource (post events)
//
// # What it does not do
//
// Write anything. It marks; a sink routes. That split is deliberate: which
// cluster an object belongs to is a property of the object, and Cluster API can
// be told how to read it (ClusterResolver), but *where* that cluster's API
// server is, and how to reach it, is the caller's business entirely.
//
// # What it costs
//
// Nothing per cluster. The recorder underneath is one broadcaster for the
// process, so events stay asynchronous and aggregated as they are for a
// single-cluster controller, and no workspace adds a goroutine or a queue.
// Aggregation does not merge across clusters despite the shared broadcaster,
// because client-go keys it on the involved object's UID among other things,
// and two clusters' identically named objects have different UIDs.
func NewClusterAwareRecorder(inner record.EventRecorder, clusterOf ClusterResolver) record.EventRecorder {
	return &clusterAwareRecorder{inner: inner, clusterOf: clusterOf}
}

type clusterAwareRecorder struct {
	inner     record.EventRecorder
	clusterOf ClusterResolver
}

func (r *clusterAwareRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	r.inner.AnnotatedEventf(object, r.annotations(object, nil), eventtype, reason, "%s", message)
}

func (r *clusterAwareRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...any) {
	r.inner.AnnotatedEventf(object, r.annotations(object, nil), eventtype, reason, messageFmt, args...)
}

func (r *clusterAwareRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...any) {
	r.inner.AnnotatedEventf(object, r.annotations(object, annotations), eventtype, reason, messageFmt, args...)
}

// annotations copies the caller's rather than writing into it, because the
// caller may be reusing the map across events and would otherwise find this
// project's routing key in it.
func (r *clusterAwareRecorder) annotations(object runtime.Object, given map[string]string) map[string]string {
	obj, ok := object.(client.Object)
	if !ok {
		// Nothing to resolve a cluster from. Passed through unmarked, which the
		// sink reports as undeliverable rather than guessing a destination.
		return given
	}
	cluster, ok := r.clusterOf(obj)
	if !ok {
		return given
	}

	out := make(map[string]string, len(given)+1)
	maps.Copy(out, given)
	out[RecordInClusterAnnotation] = string(cluster)
	return out
}
