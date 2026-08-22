//go:build integration

/*
Copyright 2026 The Kubernetes Authors.

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

package dockerbackend_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"text/template"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/test/infrastructure/container"
)

// Installing a CNI is what takes a container-backed cluster from "control
// plane initialized" to Nodes that are Ready. Without one kubelet reports
//
//	NetworkReady=false … cni plugin not initialized
//
// and every Machine stays NotReady for ever. Nothing in Cluster API installs
// one: in a real deployment that is an add-on provider's job, and in Cluster
// API's own e2e suites it is a manifest applied by the test
// (test/framework/clusterctl, CNIManifestPath). This is the same thing, minus
// the manifest.
const (
	// cniManifestPath is where kind's node image keeps the manifest for its
	// own CNI, kindnet.
	//
	// # Why read it out of the node instead of shipping one
	//
	// The image is already there. kind's node image build writes this file
	// (pkg/build/nodeimage/buildcontext.go) *and* preloads the kindnetd image
	// it names into the node's containerd store
	// (defaultCNIImages, pkg/build/nodeimage/const_cni.go), so applying it
	// pulls nothing: a CI runner that can start the cluster can install this
	// CNI, with no registry reachable and nothing vendored here. A manifest
	// carried in this repository would also have to be kept in step with the
	// Kubernetes version each node image is built for; this one cannot drift,
	// because it ships with the image it is for.
	//
	// kind installs it the same way, by reading this path back out of the
	// control plane node
	// (pkg/cluster/internal/create/actions/installcni/cni.go).
	cniManifestPath = "/kind/manifests/default-cni.yaml"

	// cniTemplateMarker is the string kind's manifest carries when it expects
	// to be rendered as a Go template. kind calls this mechanism "intentionally
	// undocumented, as an internal implementation detail … not intended for
	// external usage and is unstable", so this reads the marker rather than
	// assuming: a node image whose manifest stops carrying it is applied
	// verbatim rather than mangled.
	cniTemplateMarker = "would you kindly template this file"

	cniInstallTimeout = 10 * time.Minute
	cniPollInterval   = 5 * time.Second

	// cniFieldOwner is who server-side apply records as the owner of these
	// objects. Named for the test rather than the CNI, because what it owns is
	// this harness's copy of kind's manifest.
	cniFieldOwner = "kcp-cluster-api-cni-install-test"
)

// installCNIWhileProvisioning installs a CNI in every workload cluster as it
// comes up, and does not call one installed until its pods are running.
//
// It polls rather than waits for a signal because there is no signal to wait
// for: the kubeconfig Secret it needs is written by the control plane provider
// partway through provisioning, and the container it reads the manifest from
// exists a little before that. Errors are logged and retried rather than
// returned, because a failure here is indistinguishable from being early.
//
// "Installed" means the DaemonSet has its pods ready, not that the manifest
// applied. Those came apart in CI: an apply that reported success against an
// API server still settling left one cluster's kindnet never running, and
// because nothing looked past the apply the run reported the CNI installed and
// then waited out the whole 20 minute readiness budget on a Node stuck at
// "cni plugin not initialized". Whatever leaves the DaemonSet short now keeps
// this loop retrying, and says which DaemonSet and how many pods short in
// every line it logs.
func installCNIWhileProvisioning(t *testing.T) func(context.Context, []demo.Workspace) {
	t.Helper()
	return func(ctx context.Context, workspaces []demo.Workspace) {
		done := make(map[string]bool, len(workspaces))

		deadline := time.After(cniInstallTimeout)
		for {
			for _, ws := range workspaces {
				if done[ws.Path] {
					continue
				}
				if err := installCNI(ctx, ws); err != nil {
					logf(t, "CNI not installed in %s yet (%v); retrying", ws.Path, err)
					continue
				}
				logf(t, "CNI installed in %s", ws.Path)
				done[ws.Path] = true
			}
			if len(done) == len(workspaces) {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-deadline:
				logf(t, "gave up installing a CNI after %s; %d of %d workspaces done",
					cniInstallTimeout, len(done), len(workspaces))
				return
			case <-time.After(cniPollInterval):
			}
		}
	}
}

// installCNI applies the node image's own CNI manifest to one workspace's
// workload cluster.
func installCNI(ctx context.Context, ws demo.Workspace) error {
	cluster := &clusterv1.Cluster{}
	key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.ClusterName(0)}
	if err := ws.Client.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("reading the Cluster: %w", err)
	}

	manifest, err := readCNIManifest(ctx, cluster)
	if err != nil {
		return err
	}

	workload, err := workloadClient(ctx, ws)
	if err != nil {
		return err
	}
	daemonSets, err := applyAll(ctx, workload, manifest)
	if err != nil {
		return err
	}
	return waitForDaemonSets(ctx, workload, daemonSets)
}

// readCNIManifest reads the manifest out of one of the cluster's control plane
// containers and renders it.
func readCNIManifest(ctx context.Context, cluster *clusterv1.Cluster) (string, error) {
	runtime, err := container.NewDockerClient()
	if err != nil {
		return "", fmt.Errorf("connecting to the container runtime: %w", err)
	}
	ctx = container.RuntimeInto(ctx, runtime)

	name, err := controlPlaneContainer(ctx, runtime, cluster)
	if err != nil {
		return "", err
	}

	var out, errOut bytes.Buffer
	if err := runtime.ExecContainer(ctx, name,
		&container.ExecContainerInput{OutputBuffer: &out, ErrorBuffer: &errOut},
		"cat", cniManifestPath,
	); err != nil {
		return "", fmt.Errorf("reading %s from %s: %w (%s)", cniManifestPath, name, err, strings.TrimSpace(errOut.String()))
	}

	manifest := out.String()
	if !strings.Contains(manifest, cniTemplateMarker) {
		return manifest, nil
	}

	tmpl, err := template.New("cni").Parse(manifest)
	if err != nil {
		return "", fmt.Errorf("parsing the CNI manifest as a template: %w", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ PodSubnet string }{PodSubnet: podSubnet(cluster)}); err != nil {
		return "", fmt.Errorf("rendering the CNI manifest: %w", err)
	}
	return rendered.String(), nil
}

// podSubnet is what the manifest's one template variable wants: the pod CIDR
// the cluster was created with.
func podSubnet(cluster *clusterv1.Cluster) string {
	if blocks := cluster.Spec.ClusterNetwork.Pods.CIDRBlocks; len(blocks) > 0 {
		return strings.Join(blocks, ",")
	}
	return demo.DefaultPodCIDR
}

// controlPlaneContainer finds a running control plane container for the
// cluster, by the same labels the dev infrastructure provider sets on it.
func controlPlaneContainer(ctx context.Context, runtime container.Runtime, cluster *clusterv1.Cluster) (string, error) {
	filters := container.FilterBuilder{}
	filters.AddKeyNameValue("label", "io.x-k8s.kind.cluster", cluster.Name)
	filters.AddKeyNameValue("label", "io.x-k8s.kind.role", "control-plane")
	// The logical cluster, for the same reason every lookup in the provider
	// carries it: two workspaces hold a Cluster called demo-00, and without
	// this the first container of that name would answer for both.
	if lc := cluster.Annotations["kcp.io/cluster"]; lc != "" {
		filters.AddKeyNameValue("label", "kcp.io/logical-cluster", lc)
	}

	containers, err := runtime.ListContainers(ctx, filters)
	if err != nil {
		return "", fmt.Errorf("listing control plane containers: %w", err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("no control plane container for %s yet", cluster.Name)
	}
	return containers[0].Name, nil
}

// workloadRESTConfig builds a REST config for the workload cluster from the
// kubeconfig Secret its control plane provider wrote.
//
// Separate from workloadClient because reading a pod's log is not a
// client.Client operation - the diagnostics dump needs a clientset built from
// the same config.
func workloadRESTConfig(ctx context.Context, ws demo.Workspace) (*rest.Config, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: demo.Namespace, Name: demo.KubeconfigSecretName(demo.ClusterName(0))}
	if err := ws.Client.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("reading the workload kubeconfig Secret: %w", err)
	}
	raw, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("the kubeconfig Secret %s has no value key yet", key.Name)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing the workload kubeconfig: %w", err)
	}
	return cfg, nil
}

// workloadClient builds a client for the workload cluster from the kubeconfig
// Secret its control plane provider wrote.
func workloadClient(ctx context.Context, ws demo.Workspace) (client.Client, error) {
	cfg, err := workloadRESTConfig(ctx, ws)
	if err != nil {
		return nil, err
	}
	return client.New(cfg, client.Options{})
}

// applyAll applies every object in a multi-document manifest, and reports the
// DaemonSets among them so the caller can wait for what it just asked for.
//
// Server-side apply rather than Create, because this runs against an API
// server that has just come up and is still settling: a request can be
// answered, refused, or cut off mid-flight, and the loop above simply tries
// again. Create makes that retry a lie. It reports AlreadyExists for an object
// the previous attempt got as far as writing and moves on, so a manifest that
// applied in part is never completed and the next attempt calls it done. Apply
// converges instead: every attempt states the whole desired object, whatever
// the last one managed.
func applyAll(ctx context.Context, cl client.Client, manifest string) ([]client.ObjectKey, error) {
	var daemonSets []client.ObjectKey

	docs := yaml.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	for {
		doc, err := docs.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return daemonSets, nil
			}
			return nil, fmt.Errorf("reading the CNI manifest: %w", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{}
		if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(doc), 4096).Decode(obj); err != nil {
			return nil, fmt.Errorf("decoding a CNI object: %w", err)
		}
		if obj.GetKind() == "" {
			continue
		}
		if err := cl.Patch(ctx, obj, client.Apply,
			client.FieldOwner(cniFieldOwner), client.ForceOwnership); err != nil {
			return nil, fmt.Errorf("applying %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if obj.GetKind() == "DaemonSet" {
			daemonSets = append(daemonSets, client.ObjectKeyFromObject(obj))
		}
	}
}

// waitForDaemonSets reports whether every DaemonSet the manifest carries has a
// pod ready on every node it wants one on.
//
// This is the difference between the manifest having been accepted and the CNI
// being installed. A DaemonSet whose pods never run leaves kubelet reporting
// "cni plugin not initialized" for ever, and the object exists throughout, so
// nothing about the apply says so.
func waitForDaemonSets(ctx context.Context, cl client.Client, keys []client.ObjectKey) error {
	if len(keys) == 0 {
		return fmt.Errorf("the CNI manifest carries no DaemonSet, so there is nothing to run")
	}
	for _, key := range keys {
		ds := &appsv1.DaemonSet{}
		if err := cl.Get(ctx, key, ds); err != nil {
			return fmt.Errorf("reading DaemonSet %s: %w", key, err)
		}
		if ds.Status.DesiredNumberScheduled == 0 {
			return fmt.Errorf("DaemonSet %s is not scheduled on any node yet", key)
		}
		if ds.Status.NumberReady < ds.Status.DesiredNumberScheduled {
			return fmt.Errorf("DaemonSet %s has %d of %d pods ready",
				key, ds.Status.NumberReady, ds.Status.DesiredNumberScheduled)
		}
	}
	return nil
}
