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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// stockProbes is what clusterctl installs, read off a running manager: a
// one-second timeout on a check that opens a TLS connection to the manager's
// own webhook server.
func stockProbes() *appsv1.Deployment {
	probe := func() *corev1.Probe {
		return &corev1.Probe{TimeoutSeconds: 1, PeriodSeconds: 10, FailureThreshold: 3}
	}
	return &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:           "manager",
				LivenessProbe:  probe(),
				ReadinessProbe: probe(),
			}},
		}},
	}}
}

// TestAManagerIsNotKilledForBeingBusyWithItsOwnWebhooks.
//
// The rung this comes from ended 81 seconds in with the KubeadmControlPlane
// manager killed by its kubelet, having run out of neither memory (6 GiB
// limit, not OOM) nor CPU (15 throttled periods in 61,053). Thirty seconds of
// a slow self-dial was enough.
func TestAManagerIsNotKilledForBeingBusyWithItsOwnWebhooks(t *testing.T) {
	d := stockProbes()
	if !ProbePatience(d, 5, 5) {
		t.Fatal("the stock one-second probe was left in place")
	}

	c := d.Spec.Template.Spec.Containers[0]
	for name, probe := range map[string]*corev1.Probe{
		"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe,
	} {
		if probe.TimeoutSeconds != 5 {
			t.Errorf("%s timeout = %ds", name, probe.TimeoutSeconds)
		}
		if probe.FailureThreshold != 5 {
			t.Errorf("%s failureThreshold = %d", name, probe.FailureThreshold)
		}
		// Untouched: the probe should be no less frequent, only more patient
		// about each answer.
		if probe.PeriodSeconds != 10 {
			t.Errorf("%s period changed to %ds, so the check also got rarer", name, probe.PeriodSeconds)
		}
	}
}

// TestAppliedTwiceIsAppliedOnce, so a repeated prepare does not report a
// change it did not make and does not roll the managers for nothing — a
// rollout resets every process metric the measurement is made of.
func TestAppliedTwiceIsAppliedOnce(t *testing.T) {
	d := stockProbes()
	ProbePatience(d, 5, 5)
	if ProbePatience(d, 5, 5) {
		t.Error("an already-patient probe was reported as changed")
	}
}

// TestAMorePatientProbeIsLeftAlone. Only raised, never lowered: a cluster
// someone has already given more room should not have it taken back by a run
// that thinks it knows better.
func TestAMorePatientProbeIsLeftAlone(t *testing.T) {
	d := stockProbes()
	d.Spec.Template.Spec.Containers[0].LivenessProbe.TimeoutSeconds = 30
	d.Spec.Template.Spec.Containers[0].LivenessProbe.FailureThreshold = 10

	ProbePatience(d, 5, 5)
	p := d.Spec.Template.Spec.Containers[0].LivenessProbe
	if p.TimeoutSeconds != 30 || p.FailureThreshold != 10 {
		t.Errorf("a more patient probe was cut back to %ds / %d", p.TimeoutSeconds, p.FailureThreshold)
	}
}

// TestAContainerWithNoProbesIsNotGivenAny. Adding a probe where the
// installation has none would be inventing a way for the kubelet to kill
// something it currently never kills.
func TestAContainerWithNoProbesIsNotGivenAny(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "manager"}},
		}},
	}}
	if ProbePatience(d, 5, 5) {
		t.Error("a container with no probes was reported as changed")
	}
	if d.Spec.Template.Spec.Containers[0].LivenessProbe != nil {
		t.Error("a probe was invented for a container that had none")
	}
}
