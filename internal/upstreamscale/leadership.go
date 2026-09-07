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

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// exitSteppedDown is what a leader-elected Kubernetes component exits with when
// it cannot renew its lease. It is not a crash: the process is choosing to stop
// rather than act on state it can no longer confirm.
const exitSteppedDown = 1

// leadershipCost is what the cluster goes without while each control-plane
// component is not leading.
//
// Written out rather than summarised, because the summary is what made this
// look survivable. "kube-controller-manager restarted" reads as a process blip;
// the list is what it means.
var leadershipCost = map[string]string{
	"kube-controller-manager": "no garbage collection, no node lifecycle management, no " +
		"deployment, replicaset or job reconciliation, no endpoint or endpointslice updates, " +
		"no persistent volume binding and no taint eviction",
	"kube-scheduler": "nothing scheduled: every Pod created while it was gone stayed Pending",
}

// SteppedDown describes a Kubernetes control-plane component that lost its
// leader election, or "" when that is not what happened.
//
// # Why this is not a caveat
//
// A rung at 1500 clusters ended with kube-controller-manager exiting 1, and the
// first instinct — mine — was to treat it the way the Nutanix cloud controller
// manager is treated: a component with a short fuse, noticed first, not the
// system under test. That was wrong, and the difference is not subtle. The CCM
// is an addon that happens to run on control-plane nodes. kube-controller-manager
// *is* the control plane.
//
// Its own log says what that costs, in twenty-odd lines: the garbage collector,
// the node lifecycle controller, deployments, endpoints, taint eviction and the
// rest all shut down with it. A management cluster in that state is not a
// cluster with a caveat on its numbers — it is one that has stopped being able
// to manage itself, with a fleet still running on it.
//
// # And the five-second timeout is not over-sensitivity
//
// A lease that cannot be renewed makes the holder step aside rather than act on
// state it can no longer confirm. That is the safety mechanism working. A
// cluster where it fires repeatedly is one that is not safely operable, whatever
// the fleet counts say — so a run that met this has found its ceiling, and a
// stronger one than a convergence timeout would have been.
func SteppedDown(component string, facts deployedscale.PodFacts) string {
	// Static pods only. A Cluster API manager exits 1 for the same reason and
	// it is not the Kubernetes control plane: losing it stops reconciliation,
	// not the cluster's ability to run pods.
	if !facts.StaticPod || facts.LastExitCode != exitSteppedDown {
		return ""
	}
	role, cost := roleOf(component)
	if cost == "" {
		return ""
	}
	return fmt.Sprintf("the Kubernetes control plane shed %s: it exited 1, which is how a "+
		"leader-elected component steps down when it cannot renew its lease against the API "+
		"server. While it was gone the cluster had %s. This is the management cluster failing "+
		"to manage itself under the fleet, not a process that blipped — and stepping aside "+
		"rather than acting on state it can no longer confirm is the safety mechanism working",
		role, cost)
}

// roleOf finds which control-plane component a pod is, by the prefix its static
// pod name carries — kube-controller-manager-<node>, and so on.
func roleOf(component string) (role, cost string) {
	for name, what := range leadershipCost {
		if strings.HasPrefix(component, name) {
			return name, what
		}
	}
	return "", ""
}
