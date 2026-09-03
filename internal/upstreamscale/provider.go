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
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

// MaxInMemoryClusters is how many workload clusters one provider pod can serve.
//
// The in-memory backend gives every workload cluster its own listener, taking a
// port from the mux's range — 20000 to 30000 in
// test/infrastructure/inmemory/pkg/server/mux.go, one port per cluster, all
// inside a single process. So ten thousand clusters is a hard ceiling per
// provider pod, and it is a stated one rather than something a ladder discovers
// by dying with "no more free ports in the 20000-30000 range".
//
// It is also the reason the provider runs as one replica: two of them would
// each serve their own clusters, and nothing about the port range is shared.
const MaxInMemoryClusters = 10000

// CheckFleetFits refuses a fleet larger than one provider pod can serve.
func CheckFleetFits(clusters int) error {
	if clusters > MaxInMemoryClusters {
		return fmt.Errorf("%d clusters is more than one in-memory provider can serve: it gives each "+
			"workload cluster a listener on a port from 20000-30000, so %d is the ceiling for one pod",
			clusters, MaxInMemoryClusters)
	}
	return nil
}

// RunWithoutDocker strips what the released DevCluster provider deployment
// needs for its Docker backend and does not need for its in-memory one: the
// host's Docker socket, the hostPath volume behind it, and the privilege taken
// to use it. It reports whether anything changed.
//
// # Why this is not optional
//
// The in-memory backend creates no containers. The provider that serves it is
// the Docker provider all the same — DevCluster chooses a backend, and the
// released manifest is written for the other one. On an ordinary containerd
// node there is no /var/run/docker.sock, so the pod arrives with a hostPath
// mount to a file that does not exist and privilege it has no use for.
//
// Idempotent, because a run may be pointed at a cluster a previous run already
// prepared, and a patch that always reports a change would restart the very
// process being measured.
func RunWithoutDocker(d *appsv1.Deployment) bool {
	changed := false
	spec := &d.Spec.Template.Spec

	// The hostPath volumes, whatever they are called. Matching on the name
	// would be matching on a manifest's spelling; matching on hostPath matches
	// on what makes it unschedulable.
	removedVolumes := map[string]bool{}
	kept := spec.Volumes[:0]
	for _, v := range spec.Volumes {
		if v.HostPath != nil {
			removedVolumes[v.Name] = true
			changed = true
			continue
		}
		kept = append(kept, v)
	}
	spec.Volumes = kept

	for i := range spec.Containers {
		c := &spec.Containers[i]
		mounts := c.VolumeMounts[:0]
		for _, m := range c.VolumeMounts {
			if removedVolumes[m.Name] || strings.Contains(m.MountPath, "docker.sock") {
				changed = true
				continue
			}
			mounts = append(mounts, m)
		}
		c.VolumeMounts = mounts

		if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
			c.SecurityContext.Privileged = ptr(false)
			changed = true
		}
	}
	return changed
}

// Pin places the provider on one node, which is what a dedicated node for it
// means in practice: every in-memory workload cluster in the fleet is served
// from this one process, so where it runs is not an implementation detail.
func Pin(d *appsv1.Deployment, selector map[string]string) {
	if len(selector) == 0 {
		return
	}
	if d.Spec.Template.Spec.NodeSelector == nil {
		d.Spec.Template.Spec.NodeSelector = map[string]string{}
	}
	for k, v := range selector {
		d.Spec.Template.Spec.NodeSelector[k] = v
	}
}

// Profiling turns on the pprof endpoint every sample in this package is taken
// through, and reports whether it had to be added. Upstream managers serve no
// Go runtime metrics (see ScrapeProcess), so without this a run can measure
// reconcile rates and nothing about what the process costs.
func Profiling(d *appsv1.Deployment, addr string) bool {
	changed := false
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]
		if hasFlag(c.Args, "--profiler-address") {
			continue
		}
		c.Args = append(c.Args, "--profiler-address="+addr)
		changed = true
	}
	return changed
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }
