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

package upstreamscale

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestTheStoreCanBeSomewhereOtherThanKubeSystem.
//
// The defragmenter and the etcd sampler both looked for kubeadm's static pods:
// namespace kube-system, label component=etcd. That is the stock side's store
// and only the stock side's. kcp's is a StatefulSet in the run's own namespace,
// so against kcp both would have found nothing — the run would have logged
// "could not defragment" and carried on, and its etcd column would have been
// the thing defragmenting before the baseline was added to fix: a store
// carrying the previous run's free pages, incomparable with the rungs after it.
//
// Two stores, one lookup, told where to look.
func TestTheStoreCanBeSomewhereOtherThanKubeSystem(t *testing.T) {
	kubeadm := KubeadmStore()
	if kubeadm.Namespace != "kube-system" || kubeadm.Labels["component"] != "etcd" {
		t.Errorf("the stock store moved: %+v", kubeadm)
	}
	deployed := DeployedStore("kcp-scale")
	if deployed.Namespace != "kcp-scale" {
		t.Errorf("the kcp store is looked for in %q", deployed.Namespace)
	}
	if len(deployed.Labels) == 0 {
		t.Error("the kcp store has no selector, so the lookup would take every pod in the namespace")
	}
	// Both are read the same way, or the two etcd columns are not comparable.
	if kubeadm.MetricsPort != deployed.MetricsPort {
		t.Errorf("the two stores are scraped on different ports: %d and %d",
			kubeadm.MetricsPort, deployed.MetricsPort)
	}
}

// TestTheStoreLookupSelectsOnlyWhatItWasPointedAt, so a run against one side
// never reads or defragments the other's store — which on a cluster hosting
// both would be a stop-the-world rewrite in the middle of somebody else's
// measurement.
func TestTheStoreLookupSelectsOnlyWhatItWasPointedAt(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(
		etcdPod("kube-system", "etcd-cp-1", map[string]string{"component": "etcd"}),
		etcdPod("kube-system", "etcd-cp-0", map[string]string{"component": "etcd"}),
		etcdPod("kcp-scale", "etcd-1", DeployedStore("kcp-scale").Labels),
		etcdPod("kcp-scale", "etcd-0", DeployedStore("kcp-scale").Labels),
		etcdPod("kcp-scale", "kcp-abc", map[string]string{"other": "thing"}),
	).Build()

	stock, err := StorePods(context.Background(), cl, KubeadmStore())
	if err != nil {
		t.Fatalf("the stock store: %v", err)
	}
	// In name order, because a defragmentation walks them one at a time and
	// two runs that walked them differently cannot be lined up.
	if got := names(stock); got != "etcd-cp-0,etcd-cp-1" {
		t.Errorf("the stock store's members = %q", got)
	}

	kcp, err := StorePods(context.Background(), cl, DeployedStore("kcp-scale"))
	if err != nil {
		t.Fatalf("the kcp store: %v", err)
	}
	if got := names(kcp); got != "etcd-0,etcd-1" {
		t.Errorf("the kcp store's members = %q — the shard's own pod must not be in this", got)
	}
}

// TestAStoreThatIsNotThereIsAnError rather than an empty defragmentation that
// reports success, which is what "no members" would otherwise look like.
func TestAStoreThatIsNotThereIsAnError(t *testing.T) {
	s, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).Build()
	if _, err := StorePods(context.Background(), cl, DeployedStore("kcp-scale")); err == nil {
		t.Error("a store with no members read as a store with nothing to do")
	}
}

func etcdPod(namespace, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func names(pods []corev1.Pod) string {
	var out []string
	for _, p := range pods {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}
