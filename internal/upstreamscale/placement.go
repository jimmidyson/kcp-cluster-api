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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/jimmidyson/kcp-cluster-api/internal/deployedscale"
)

// Guarantee sets identical requests and limits on every container, which is
// what puts the pod in the Guaranteed QoS class, and tells the Go runtime about
// the memory limit it has just been given. It reports whether anything changed.
//
// # Why Guaranteed rather than generous
//
// A Burstable component that finished its work measured a node that happened to
// have room. Its numbers do not carry to a cluster where something else was
// running, and worse, they move between rungs of the same climb as the fleet
// around them grows — so a cost model fitted across those rungs is fitted partly
// to how contended the node was at each one.
//
// Guaranteed costs headroom and buys comparability. It also sharpens the
// failure the ladder is looking for: a component with a limit it cannot exceed
// either fits in what it was given or is killed, and both are answers, where a
// Burstable component that slows down under contention is neither.
//
// # Why GOMEMLIMIT comes with it
//
// A Go process cannot see its cgroup limit. kcp was OOM killed against 4 GiB
// while holding 1.63 GiB of live heap, because the collector had grown the heap
// to 3 GiB with nothing telling it a ceiling existed; the identical fleet
// reached its target once GOMEMLIMIT was set. Setting a limit without setting
// this makes an OOM kill mean "the collector was uninformed" rather than "this
// component needs more memory" — and the whole point of Guaranteed resources
// here is that the second reading is the true one.
func Guarantee(d *appsv1.Deployment, cpu, memory resource.Quantity) bool {
	changed := false
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]

		want := corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory}
		if !sameResources(c.Resources.Requests, want) || !sameResources(c.Resources.Limits, want) {
			c.Resources.Requests = want.DeepCopy()
			c.Resources.Limits = want.DeepCopy()
			changed = true
		}

		for _, env := range deployedscale.MemoryLimitEnv(memory) {
			if setEnv(&c.Env, env) {
				changed = true
			}
		}
	}
	return changed
}

// Dedicate places a deployment on the nodes a selector names and tolerates the
// taint that keeps everything else off them.
//
// The two go together. A dedicated node is only dedicated if it is tainted, and
// a tainted node takes nothing that does not tolerate it — so a run that
// selected the node without tolerating its taint would leave the component
// Pending beside an idle machine, which is a slow way to discover a typo.
func Dedicate(d *appsv1.Deployment, selector map[string]string, tolerations ...corev1.Toleration) {
	spec := &d.Spec.Template.Spec
	if len(selector) > 0 {
		if spec.NodeSelector == nil {
			spec.NodeSelector = map[string]string{}
		}
		for k, v := range selector {
			spec.NodeSelector[k] = v
		}
	}
	for _, want := range tolerations {
		if !tolerated(spec.Tolerations, want) {
			spec.Tolerations = append(spec.Tolerations, want)
		}
	}
}

// KeepOffControlPlane requires the deployment to schedule on a node that is
// not a control plane node, and reports whether anything changed.
//
// # The rung this cost
//
// A climb reached 1000 clusters and then lost the VIP at 1500: kube-vip on one
// control plane node could not renew its lease, because that node's etcd
// member had stalled for eighty seconds on a compaction its two peers finished
// in three and ten. The member was not on a slower disk. It was on the node
// where the DevCluster provider had been scheduled — a 24 GiB Guaranteed pod
// beside a kube-apiserver that was already 20 GiB resident, on a 32 GiB node.
// What was left for the page cache was not enough for etcd's backend file, so
// its compaction read the file from disk while its apply loop waited, and
// every write through that member timed out.
//
// It was there for a reason a scheduler cannot see. clusterctl's provider
// manifests tolerate the control plane taint, so that a provider can run on a
// single-node bootstrap cluster, and kubeadm gives kube-apiserver a CPU request
// and no memory request — so to the scheduler a control plane node holding a
// 20 GiB API server is the emptiest node in the cluster.
//
// # Why affinity rather than dropping the toleration
//
// Removing the toleration works only while the taint is there, and a cluster
// whose control plane nodes were untainted would put the provider straight
// back. A required node affinity away from the control plane label says what is
// meant, and holds either way.
//
// The requirement is ANDed into every existing term rather than added as a
// term of its own: terms are ORed, so a separate term would let the scheduler
// satisfy either one.
func KeepOffControlPlane(d *appsv1.Deployment) bool {
	want := corev1.NodeSelectorRequirement{Key: ControlPlaneNodeLabel, Operator: corev1.NodeSelectorOpDoesNotExist}
	spec := &d.Spec.Template.Spec
	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	required := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		required = &corev1.NodeSelector{}
		spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = required
	}
	if len(required.NodeSelectorTerms) == 0 {
		required.NodeSelectorTerms = []corev1.NodeSelectorTerm{{}}
	}

	changed := false
	for i := range required.NodeSelectorTerms {
		term := &required.NodeSelectorTerms[i]
		if !requires(term.MatchExpressions, want) {
			term.MatchExpressions = append(term.MatchExpressions, want)
			changed = true
		}
	}
	return changed
}

func requires(have []corev1.NodeSelectorRequirement, want corev1.NodeSelectorRequirement) bool {
	for _, r := range have {
		if r.Key == want.Key && r.Operator == want.Operator {
			return true
		}
	}
	return false
}

// QoSClass is what Kubernetes will assign this pod, worked out from the
// manifest so that a report can state it rather than a reader having to trust
// that setting resources worked.
//
// It is a condition the numbers only mean anything under, in the same class as
// which node a component ran on: a Burstable process that had a quiet node
// measured a quiet node.
func QoSClass(d *appsv1.Deployment) string {
	containers := d.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return "BestEffort"
	}
	anySet := false
	guaranteed := true
	for _, c := range containers {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			request, hasRequest := c.Resources.Requests[name]
			limit, hasLimit := c.Resources.Limits[name]
			if hasRequest || hasLimit {
				anySet = true
			}
			if !hasRequest || !hasLimit || request.Cmp(limit) != 0 {
				guaranteed = false
			}
		}
	}
	switch {
	case !anySet:
		return "BestEffort"
	case guaranteed:
		return "Guaranteed"
	default:
		return "Burstable"
	}
}

func sameResources(have, want corev1.ResourceList) bool {
	if len(have) != len(want) {
		return false
	}
	for name, w := range want {
		h, ok := have[name]
		if !ok || h.Cmp(w) != 0 {
			return false
		}
	}
	return true
}

func setEnv(env *[]corev1.EnvVar, want corev1.EnvVar) bool {
	for i, e := range *env {
		if e.Name == want.Name {
			if e.Value == want.Value {
				return false
			}
			(*env)[i] = want
			return true
		}
	}
	*env = append(*env, want)
	return true
}

func tolerated(have []corev1.Toleration, want corev1.Toleration) bool {
	for _, t := range have {
		if t.Key == want.Key && t.Operator == want.Operator &&
			t.Value == want.Value && t.Effect == want.Effect {
			return true
		}
	}
	return false
}
