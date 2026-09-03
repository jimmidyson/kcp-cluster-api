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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// etcdctlDefrag is run inside each etcd static pod, against that member only.
//
// The pod's own certificates, at the paths kubeadm puts them: a defrag needs
// the client API rather than the metrics port, and the member already holds
// what it takes to talk to itself.
var etcdctlDefrag = []string{
	"etcdctl",
	"--endpoints=https://127.0.0.1:2379",
	"--cacert=/etc/kubernetes/pki/etcd/ca.crt",
	"--cert=/etc/kubernetes/pki/etcd/server.crt",
	"--key=/etc/kubernetes/pki/etcd/server.key",
	"defrag",
}

// Defragmenter reclaims the free space inside etcd's backend file.
//
// # When to run this, and when not to
//
// **Between rungs, never inside one, and never during the soak.**
//
// Between rungs, because the quota counts the backend file rather than the live
// data in it: a converging Cluster API fleet churns, compaction frees pages
// without returning them, and a rung that reaches the quota with most of the
// file free records a ceiling about accumulated free pages rather than about
// how much state the store can hold. Each rung should start from a store whose
// size means what it says.
//
// Not inside a rung, because a defrag is a stop-the-world rewrite on the member
// it runs against: writes to it block, latencies spike, and a leader change is
// possible. All three would land in the middle of a measurement and none of them
// would be about the fleet.
//
// Not during the soak either, and this is the sharper one: the soak asks whether
// a held fleet drifts when nothing is being asked of the cluster. A defrag is
// something being asked of the cluster.
type Defragmenter struct {
	cfg    *rest.Config
	client rest.Interface
}

// NewDefragmenter builds one from the cluster's config.
func NewDefragmenter(cfg *rest.Config, restClient rest.Interface) *Defragmenter {
	return &Defragmenter{cfg: cfg, client: restClient}
}

// DefragResult is what one member's defragmentation did.
type DefragResult struct {
	Pod         string `json:"pod"`
	BeforeBytes uint64 `json:"beforeBytes"`
	AfterBytes  uint64 `json:"afterBytes"`
	Err         string `json:"error,omitempty"`
}

// Reclaimed is how much the file shrank.
func (d DefragResult) Reclaimed() uint64 {
	if d.AfterBytes > d.BeforeBytes {
		return 0
	}
	return d.BeforeBytes - d.AfterBytes
}

// All defragments every etcd member, one at a time.
//
// One at a time because each blocks writes to the member it runs on, and three
// at once on a three-member cluster is an outage rather than a maintenance
// window.
//
// Best effort per member, and reported: a member that will not defragment is
// worth knowing about — it is the one whose file will reach the quota first —
// but it is not a reason to abandon a climb that is otherwise going well.
func (d *Defragmenter) All(ctx context.Context, cl client.Client, sampler *Sampler) ([]DefragResult, error) {
	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace("kube-system"),
		client.MatchingLabels{"component": "etcd"}); err != nil {
		return nil, fmt.Errorf("listing etcd pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no etcd pods in kube-system: nothing to defragment, and a managed " +
			"control plane is not a cluster this measurement can see the store of")
	}

	var out []DefragResult
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		result := DefragResult{Pod: pod.Name}
		if before, err := sampler.etcdOf(ctx, pod.Name); err == nil {
			result.BeforeBytes = before.DBTotalBytes
		}
		if err := d.exec(ctx, pod.Namespace, pod.Name); err != nil {
			result.Err = err.Error()
			out = append(out, result)
			continue
		}
		if after, err := sampler.etcdOf(ctx, pod.Name); err == nil {
			result.AfterBytes = after.DBTotalBytes
		}
		out = append(out, result)
	}
	return out, nil
}

func (d *Defragmenter) exec(ctx context.Context, namespace, pod string) error {
	req := d.client.Post().
		Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "etcd",
			Command:   etcdctlDefrag,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(d.cfg, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("building an executor for %s: %w", pod, err)
	}
	var stdout, stderr strings.Builder
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout, Stderr: &stderr,
	}); err != nil {
		return fmt.Errorf("defragmenting %s: %w: %s", pod, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// DescribeDefrag is the line a run records between two rungs.
//
// Recorded rather than silent, because a defrag is a perturbation: a reader
// comparing two rungs needs to know one happened between them.
func DescribeDefrag(results []DefragResult) string {
	if len(results) == 0 {
		return "no etcd member was defragmented"
	}
	var parts []string
	for _, r := range results {
		if r.Err != "" {
			parts = append(parts, fmt.Sprintf("%s failed (%s)", r.Pod, r.Err))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s reclaimed %s (%s to %s)",
			r.Pod, humanBytes(r.Reclaimed()), humanBytes(r.BeforeBytes), humanBytes(r.AfterBytes)))
	}
	return "defragmented between rungs: " + strings.Join(parts, "; ")
}
