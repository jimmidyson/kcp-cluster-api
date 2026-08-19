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

package demo

import (
	"fmt"
	"io"
	"text/tabwriter"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	infrav1 "sigs.k8s.io/cluster-api/test/infrastructure/docker/api/v1beta2"
)

// ClusterStatus is what the demo reports about one cluster in one workspace.
type ClusterStatus struct {
	// Workspace is the human-readable workspace path, e.g. root:capi-demo-1.
	Workspace string

	// LogicalCluster is the internal cluster name the manager engages the
	// workspace by. Reported because it is what appears in the manager's
	// logs, and matching a log line to a workspace is otherwise guesswork.
	LogicalCluster string

	// Cluster is the Cluster object's name, repeated in every workspace.
	Cluster string

	// Provisioned reports Cluster.status.initialization.infrastructureProvisioned:
	// the infrastructure provider has done its work for this cluster.
	Provisioned bool

	// Ready reports the Cluster's Available condition, which is the demo's
	// done-condition: Cluster API sets it once the remote connection probe,
	// the infrastructure, the control plane and the workers are all good, so
	// it is the single answer to "is this a cluster somebody can use?".
	Ready bool

	// Detail says what the cluster is waiting on, taken from whichever
	// condition is the one still outstanding.
	Detail string
}

// Summarise reads one cluster's demo status out of the two objects it spans.
//
// A missing DevCluster is reported rather than treated as an error: the
// Cluster reconciler resolves the reference asynchronously, so "not there
// yet" is an ordinary state during a demo run.
func Summarise(workspace, logicalCluster string, cluster *clusterv1.Cluster, devCluster *infrav1.DevCluster) ClusterStatus {
	status := ClusterStatus{
		Workspace:      workspace,
		LogicalCluster: logicalCluster,
		Cluster:        cluster.Name,
	}

	status.Provisioned = ptr.Deref(cluster.Status.Initialization.InfrastructureProvisioned, false)

	if available := meta.FindStatusCondition(cluster.Status.Conditions, clusterv1.ClusterAvailableCondition); available != nil {
		if available.Status == metav1.ConditionTrue {
			status.Ready = true
			status.Detail = "cluster ready"
			return status
		}
		// The Available condition summarises the others, so its message
		// already names whichever of them is outstanding. Reporting it rather
		// than picking a condition ourselves is what keeps this honest as the
		// cluster moves through its states.
		status.Detail = conditionDetail(available)
		return status
	}

	if status.Provisioned {
		status.Detail = "infrastructure provisioned"
		return status
	}

	if cond := meta.FindStatusCondition(cluster.Status.Conditions, string(clusterv1.ClusterInfrastructureReadyCondition)); cond != nil {
		status.Detail = conditionDetail(cond)
		return status
	}

	if devCluster == nil {
		status.Detail = "waiting for the DevCluster to be created"
		return status
	}
	status.Detail = "waiting for the Cluster reconciler"
	return status
}

// AllProvisioned reports whether every cluster in the snapshot is provisioned,
// which is the demo's done-condition.
//
// An empty snapshot is not done: a run that created nothing has provisioned
// nothing, and reporting success for it would be the one failure mode a demo
// must not have.
func AllProvisioned(statuses []ClusterStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !s.Provisioned {
			return false
		}
	}
	return true
}

// AllClustersReady reports whether every cluster in the snapshot is Available.
//
// An empty snapshot is not ready, for the same reason it is not provisioned: a
// run that created nothing has made nothing ready.
func AllClustersReady(statuses []ClusterStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if !s.Ready {
			return false
		}
	}
	return true
}

// RenderTable writes the snapshot as an aligned table.
func RenderTable(w io.Writer, statuses []ClusterStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "WORKSPACE\tLOGICAL CLUSTER\tCLUSTER\tPROVISIONED\tREADY\tDETAIL"); err != nil {
		return err
	}
	for _, s := range statuses {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Workspace, s.LogicalCluster, s.Cluster, yesNo(s.Provisioned), yesNo(s.Ready), s.Detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// MachineStatus is what the demo reports about one control plane machine.
type MachineStatus struct {
	Workspace      string
	LogicalCluster string
	Machine        string

	// Bootstrapped reports that the bootstrap provider produced this
	// machine's data secret - the thing that has to happen before any
	// infrastructure provider can turn a Machine into a node.
	Bootstrapped bool

	// DataSecret names the Secret holding that data, in this workspace.
	DataSecret string

	// Ready reports the Machine's Ready condition: its bootstrap config and
	// infrastructure are ready and its Node is healthy. It is what the demo
	// waits for, because a control plane whose machines are not ready is not a
	// cluster anybody can use.
	Ready bool

	// Phase is the Machine's own phase, reported alongside: it names where in
	// provisioning a machine that is not ready has got to.
	Phase string

	// Detail says what the machine is waiting on when it has no data secret.
	Detail string
}

// ControlPlaneStatus is what the demo reports about one cluster's control
// plane.
type ControlPlaneStatus struct {
	Workspace      string
	LogicalCluster string
	ControlPlane   string

	// Initialized reports status.initialization.controlPlaneInitialized: the
	// control plane can accept requests. It is the demo's done-condition
	// because it is the point at which there is a cluster to talk to.
	Initialized bool

	// Ready reports that every replica the control plane was asked for is
	// ready, which is the demo's done-condition for it: initialized says a
	// machine came up, ready says the control plane it forms is usable.
	Ready bool

	// ReadyReplicas and DesiredReplicas are the counts behind Ready, reported
	// alongside: a control plane is initialized by its first machine and
	// complete some time later, and the counts are what show that gap
	// closing.
	ReadyReplicas   int32
	DesiredReplicas int32

	Detail string
}

// SummariseControlPlane reads one control plane's demo status.
func SummariseControlPlane(workspace, logicalCluster string, kcp *controlplanev1.KubeadmControlPlane) ControlPlaneStatus {
	status := ControlPlaneStatus{
		Workspace:       workspace,
		LogicalCluster:  logicalCluster,
		ControlPlane:    kcp.Name,
		ReadyReplicas:   ptr.Deref(kcp.Status.ReadyReplicas, 0),
		DesiredReplicas: ptr.Deref(kcp.Spec.Replicas, 0),
	}
	status.Initialized = ptr.Deref(kcp.Status.Initialization.ControlPlaneInitialized, false)
	// Desired above zero is part of it: a control plane asked for no replicas
	// has none outstanding, and calling that ready would report success for a
	// cluster with no control plane at all.
	status.Ready = status.DesiredReplicas > 0 && status.ReadyReplicas >= status.DesiredReplicas

	if status.Ready {
		status.Detail = "control plane ready"
		return status
	}

	if cond := meta.FindStatusCondition(kcp.Status.Conditions, controlplanev1.KubeadmControlPlaneAvailableCondition); cond != nil && cond.Reason != "" {
		status.Detail = conditionDetail(cond)
		return status
	}
	if status.Initialized {
		status.Detail = "control plane initialized"
		return status
	}
	status.Detail = "waiting for the control plane provider"
	return status
}

// AllInitialized reports whether every control plane can accept requests. An
// empty snapshot is vacuously true: a run that asked for no control plane is
// not waiting for one.
func AllInitialized(statuses []ControlPlaneStatus) bool {
	for _, s := range statuses {
		if !s.Initialized {
			return false
		}
	}
	return true
}

// AllControlPlanesReady reports whether every control plane has every replica
// it was asked for. Empty is vacuously true, as for AllInitialized.
func AllControlPlanesReady(statuses []ControlPlaneStatus) bool {
	for _, s := range statuses {
		if !s.Ready {
			return false
		}
	}
	return true
}

// RenderControlPlaneTable writes the control plane snapshot as an aligned
// table, and nothing at all when there are none.
func RenderControlPlaneTable(w io.Writer, statuses []ControlPlaneStatus) error {
	if len(statuses) == 0 {
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "WORKSPACE\tCONTROL PLANE\tINITIALIZED\tREADY\tDETAIL"); err != nil {
		return err
	}
	for _, s := range statuses {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%s\n",
			s.Workspace, s.ControlPlane, yesNo(s.Initialized), s.ReadyReplicas, s.DesiredReplicas, s.Detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// SummariseMachine reads one machine's demo status.
func SummariseMachine(workspace, logicalCluster string, machine *clusterv1.Machine, config *bootstrapv1.KubeadmConfig) MachineStatus {
	status := MachineStatus{
		Workspace:      workspace,
		LogicalCluster: logicalCluster,
		Machine:        machine.Name,
		Phase:          machine.Status.Phase,
		Ready:          meta.IsStatusConditionTrue(machine.Status.Conditions, clusterv1.MachineReadyCondition),
	}

	if name := machine.Spec.Bootstrap.DataSecretName; name != nil && *name != "" {
		status.Bootstrapped = true
		status.DataSecret = *name
		status.Detail = "bootstrap data ready"
		if status.Ready {
			status.Detail = "machine ready"
		} else if cond := meta.FindStatusCondition(machine.Status.Conditions, clusterv1.MachineReadyCondition); cond != nil {
			// Past bootstrap the interesting question is what readiness is
			// waiting on, and the Ready condition summarises the rest.
			status.Detail = conditionDetail(cond)
		}
		return status
	}

	if config == nil {
		status.Detail = "waiting for the KubeadmConfig to be created"
		return status
	}
	if name := config.Status.DataSecretName; name != "" {
		// The config has produced the secret but the Machine has not picked it
		// up yet: a real state worth naming, because it separates a bootstrap
		// provider that is not working from a Machine controller that has not
		// caught up.
		status.Bootstrapped = true
		status.DataSecret = name
		status.Detail = "bootstrap data ready, not yet on the Machine"
		return status
	}

	if cond := meta.FindStatusCondition(config.Status.Conditions, bootstrapv1.KubeadmConfigDataSecretAvailableCondition); cond != nil {
		status.Detail = cond.Reason
		if cond.Message != "" {
			status.Detail = fmt.Sprintf("%s: %s", cond.Reason, cond.Message)
		}
		return status
	}
	status.Detail = "waiting for the bootstrap provider"
	return status
}

// AllBootstrapped reports whether every machine has its bootstrap data. An
// empty snapshot is vacuously true here, unlike AllProvisioned: a run that
// asked for no machines is not waiting for any.
func AllBootstrapped(statuses []MachineStatus) bool {
	for _, s := range statuses {
		if !s.Bootstrapped {
			return false
		}
	}
	return true
}

// AllMachinesReady reports whether every machine is Ready. Empty is vacuously
// true, as for AllBootstrapped.
func AllMachinesReady(statuses []MachineStatus) bool {
	for _, s := range statuses {
		if !s.Ready {
			return false
		}
	}
	return true
}

// RenderMachineTable writes the machine snapshot as an aligned table, and
// nothing at all when there are no machines.
func RenderMachineTable(w io.Writer, statuses []MachineStatus) error {
	if len(statuses) == 0 {
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "WORKSPACE\tMACHINE\tBOOTSTRAPPED\tREADY\tDATA SECRET\tPHASE\tDETAIL"); err != nil {
		return err
	}
	for _, s := range statuses {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Workspace, s.Machine, yesNo(s.Bootstrapped), yesNo(s.Ready),
			orDash(s.DataSecret), orDash(s.Phase), s.Detail); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// conditionDetail renders a condition as the reason, and the message after it
// when there is one - which is where Cluster API says what is outstanding.
func conditionDetail(cond *metav1.Condition) string {
	if cond.Message == "" {
		return cond.Reason
	}
	return fmt.Sprintf("%s: %s", cond.Reason, cond.Message)
}
