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
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// autoscalerAnnotations size a MachineDeployment when the cluster autoscaler
// owns it. Cluster API will not manage replicas on a pool wearing them.
var autoscalerAnnotations = []string{
	"cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size",
	"cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size",
}

// unwantedAddons are the ones this measurement does not use. Everything left on
// is another controller reconciling against the API server whose cost is the
// subject of the run.
var unwantedAddons = []string{csiAddon, "cosi", "clusterAutoscaler", "serviceLoadBalancer", "nfd"}

// csiAddon is named because it is the one of the five that a run can ask to
// keep. See Sizing.KeepCSI.
const csiAddon = "csi"

// NodePoolLabel marks which pool a node belongs to, and its domain is the
// mechanism rather than a naming choice.
//
// Cluster API does not copy arbitrary Machine labels to a Node. The kubelet
// cannot self-assign labels outside the NodeRestriction domains, so the Machine
// controller syncs only what `util/labels.GetManagedLabels` admits:
// node-role.kubernetes.io, node-restriction.kubernetes.io, and
// node.cluster.x-k8s.io, each with their subdomains — plus anything matching a
// regex given to the core manager's --additional-sync-machine-labels.
//
// A pool labelled `scale-role=control-plane` would reach the
// MachineDeployment, the MachineSet and the Machine, and stop there. The nodes
// would come up unlabelled, the shard's pods would be unschedulable, and
// nothing would say why — so the label lives in a domain that propagates
// without asking anything of the management cluster's own flags.
const (
	NodePoolLabel        = clusterv1.ManagedNodeLabelDomain + "/scale-role"
	NodePoolControlPlane = "control-plane"
	NodePoolManagers     = "managers"
)

// TrimForScale takes the manifest clusterctl generated from CAREN's own
// quick-start example and makes it a management cluster for a scale test.
//
// Run on the generated manifest rather than on the template, deliberately: the
// template carries ${VARIABLE} placeholders in fields that are numbers once
// substituted, and a round trip through a YAML parser would quote them —
// turning `replicas: ${COUNT}` into a string where an integer belongs. After
// clusterctl has substituted, every value is concrete and the round trip is
// safe.
//
// # What it changes and why
//
//   - **The worker pool gets a replica count.** CAREN's example sets no
//     replicas at all: the size is a pair of cluster-autoscaler annotations, so
//     the pool is whatever the autoscaler decides. A scale test cannot have its
//     own management cluster resizing underneath it. The annotations come off
//     and a count goes on — both, because Cluster API refuses a topology that
//     sets replicas while the annotations are still there.
//   - **Addons this run does not use come off**: CSI and COSI (nothing here
//     asks for a PersistentVolume), the autoscaler, the service load balancer
//     and node feature discovery.
//   - **The CNI and the cloud provider stay.** The CNI is how pods talk at all,
//     and the cloud provider is what clears the uninitialized taint from a new
//     node — without it the cluster's nodes never become schedulable, which
//     presents as a scale test that cannot place its own controllers.
//
// Sizing is what a scale test needs a CAREN-generated cluster to differ in.
//
// A zero field means "leave the template's own value alone", so the trimmer
// stays usable on a template whose sizes are already right.
type Sizing struct {
	// Workers is how many worker nodes the cluster has in total.
	Workers int

	// ControlPlanePoolWorkers splits those workers into two pools, so that the
	// control plane under test can be given nodes of its own.
	//
	// # Why the cluster does this rather than a person
	//
	// R5 gives the kcp side's shard and store their own nodes, and the managers
	// have to be kept off them: a manager sharing a node with the shard it is
	// driving makes the shard's figures a measurement of both. That needs the
	// nodes labelled, and labelling them by hand is a step that has to be
	// remembered, and redone every time a node is replaced — on a cluster whose
	// whole purpose is to be pushed until something breaks.
	//
	// Through the topology it is a property of the cluster instead: the label
	// travels MachineDeployment → MachineSet → Machine → Node, so a node that
	// is rolled comes back labelled. See NodePoolLabel for what makes it reach
	// the Node at all.
	//
	// Carved out of Workers rather than added to it: WORKER_COUNT is how many
	// workers the cluster has, and a flag that quietly bought three more
	// machines would be a bill nobody asked for. Zero leaves the template's
	// single pool exactly as it was, which is the cluster the recorded stock
	// runs were taken on.
	ControlPlanePoolWorkers int

	// KeepCSI leaves the CSI addon on.
	//
	// Off by default, because the stock run asks for no PersistentVolume and
	// every addon left on is another controller reconciling against the API
	// server whose cost is the subject. The kcp side of the comparison asks
	// for three volumes — one per etcd member, because a member that loses its
	// data directory cannot rejoin the quorum — and both sides share one
	// cluster, so that cluster needs a provisioner.
	//
	// A knob rather than a new default: the stock figures already recorded
	// were taken without CSI, and flipping the default would make the next
	// stock run quietly incomparable with them.
	KeepCSI bool

	// ClusterClass is the class the Cluster should name.
	//
	// CAREN's template names CAREN's own class, and the provisioning script
	// works on a copy of it — the copy is where the etcd backend quota and the
	// metrics port are patched in. Without this the copy sits unused and the
	// cluster comes up with the stock quota and etcd bound to 127.0.0.1, which
	// is invisible until something needs either.
	ClusterClass string

	// The rest are the node sizes. CAREN's example builds every node at 2 vCPU
	// and 4 GiB — a sensible quick start, and a sixth of the memory the sizing
	// document asks the control plane for.
	ControlPlaneVCPUs  int
	ControlPlaneMemory string
	ControlPlaneDisk   string
	WorkerVCPUs        int
	WorkerMemory       string
	WorkerDisk         string
}

func TrimForScale(manifest string, sizing Sizing) (string, error) {
	docs, err := split(manifest)
	if err != nil {
		return "", err
	}

	trimmed := false
	for _, doc := range docs {
		if doc.GetKind() != "Cluster" {
			continue
		}
		if err := trimCluster(doc, sizing); err != nil {
			return "", err
		}
		trimmed = true
	}
	if !trimmed {
		return "", errors.New("no Cluster in the generated manifest: clusterctl generate cluster " +
			"produced something this does not recognise, and applying it unchanged would create a " +
			"cluster sized by an autoscaler this run removes")
	}

	var out bytes.Buffer
	for i, doc := range docs {
		if i > 0 {
			out.WriteString("---\n")
		}
		raw, err := yaml.Marshal(doc.Object)
		if err != nil {
			return "", fmt.Errorf("re-encoding %s/%s: %w", doc.GetKind(), doc.GetName(), err)
		}
		out.Write(raw)
	}
	return out.String(), nil
}

func trimCluster(doc *unstructured.Unstructured, sizing Sizing) error {
	if sizing.ClusterClass != "" {
		if err := unstructured.SetNestedField(doc.Object, sizing.ClusterClass,
			"spec", "topology", "classRef", "name"); err != nil {
			return fmt.Errorf("naming the cluster class: %w", err)
		}
	}

	// Every machine deployment: annotations off, replicas on.
	pools, found, err := unstructured.NestedSlice(doc.Object, "spec", "topology", "workers", "machineDeployments")
	if err != nil {
		return fmt.Errorf("reading the worker pools: %w", err)
	}
	if found {
		for i := range pools {
			pool, ok := pools[i].(map[string]any)
			if !ok {
				continue
			}
			if metadata, ok := pool["metadata"].(map[string]any); ok {
				if annotations, ok := metadata["annotations"].(map[string]any); ok {
					for _, a := range autoscalerAnnotations {
						delete(annotations, a)
					}
					if len(annotations) == 0 {
						delete(metadata, "annotations")
					}
				}
				if len(metadata) == 0 {
					delete(pool, "metadata")
				}
			}
			pool["replicas"] = int64(sizing.Workers)
		}
		if sizing.ControlPlanePoolWorkers > 0 {
			pools, err = splitPool(pools, sizing)
			if err != nil {
				return err
			}
		}
		if err := unstructured.SetNestedSlice(doc.Object,
			pools, "spec", "topology", "workers", "machineDeployments"); err != nil {
			return fmt.Errorf("writing the worker pools: %w", err)
		}
	}

	// The addons live under the clusterConfig topology variable.
	variables, found, err := unstructured.NestedSlice(doc.Object, "spec", "topology", "variables")
	if err != nil {
		return fmt.Errorf("reading the topology variables: %w", err)
	}
	if !found {
		return nil
	}
	for i := range variables {
		variable, ok := variables[i].(map[string]any)
		if !ok || variable["name"] != "clusterConfig" {
			continue
		}
		value, ok := variable["value"].(map[string]any)
		if !ok {
			continue
		}
		if addons, ok := value["addons"].(map[string]any); ok {
			for _, name := range unwantedAddons {
				if name == csiAddon && sizing.KeepCSI {
					continue
				}
				delete(addons, name)
			}
		}
		// The control plane's machine size lives under this same variable.
		resize(nested(value, "controlPlane", "nutanix", "machineDetails"),
			sizing.ControlPlaneVCPUs, sizing.ControlPlaneMemory, sizing.ControlPlaneDisk)
	}
	for i := range variables {
		variable, ok := variables[i].(map[string]any)
		if !ok || variable["name"] != "workerConfig" {
			continue
		}
		value, ok := variable["value"].(map[string]any)
		if !ok {
			continue
		}
		resize(nested(value, "nutanix", "machineDetails"),
			sizing.WorkerVCPUs, sizing.WorkerMemory, sizing.WorkerDisk)
	}
	if err := unstructured.SetNestedSlice(doc.Object, variables, "spec", "topology", "variables"); err != nil {
		return fmt.Errorf("writing the topology variables: %w", err)
	}
	return nil
}

// resize adjusts the fields a scale test cares about and leaves the rest of
// machineDetails alone: the image, the subnet and the Prism Element cluster are
// the operator's environment, not this harness's business.
//
// vCPUs are set through the socket count, with the cores per socket left as the
// template had them. Two numbers multiply to make a vCPU count and only one of
// them needs to move; changing both invites a cluster with four times the CPUs
// anyone asked for.
func resize(details map[string]any, vcpus int, memory, disk string) {
	if details == nil {
		return
	}
	if vcpus > 0 {
		details["vcpuSockets"] = int64(vcpus)
	}
	if memory != "" {
		details["memorySize"] = memory
	}
	if disk != "" {
		details["systemDiskSize"] = disk
	}
}

// nested walks a path of maps, returning nil rather than creating anything: a
// field this harness does not find is one the template does not have, and
// inventing it would produce a cluster shaped by a guess.
func nested(m map[string]any, path ...string) map[string]any {
	for _, key := range path {
		next, ok := m[key].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

func split(manifest string) ([]*unstructured.Unstructured, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	var docs []*unstructured.Unstructured
	for {
		raw, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("splitting the manifest: %w", err)
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		object := map[string]any{}
		if err := yaml.Unmarshal(raw, &object); err != nil {
			return nil, fmt.Errorf("parsing a document: %w", err)
		}
		if len(object) == 0 {
			continue
		}
		docs = append(docs, &unstructured.Unstructured{Object: object})
	}
	return docs, nil
}

// splitPool turns the template's single worker pool into two labelled ones: the
// nodes the control plane under test is given, and the nodes everything else
// runs on.
//
// The first pool is the one copied, because CAREN's example has exactly one and
// a class is what a copy has to share. Both come out labelled, so that each
// side of the comparison has something to select on rather than one being
// "wherever the other is not".
func splitPool(pools []any, sizing Sizing) ([]any, error) {
	if sizing.ControlPlanePoolWorkers >= sizing.Workers {
		return nil, fmt.Errorf("%d of %d workers given to the control plane leaves the managers nowhere "+
			"to run: they would sit Pending on a cluster that looks correctly sized",
			sizing.ControlPlanePoolWorkers, sizing.Workers)
	}
	if len(pools) == 0 {
		return nil, errors.New("no worker pool to split: this manifest has none")
	}
	if len(pools) > 1 {
		return nil, fmt.Errorf("this manifest has %d worker pools and the split assumes one: "+
			"which of them the control plane should get is a question this cannot answer", len(pools))
	}

	first, ok := pools[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the worker pool is %T", pools[0])
	}
	name, _ := first["name"].(string)

	controlPlane := deepCopyPool(first)
	controlPlane["name"] = name + "-" + NodePoolControlPlane
	controlPlane["replicas"] = int64(sizing.ControlPlanePoolWorkers)
	labelPool(controlPlane, NodePoolControlPlane)

	managers := deepCopyPool(first)
	managers["name"] = name + "-" + NodePoolManagers
	managers["replicas"] = int64(sizing.Workers - sizing.ControlPlanePoolWorkers)
	labelPool(managers, NodePoolManagers)

	return []any{controlPlane, managers}, nil
}

// labelPool sets the pool's role, which the topology copies onto the
// MachineDeployment and its template and which reaches the Node from there.
func labelPool(pool map[string]any, role string) {
	metadata, ok := pool["metadata"].(map[string]any)
	if !ok {
		metadata = map[string]any{}
		pool["metadata"] = metadata
	}
	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
		metadata["labels"] = labels
	}
	labels[NodePoolLabel] = role
}

// deepCopyPool copies a pool so the two do not share the maps inside it, which
// a shallow copy would — leaving both pools with whichever label was set last.
func deepCopyPool(pool map[string]any) map[string]any {
	out := runtime.DeepCopyJSON(map[string]any{"pool": pool})
	copied, _ := out["pool"].(map[string]any)
	return copied
}
