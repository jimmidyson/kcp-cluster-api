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

// Package multicluster_test exercises the fleet-wide wiring against a real API
// server.
//
// It is an external test package on purpose: controllers/external imports
// util/multicluster, and controller-runtime's envtest pulls in enough of Cluster
// API to reach it, so an in-package test would be a cycle.
//
// The provider is multicluster-runtime's namespace provider, which presents each
// namespace as a cluster. That is not kcp, and it does not have to be: what is
// under test is whether a single controller keeps two clusters' work apart, and
// two namespaces are two clusters as far as every layer here is concerned.
package multicluster_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcmulticluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	namespaceprovider "sigs.k8s.io/multicluster-runtime/providers/namespace"

	capicontrollerutil "sigs.k8s.io/cluster-api/util/controller"
	capimulticluster "sigs.k8s.io/cluster-api/util/multicluster"
)

// fanoutLabel marks the ConfigMaps the map function counts. Each cluster gets a
// different number of them, so the name the map function produces says which
// cluster it listed in — see recordingReconciler.
const fanoutLabel = "capi-fleet-test"

var restConfig *rest.Config

func TestMain(m *testing.M) {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start test environment: %v\n", err)
		os.Exit(1)
	}
	restConfig = cfg

	code := m.Run()

	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to stop test environment: %v\n", err)
	}
	os.Exit(code)
}

// recordingReconciler records what it was asked to reconcile, and in which
// cluster.
//
// It reads the cluster from the context rather than from the request, because
// that is what an unmodified Cluster API reconciler can see: the builder hands
// it a plain reconcile.Request and puts the cluster in the context.
type recordingReconciler struct {
	client client.Client

	mu   sync.Mutex
	seen map[string][]types.NamespacedName
}

func (r *recordingReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return reconcile.Result{}, fmt.Errorf("no cluster in the reconcile context for %s", req)
	}

	r.mu.Lock()
	r.seen[string(clusterName)] = append(r.seen[string(clusterName)], req.NamespacedName)
	r.mu.Unlock()

	// Read through the cluster-aware client, so a client that resolved to the
	// wrong cluster would be visible as a wrong value rather than as nothing.
	cm := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, req.NamespacedName, cm); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	return reconcile.Result{}, nil
}

func (r *recordingReconciler) namesIn(clusterName string) []types.NamespacedName {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.NamespacedName, len(r.seen[clusterName]))
	copy(out, r.seen[clusterName])
	return out
}

// TestFleetWideControllerKeepsClustersApart is the whole point of the fleet-wide
// wiring, stated as a test: one controller, one queue, one reconciler, and two
// clusters whose work does not mix.
//
// It asserts three things that fail independently:
//
//  1. The reconciler is told which cluster each request came from, and the
//     cluster-aware client reads from that cluster — checked with two ConfigMaps
//     that share a name and differ in content.
//  2. A map function passed to Watches lists within the cluster the event came
//     from. This is what LiftWithClusterInContext exists for; without it the map
//     function's context carries no cluster and the list is unscoped.
//  3. Neither of the above leaks: nothing from one cluster is enqueued against
//     the other.
func TestFleetWideControllerKeepsClustersApart(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).ToNot(HaveOccurred())

	// alpha holds one labelled ConfigMap and beta holds two, so the count a map
	// function reports identifies the cluster it listed in. An unscoped list
	// would report three, which is neither.
	const alpha, beta = "fleet-alpha", "fleet-beta"
	for _, ns := range []string{alpha, beta} {
		g.Expect(client.IgnoreAlreadyExists(raw.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}))).To(Succeed())
	}
	mustCreateConfigMap(ctx, g, raw, alpha, "shared-name", map[string]string{"owner": alpha}, true)
	mustCreateConfigMap(ctx, g, raw, beta, "shared-name", map[string]string{"owner": beta}, true)
	mustCreateConfigMap(ctx, g, raw, beta, "extra", map[string]string{"owner": beta}, true)

	hostCluster, err := cluster.New(restConfig, func(o *cluster.Options) { o.Scheme = scheme.Scheme })
	g.Expect(err).ToNot(HaveOccurred())
	provider := namespaceprovider.New(hostCluster)

	mgr, err := mcmanager.New(restConfig, provider, manager.Options{
		Scheme:         scheme.Scheme,
		Metrics:        server.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	g.Expect(err).ToNot(HaveOccurred())

	clusterAwareClient := capimulticluster.NewClusterAwareClient(mgr)
	r := &recordingReconciler{client: clusterAwareClient, seen: map[string][]types.NamespacedName{}}

	// secretToFanout is deliberately shaped like Cluster API's own map
	// functions: it closes over a client and lists with the context it is given.
	secretToFanout := func(ctx context.Context, o client.Object) []ctrl.Request {
		if o.GetName() != "fanout-trigger" {
			return nil
		}
		list := &corev1.ConfigMapList{}
		// InNamespace("default") is the namespace provider's rule, not ours: it
		// presents each namespace as a cluster whose only namespace is
		// "default", and rejects a list scoped to anything else.
		if err := clusterAwareClient.List(ctx, list, client.InNamespace(metav1.NamespaceDefault), client.MatchingLabels{fanoutLabel: "true"}); err != nil {
			// Surface the failure as a request that no correct run produces,
			// rather than as silence that looks like a timing problem.
			ctrl.Log.Error(err, "fanout list failed")
			return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: "default", Name: "fanout-error"}}}
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{
			Namespace: "default",
			Name:      fmt.Sprintf("fanout-%d", len(list.Items)),
		}}}
	}

	_, err = capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, ctrl.Log.WithName("fleet-test")).
		For(&corev1.ConfigMap{}).
		Named("fleet-test").
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(secretToFanout)).
		WithOptions(controller.TypedOptions[mcreconcile.Request]{MaxConcurrentReconciles: 2}).
		Build(ctx, r)
	g.Expect(err).ToNot(HaveOccurred())

	go func() { _ = hostCluster.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()

	// 1 and 3: each cluster's ConfigMap is reconciled against its own cluster.
	g.Eventually(func() []types.NamespacedName { return r.namesIn(alpha) }, 30*time.Second, 100*time.Millisecond).
		Should(ContainElement(types.NamespacedName{Namespace: "default", Name: "shared-name"}))
	g.Eventually(func() []types.NamespacedName { return r.namesIn(beta) }, 30*time.Second, 100*time.Millisecond).
		Should(ContainElement(types.NamespacedName{Namespace: "default", Name: "extra"}))

	// 2: the map function lists in the cluster the Secret came from. alpha has
	// one labelled ConfigMap, so a correctly scoped list produces fanout-1;
	// beta's would produce fanout-2 and an unscoped one fanout-3 or more.
	g.Expect(raw.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: alpha, Name: "fanout-trigger"},
	})).To(Succeed())

	g.Eventually(func() []types.NamespacedName { return r.namesIn(alpha) }, 30*time.Second, 100*time.Millisecond).
		Should(ContainElement(types.NamespacedName{Namespace: "default", Name: "fanout-1"}))

	// 3 again, for the watch path: alpha's Secret never enqueues against beta,
	// and never produces a count taken from anywhere but alpha.
	g.Consistently(func() []types.NamespacedName { return r.namesIn(beta) }, 3*time.Second, 200*time.Millisecond).
		ShouldNot(ContainElement(HaveField("Name", HavePrefix("fanout-"))))
	g.Expect(r.namesIn(alpha)).ToNot(ContainElement(HaveField("Name", "fanout-error")))
	g.Expect(r.namesIn(alpha)).ToNot(ContainElement(HaveField("Name", "fanout-3")))
}

func mustCreateConfigMap(ctx context.Context, g *WithT, c client.Client, namespace, name string, data map[string]string, labelled bool) {
	t := g
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       data,
	}
	if labelled {
		cm.Labels = map[string]string{fanoutLabel: "true"}
	}
	t.Expect(client.IgnoreAlreadyExists(c.Create(ctx, cm))).To(Succeed())
}

// countingReconciler records how many times each request was reconciled, and
// reads through the client it is given so that an unresolvable cluster surfaces
// as a reconcile error rather than as silence.
type countingReconciler struct {
	client client.Client

	mu     sync.Mutex
	counts map[string]int
}

func (r *countingReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.mu.Lock()
	r.counts[req.Name]++
	r.mu.Unlock()

	cm := &corev1.ConfigMap{}
	if err := r.client.Get(ctx, req.NamespacedName, cm); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	return reconcile.Result{}, nil
}

func (r *countingReconciler) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[name]
}

// TestWildcardSourceDropsUnresolvableClusters covers the failure mode wildcard
// registration introduces.
//
// A per-cluster source only fires for clusters the provider has engaged, so a
// request naming a cluster the provider does not have could not arise. One
// shared registration sees every object the endpoint serves, so it can — and
// under kcp it routinely will, because a workspace can bind an APIExport before
// the provider engages it.
//
// Unhandled, each such request retries with backoff forever. What stops it is
// mcreconcile.NewClusterNotFoundWrapper, which the multicluster builder applies
// and which the wildcard path has to apply itself; and that only works if
// ErrClusterNotFound survives every wrapping between the client that raises it
// and the wrapper that reads it — the cluster-aware client's, the reconciler
// wrapper's, and the cluster-in-context adapter's.
//
// This asserts the end of that chain rather than any link in it.
func TestWildcardSourceDropsUnresolvableClusters(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).ToNot(HaveOccurred())

	const ns = "wildcard-drop"
	g.Expect(client.IgnoreAlreadyExists(raw.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))).To(Succeed())

	hostCluster, err := cluster.New(restConfig, func(o *cluster.Options) { o.Scheme = scheme.Scheme })
	g.Expect(err).ToNot(HaveOccurred())
	provider := namespaceprovider.New(hostCluster)

	mgr, err := mcmanager.New(restConfig, provider, manager.Options{
		Scheme:         scheme.Scheme,
		Metrics:        server.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	g.Expect(err).ToNot(HaveOccurred())

	r := &countingReconciler{
		client: capimulticluster.NewClusterAwareClient(mgr),
		counts: map[string]int{},
	}

	// The resolver lies for one object, which is the whole point: it is the
	// cheapest faithful stand-in for a workspace the provider has not engaged,
	// and it does not require racing the provider to produce one.
	const unresolvable = "names-a-cluster-that-does-not-exist"
	clusterOf := func(o client.Object) (mcmulticluster.ClusterName, bool) {
		if o.GetName() == unresolvable {
			return "no-such-cluster", true
		}
		return mcmulticluster.ClusterName(o.GetNamespace()), o.GetNamespace() != ""
	}

	registry := &capicontrollerutil.WildcardRegistry{}
	_, err = capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, ctrl.Log.WithName("wildcard-drop")).
		WithWildcardRegistry(registry, clusterOf).
		For(&corev1.ConfigMap{}).
		Named("wildcard-drop").
		WithOptions(controller.TypedOptions[mcreconcile.Request]{MaxConcurrentReconciles: 2}).
		Build(ctx, r)
	g.Expect(err).ToNot(HaveOccurred())

	// The cache arrives after the controller was built, which is the ordering a
	// real provider imposes and the reason the registry exists.
	g.Expect(registry.AddCache("local", mgr.GetLocalManager().GetCache())).To(Succeed())

	go func() { _ = hostCluster.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()

	mustCreateConfigMap(ctx, g, raw, ns, "resolvable", map[string]string{"owner": ns}, false)
	mustCreateConfigMap(ctx, g, raw, ns, unresolvable, map[string]string{"owner": ns}, false)

	// The resolvable one proves the wildcard registration is live, so that the
	// assertion below is about dropping rather than about nothing having
	// happened at all.
	g.Eventually(func() int { return r.count("resolvable") }, 30*time.Second, 100*time.Millisecond).
		Should(BeNumerically(">=", 1))

	// The unresolvable one is reconciled, fails to resolve its cluster, and is
	// not retried. A single-figure count is the assertion: retries with backoff
	// would climb without bound, and the count is deliberately not pinned to
	// exactly one, because the informer may legitimately deliver more than one
	// event for the object.
	g.Eventually(func() int { return r.count(unresolvable) }, 30*time.Second, 100*time.Millisecond).
		Should(BeNumerically(">=", 1))
	settled := r.count(unresolvable)
	g.Consistently(func() int { return r.count(unresolvable) }, 5*time.Second, 250*time.Millisecond).
		Should(BeNumerically("<=", settled), "an unresolvable cluster is being retried rather than dropped")
}

// recordingRawSource is a raw source that says whether it was ever started, and
// enqueues one request when it is.
//
// It stands in for clustercache's Cluster-event source, which is the only raw
// source the fleet-wide wiring uses. What matters about that one is not what it
// carries but that something reads its channel: its sender blocks on a
// consumer that only exists once Start has run.
type recordingRawSource struct {
	request mcreconcile.Request

	mu      sync.Mutex
	started bool
}

func (s *recordingRawSource) Start(_ context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	q.Add(s.request)
	return nil
}

func (s *recordingRawSource) wasStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// TestWildcardControllerStartsRawSources covers a source that is declared and
// then never read.
//
// In wildcard mode the multicluster builder is not the thing that builds the
// controller, so a source handed to it goes nowhere: it is never started, and
// nothing ever reads what it is sent. The symptom is not an error anywhere.
// It is the ClusterCache's sends to that source blocking until they time out,
// and a control plane provider that asked to hear about a failed connection
// probe never hearing about one.
//
// The assertion is the whole chain: the source is started, and what it enqueues
// reaches the reconciler with its cluster intact.
func TestWildcardControllerStartsRawSources(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	raw, err := client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).ToNot(HaveOccurred())

	const ns = "wildcard-raw-source"
	g.Expect(client.IgnoreAlreadyExists(raw.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))).To(Succeed())

	hostCluster, err := cluster.New(restConfig, func(o *cluster.Options) { o.Scheme = scheme.Scheme })
	g.Expect(err).ToNot(HaveOccurred())
	provider := namespaceprovider.New(hostCluster)

	mgr, err := mcmanager.New(restConfig, provider, manager.Options{
		Scheme:         scheme.Scheme,
		Metrics:        server.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	g.Expect(err).ToNot(HaveOccurred())

	r := &countingReconciler{
		client: capimulticluster.NewClusterAwareClient(mgr),
		counts: map[string]int{},
	}

	// The namespace provider presents a namespace as a cluster whose only
	// namespace is "default", so a request it can resolve is one naming the
	// namespace as the cluster.
	src := &recordingRawSource{request: mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "from-the-raw-source"}},
		ClusterName: ns,
	}}

	clusterOf := func(o client.Object) (mcmulticluster.ClusterName, bool) {
		return mcmulticluster.ClusterName(o.GetNamespace()), o.GetNamespace() != ""
	}

	registry := &capicontrollerutil.WildcardRegistry{}
	_, err = capicontrollerutil.NewMulticlusterControllerManagedBy(mgr, ctrl.Log.WithName("wildcard-raw-source")).
		WithWildcardRegistry(registry, clusterOf).
		For(&corev1.ConfigMap{}).
		Named("wildcard-raw-source").
		WatchesRawSource(src).
		WithOptions(controller.TypedOptions[mcreconcile.Request]{MaxConcurrentReconciles: 2}).
		Build(ctx, r)
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(registry.AddCache("local", mgr.GetLocalManager().GetCache())).To(Succeed())

	go func() { _ = hostCluster.Start(ctx) }()
	go func() { _ = mgr.Start(ctx) }()

	g.Eventually(src.wasStarted, 30*time.Second, 100*time.Millisecond).
		Should(BeTrue(), "the raw source was declared but never started, so nothing reads what is sent to it")
	g.Eventually(func() int { return r.count("from-the-raw-source") }, 30*time.Second, 100*time.Millisecond).
		Should(BeNumerically(">=", 1), "the raw source started but what it enqueued never reached the reconciler")
}
