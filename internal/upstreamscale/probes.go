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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ProbePatience gives a manager's health checks room to answer under load.
//
// # What kills a manager that has not run out of anything
//
// A rung of 1000 clusters ended 81 seconds in:
//
//	capi-kubeadm-control-plane-controller-manager restarted 1 time(s) — killed
//	with SIGKILL (137) but not flagged OOMKilled, and its memory limit is 6.0 GiB
//
// It was not short of memory and it was not short of CPU: 15 throttled CFS
// periods out of 61,053, which is a fortieth of one percent. Its liveness probe
// was `timeoutSeconds: 1`, `periodSeconds: 10`, `failureThreshold: 3`, so about
// thirty seconds of slow answers is a kill.
//
// And the check is not a ping. Cluster API wires healthz to controller-runtime's
// webhook StartedChecker, which opens a TLS connection to the manager's own
// webhook server on every probe. So the manager is killed when its webhook
// server is busy — and on this cluster a KubeadmControlPlane update costs about
// 139ms across its defaulting and validating webhooks, both served by that same
// process, while the KCP controller writes one per cluster per health probe.
// The load that fails the check is the load the check's own process is under.
//
// # Why this is not tuning the result
//
// A one-second self-dial deciding whether a manager lives is not a capacity
// measurement. Nothing here makes the cluster hold more clusters: it stops a
// run ending before it reaches a ceiling, which is the difference between
// having a number and having none. The same argument as the leader-election
// FlowSchema, and it applies to both sides of the comparison or the side that
// keeps its managers is being compared against the side that loses them.
//
// The slow webhooks stay slow and stay measured. What changes is that the run
// gets to find out what they cost instead of being killed by them.
func ProbePatience(d *appsv1.Deployment, timeoutSeconds, failureThreshold int32) bool {
	changed := false
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]
		// Liveness decides whether the container is killed. Readiness only
		// takes it out of a Service, but a manager flapping out of its webhook
		// Service is an admission failure for everything it admits — which is
		// the "ClusterClass can not be retrieved" that abandoned another rung.
		for _, probe := range []*corev1.Probe{c.LivenessProbe, c.ReadinessProbe} {
			if probe == nil {
				continue
			}
			if probe.TimeoutSeconds < timeoutSeconds {
				probe.TimeoutSeconds = timeoutSeconds
				changed = true
			}
			if probe.FailureThreshold < failureThreshold {
				probe.FailureThreshold = failureThreshold
				changed = true
			}
		}
	}
	return changed
}
