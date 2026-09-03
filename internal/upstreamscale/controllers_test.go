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

import "testing"

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
		if c.Name == "" || c.Namespace == "" || c.Deployment == "" {
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
