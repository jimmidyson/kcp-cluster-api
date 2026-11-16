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
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type recordedEvent struct {
	annotations map[string]string
	reason      string
}

type capturingRecorder struct{ events []recordedEvent }

func (c *capturingRecorder) Event(runtime.Object, string, string, string) {
	panic("the cluster-aware recorder must route everything through AnnotatedEventf")
}

func (c *capturingRecorder) Eventf(runtime.Object, string, string, string, ...any) {
	panic("the cluster-aware recorder must route everything through AnnotatedEventf")
}

func (c *capturingRecorder) AnnotatedEventf(_ runtime.Object, annotations map[string]string, _, reason, _ string, _ ...any) {
	c.events = append(c.events, recordedEvent{annotations: annotations, reason: reason})
}

func inCluster(name string) client.Object {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace:   "default",
		Name:        "subject",
		Annotations: map[string]string{"kcp.io/cluster": name},
	}}
}

func clusterOfAnnotation(o client.Object) (mcmulticluster.ClusterName, bool) {
	name := o.GetAnnotations()["kcp.io/cluster"]
	return mcmulticluster.ClusterName(name), name != ""
}

// TestClusterAwareRecorderMarksEveryEntryPoint covers all three, because a
// reconciler may use any of them and an unmarked event is one the sink cannot
// deliver.
func TestClusterAwareRecorderMarksEveryEntryPoint(t *testing.T) {
	g := NewWithT(t)

	inner := &capturingRecorder{}
	r := NewClusterAwareRecorder(inner, clusterOfAnnotation)

	r.Event(inCluster("ws-a"), corev1.EventTypeNormal, "One", "message")
	r.Eventf(inCluster("ws-b"), corev1.EventTypeNormal, "Two", "message %d", 2)
	r.AnnotatedEventf(inCluster("ws-c"), map[string]string{"keep": "me"}, corev1.EventTypeNormal, "Three", "message")

	g.Expect(inner.events).To(HaveLen(3))
	g.Expect(inner.events[0].annotations).To(HaveKeyWithValue(RecordInClusterAnnotation, "ws-a"))
	g.Expect(inner.events[1].annotations).To(HaveKeyWithValue(RecordInClusterAnnotation, "ws-b"))
	g.Expect(inner.events[2].annotations).To(HaveKeyWithValue(RecordInClusterAnnotation, "ws-c"))

	// The caller's own annotations survive alongside the mark.
	g.Expect(inner.events[2].annotations).To(HaveKeyWithValue("keep", "me"))
}

// TestClusterAwareRecorderDoesNotWriteIntoTheCallersMap covers a map a caller
// reuses across events.
//
// Writing into it would leave this project's routing key in the caller's map,
// and the next event recorded with it for a different object would carry the
// previous object's cluster — an event delivered to the wrong tenant, from a
// line of code that looks correct.
func TestClusterAwareRecorderDoesNotWriteIntoTheCallersMap(t *testing.T) {
	g := NewWithT(t)

	inner := &capturingRecorder{}
	r := NewClusterAwareRecorder(inner, clusterOfAnnotation)

	shared := map[string]string{"keep": "me"}
	r.AnnotatedEventf(inCluster("ws-a"), shared, corev1.EventTypeNormal, "One", "message")
	r.AnnotatedEventf(inCluster("ws-b"), shared, corev1.EventTypeNormal, "Two", "message")

	g.Expect(shared).To(Equal(map[string]string{"keep": "me"}))
	g.Expect(inner.events[0].annotations).To(HaveKeyWithValue(RecordInClusterAnnotation, "ws-a"))
	g.Expect(inner.events[1].annotations).To(HaveKeyWithValue(RecordInClusterAnnotation, "ws-b"))
}

// TestClusterAwareRecorderLeavesAnUnresolvableObjectUnmarked keeps the decision
// with the sink.
//
// Marking it with a guess would deliver one tenant's event to another. Passing
// it through unmarked makes it undeliverable, which the sink reports — a lost
// event with a reason, rather than a misplaced one without.
func TestClusterAwareRecorderLeavesAnUnresolvableObjectUnmarked(t *testing.T) {
	g := NewWithT(t)

	inner := &capturingRecorder{}
	r := NewClusterAwareRecorder(inner, clusterOfAnnotation)

	r.Eventf(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "no-cluster"}},
		corev1.EventTypeNormal, "One", "message")

	g.Expect(inner.events).To(HaveLen(1))
	g.Expect(inner.events[0].annotations).ToNot(HaveKey(RecordInClusterAnnotation))
}
