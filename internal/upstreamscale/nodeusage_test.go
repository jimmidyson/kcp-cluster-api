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

// cadvisorNode is one control plane node's exposition, cut down to the series
// this reads: an API server, an etcd member, the controller manager, the
// scheduler, a CNI pod, and the POD sandbox containers the kubelet also
// reports.
const cadvisorNode = `
container_memory_working_set_bytes{container="kube-apiserver",namespace="kube-system",pod="kube-apiserver-cp-0"} 2.1288392704e+10
container_cpu_usage_seconds_total{container="kube-apiserver",namespace="kube-system",pod="kube-apiserver-cp-0"} 14204.5
container_memory_working_set_bytes{container="etcd",namespace="kube-system",pod="etcd-cp-0"} 2.684354560e+09
container_cpu_usage_seconds_total{container="etcd",namespace="kube-system",pod="etcd-cp-0"} 3810.25
container_memory_working_set_bytes{container="kube-controller-manager",namespace="kube-system",pod="kube-controller-manager-cp-0"} 5.36870912e+09
container_cpu_usage_seconds_total{container="kube-controller-manager",namespace="kube-system",pod="kube-controller-manager-cp-0"} 6120.75
container_memory_working_set_bytes{container="kube-scheduler",namespace="kube-system",pod="kube-scheduler-cp-0"} 1.34217728e+08
container_cpu_usage_seconds_total{container="kube-scheduler",namespace="kube-system",pod="kube-scheduler-cp-0"} 210.5
container_memory_working_set_bytes{container="cilium-agent",namespace="kube-system",pod="cilium-9xk2v"} 4.02653184e+08
container_cpu_usage_seconds_total{container="cilium-agent",namespace="kube-system",pod="cilium-9xk2v"} 88.25
container_memory_working_set_bytes{container="POD",namespace="kube-system",pod="kube-apiserver-cp-0"} 4.194304e+06
container_memory_working_set_bytes{id="/system.slice/kubelet.service"} 3.28e+08
`

// TestEveryPodOnTheNodeIsMeasured, not only the ones somebody thought to name.
//
// The question this answers is "what does the control plane cost", and the
// answer has to include the components nobody listed: kube-controller-manager
// holds an informer per resource through its garbage collector, so every
// Cluster API CRD is cached there too, and until now nothing had ever looked at
// it. A scrape that reports only the pods a list names cannot find that.
func TestEveryPodOnTheNodeIsMeasured(t *testing.T) {
	usage, err := ParseNodeUsage(strings.NewReader(cadvisorNode))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	for _, want := range []string{
		"kube-system/kube-apiserver-cp-0",
		"kube-system/etcd-cp-0",
		"kube-system/kube-controller-manager-cp-0",
		"kube-system/kube-scheduler-cp-0",
		"kube-system/cilium-9xk2v",
	} {
		if _, ok := usage[want]; !ok {
			t.Errorf("%s was not measured; the node holds it and the run would not know", want)
		}
	}
	if len(usage) != 5 {
		t.Errorf("measured %d pods, want the five on the node: %v", len(usage), keysOf(usage))
	}
}

// TestTheSandboxIsNotAWorkload.
//
// The kubelet reports a "POD" container per pod — the pause sandbox — and a
// machine-level series with no pod at all. Counting the sandbox would add its
// few MiB to every pod's figure; counting the machine series would attribute
// the whole node to a pod named "".
func TestTheSandboxIsNotAWorkload(t *testing.T) {
	usage, err := ParseNodeUsage(strings.NewReader(cadvisorNode))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	api := usage["kube-system/kube-apiserver-cp-0"]
	if api.WorkingSetBytes != 21288392704 {
		t.Errorf("the API server measured %d bytes, want its container's alone (the sandbox's 4 MiB "+
			"must not be added)", api.WorkingSetBytes)
	}
	if _, ok := usage["kube-system/"]; ok {
		t.Error("a machine-level series was recorded as a pod")
	}
}

// TestTheFiguresAreTheOnesALimitIsSetAgainst: working set and cumulative CPU,
// the same two quantities the managers are already reported with, so the
// control plane sits in the same table rather than beside it.
func TestTheFiguresAreTheOnesALimitIsSetAgainst(t *testing.T) {
	usage, err := ParseNodeUsage(strings.NewReader(cadvisorNode))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	kcm := usage["kube-system/kube-controller-manager-cp-0"]
	if kcm.WorkingSetBytes != 5368709120 {
		t.Errorf("working set = %d", kcm.WorkingSetBytes)
	}
	if kcm.CPUSeconds != 6120.75 {
		t.Errorf("cpu seconds = %v", kcm.CPUSeconds)
	}
}

// TestAnEmptyScrapeIsAnError, not a control plane that costs nothing. A node
// that answered with no series at all is a scrape that failed, and reporting it
// as zero is wrong in the direction nobody checks.
func TestAnEmptyScrapeIsAnError(t *testing.T) {
	if _, err := ParseNodeUsage(strings.NewReader("# nothing here\n")); err == nil {
		t.Error("an empty exposition was accepted as a node with no pods on it")
	}
}

func keysOf(m map[string]ContainerUsage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
