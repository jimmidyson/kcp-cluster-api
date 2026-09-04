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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// generated is the shape clusterctl produces from CAREN's own quick-start
// example once its variables are substituted: a credentials Secret it does not
// touch, and a Cluster whose topology carries the addons CAREN turns on by
// default and a worker pool sized by autoscaler annotations rather than by a
// replica count.
const generated = `apiVersion: v1
kind: Secret
metadata:
  name: capi-scale-pc-creds
stringData:
  credentials: "[]"
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: capi-scale
spec:
  topology:
    classRef:
      name: nutanix-quick-start
    controlPlane:
      replicas: 3
    variables:
    - name: clusterConfig
      value:
        addons:
          ccm:
            strategy: HelmAddon
          clusterAutoscaler: {}
          cni:
            provider: Cilium
          cosi: {}
          csi:
            defaultStorage:
              provider: nutanix
            snapshotController:
              strategy: HelmAddon
          nfd: {}
          serviceLoadBalancer:
            provider: MetalLB
        controlPlane:
          nutanix:
            machineDetails:
              memorySize: 4Gi
    - name: workerConfig
      value:
        nutanix:
          machineDetails:
            bootType: uefi
            memorySize: 4Gi
            systemDiskSize: 40Gi
            vcpuSockets: 2
            vcpusPerSocket: 1
    version: v1.32.0
    workers:
      machineDeployments:
      - class: default-worker
        metadata:
          annotations:
            cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "4"
            cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "4"
        name: md-0
`

// TestTheWorkerPoolGetsAReplicaCount is the finding that would have produced a
// one-node worker pool on a cluster asked for four.
//
// CAREN's example does not set replicas on its MachineDeployment at all: the
// size is expressed as cluster-autoscaler min and max annotations, so the pool
// is whatever the autoscaler decides. A scale test cannot have its own
// management cluster resizing underneath it — and with the autoscaler addon
// removed, as it is here, nothing would size the pool at all.
//
// So the annotations go and an explicit replica count takes their place. Both
// halves, because Cluster API refuses a topology that sets replicas while the
// autoscaler annotations are still on it.
func TestTheWorkerPoolGetsAReplicaCount(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if strings.Contains(out, "autoscaler-node-group") {
		t.Error("the autoscaler annotations survived, so Cluster API will refuse the replica count beside them")
	}
	if !strings.Contains(out, "replicas: 4") {
		t.Errorf("no worker replica count in:\n%s", out)
	}
	// The control plane's own count is untouched: clusterctl already set it.
	if !strings.Contains(out, "replicas: 3") {
		t.Error("the control plane replica count was lost")
	}
}

// TestTheAddonsThisTestDoesNotNeedAreRemoved. Nothing here asks for a
// PersistentVolume, a load balancer or node feature discovery, and every addon
// left on is another controller on the API server whose cost is the subject of
// the measurement.
func TestTheAddonsThisTestDoesNotNeedAreRemoved(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	for _, gone := range []string{"csi", "cosi", "clusterAutoscaler", "serviceLoadBalancer", "nfd"} {
		if strings.Contains(out, gone+":") {
			t.Errorf("addon %q survived", gone)
		}
	}

	// What must stay. The CNI is how pods talk at all, and the cloud provider
	// is what clears the uninitialized taint from a new node — removing it
	// leaves a cluster whose nodes never become schedulable, which presents as
	// a scale test that cannot place its own controllers.
	for _, kept := range []string{"cni", "ccm"} {
		if !strings.Contains(out, kept+":") {
			t.Errorf("addon %q was removed and is needed", kept)
		}
	}
}

// TestEveryOtherDocumentIsPassedThrough. The generated manifest carries the
// credentials Secrets the cluster needs, and a trimmer that dropped them would
// produce a cluster that cannot talk to Prism.
func TestEveryOtherDocumentIsPassedThrough(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if !strings.Contains(out, "capi-scale-pc-creds") {
		t.Error("the credentials Secret was dropped")
	}
	if !strings.Contains(out, "kind: Cluster") {
		t.Error("the Cluster was dropped")
	}
}

func TestTrimmingSomethingWithNoClusterIsAnError(t *testing.T) {
	if _, err := TrimForScale("apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n", Sizing{Workers: 4}); err == nil {
		t.Error("a manifest with no Cluster in it was trimmed without complaint")
	}
}

// TestTheNodesAreSizedForTheTest is the difference between a management cluster
// and a management cluster that can hold this fleet.
//
// CAREN's example builds every node at 2 vCPU and 4 GiB, which is a sensible
// quick start and a sixth of the memory the sizing document asks the control
// plane for. Nothing in the run would report it as wrong: the cluster comes up,
// the controllers schedule, and the ceiling the ladder finds is the box rather
// than Cluster API.
func TestTheNodesAreSizedForTheTest(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{
		Workers:            4,
		ControlPlaneVCPUs:  16,
		ControlPlaneMemory: "32Gi",
		ControlPlaneDisk:   "200Gi",
		WorkerVCPUs:        16,
		WorkerMemory:       "32Gi",
		WorkerDisk:         "100Gi",
	})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if strings.Contains(out, "memorySize: 4Gi") {
		t.Errorf("a node is still at the example's 4Gi:\n%s", out)
	}
	if n := strings.Count(out, "memorySize: 32Gi"); n != 2 {
		t.Errorf("memorySize: 32Gi appears %d times, want 2 (control plane and workers)", n)
	}
	if n := strings.Count(out, "vcpuSockets: 16"); n != 2 {
		t.Errorf("vcpuSockets: 16 appears %d times, want 2", n)
	}
	if strings.Contains(out, "vcpusPerSocket: 2") {
		t.Error("vcpusPerSocket was changed; vCPUs are set through the socket count alone")
	}
	if !strings.Contains(out, "systemDiskSize: 200Gi") || !strings.Contains(out, "systemDiskSize: 100Gi") {
		t.Errorf("disks were not sized separately:\n%s", out)
	}

	// Everything else in machineDetails is the operator's environment — the
	// image, the subnet, the Prism Element cluster — and must survive.
	if !strings.Contains(out, "bootType: uefi") {
		t.Error("the rest of machineDetails was replaced rather than adjusted")
	}
}

// TestSizingIsOptional. A zero value means "leave it as the template had it",
// so the trimmer stays usable for its other job — fixing the worker count and
// dropping addons — on a template whose sizes are already right.
func TestSizingIsOptional(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if !strings.Contains(out, "memorySize: 4Gi") {
		t.Error("an unset size changed the template's own value")
	}
}

// TestTheClusterIsPointedAtThePatchedClass is the bug this test exists because
// of.
//
// The provisioning script copies CAREN's ClusterClass and adds patches to the
// copy — the etcd backend quota, and the metrics port without which the store
// cannot be measured. Nothing pointed the generated Cluster at that copy:
// CAREN's template names nutanix-quick-start, so the copy sat unused and every
// cluster came up with the stock 2 GiB quota and etcd's metrics bound to
// 127.0.0.1.
//
// Silent, entirely: the cluster builds, the run starts, and the two things the
// copy existed to change are both invisible until something needs them.
func TestTheClusterIsPointedAtThePatchedClass(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4, ClusterClass: "capi-scale-scale"})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if !strings.Contains(out, "name: capi-scale-scale") {
		t.Errorf("the Cluster does not name the patched class:\n%s", out)
	}
	if strings.Contains(out, "name: nutanix-quick-start") {
		t.Error("the Cluster still names CAREN's own class, so its patches are the stock ones")
	}

	// Unset leaves the template's own class alone, so the trimmer stays usable
	// against a class nobody copied.
	same, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if !strings.Contains(same, "name: nutanix-quick-start") {
		t.Error("an unset class name changed the template's own")
	}
}

// TestCsiStaysWhenTheStoreNeedsIt.
//
// The trimmer removes CSI on the reasoning that nothing in the stock run asks
// for a PersistentVolume, which was true of the stock run. The kcp side of the
// comparison asks for three: its etcd members each take a volume, because a
// member that loses its data directory cannot rejoin the quorum. The two sides
// share one cluster, so the cluster needs CSI, so the trimmer needs to be able
// to keep it.
//
// A knob rather than a change of default: the stock figures already recorded
// were taken on a cluster without CSI, and flipping the default would silently
// make the next stock run incomparable with them.
func TestCsiStaysWhenTheStoreNeedsIt(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4, KeepCSI: true})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if !strings.Contains(out, "csi") {
		t.Error("CSI was removed from a cluster whose store needs volumes")
	}
	// Everything else this run does not use still goes.
	for _, addon := range []string{"cosi", "clusterAutoscaler", "serviceLoadBalancer", "nfd"} {
		if strings.Contains(out, addon) {
			t.Errorf("%s survived: keeping CSI is not keeping everything", addon)
		}
	}

	// And the default is unchanged, so the stock run measures what it measured.
	without, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	if strings.Contains(without, "csi") {
		t.Error("CSI is on by default now, which changes the stock run under it")
	}
}

// TestTheNodePoolLabelIsOneClusterApiActuallyPropagates.
//
// This is the whole mechanism in one assertion. Cluster API does not copy
// arbitrary Machine labels to a Node — the kubelet cannot self-assign labels
// outside the NodeRestriction domains, so the Machine controller syncs only
// what util/labels.GetManagedLabels admits: node-role.kubernetes.io,
// node-restriction.kubernetes.io, and node.cluster.x-k8s.io, each with their
// subdomains.
//
// A pool labelled `scale-role=control-plane` would therefore reach the
// MachineDeployment, the MachineSet and the Machine, and stop there — and the
// failure is silent: the nodes come up unlabelled, the shard's pods are
// unschedulable, and nothing says why. Tying this constant to upstream's own
// means a rename upstream fails here rather than in a run.
func TestTheNodePoolLabelIsOneClusterApiActuallyPropagates(t *testing.T) {
	domain, _, ok := strings.Cut(NodePoolLabel, "/")
	if !ok {
		t.Fatalf("%q has no domain, so nothing propagates it", NodePoolLabel)
	}
	if domain != clusterv1.ManagedNodeLabelDomain &&
		!strings.HasSuffix(domain, "."+clusterv1.ManagedNodeLabelDomain) {
		t.Errorf("the pool label's domain is %q, which Cluster API does not sync to Nodes; "+
			"it syncs %q and its subdomains", domain, clusterv1.ManagedNodeLabelDomain)
	}
}

// TestTheWorkerPoolSplitsIntoTwo, so that the control plane under test can be
// given its own nodes and the managers kept off them (R5). Carved out of the
// worker count rather than added to it: WORKER_COUNT is how many workers the
// cluster has, and a flag that quietly bought three more machines would be a
// bill nobody asked for.
func TestTheWorkerPoolSplitsIntoTwo(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4, ControlPlanePoolWorkers: 3})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}

	pools := workerPools(t, out)
	if len(pools) != 2 {
		t.Fatalf("%d pools, want the control plane's and the managers'", len(pools))
	}

	byLabel := map[string]map[string]any{}
	for _, pool := range pools {
		metadata, _ := pool["metadata"].(map[string]any)
		labels, _ := metadata["labels"].(map[string]any)
		role, _ := labels[NodePoolLabel].(string)
		if role == "" {
			t.Fatalf("a pool carries no %s label, so its nodes are indistinguishable: %v", NodePoolLabel, pool)
		}
		byLabel[role] = pool
	}

	cp, ok := byLabel[NodePoolControlPlane]
	if !ok {
		t.Fatalf("no %s pool: %v", NodePoolControlPlane, byLabel)
	}
	if got := replicasOf(t, cp); got != 3 {
		t.Errorf("the control plane pool has %d nodes, want 3", got)
	}
	managers, ok := byLabel[NodePoolManagers]
	if !ok {
		t.Fatalf("no %s pool: %v", NodePoolManagers, byLabel)
	}
	if got := replicasOf(t, managers); got != 1 {
		t.Errorf("the managers' pool has %d nodes, want the remaining 1", got)
	}

	// Same class, different names: two pools of one class is ordinary, and two
	// entries sharing a name is a topology Cluster API refuses.
	if cp["class"] != managers["class"] {
		t.Errorf("the two pools are of different classes: %v and %v", cp["class"], managers["class"])
	}
	if cp["name"] == managers["name"] {
		t.Errorf("both pools are called %v", cp["name"])
	}
}

// TestOnePoolStaysOnePool, so the cluster the recorded stock runs were taken on
// is still what this produces when nothing asks for a split.
func TestOnePoolStaysOnePool(t *testing.T) {
	out, err := TrimForScale(generated, Sizing{Workers: 4})
	if err != nil {
		t.Fatalf("trimming: %v", err)
	}
	pools := workerPools(t, out)
	if len(pools) != 1 {
		t.Fatalf("%d pools, want the one the template has", len(pools))
	}
	if metadata, ok := pools[0]["metadata"].(map[string]any); ok {
		if _, labelled := metadata["labels"]; labelled {
			t.Errorf("an unsplit pool was labelled: %v", metadata)
		}
	}
	if got := replicasOf(t, pools[0]); got != 4 {
		t.Errorf("replicas = %d, want 4", got)
	}
}

// TestAPoolSplitThatLeavesNoRoomIsRefused. Three of three workers given to the
// control plane leaves the managers nowhere to run, and the failure would be
// four pods Pending on a cluster that looks correctly sized.
func TestAPoolSplitThatLeavesNoRoomIsRefused(t *testing.T) {
	for _, sizing := range []Sizing{
		{Workers: 3, ControlPlanePoolWorkers: 3},
		{Workers: 3, ControlPlanePoolWorkers: 4},
	} {
		if _, err := TrimForScale(generated, sizing); err == nil {
			t.Errorf("a split of %d from %d workers was accepted",
				sizing.ControlPlanePoolWorkers, sizing.Workers)
		}
	}
}

// replicasOf reads a pool's count whatever numeric type the YAML round trip
// left it as.
func replicasOf(t *testing.T, pool map[string]any) int {
	t.Helper()
	switch n := pool["replicas"].(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("replicas is %T (%v)", pool["replicas"], pool["replicas"])
		return 0
	}
}

func workerPools(t *testing.T, manifest string) []map[string]any {
	t.Helper()
	var cluster map[string]any
	for _, doc := range strings.Split(manifest, "\n---\n") {
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			continue
		}
		if kind, _ := obj["kind"].(string); kind == "Cluster" {
			cluster = obj
		}
	}
	if cluster == nil {
		t.Fatal("no Cluster in the manifest")
	}
	raw, found, err := unstructured.NestedSlice(cluster, "spec", "topology", "workers", "machineDeployments")
	if err != nil || !found {
		t.Fatalf("reading the pools: %v (found %v)", err, found)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		pool, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("a pool is %T", p)
		}
		out = append(out, pool)
	}
	return out
}
