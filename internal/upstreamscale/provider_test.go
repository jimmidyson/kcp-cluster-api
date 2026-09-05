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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// released is the shape of the DevCluster provider's own manager Deployment as
// it ships: it is the Docker provider, and the in-memory backend is a mode of
// it, so the manifest mounts the host's Docker socket and takes privilege it
// only needs for the other backend.
func released() *appsv1.Deployment {
	yes := true
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "capd-controller-manager", Namespace: "capd-system"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name: "manager",
						VolumeMounts: []corev1.VolumeMount{
							{Name: "dockersock", MountPath: "/var/run/docker.sock"},
							{Name: "cert", MountPath: "/tmp/k8s-webhook-server/serving-certs"},
						},
						SecurityContext: &corev1.SecurityContext{Privileged: &yes},
					}},
					Volumes: []corev1.Volume{
						{Name: "dockersock", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/var/run/docker.sock"},
						}},
						{Name: "cert", VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "capd-webhook-service-cert"},
						}},
					},
				},
			},
		},
	}
}

// TestTheProviderIsMadeToRunWithoutDocker is a finding, not a convenience.
//
// An in-memory DevCluster creates no containers and needs no container runtime,
// but the provider that serves it is the Docker provider and its released
// deployment mounts /var/run/docker.sock from the host and runs privileged. An
// ordinary containerd node has no such socket, so the pod either fails to
// schedule or comes up unable to do the one thing the manifest took privilege
// for. Discovering that on the cluster costs an afternoon; this costs a test.
func TestTheProviderIsMadeToRunWithoutDocker(t *testing.T) {
	d := released()
	if !RunWithoutDocker(d) {
		t.Fatal("nothing was changed in a deployment that mounts the Docker socket")
	}

	container := d.Spec.Template.Spec.Containers[0]
	for _, m := range container.VolumeMounts {
		if strings.Contains(m.MountPath, "docker.sock") {
			t.Errorf("the Docker socket is still mounted at %s", m.MountPath)
		}
	}
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.HostPath != nil {
			t.Errorf("a hostPath volume survived: %s -> %s", v.Name, v.HostPath.Path)
		}
	}
	if container.SecurityContext != nil && container.SecurityContext.Privileged != nil &&
		*container.SecurityContext.Privileged {
		t.Error("the container is still privileged")
	}

	// What must NOT be removed: the webhook certificate. Stock Cluster API
	// serves validating and defaulting webhooks, and a provider without its
	// serving certificate fails every admission request rather than starting
	// and being obviously broken.
	var keptCert bool
	for _, m := range container.VolumeMounts {
		if m.Name == "cert" {
			keptCert = true
		}
	}
	if !keptCert {
		t.Error("the webhook serving certificate was removed with the Docker socket")
	}

	// Idempotent: a run that is re-applied against a cluster where a previous
	// run already patched the deployment must not report a change it did not
	// make, or every run would restart the provider it is measuring.
	if RunWithoutDocker(d) {
		t.Error("patching an already-patched deployment reported a change")
	}
}

// TestTheMuxPortRangeIsAStatedCeiling. The in-memory backend gives each
// workload cluster a listener on a port from 20000-30000, one port per cluster,
// from a single process. Ten thousand clusters is therefore a hard bound per
// provider pod — worth knowing before a ladder climbs towards it rather than
// after a run dies at it with "no more free ports".
func TestTheMuxPortRangeIsAStatedCeiling(t *testing.T) {
	if MaxInMemoryClusters != 10000 {
		t.Errorf("MaxInMemoryClusters = %d, want 10000 (ports 20000-30000)", MaxInMemoryClusters)
	}
	if err := CheckFleetFits(9999); err != nil {
		t.Errorf("a fleet inside the range was refused: %v", err)
	}
	err := CheckFleetFits(10001)
	if err == nil {
		t.Fatal("a fleet larger than the port range was accepted")
	}
	if !strings.Contains(err.Error(), "20000") {
		t.Errorf("the refusal does not say where the bound comes from: %v", err)
	}
}

// TestTheTopologyGateIsChecked, because the run cannot work without it and the
// failure names something else.
//
// A ClusterClass-based fleet needs the ClusterTopology feature gate on the
// providers, and clusterctl sets it from CLUSTER_TOPOLOGY at init time. Without
// it the objects are refused by an admission webhook — "spec: Forbidden: can be
// set only if the ClusterTopology feature flag is enabled" — which reads like a
// problem with the object rather than with how the provider was installed.
func TestTheTopologyGateIsChecked(t *testing.T) {
	off := released()
	if TopologyEnabled(off) {
		t.Error("a deployment with no feature gates was reported as having the topology gate")
	}

	on := released()
	on.Spec.Template.Spec.Containers[0].Args = []string{
		"--leader-elect",
		"--feature-gates=MachinePool=true,ClusterTopology=true",
	}
	if !TopologyEnabled(on) {
		t.Error("ClusterTopology=true was not recognised")
	}

	// Explicitly off is not the same as absent, and both are wrong here.
	disabled := released()
	disabled.Spec.Template.Spec.Containers[0].Args = []string{"--feature-gates=ClusterTopology=false"}
	if TopologyEnabled(disabled) {
		t.Error("ClusterTopology=false was read as enabled")
	}

	// A gate on any container counts: these deployments have carried sidecars,
	// and the manager is not always first.
	sidecar := released()
	sidecar.Spec.Template.Spec.Containers = append([]corev1.Container{{Name: "kube-rbac-proxy"}},
		corev1.Container{Name: "manager", Args: []string{"--feature-gates=ClusterTopology=true"}})
	if !TopologyEnabled(sidecar) {
		t.Error("the gate was missed on a deployment whose manager is not the first container")
	}
}

// TestTheTopologyGateIsSetRatherThanReported. The gate is a container argument,
// so the step that already patches these deployments can simply add it — and
// then a cluster whose providers were installed without it is one command from
// being measurable rather than one reinstall.
func TestTheTopologyGateIsSetRatherThanReported(t *testing.T) {
	// No feature gates at all: the flag has to be added.
	fresh := released()
	fresh.Spec.Template.Spec.Containers[0].Args = []string{"--leader-elect"}
	if !EnableTopology(fresh) {
		t.Fatal("nothing changed on a deployment with no gate")
	}
	if !TopologyEnabled(fresh) {
		t.Error("the gate was not enabled")
	}
	if got := fresh.Spec.Template.Spec.Containers[0].Args; len(got) != 2 {
		t.Errorf("args = %v, want the original plus one", got)
	}

	// Gates already present: ClusterTopology joins them rather than replacing
	// them, because a provider installed with MachinePool on wants to keep it.
	existing := released()
	existing.Spec.Template.Spec.Containers[0].Args = []string{"--feature-gates=MachinePool=true"}
	if !EnableTopology(existing) {
		t.Fatal("nothing changed on a deployment with other gates")
	}
	args := strings.Join(existing.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "MachinePool=true") {
		t.Errorf("an existing gate was lost: %s", args)
	}
	if !TopologyEnabled(existing) {
		t.Errorf("the gate was not enabled: %s", args)
	}

	// Explicitly false is corrected rather than left alone.
	off := released()
	off.Spec.Template.Spec.Containers[0].Args = []string{"--feature-gates=ClusterTopology=false"}
	if !EnableTopology(off) || !TopologyEnabled(off) {
		t.Errorf("an explicitly disabled gate was not corrected: %v",
			off.Spec.Template.Spec.Containers[0].Args)
	}

	// Idempotent, like every other patch here: re-running must not restart a
	// controller whose metrics are the measurement.
	if EnableTopology(off) {
		t.Error("re-enabling an enabled gate reported a change")
	}
}

// TestTheManagersAreNotRateLimitedIntoTheirOwnCeiling.
//
// At 1,000 clusters the core manager lost its leader election and exited:
//
//	"Waited before sending request" delay="1.254030282s"
//	  reason="client-side throttling, not priority and fairness"
//	"Failed to renew lease" err="context deadline exceeded"
//	"Problem running manager" err="leader election lost"
//
// The API server was not refusing anything. The manager was queueing its own
// requests behind its own limiter for over a second each, until a five-second
// lease renewal could not get through — and a lost lease is an orderly exit,
// not a crash, so nothing in the run said "out of memory" or "out of anything".
//
// That is a real finding about the defaults and it is not the finding this run
// is looking for: a ceiling reached because a client throttled itself is a
// ceiling about a flag. The flags are raised so that what stops the run is the
// machine, and the report records what they were raised to, because the two
// sides of the comparison have to be given the same room.
func TestTheManagersAreNotRateLimitedIntoTheirOwnCeiling(t *testing.T) {
	d := deploymentWithArgs("manager", "--leader-elect")

	if !ClientLimits(&d, 500, 1000) {
		t.Fatal("raising the client limits reported no change")
	}
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--kube-api-qps=500") || !strings.Contains(args, "--kube-api-burst=1000") {
		t.Errorf("the manager is still on its default limits: %q", args)
	}

	// Idempotent: the prepare step runs before every run, and a second pass
	// must not append a duplicate flag — a repeated flag takes the last value
	// on a command line, which is a silent way to measure something else.
	if ClientLimits(&d, 500, 1000) {
		t.Error("a second pass reported a change")
	}
	if n := strings.Count(strings.Join(d.Spec.Template.Spec.Containers[0].Args, " "), "--kube-api-qps"); n != 1 {
		t.Errorf("--kube-api-qps appears %d times", n)
	}
}

// TestAnExistingLimitIsReplacedRatherThanAppended, so a cluster prepared once
// at one value and again at another ends up at the second.
func TestAnExistingLimitIsReplacedRatherThanAppended(t *testing.T) {
	d := deploymentWithArgs("manager", "--kube-api-qps=100", "--kube-api-burst=200")

	if !ClientLimits(&d, 500, 1000) {
		t.Fatal("replacing the limits reported no change")
	}
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if strings.Contains(args, "--kube-api-qps=100") || strings.Contains(args, "--kube-api-burst=200") {
		t.Errorf("the old limits are still there: %q", args)
	}
	if !strings.Contains(args, "--kube-api-qps=500") {
		t.Errorf("the new limit is not there: %q", args)
	}
}

// TestTheLeaseSurvivesASlowMoment.
//
// The limiter is the cause and the deadline is what turned it into an exit. A
// manager that cannot renew a lease within ten seconds gives up leading, and at
// a fleet size where the API server occasionally takes longer than that, the
// run ends with a restart rather than a measurement. The deadlines are widened
// so that a slow moment is a slow moment.
func TestTheLeaseSurvivesASlowMoment(t *testing.T) {
	d := deploymentWithArgs("manager", "--leader-elect")

	if !LeaderElectionDeadlines(&d, time.Minute, 40*time.Second, 5*time.Second) {
		t.Fatal("widening the deadlines reported no change")
	}
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	for _, want := range []string{
		"--leader-elect-lease-duration=1m0s",
		"--leader-elect-renew-deadline=40s",
		"--leader-elect-retry-period=5s",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s: %q", want, args)
		}
	}
	if LeaderElectionDeadlines(&d, time.Minute, 40*time.Second, 5*time.Second) {
		t.Error("a second pass reported a change")
	}
}

// deploymentWithArgs is a manager with the arguments given and nothing else
// that matters here.
func deploymentWithArgs(container string, args ...string) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "capi-controller-manager", Namespace: "capi-system"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: container, Args: args}}},
			},
		},
	}
}
