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

package deployedscale

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func etcdOptions() Options {
	o := testOptions()
	o.Etcd = EtcdOptions{
		Members:      3,
		StorageClass: "nutanix-volume",
		StorageSize:  "100Gi",
		QuotaBytes:   8589934592,
	}
	return o
}

// TestTheStoreMatchesTheStockOneOrTheComparisonIsNotOne.
//
// The stock side is measured against kubeadm's etcd: three members, one per
// control plane node, on node-local disk, with an 8 GiB backend quota and
// metrics open on :2381 so the harness can see how close the store is to it.
// kcp's default is a single embedded member inside the shard's own pod,
// sharing the shard's memory limit, with no quota and nothing to scrape.
//
// Those are not the same store, and no per-cluster figure measured against one
// can be subtracted from a figure measured against the other. So the kcp side
// is given the stock side's store.
func TestTheStoreMatchesTheStockOneOrTheComparisonIsNotOne(t *testing.T) {
	o := etcdOptions()
	set := o.EtcdStatefulSet()

	if got := *set.Spec.Replicas; got != 3 {
		t.Errorf("members = %d, want the stock store's 3", got)
	}
	args := strings.Join(set.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--quota-backend-bytes=8589934592") {
		t.Errorf("no 8 GiB quota, so this store has a different cliff from the stock one: %q", args)
	}
	// Bound on all interfaces, not 127.0.0.1: the whole point is that
	// something outside the pod can read how close the store is to its quota.
	if !strings.Contains(args, fmt.Sprintf("--listen-metrics-urls=http://0.0.0.0:%d", EtcdMetricsPort)) {
		t.Errorf("metrics are not reachable from outside the pod: %q", args)
	}

	// One member per node, or three members share a failure domain and the
	// store is three copies of one disk's latency.
	affinity := set.Spec.Template.Spec.Affinity
	if affinity == nil || affinity.PodAntiAffinity == nil ||
		len(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) == 0 {
		t.Error("no required anti-affinity: nothing stops all three members landing on one node")
	}

	// Persistent, because a member that loses its data directory cannot
	// rejoin, and an emptyDir loses it on every restart.
	if len(set.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("%d volume claim templates, want 1", len(set.Spec.VolumeClaimTemplates))
	}
	claim := set.Spec.VolumeClaimTemplates[0]
	if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "nutanix-volume" {
		t.Errorf("storage class = %v, want the one the cluster provides", claim.Spec.StorageClassName)
	}
}

// TestEveryMemberKnowsEveryOther. A StatefulSet gives each pod a stable name,
// and etcd needs every one of them in --initial-cluster before any of them
// starts: a member that is not listed is a member the quorum will not accept.
func TestEveryMemberKnowsEveryOther(t *testing.T) {
	o := etcdOptions()
	args := strings.Join(o.EtcdStatefulSet().Spec.Template.Spec.Containers[0].Args, " ")

	for i := range 3 {
		peer := fmt.Sprintf("%s-%d=http://%s-%d.%s.%s.svc:%d",
			EtcdName, i, EtcdName, i, EtcdName, o.Namespace, EtcdPeerPort)
		if !strings.Contains(args, peer) {
			t.Errorf("member %d is not in the initial cluster: %q", i, args)
		}
	}
	// Each pod has to know which of those it is, and the only thing that
	// differs between pods of a StatefulSet is the name Kubernetes gives them.
	if !strings.Contains(args, "--name=$(POD_NAME)") {
		t.Errorf("members are not named from their pod: %q", args)
	}
	container := o.EtcdStatefulSet().Spec.Template.Spec.Containers[0]
	var found bool
	for _, e := range container.Env {
		if e.Name == "POD_NAME" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil &&
			e.ValueFrom.FieldRef.FieldPath == "metadata.name" {
			found = true
		}
	}
	if !found {
		t.Error("POD_NAME is not taken from the pod, so $(POD_NAME) expands to nothing")
	}
}

// TestTheShardUsesTheExternalStoreWhenThereIsOne, and its own embedded one when
// there is not — because the in-process runs and the local demo have no
// StatefulSet to talk to and should keep working.
func TestTheShardUsesTheExternalStoreWhenThereIsOne(t *testing.T) {
	o := etcdOptions()
	args := strings.Join(KcpArgs(o.KcpBaseURL(), "/data", CredentialsMountPath, KcpPort, o.EtcdEndpoints()), " ")
	for i := range 3 {
		want := fmt.Sprintf("%s-%d.%s.%s.svc:%d", EtcdName, i, EtcdName, o.Namespace, EtcdClientPort)
		if !strings.Contains(args, want) {
			t.Errorf("the shard does not address member %d: %q", i, args)
		}
	}

	// No external store: no flag, and kcp starts the embedded one it defaults
	// to. A flag with an empty value would be worse than no flag.
	embedded := strings.Join(KcpArgs(o.KcpBaseURL(), "/data", CredentialsMountPath, KcpPort, nil), " ")
	if strings.Contains(embedded, "--etcd-servers") {
		t.Errorf("an --etcd-servers flag with nothing behind it: %q", embedded)
	}
}

// TestTheStoreIsInTheObjectsAndTheShardWaitsForIt. Ordering is not cosmetic:
// kcp exits if it cannot reach its store at startup, and a CrashLoopBackOff
// that resolves itself reads in the report as a shard that restarted, which
// disqualifies its samples.
func TestTheStoreIsInTheObjectsAndTheShardWaitsForIt(t *testing.T) {
	o := etcdOptions()
	objects := testObjects(t, o)

	var sawStore, sawShard bool
	for _, obj := range objects {
		switch obj.GetName() {
		case EtcdName:
			sawStore = true
		case KcpName:
			if sawShard {
				continue
			}
			sawShard = true
			if !sawStore {
				t.Error("the shard is applied before its store, so it starts with nothing to talk to")
			}
		}
	}
	if !sawStore {
		t.Error("no etcd in the objects a run applies")
	}
}

// TestNoStoreAskedForMeansNoStoreDeployed, so the existing kind-based runs and
// anything else that wants the embedded store are unchanged by this.
func TestNoStoreAskedForMeansNoStoreDeployed(t *testing.T) {
	o := testOptions()
	if o.Etcd.Members != 0 {
		t.Fatal("the default options grew an etcd")
	}
	for _, obj := range testObjects(t, o) {
		if obj.GetName() == EtcdName {
			if _, isService := obj.(*corev1.Service); !isService {
				t.Errorf("an etcd was deployed without being asked for: %T", obj)
			}
		}
	}
}
