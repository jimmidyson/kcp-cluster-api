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
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sandboxContainer is the pause container the kubelet reports beside every
// pod's real ones. Its few MiB belong to no workload, and adding them would put
// a constant on every pod's figure.
const sandboxContainer = "POD"

// ParseNodeUsage reads every pod on one node out of a kubelet cAdvisor
// exposition, keyed "namespace/pod".
//
// # Why the whole node rather than a named list
//
// The question is what a control plane costs, and a scrape that reports only
// the pods somebody thought to name cannot answer it. kube-controller-manager
// is the case in point: its garbage collector holds an informer per resource,
// so every Cluster API CRD is cached there as well as in the API server, and no
// run had ever looked at it because no list mentioned it.
//
// It is also the only path that works. The API server's own /metrics needs
// credentials, and the pod proxy strips them — a request through it arrives as
// system:anonymous and is refused, which is what reduced every recorded
// control-plane figure to one arbitrary instance behind the VIP. The kubelet's
// cAdvisor endpoint is reached through the *node* proxy, which the driver is
// authorized for, and it reports every container on the node: all three API
// servers, all three etcd members, the controller manager, the scheduler and
// whatever else the node is carrying.
//
// What it does not give is a process's own metrics — heap, stored objects,
// request latency, etcd's database size. Those still come from the endpoints
// that serve them. This is the resource half, and the resource half is what a
// limit is set against.
func ParseNodeUsage(r io.Reader) (map[string]ContainerUsage, error) {
	out := map[string]ContainerUsage{}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, ok := series(line)
		if !ok {
			continue
		}

		container := labels["container"]
		namespace, pod := labels["namespace"], labels["pod"]
		// A series with no pod is the machine or a system slice; one with no
		// container, or the sandbox, is not a workload.
		if namespace == "" || pod == "" || container == "" || container == sandboxContainer {
			continue
		}
		key := namespace + "/" + pod
		usage := out[key]
		switch name {
		case "container_memory_working_set_bytes":
			usage.WorkingSetBytes += uint64(value)
		case "container_cpu_usage_seconds_total":
			usage.CPUSeconds += value
		case "container_cpu_cfs_throttled_seconds_total":
			usage.ThrottledSeconds += value
		case "container_cpu_cfs_periods_total":
			usage.Periods += int64(value)
		case "container_cpu_cfs_throttled_periods_total":
			usage.ThrottledPeriods += int64(value)
		default:
			continue
		}
		out[key] = usage
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading the cadvisor exposition: %w", err)
	}
	if len(out) == 0 {
		// Never an empty control plane: a node that answered with no series at
		// all is a scrape that failed, and reporting it as a node costing
		// nothing is wrong in the direction nobody checks.
		return nil, errors.New("no cAdvisor series for any pod on this node: the scrape reached " +
			"something, but not a kubelet reporting containers")
	}
	return out, nil
}
