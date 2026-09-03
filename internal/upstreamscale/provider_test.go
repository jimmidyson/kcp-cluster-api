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
