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
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

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

// defragTimeout is how long a member is given to rewrite its file.
//
// etcdctl's own default is five seconds, which is not enough once a fleet is in
// the store: at 500 clusters a member timed out at 5.004s with "context
// deadline exceeded" and reclaimed nothing while its two peers finished. A run
// that cannot defragment starts its next rung from a file full of free pages,
// and a ceiling reached that way is about accumulated fragmentation rather than
// about how much state the store can hold — which is the confusion the
// defragmentation is there to remove.
//
// Five minutes is long rather than tuned. The cost of overshooting is a wait
// between rungs; the cost of undershooting is the measurement.
const defragTimeout = "--command-timeout=5m"

// StoreLocation is where a run's etcd is and how its metrics are reached.
//
// There are two stores in this exercise and they live in different places. The
// stock side's is kubeadm's static pods, in kube-system, labelled
// component=etcd. kcp's is a StatefulSet in the run's own namespace. Both the
// sampler and the defragmenter looked only for the first, so against kcp they
// would have found nothing and said so in a log line — leaving its etcd column
// exactly the thing that defragmenting before the baseline was added to fix.
type StoreLocation struct {
	Namespace string
	Labels    map[string]string
	// MetricsPort is where the member serves /metrics. The same on both sides,
	// or the two etcd columns are read differently and are not comparable.
	MetricsPort int
	// Container is what to exec into for a defragmentation. Named rather than
	// left out: an exec with no container runs in whichever one the pod lists
	// first, which on a store that grows a sidecar would silently stop being
	// etcd.
	Container string
	// Defrag is the command that defragments one member from inside it, which
	// differs because the two stores are reached differently — see
	// DefragCommand.
	Defrag []string
}

// DefragCommand is how to defragment this store's member from inside it.
//
// A defrag needs etcd's client API rather than its metrics port, so it needs
// whatever that API requires: kubeadm's serves it over TLS and the member holds
// the certificates to talk to itself, while the store this run deploys serves it
// in the clear inside the pod network and holds no certificates at all. The
// command was kubeadm's, hardcoded, which against the deployed store would have
// failed on every member — leaving the kcp side's store the only one never
// defragmented, and a difference between the two sides' figures that nothing in
// them would explain.
func (s StoreLocation) DefragCommand() []string {
	if len(s.Defrag) > 0 {
		return s.Defrag
	}
	return []string{"etcdctl", "defrag"}
}

// KubeadmStore is the stock side's: kubeadm's static pods, whose metrics port
// the ClusterClass patch opens.
func KubeadmStore() StoreLocation {
	return StoreLocation{
		Namespace:   "kube-system",
		Labels:      map[string]string{"component": "etcd"},
		MetricsPort: EtcdMetricsPort,
		Container:   "etcd",
		Defrag: []string{
			"etcdctl",
			"--endpoints=https://127.0.0.1:2379",
			"--cacert=/etc/kubernetes/pki/etcd/ca.crt",
			"--cert=/etc/kubernetes/pki/etcd/server.crt",
			"--key=/etc/kubernetes/pki/etcd/server.key",
			defragTimeout,
			"defrag",
		},
	}
}

// DeployedStore is the kcp side's: the StatefulSet a run deploys into its own
// namespace, labelled the way everything else that run creates is labelled.
func DeployedStore(namespace string) StoreLocation {
	return StoreLocation{
		Namespace:   namespace,
		Labels:      map[string]string{deployedscale.ComponentLabel: deployedscale.EtcdName},
		MetricsPort: EtcdMetricsPort,
		Container:   deployedscale.EtcdName,
		// In the clear on the loopback inside the member's own pod. It holds no
		// certificates: nothing outside the pod network reaches it, and giving
		// it TLS would be a second difference from the stock store on top of
		// the one being measured.
		Defrag: []string{
			"etcdctl",
			fmt.Sprintf("--endpoints=http://127.0.0.1:%d", deployedscale.EtcdClientPort),
			defragTimeout,
			"defrag",
		},
	}
}

// StorePods is the running members of a store, in name order.
//
// Ordered because a defragmentation walks them one at a time and a run that
// takes them in whatever order the API server listed them is a run whose
// "reclaimed" figures cannot be lined up against the previous one's.
func StorePods(ctx context.Context, cl client.Client, store StoreLocation) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := cl.List(ctx, &pods, client.InNamespace(store.Namespace),
		client.MatchingLabels(store.Labels)); err != nil {
		return nil, fmt.Errorf("listing etcd pods in %s: %w", store.Namespace, err)
	}
	var running []corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].DeletionTimestamp == nil && pods.Items[i].Status.Phase == corev1.PodRunning {
			running = append(running, pods.Items[i])
		}
	}
	if len(running) == 0 {
		return nil, fmt.Errorf("no running etcd pods matching %v in %s: nothing to read or defragment, "+
			"and a managed control plane is not a cluster this measurement can see the store of",
			store.Labels, store.Namespace)
	}
	sort.Slice(running, func(i, j int) bool { return running[i].Name < running[j].Name })
	return running, nil
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

// Measured reports whether both readings arrived. A backend file is never zero
// bytes, so a zero on either side is a reading that did not happen rather than
// a store that holds nothing.
func (d DefragResult) Measured() bool { return d.BeforeBytes > 0 && d.AfterBytes > 0 }

func sizeOrUnknown(n uint64) string {
	if n == 0 {
		return "unknown"
	}
	return humanBytes(n)
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
	return d.AllAt(ctx, cl, sampler, KubeadmStore())
}

// AllAt defragments the members of one named store. See StoreLocation.
func (d *Defragmenter) AllAt(ctx context.Context, cl client.Client, sampler *Sampler,
	store StoreLocation,
) ([]DefragResult, error) {
	members, err := StorePods(ctx, cl, store)
	if err != nil {
		return nil, err
	}

	var out []DefragResult
	for i := range members {
		pod := &members[i]
		result := DefragResult{Pod: pod.Name}
		if before, err := sampler.etcdMemberAt(ctx, store, pod.Name); err == nil {
			result.BeforeBytes = before.DBTotalBytes
		}
		if err := d.exec(ctx, store, pod.Name); err != nil {
			result.Err = err.Error()
			out = append(out, result)
			continue
		}
		if after, err := sampler.etcdMemberAt(ctx, store, pod.Name); err == nil {
			result.AfterBytes = after.DBTotalBytes
		}
		out = append(out, result)
	}
	return out, nil
}

func (d *Defragmenter) exec(ctx context.Context, store StoreLocation, pod string) error {
	req := d.client.Post().
		Resource("pods").Namespace(store.Namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: store.Container,
			Command:   store.DefragCommand(),
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
		if !r.Measured() {
			// "reclaimed 0 B (0 B to 1.3 GiB)" is what this used to print, and
			// it reads as a member that grew from nothing — a defect in the
			// measurement wearing the costume of a finding about the store.
			parts = append(parts, fmt.Sprintf("%s defragmented, size **not measured** "+
				"(before %s, after %s)", r.Pod, sizeOrUnknown(r.BeforeBytes), sizeOrUnknown(r.AfterBytes)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s reclaimed %s (%s to %s)",
			r.Pod, humanBytes(r.Reclaimed()), humanBytes(r.BeforeBytes), humanBytes(r.AfterBytes)))
	}
	return "defragmented between rungs: " + strings.Join(parts, "; ")
}
