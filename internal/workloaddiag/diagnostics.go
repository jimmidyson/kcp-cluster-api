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

package workloaddiag

import (
	"context"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultNamespace is where a cluster's own components run, and so where
	// the answer to "why is this Node not ready?" is.
	DefaultNamespace = "kube-system"

	// DefaultLogTailLines is how much of a container's log is kept. Enough to
	// carry a startup and the error that followed it, and little enough that a
	// crash-looping pod does not bury everything else in the report.
	DefaultLogTailLines = int64(200)
)

// Options says which cluster is being read and what is worth reading.
type Options struct {
	// Workspace and Cluster name the cluster in the report. They are what
	// makes one report distinguishable from another in a run where every
	// workspace holds a cluster of the same name.
	Workspace string
	Cluster   string

	// Namespace is where pods and DaemonSets are read from. Empty means
	// DefaultNamespace.
	Namespace string

	// LogFrom names the DaemonSets whose pods' logs are collected whether or
	// not they look unhealthy — the CNI, in this project's use. The caller
	// names them because only the caller knows which DaemonSet the cluster
	// cannot come up without, and its log is the evidence even when it reports
	// itself ready.
	LogFrom []string

	// LogTailLines is how many lines of each collected log are kept. Zero
	// means DefaultLogTailLines.
	LogTailLines int64
}

func (o Options) namespace() string {
	if o.Namespace == "" {
		return DefaultNamespace
	}
	return o.Namespace
}

func (o Options) tailLines() int64 {
	if o.LogTailLines == 0 {
		return DefaultLogTailLines
	}
	return o.LogTailLines
}

// Report is one workload cluster's account of itself.
type Report struct {
	Workspace string
	Cluster   string

	Nodes      []Node
	DaemonSets []DaemonSet
	Pods       []Pod

	// Probes are questions the API server cannot answer, asked of the node
	// itself and filled in by the caller — whether the CNI wrote its
	// configuration file at all, for the failure this package was written for.
	Probes []Probe

	// Notes are the parts that could not be read, kept rather than returned so
	// that the parts that could be read still are.
	Notes []string
}

// Condition is one node condition, flattened to strings because a report is
// read rather than acted on.
type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

// Node is one node of the workload cluster and every condition it carries.
type Node struct {
	Name       string
	Conditions []Condition
}

// Ready is the node's Ready condition, or the zero Condition when it has none
// — a node that has not reported yet, which is itself the finding.
func (n Node) Ready() Condition {
	for _, c := range n.Conditions {
		if c.Type == string(corev1.NodeReady) {
			return c
		}
	}
	return Condition{}
}

// Others is every condition except Ready, which the renderer prints compactly:
// they matter (a node under disk pressure fails differently) but not one row
// each.
func (n Node) Others() []Condition {
	others := make([]Condition, 0, len(n.Conditions))
	for _, c := range n.Conditions {
		if c.Type != string(corev1.NodeReady) {
			others = append(others, c)
		}
	}
	return others
}

// DaemonSet is what a DaemonSet says about itself, which is how this project's
// CNI install decides it is done.
type DaemonSet struct {
	Namespace string
	Name      string
	Desired   int32
	Current   int32
	Ready     int32
	Available int32
}

// Pod is one pod, its readiness, and the logs collected from it.
type Pod struct {
	Namespace string
	Name      string
	Node      string
	Phase     string

	// Ready is "ready/total" across the pod's containers, the count kubectl
	// shows, because "the pod is Running" and "the pod is working" are not the
	// same claim.
	Ready    string
	Restarts int32

	// Detail is why a container is not running, taken from the first one that
	// is not.
	Detail string

	Logs []Log
}

// Log is one container's log, or the reason there is not one.
type Log struct {
	Container string

	// Previous marks the log of the container that died rather than the one
	// running now. A pod that restarted after writing its network
	// configuration says nothing useful in its current log.
	Previous bool

	Content string
	Err     string
}

// Probe is a command run against the node itself and what it said.
type Probe struct {
	Description string
	Output      string
	Err         string
}

// Collect reads one workload cluster. Every read that fails is recorded on the
// report's Notes and the rest of the collection continues: this runs against a
// cluster that is already failing, and being unable to read one part of it is
// ordinary.
func Collect(ctx context.Context, cl client.Client, pods corev1client.PodsGetter, opts Options) Report {
	report := Report{Workspace: opts.Workspace, Cluster: opts.Cluster}

	nodes := &corev1.NodeList{}
	if err := cl.List(ctx, nodes); err != nil {
		report.note("listing Nodes: %v", err)
	}
	for _, n := range nodes.Items {
		report.Nodes = append(report.Nodes, summariseNode(n))
	}

	namespace := opts.namespace()

	daemonSets := &appsv1.DaemonSetList{}
	if err := cl.List(ctx, daemonSets, client.InNamespace(namespace)); err != nil {
		report.note("listing DaemonSets in %s: %v", namespace, err)
	}
	for _, ds := range daemonSets.Items {
		report.DaemonSets = append(report.DaemonSets, DaemonSet{
			Namespace: ds.Namespace,
			Name:      ds.Name,
			Desired:   ds.Status.DesiredNumberScheduled,
			Current:   ds.Status.CurrentNumberScheduled,
			Ready:     ds.Status.NumberReady,
			Available: ds.Status.NumberAvailable,
		})
	}

	podList := &corev1.PodList{}
	if err := cl.List(ctx, podList, client.InNamespace(namespace)); err != nil {
		report.note("listing Pods in %s: %v", namespace, err)
	}
	for _, p := range podList.Items {
		pod := summarisePod(p)
		if wantLogs(p, opts.LogFrom) {
			pod.Logs = collectLogs(ctx, pods, p, opts.tailLines())
		}
		report.Pods = append(report.Pods, pod)
	}

	// Sorted so that two runs of the same failure produce reports that can be
	// diffed against each other.
	slices.SortFunc(report.Nodes, func(a, b Node) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(report.DaemonSets, func(a, b DaemonSet) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(report.Pods, func(a, b Pod) int { return strings.Compare(a.Name, b.Name) })

	return report
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

func summariseNode(n corev1.Node) Node {
	node := Node{Name: n.Name}
	for _, c := range n.Status.Conditions {
		node.Conditions = append(node.Conditions, Condition{
			Type:    string(c.Type),
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	return node
}

func summarisePod(p corev1.Pod) Pod {
	pod := Pod{
		Namespace: p.Namespace,
		Name:      p.Name,
		Node:      p.Spec.NodeName,
		Phase:     string(p.Status.Phase),
	}

	ready := 0
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		pod.Restarts += cs.RestartCount
		if pod.Detail == "" {
			pod.Detail = containerDetail(cs)
		}
	}
	pod.Ready = fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers))

	// A pod with no container statuses at all has not been scheduled or has
	// not started; its conditions are the only account of why.
	if pod.Detail == "" && len(p.Status.ContainerStatuses) == 0 {
		for _, c := range p.Status.Conditions {
			if c.Status != corev1.ConditionTrue && c.Reason != "" {
				pod.Detail = conditionDetail(c.Reason, c.Message)
				break
			}
		}
	}
	return pod
}

// containerDetail is why a container is not running, or empty when it is.
func containerDetail(cs corev1.ContainerStatus) string {
	switch {
	case cs.State.Waiting != nil:
		return conditionDetail(cs.State.Waiting.Reason, cs.State.Waiting.Message)
	case cs.State.Terminated != nil:
		return conditionDetail(cs.State.Terminated.Reason,
			fmt.Sprintf("exit code %d, %s", cs.State.Terminated.ExitCode, cs.State.Terminated.Message))
	default:
		return ""
	}
}

func conditionDetail(reason, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return reason
	}
	return fmt.Sprintf("%s: %s", reason, message)
}

// wantLogs reports whether a pod's log is worth the request: it belongs to one
// of the DaemonSets the caller named, or it is not fully ready.
func wantLogs(p corev1.Pod, logFrom []string) bool {
	for _, owner := range p.OwnerReferences {
		if owner.Kind != "DaemonSet" {
			continue
		}
		for _, name := range logFrom {
			if owner.Name == name {
				return true
			}
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if !cs.Ready {
			return true
		}
	}
	// A pod with no container statuses has not started one, so there is no log
	// to read. What is wrong with it is on the pod itself, which summarisePod
	// already reports.
	return false
}

// collectLogs reads the tail of every container's log, and of the container
// that died before it whenever one did.
func collectLogs(ctx context.Context, pods corev1client.PodsGetter, p corev1.Pod, tail int64) []Log {
	restarts := map[string]int32{}
	for _, cs := range p.Status.ContainerStatuses {
		restarts[cs.Name] = cs.RestartCount
	}

	var logs []Log
	for _, c := range p.Spec.Containers {
		logs = append(logs, readLog(ctx, pods, p, c.Name, false, tail))
		if restarts[c.Name] > 0 {
			logs = append(logs, readLog(ctx, pods, p, c.Name, true, tail))
		}
	}
	return logs
}

func readLog(ctx context.Context, pods corev1client.PodsGetter, p corev1.Pod, container string, previous bool, tail int64) Log {
	log := Log{Container: container, Previous: previous}
	raw, err := pods.Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
		TailLines: ptr.To(tail),
	}).DoRaw(ctx)
	if err != nil {
		log.Err = fmt.Sprintf("reading the log: %v", err)
		return log
	}
	log.Content = strings.TrimRight(string(raw), "\n")
	return log
}
