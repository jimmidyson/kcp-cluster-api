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

// Command capiscale-template turns the manifest clusterctl generated from
// CAREN's quick-start example into a management cluster for a scale test:
// nodes sized for the fleet rather than for a quick start, a fixed worker count
// instead of an autoscaled one, and without the addons this measurement does
// not use.
//
// Reads a generated manifest on stdin, writes one on stdout. It runs after
// clusterctl rather than on the template because the template's ${VARIABLE}
// placeholders sit in fields that are numbers once substituted, and a round
// trip through a YAML parser would quote them.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jimmidyson/kcp-cluster-api/internal/upstreamscale"
)

func main() {
	fs := flag.NewFlagSet("capiscale-template", flag.ExitOnError)
	sizing := upstreamscale.Sizing{}
	fs.IntVar(&sizing.Workers, "workers", 4, "How many worker nodes the cluster has in total, replacing "+
		"the autoscaler annotations CAREN's example sizes them with. A scale test cannot have its own "+
		"management cluster resizing underneath it.")
	// The comparison gives the control plane under test its own nodes, and the
	// labels have to come from the topology rather than from somebody
	// remembering to run kubectl: a node that is replaced comes back with
	// whatever the MachineDeployment says and nothing else.
	fs.IntVar(&sizing.ControlPlanePoolWorkers, "control-plane-pool-workers", 0,
		"Split the workers into two labelled pools, this many of them for the control plane under test "+
			"and the rest for everything else. Carved out of -workers rather than added to it. Zero "+
			"leaves the single pool the stock runs were taken on.")
	// CAREN's example builds every node at 2 vCPU and 4 GiB. That is a sensible
	// quick start and a sixth of the memory the sizing document asks the
	// control plane for — and nothing in a run would report it as wrong: the
	// cluster comes up, the controllers schedule, and the ceiling the ladder
	// finds is the box rather than Cluster API. Zero or empty leaves the
	// template's own value alone.
	fs.IntVar(&sizing.ControlPlaneVCPUs, "control-plane-vcpus", 16, "vCPUs per control plane node.")
	fs.StringVar(&sizing.ControlPlaneMemory, "control-plane-memory", "32Gi", "Memory per control plane node.")
	fs.StringVar(&sizing.ControlPlaneDisk, "control-plane-disk", "200Gi", "System disk per control plane "+
		"node. Larger than the example's 40Gi because etcd's revisions between compactions are what a "+
		"climbing fleet fills a disk with.")
	// The kcp side of the comparison gives its etcd a volume per member, so a
	// cluster hosting both sides needs a provisioner. Off by default so the
	// stock run keeps measuring what the recorded stock figures measured.
	fs.BoolVar(&sizing.KeepCSI, "keep-csi", false, "Leave the CSI addon on. Needed by the kcp side of "+
		"the comparison, whose etcd members each take a PersistentVolume; off for a stock-only cluster, "+
		"where nothing asks for one and every addon is another controller against the API server "+
		"under test.")
	fs.IntVar(&sizing.WorkerVCPUs, "worker-vcpus", 16, "vCPUs per worker node.")
	fs.StringVar(&sizing.WorkerMemory, "worker-memory", "32Gi", "Memory per worker node.")
	fs.StringVar(&sizing.WorkerDisk, "worker-disk", "100Gi", "System disk per worker node.")
	// Without this the Cluster names CAREN's own class and the copy the script
	// patched — etcd's backend quota, and the metrics port without which the
	// store cannot be measured — sits unused.
	fs.StringVar(&sizing.ClusterClass, "cluster-class", "", "ClusterClass the Cluster should name. "+
		"Empty leaves the template's own, which is CAREN's unpatched one.")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading stdin: %v\n", err)
		os.Exit(1)
	}
	out, err := upstreamscale.TrimForScale(string(in), sizing)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not trim the manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
