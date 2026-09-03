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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestEveryControllerIsSizedAndReachable. The list is the single place the
// prepare tool and the sampler both read: a controller missing a size would be
// left Burstable and carry its neighbours, and one missing a namespace would be
// sized and never sampled.
func TestEveryControllerIsSizedAndReachable(t *testing.T) {
	controllers := Controllers()
	if len(controllers) != 4 {
		t.Fatalf("%d controllers, want the four clusterctl installs", len(controllers))
	}
	devClusters := 0
	for _, c := range controllers {
		if c.Name == "" || c.Namespace == "" || c.Deployment == "" || c.Container == "" {
			t.Errorf("%+v is not addressable", c)
		}
		if _, _, err := c.Quantities(); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
		if c.DevCluster {
			devClusters++
		}
	}
	// Exactly one, because the Docker socket removal applies to exactly one
	// and marking a second would strip a volume some other provider needs.
	if devClusters != 1 {
		t.Errorf("%d controllers marked DevCluster, want 1", devClusters)
	}
}

// TestPodFactsComeFromTheManagerContainer. The first run reported every
// controller not ready, with no memory limit and no restarts, at every rung
// — because the sampler looked for a container named after the deployment,
// and clusterctl names every provider's container "manager". Facts read from
// a container that is not there are all zero, and zero restarts and no OOM
// kill is exactly what Classify reads as "nothing died", so a controller the
// kernel killed would have been reported as a fleet that did not keep up.
func TestPodFactsComeFromTheManagerContainer(t *testing.T) {
	for _, c := range Controllers() {
		pod := &corev1.Pod{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "manager",
				Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse(c.Memory),
				}},
			}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "manager", Ready: true, RestartCount: 2,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137,
				}},
			}}},
		}
		facts := c.PodFacts(pod)
		if !facts.Ready || facts.MemoryLimitBytes == 0 {
			t.Errorf("%s: facts were not read from the manager container: %+v", c.Name, facts)
		}
		if facts.RestartCount != 2 || !facts.OOMKilled {
			t.Errorf("%s: the kill was not seen: %+v", c.Name, facts)
		}
	}
}
