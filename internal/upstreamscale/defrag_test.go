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

// TestEachStoreCarriesHowToTalkToItself.
//
// A defragmentation is an exec inside the member, and the two stores are
// reached differently: kubeadm's serves the client API over TLS with the
// cluster's own certificates at kubeadm's paths, and the store this run deploys
// serves it in the clear inside the pod network. The command was kubeadm's,
// hardcoded — so against the deployed store every member would have failed with
// a certificate error, the run would have gone on, and the kcp side's store
// would have been the only one never defragmented. That is a difference between
// the two sides' figures that nothing in them would explain.
func TestEachStoreCarriesHowToTalkToItself(t *testing.T) {
	kubeadm := strings.Join(KubeadmStore().DefragCommand(), " ")
	if !strings.Contains(kubeadm, "/etc/kubernetes/pki/etcd/ca.crt") {
		t.Errorf("kubeadm's store lost its certificates: %q", kubeadm)
	}
	if !strings.Contains(kubeadm, "https://127.0.0.1:2379") {
		t.Errorf("kubeadm's store is not addressed over TLS: %q", kubeadm)
	}

	deployed := strings.Join(DeployedStore("kcp-scale").DefragCommand(), " ")
	if strings.Contains(deployed, "cacert") || strings.Contains(deployed, "https") {
		t.Errorf("the deployed store is addressed with certificates it does not have: %q", deployed)
	}
	if !strings.Contains(deployed, "http://127.0.0.1:2379") {
		t.Errorf("the deployed store is not addressed at all: %q", deployed)
	}
	for _, cmd := range []string{kubeadm, deployed} {
		if !strings.HasPrefix(cmd, "etcdctl ") || !strings.HasSuffix(cmd, " defrag") {
			t.Errorf("this does not defragment anything: %q", cmd)
		}
	}
}

// TestBothStoresNameTheContainerToExecIn, because an exec with no container
// runs in whichever one the pod lists first.
func TestBothStoresNameTheContainerToExecIn(t *testing.T) {
	for _, store := range []StoreLocation{KubeadmStore(), DeployedStore("kcp-scale")} {
		if store.Container == "" {
			t.Errorf("%v names no container", store.Labels)
		}
	}
}

// TestADefragIsGivenLongEnoughToFinish.
//
// etcdctl's own command timeout is five seconds, and a defragmentation of a
// backend with a fleet in it takes longer than that: at 500 clusters a member
// timed out with "context deadline exceeded" after 5.004s and reclaimed
// nothing, while its two peers finished. A run that cannot defragment is a run
// whose next rung starts from a file full of free pages, and whose ceiling is
// then about accumulated fragmentation rather than about how much state the
// store can hold — the exact confusion the defragmentation exists to remove.
func TestADefragIsGivenLongEnoughToFinish(t *testing.T) {
	for _, store := range []StoreLocation{KubeadmStore(), DeployedStore("kcp-scale")} {
		cmd := strings.Join(store.DefragCommand(), " ")
		if !strings.Contains(cmd, "--command-timeout") {
			t.Errorf("no command timeout, so etcdctl's own 5s applies: %q", cmd)
		}
	}
}

// TestAMemberThatCouldNotBeMeasuredDoesNotReportAShrink.
//
// A defragmentation whose before-reading failed printed "reclaimed 0 B (0 B to
// 1.3 GiB)" — which reads as a member that grew, from a run that had simply
// failed to measure it. Either both readings are there or the result says the
// figures are missing.
func TestAMemberThatCouldNotBeMeasuredDoesNotReportAShrink(t *testing.T) {
	got := DescribeDefrag([]DefragResult{
		{Pod: "etcd-0", BeforeBytes: 1_932_735_283, AfterBytes: 1_395_864_371},
		{Pod: "etcd-1", BeforeBytes: 0, AfterBytes: 1_395_864_371},
	})
	if !strings.Contains(got, "etcd-0 reclaimed") {
		t.Errorf("the member that was measured lost its figure: %q", got)
	}
	if strings.Contains(got, "0 B to") {
		t.Errorf("a member with no before-reading was reported as having grown from nothing: %q", got)
	}
	if !strings.Contains(got, "etcd-1") || !strings.Contains(got, "not measured") {
		t.Errorf("the member that could not be measured is not named as such: %q", got)
	}
}
