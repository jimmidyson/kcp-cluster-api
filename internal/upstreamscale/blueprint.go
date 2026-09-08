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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// blueprintReady is how long a tenant's ClusterClass is given to reconcile
// before its Clusters are created. Generous: the cost of overshooting is a
// wait, and the cost of undershooting is a fleet whose early Clusters were
// admitted differently from its late ones.
const blueprintReady = 2 * time.Minute

// ClassOf finds the ClusterClass among a blueprint's objects.
//
// By type rather than by name. The class is demo's and its name is demo's to
// choose, and a run that hardcoded it would go quietly back to not waiting for
// anything the day that changed.
func ClassOf(objects []client.Object) (*clusterv1.ClusterClass, bool) {
	for _, o := range objects {
		if class, ok := o.(*clusterv1.ClusterClass); ok {
			return class, true
		}
	}
	return nil, false
}

// WaitForBlueprint blocks until a namespace's ClusterClass has been reconciled.
//
// # The line this is for
//
//	Cluster refers to ClusterClass capi-scale-0001/demo, but this ClusterClass
//	hasn't been successfully reconciled. Cluster topology has not been fully
//	validated. Please take a look at the ClusterClass status
//
// The blueprint is applied with the class last, so by the time a Cluster is
// created the class exists — but existing is not the same as reconciled. The
// ClusterClass controller still has to resolve the template references and
// reconcile the variables before a Cluster's topology can be validated against
// it, and at three hundred namespaces that controller has a backlog. So the
// first Clusters in each namespace are admitted without full validation.
//
// # Why a warning is worth waiting out
//
// Nothing breaks. The Cluster is admitted and reconciled again once the class
// is ready. What it costs is uniformity: those Clusters did less admission work
// than their neighbours, and admission work is the thing this run is now
// measuring — a KubeadmControlPlane update costs about 139ms across two
// webhooks, and a fleet where an unknown early fraction skipped some of that is
// a fleet with a fitted cost that is quietly low.
//
// Per namespace, so a rung is not serialised: each tenant waits for its own
// class while the others get on with theirs.
func WaitForBlueprint(ctx context.Context, cl client.Client, namespace, name string) error {
	deadline := time.Now().Add(blueprintReady)
	key := client.ObjectKey{Namespace: namespace, Name: name}

	for {
		var class clusterv1.ClusterClass
		err := cl.Get(ctx, key, &class)
		switch {
		case err != nil && !Transient(err):
			return fmt.Errorf("reading ClusterClass %s: %w", key, err)
		case err == nil && ClassReconciled(&class):
			return nil
		}

		// With the class's own status on the line, either way out. The
		// blueprint namespace is torn down at the end of the run, so the
		// status is gone by the time anyone reads the report, and a line that
		// said only "was not reconciled" left a run to be diagnosed against
		// an empty cluster. See DescribeClassStatus.
		if time.Now().After(deadline) {
			return fmt.Errorf("ClusterClass %s was not reconciled within %s, so its Clusters would "+
				"be admitted without their topologies being validated against it — %s",
				key, blueprintReady, DescribeClassStatus(&class, err))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for ClusterClass %s: %w — %s", key, ctx.Err(), DescribeClassStatus(&class, err))
		case <-time.After(time.Second):
		}
	}
}

// DescribeClassStatus says why a ClusterClass does not count as reconciled, from
// the last read of it and the error that read returned.
//
// The two shapes point at different things. A status that has never observed
// the class's generation means the ClusterClass controller has not touched it
// at all, which is a manager that is not running, not leading, or not started
// with the topology feature on. A status that has observed it and carries a
// False condition means the controller looked and refused, and the reason is
// in the condition.
func DescribeClassStatus(class *clusterv1.ClusterClass, readErr error) string {
	if readErr != nil {
		return "the last read of it failed: " + readErr.Error()
	}
	if class.Status.ObservedGeneration < class.Generation {
		return fmt.Sprintf("its status has observed generation %d of %d, so the ClusterClass controller "+
			"has not reconciled it since it was written: check that capi-controller-manager is running, "+
			"holds its leader election lease, and was started with the ClusterTopology feature gate on",
			class.Status.ObservedGeneration, class.Generation)
	}
	var unmet []string
	for _, c := range class.Status.Conditions {
		if c.Status == metav1.ConditionTrue {
			continue
		}
		line := fmt.Sprintf("%s is %s (%s", c.Type, c.Status, c.Reason)
		if c.Message != "" {
			line += ": " + c.Message
		}
		unmet = append(unmet, line+")")
	}
	if len(unmet) == 0 {
		return "the controller has observed it and reports nothing amiss, which this code did not expect"
	}
	return "the controller has observed it and reports " + strings.Join(unmet, "; ")
}

// ClassReconciled reports whether the controller has caught up with this
// ClusterClass.
//
// Generation first, because it is the one signal every version of the API
// carries: a status that has not observed the current generation describes the
// object as it was before it was written.
//
// Then VariablesReady, but only when the class actually carries it. Requiring
// a condition by name is how a wait becomes a hang against a Cluster API whose
// conditions have been renamed underneath it — and this repository tracks two
// lines of Cluster API on purpose.
func ClassReconciled(class *clusterv1.ClusterClass) bool {
	if class.Status.ObservedGeneration < class.Generation {
		return false
	}
	for _, c := range class.Status.Conditions {
		if c.Type == clusterv1.ClusterClassVariablesReadyCondition {
			return c.Status == metav1.ConditionTrue
		}
	}
	return true
}
