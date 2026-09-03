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
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
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
var unwantedAddons = []string{"csi", "cosi", "clusterAutoscaler", "serviceLoadBalancer", "nfd"}

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
func TrimForScale(manifest string, workerReplicas int) (string, error) {
	docs, err := split(manifest)
	if err != nil {
		return "", err
	}

	trimmed := false
	for _, doc := range docs {
		if doc.GetKind() != "Cluster" {
			continue
		}
		if err := trimCluster(doc, workerReplicas); err != nil {
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

func trimCluster(doc *unstructured.Unstructured, workerReplicas int) error {
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
			pool["replicas"] = int64(workerReplicas)
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
		addons, ok := value["addons"].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range unwantedAddons {
			delete(addons, name)
		}
	}
	if err := unstructured.SetNestedSlice(doc.Object, variables, "spec", "topology", "variables"); err != nil {
		return fmt.Errorf("writing the topology variables: %w", err)
	}
	return nil
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
