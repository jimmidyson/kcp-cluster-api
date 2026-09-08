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

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// TestKubeVIPIsASidecarAndTheAPIServerIsNot: both are static pods in the same
// manifests directory, and only one of them dying is the control plane dying.
func TestKubeVIPIsASidecarAndTheAPIServerIsNot(t *testing.T) {
	for name, core := range map[string]bool{
		"kube-apiserver-capi-scale-vtgjn-szrhq":             true,
		"etcd-capi-scale-vtgjn-szrhq":                       true,
		"kube-controller-manager-capi-scale-vtgjn-mxt5v":    true,
		"kube-scheduler-capi-scale-vtgjn-mxt5v":             true,
		"kube-vip-capi-scale-vtgjn-szrhq":                   false,
		"nutanix-cloud-controller-manager-76bdd45c76-68n8d": false,
		"cilium-4bs75":       false,
		"etcdbackup-agent-x": false,
	} {
		if got := IsCoreControlPlane(name); got != core {
			t.Errorf("IsCoreControlPlane(%q) = %v, want %v", name, got, core)
		}
	}

	dead := func(name string) deployedscale.ComponentSample {
		return deployedscale.ComponentSample{Component: name, Pod: deployedscale.PodFacts{RestartCount: 1}}
	}
	core, sidecars := SplitSidecars([]deployedscale.ComponentSample{
		dead("kube-vip-a"), dead("etcd-a"), dead("kube-apiserver-a"),
	})
	if len(core) != 2 || len(sidecars) != 1 || sidecars[0].Component != "kube-vip-a" {
		t.Errorf("SplitSidecars() = core %v, sidecars %v", core, sidecars)
	}
}

// TestAnIncidentSaysWhatTheVIPMovingCost, rather than that the process's
// samples are no longer comparable, which is a manager's sentence.
func TestAnIncidentSaysWhatTheVIPMovingCost(t *testing.T) {
	vip := DescribeIncident(deployedscale.ComponentSample{
		Component: "kube-vip-a",
		Pod:       deployedscale.PodFacts{RestartCount: 1, Ready: true, LastExitCode: 0, LastReason: "Completed"},
	})
	for _, want := range []string{"kube-vip-a restarted 1 time(s)", KubeVIPLease, "API endpoint moved", "every connection through it was cut"} {
		if !strings.Contains(vip, want) {
			t.Errorf("the kube-vip incident does not say %q: %s", want, vip)
		}
	}
	if strings.Contains(vip, "not comparable") || strings.Contains(vip, "had not come back") {
		t.Errorf("a kube-vip that came back reads wrongly: %s", vip)
	}

	ccm := DescribeIncident(deployedscale.ComponentSample{
		Component: "nutanix-cloud-controller-manager-x",
		Pod:       deployedscale.PodFacts{RestartCount: 2, Ready: false, LastExitCode: 1, LastReason: "Error"},
	})
	for _, want := range []string{"restarted 2 time(s)", "leader election", "had not come back when this was read"} {
		if !strings.Contains(ccm, want) {
			t.Errorf("the CCM incident does not say %q: %s", want, ccm)
		}
	}
	if strings.Contains(ccm, KubeVIPLease) {
		t.Errorf("a CCM was given kube-vip's sentence: %s", ccm)
	}
}
