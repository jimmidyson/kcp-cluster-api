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

package fleet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/controllers/clustercache"
)

// fakeClusterCache is a ClusterCache whose Cluster-event channel the test
// drives.
//
// It reproduces what the real GetClusterSource does — wrap the channel in a
// source with the caller's map function — rather than using
// clustercache.NewFakeClusterCache, whose event-sending path is unexported and
// so cannot be driven from here. Every other method is left nil: this test is
// about the fan-in, and a call to any of them would be a bug the panic reports.
type fakeClusterCache struct {
	clustercache.ClusterCache

	ch chan event.GenericEvent

	mu   sync.Mutex
	name string
}

func newFakeClusterCache() *fakeClusterCache {
	return &fakeClusterCache{ch: make(chan event.GenericEvent)}
}

func (f *fakeClusterCache) GetClusterSource(controllerName string, mapFunc func(context.Context, client.Object) []ctrl.Request, _ ...clustercache.GetClusterSourceOption) source.Source {
	f.mu.Lock()
	f.name = controllerName
	f.mu.Unlock()
	return source.Channel(f.ch, handler.TypedEnqueueRequestsFromMapFunc(mapFunc))
}

func (f *fakeClusterCache) controllerName() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.name
}

// TestSourceFanInLabelsEventsWithTheirWorkspace is the property the whole
// adapter exists for: one source, many workspaces, and every request carrying
// the workspace whose ClusterCache produced it.
//
// It also covers the two orderings that differ, because a workspace can engage
// either side of the controller starting the source, and losing the ones that
// engage first would be invisible until a Cluster's remote connection dropped.
func TestSourceFanInLabelsEventsWithTheirWorkspace(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	caches := NewClusterCaches()

	// "before" is registered ahead of Start, "after" behind it.
	before, after := newFakeClusterCache(), newFakeClusterCache()
	g.Expect(caches.Add("ws-before", before)).To(Succeed())

	// mapFuncWorkspaces records the workspace each call saw in its context. The
	// map function is where a fleet-wide controller would list, so a call that
	// cannot see its workspace is the failure this guards.
	var mu sync.Mutex
	mapFuncWorkspaces := map[string]string{}
	mapFunc := func(ctx context.Context, o client.Object) []ctrl.Request {
		workspace, _ := mccontext.ClusterFrom(ctx)
		mu.Lock()
		mapFuncWorkspaces[o.GetName()] = string(workspace)
		mu.Unlock()
		return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
	}

	src := caches.Source("test-controller", mapFunc)

	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer queue.ShutDown()
	g.Expect(src.Start(ctx, queue)).To(Succeed())

	g.Expect(caches.Add("ws-after", after)).To(Succeed())

	// Both were asked for a source under the controller's own name, which is
	// what keeps the probe-failure bookkeeping attributable.
	g.Eventually(before.controllerName).Should(Equal("test-controller"))
	g.Eventually(after.controllerName).Should(Equal("test-controller"))

	sendCluster(g, ctx, before.ch, "from-before")
	sendCluster(g, ctx, after.ch, "from-after")

	got := map[string]string{}
	for range 2 {
		req := nextRequest(g, queue)
		got[req.Name] = string(req.ClusterName)
	}
	g.Expect(got).To(Equal(map[string]string{
		"from-before": "ws-before",
		"from-after":  "ws-after",
	}))

	mu.Lock()
	defer mu.Unlock()
	g.Expect(mapFuncWorkspaces).To(Equal(map[string]string{
		"from-before": "ws-before",
		"from-after":  "ws-after",
	}))
}

// TestSourceFanInStopsAfterRemove covers workspace disengagement: a removed
// workspace's events must stop arriving, or a tenant that has gone away keeps
// waking a controller that can no longer serve it.
func TestSourceFanInStopsAfterRemove(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	caches := NewClusterCaches()
	cc := newFakeClusterCache()
	g.Expect(caches.Add("ws", cc)).To(Succeed())

	src := caches.Source("test-controller", func(_ context.Context, o client.Object) []ctrl.Request {
		return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
	})
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer queue.ShutDown()
	g.Expect(src.Start(ctx, queue)).To(Succeed())

	sendCluster(g, ctx, cc.ch, "before-remove")
	g.Expect(nextRequest(g, queue).Name).To(Equal("before-remove"))

	caches.Remove("ws")

	// The send must not be received. A blocking send is the right probe: the
	// real ClusterCache sends the same way, so a source that had not stopped
	// would take this and enqueue it.
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		select {
		case cc.ch <- event.GenericEvent{Object: cluster("after-remove")}:
		case <-time.After(2 * time.Second):
		}
	}()
	<-sent
	g.Consistently(queue.Len, time.Second, 100*time.Millisecond).Should(BeZero())
}

// TestScopedClusterCacheResolvesTheContextWorkspace covers the other view of the
// registry, including both ways it can fail to resolve one.
func TestScopedClusterCacheResolvesTheContextWorkspace(t *testing.T) {
	g := NewWithT(t)

	caches := NewClusterCaches()
	scoped := caches.Scoped()

	_, err := scoped.GetClient(context.Background(), client.ObjectKey{Name: "c"})
	g.Expect(errors.Is(err, ErrNoWorkspaceInContext)).To(BeTrue(), "got %v", err)

	ctx := mccontext.WithCluster(context.Background(), "ws")
	_, err = scoped.GetClient(ctx, client.ObjectKey{Name: "c"})
	g.Expect(errors.Is(err, ErrWorkspaceNotRegistered)).To(BeTrue(), "got %v", err)

	// GetHealthCheckingState cannot report an error, so it degrades to the zero
	// state rather than to another workspace's answer.
	g.Expect(scoped.GetHealthCheckingState(ctx, client.ObjectKey{Name: "c"})).
		To(Equal(clustercache.HealthCheckingState{}))

	// GetClusterSource has no context to resolve, and fails rather than
	// guessing.
	err = scoped.GetClusterSource("test-controller", nil).Start(ctx, nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("cannot serve GetClusterSource"))
}

// TestClusterCachesRejectsDuplicateWorkspaces guards the registration path: a
// second ClusterCache for a workspace would leave the first one's sources
// attached and its clients unreachable.
func TestClusterCachesRejectsDuplicateWorkspaces(t *testing.T) {
	g := NewWithT(t)

	caches := NewClusterCaches()
	g.Expect(caches.Add("ws", newFakeClusterCache())).To(Succeed())
	g.Expect(caches.Add("ws", newFakeClusterCache())).ToNot(Succeed())
	g.Expect(caches.Add("ws-nil", nil)).ToNot(Succeed())

	caches.Remove("ws")
	g.Expect(caches.Add("ws", newFakeClusterCache())).To(Succeed())
}

// TestSourceRejectsASecondStart guards against a source being registered with
// two controllers, which would give the second one the first one's queue.
func TestSourceRejectsASecondStart(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	caches := NewClusterCaches()
	src := caches.Source("test-controller", nil)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer queue.ShutDown()

	g.Expect(src.Start(ctx, queue)).To(Succeed())
	g.Expect(src.Start(ctx, queue)).ToNot(Succeed())
}

var _ multicluster.ClusterName = "" // the registry is keyed on these

func cluster(name string) *clusterv1.Cluster {
	return &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
}

func sendCluster(g *WithT, ctx context.Context, ch chan event.GenericEvent, name string) {
	g.THelper()
	select {
	case ch <- event.GenericEvent{Object: cluster(name)}:
	case <-ctx.Done():
		g.Expect(ctx.Err()).ToNot(HaveOccurred())
	case <-time.After(10 * time.Second):
		g.Expect(false).To(BeTrue(), "timed out sending Cluster event %q; the source is not attached", name)
	}
}

func nextRequest(g *WithT, queue workqueue.TypedRateLimitingInterface[mcreconcile.Request]) mcreconcile.Request {
	g.THelper()
	got := make(chan mcreconcile.Request, 1)
	go func() {
		item, shutdown := queue.Get()
		if !shutdown {
			queue.Done(item)
			got <- item
		}
	}()
	select {
	case req := <-got:
		return req
	case <-time.After(10 * time.Second):
		g.Expect(false).To(BeTrue(), "timed out waiting for a request")
		return mcreconcile.Request{}
	}
}
