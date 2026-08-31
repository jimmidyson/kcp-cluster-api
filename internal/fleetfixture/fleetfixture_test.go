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

package fleetfixture

import (
	"os"
	"strings"
	"testing"
)

// TestCRDPathsResolve is the cheap half of what an integration run would find
// out the expensive way.
//
// A manifest that moved in a Cluster API bump fails a sweep or a target run
// minutes in, after a kcp server has started, with an error about a file. Here
// it fails in milliseconds and names the file, which is the difference between
// a bump that is diagnosed and one that is merely observed.
func TestCRDPathsResolve(t *testing.T) {
	paths, err := CRDPaths()
	if err != nil {
		t.Fatalf("resolving the fleet's CRD manifests: %v", err)
	}

	want := len(CoreCRDs) + len(BootstrapCRDs) + len(ControlPlaneCRDs) + len(DevCRDs)
	if len(paths) != want {
		t.Fatalf("resolved %d manifests, want %d", len(paths), want)
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("resolved manifest does not exist: %v", err)
		}
	}
}

// TestDevCRDsComeFromTheTestModule pins the split that is easy to get wrong.
//
// The dev infrastructure provider lives in Cluster API's *test* module, not the
// main one. Resolving its manifests against the main module fails with a path
// that looks plausible, so the two lists are resolved through different helpers
// and this is what keeps them that way.
func TestDevCRDsComeFromTheTestModule(t *testing.T) {
	core, err := CoreModulePaths(CoreCRDs)
	if err != nil {
		t.Fatalf("resolving the core manifests: %v", err)
	}
	dev, err := DevModulePaths(DevCRDs)
	if err != nil {
		t.Fatalf("resolving the dev manifests: %v", err)
	}

	if _, err := CoreModulePaths(DevCRDs); err == nil {
		t.Error("the dev provider's manifests resolved against the main module, which is not where they live")
	}

	// Different module directories, which is the property the split exists for.
	if commonPrefix(core[0], dev[0]) == core[0] {
		t.Errorf("core manifest %q and dev manifest %q come from one module directory", core[0], dev[0])
	}
}

func TestMuxPortsSpansAtLeastTheClusterCount(t *testing.T) {
	for _, clusters := range []int{0, 1, 200, 2000} {
		ports, err := MuxPorts(clusters)
		if err != nil {
			t.Fatalf("MuxPorts(%d): %v", clusters, err)
		}
		if span := int(ports.MaxPort - ports.MinPort); span < clusters {
			t.Errorf("MuxPorts(%d) spans %d ports: the in-memory backend takes one listener per workload cluster",
				clusters, span)
		}
		if ports.MinPort <= 0 || ports.DebugPort <= 0 {
			t.Errorf("MuxPorts(%d) returned ports %+v", clusters, ports)
		}
		// The debug port must not fall inside the listener range, or a workload
		// cluster and the mux's own debug endpoint would contend for it.
		if ports.DebugPort >= ports.MinPort && ports.DebugPort <= ports.MaxPort {
			t.Errorf("MuxPorts(%d) put the debug port %d inside the listener range %d-%d",
				clusters, ports.DebugPort, ports.MinPort, ports.MaxPort)
		}
	}

	if _, err := MuxPorts(-1); err == nil {
		t.Error("a negative cluster count was accepted")
	}
}

func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return strings.TrimSuffix(a[:n], "/")
}
