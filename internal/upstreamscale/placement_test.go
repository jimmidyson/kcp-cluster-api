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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestGuaranteedIsRequestsEqualToLimits, on every container, for both
// resources. Anything else is Burstable, and a Burstable component's numbers
// carry whatever else was happening on its node.
func TestGuaranteedIsRequestsEqualToLimits(t *testing.T) {
	d := released()
	// A second container, because these deployments carry sidecars and a QoS
	// class is a property of the pod: one unbounded container makes the whole
	// pod Burstable however carefully the others are set.
	d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, corev1.Container{Name: "kube-rbac-proxy"})

	if !Guarantee(d, resource.MustParse("6"), resource.MustParse("24Gi")) {
		t.Fatal("nothing was changed on a deployment with no resources set")
	}
	if got := QoSClass(d); got != "Guaranteed" {
		t.Fatalf("QoS = %s, want Guaranteed", got)
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Resources.Requests.Cpu().Cmp(*c.Resources.Limits.Cpu()) != 0 {
			t.Errorf("%s: cpu request %s != limit %s", c.Name,
				c.Resources.Requests.Cpu(), c.Resources.Limits.Cpu())
		}
		if c.Resources.Requests.Memory().Cmp(*c.Resources.Limits.Memory()) != 0 {
			t.Errorf("%s: memory request %s != limit %s", c.Name,
				c.Resources.Requests.Memory(), c.Resources.Limits.Memory())
		}
	}

	if Guarantee(d, resource.MustParse("6"), resource.MustParse("24Gi")) {
		t.Error("re-applying the same resources reported a change, which would restart the process being measured")
	}
}

// TestAGuaranteedContainerIsAlsoToldItsCeiling is the lesson the kcp runs paid
// for. A Go process does not see its cgroup limit: kcp was OOM killed against
// 4 GiB while holding 1.63 GiB of live heap, because the collector had grown
// the heap to 3 GiB with nothing telling it a limit existed. The identical
// fleet reached its target once GOMEMLIMIT was set.
//
// That matters more here, not less. The point of Guaranteed resources is that
// an OOM kill means "this component needs more memory"; without GOMEMLIMIT it
// means "the collector did not know when to run", and the run would be raising
// limits to buy headroom for garbage.
func TestAGuaranteedContainerIsAlsoToldItsCeiling(t *testing.T) {
	d := released()
	Guarantee(d, resource.MustParse("6"), resource.MustParse("24Gi"))

	var found string
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "GOMEMLIMIT" {
			found = e.Value
		}
	}
	if found == "" {
		t.Fatal("no GOMEMLIMIT: an OOM kill would mean the collector was uninformed, not that the fleet needs more memory")
	}
	if !strings.HasSuffix(found, "B") {
		t.Errorf("GOMEMLIMIT = %q, want a byte count", found)
	}
	// Under the limit, with headroom for what the runtime holds outside the
	// heap. Equal to the limit is an OOM kill waiting for a stack to grow.
	limit := resource.MustParse("24Gi")
	if parsed := resource.MustParse(strings.TrimSuffix(found, "B")); parsed.Value() >= limit.Value() {
		t.Errorf("GOMEMLIMIT %s is not below the container limit %s", found, &limit)
	}
}

// TestTheProviderToleratesItsOwnTaint. A dedicated node is only dedicated if it
// is tainted, and a tainted node takes nothing that does not tolerate it — so
// the toleration and the placement are one change, not two, and a run that made
// half of it would leave the provider Pending with its node idle.
func TestTheProviderToleratesItsOwnTaint(t *testing.T) {
	d := released()
	Dedicate(d, map[string]string{"scale-role": "devcluster"},
		corev1.Toleration{Key: "scale-role", Operator: corev1.TolerationOpEqual,
			Value: "devcluster", Effect: corev1.TaintEffectNoSchedule})

	if got := d.Spec.Template.Spec.NodeSelector["scale-role"]; got != "devcluster" {
		t.Errorf("node selector = %q, want devcluster", got)
	}
	if len(d.Spec.Template.Spec.Tolerations) != 1 {
		t.Fatalf("tolerations = %v", d.Spec.Template.Spec.Tolerations)
	}

	// Idempotent, for the same reason everything else here is: a re-applied run
	// must not restart the process it is measuring.
	Dedicate(d, map[string]string{"scale-role": "devcluster"},
		corev1.Toleration{Key: "scale-role", Operator: corev1.TolerationOpEqual,
			Value: "devcluster", Effect: corev1.TaintEffectNoSchedule})
	if len(d.Spec.Template.Spec.Tolerations) != 1 {
		t.Errorf("a second identical toleration was added: %v", d.Spec.Template.Spec.Tolerations)
	}
}

// TestBurstableIsNamedAsSuch. The report states the conditions its numbers only
// mean anything under, and QoS is one of them: a Burstable component that had a
// quiet node measured a quiet node.
func TestBurstableIsNamedAsSuch(t *testing.T) {
	d := released()
	d.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}
	if got := QoSClass(d); got != "Burstable" {
		t.Errorf("QoS = %s, want Burstable", got)
	}

	bare := released()
	if got := QoSClass(bare); got != "BestEffort" {
		t.Errorf("QoS = %s, want BestEffort", got)
	}
}
