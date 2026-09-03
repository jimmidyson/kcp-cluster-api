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
	out, err := TrimForScale(generated, 4)
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
	out, err := TrimForScale(generated, 4)
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
	out, err := TrimForScale(generated, 4)
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
	if _, err := TrimForScale("apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n", 4); err == nil {
		t.Error("a manifest with no Cluster in it was trimmed without complaint")
	}
}
