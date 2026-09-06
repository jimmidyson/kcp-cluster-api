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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestAClassThatExistsIsNotYetAClassThatIsReconciled.
//
// The warning this comes from:
//
//	Cluster refers to ClusterClass capi-scale-0001/demo, but this ClusterClass
//	hasn't been successfully reconciled. Cluster topology has not been fully
//	validated.
//
// The blueprint is applied class-last so the class exists by the time a Cluster
// is created. Existing was never the property that mattered.
func TestAClassThatExistsIsNotYetAClassThatIsReconciled(t *testing.T) {
	fresh := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "capi-scale-0001", Generation: 1},
	}
	if ClassReconciled(fresh) {
		t.Error("a class whose status has not observed its generation was called reconciled")
	}

	caughtUp := fresh.DeepCopy()
	caughtUp.Status.ObservedGeneration = 1
	if !ClassReconciled(caughtUp) {
		t.Error("a class the controller has caught up with was called unreconciled")
	}
}

// TestVariablesAreWaitedForWhenTheClassReportsThem, and not required when it
// does not: naming a condition is how a wait becomes a hang against a Cluster
// API whose conditions were renamed underneath it, and this repository tracks
// two lines of Cluster API on purpose.
func TestVariablesAreWaitedForWhenTheClassReportsThem(t *testing.T) {
	class := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Generation: 1},
		Status: clusterv1.ClusterClassStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{{
				Type: clusterv1.ClusterClassVariablesReadyCondition, Status: metav1.ConditionFalse,
				Reason: "NotYet", LastTransitionTime: metav1.Now(),
			}},
		},
	}
	if ClassReconciled(class) {
		t.Error("a class reporting its variables are not ready was called reconciled")
	}

	class.Status.Conditions[0].Status = metav1.ConditionTrue
	if !ClassReconciled(class) {
		t.Error("a class reporting ready variables was still called unreconciled")
	}

	// A class carrying some other condition and not this one is judged on its
	// generation alone rather than held forever.
	unknown := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Generation: 1},
		Status: clusterv1.ClusterClassStatus{ObservedGeneration: 1,
			Conditions: []metav1.Condition{{Type: "SomethingElse", Status: metav1.ConditionFalse,
				Reason: "Whatever", LastTransitionTime: metav1.Now()}}},
	}
	if !ClassReconciled(unknown) {
		t.Error("a class whose conditions this code does not know was waited on forever")
	}
}

// TestTheClassIsFoundByTypeRatherThanByName, so a blueprint that renames its
// class does not go quietly back to waiting for nothing.
func TestTheClassIsFoundByTypeRatherThanByName(t *testing.T) {
	blueprint := Blueprint("capi-scale-0001")
	class, ok := ClassOf(blueprint)
	if !ok {
		t.Fatal("the blueprint has no ClusterClass in it")
	}
	if class.Namespace != "capi-scale-0001" {
		t.Errorf("the class is in %q rather than the namespace asked for", class.Namespace)
	}
	if _, ok := ClassOf(nil); ok {
		t.Error("an empty blueprint produced a class")
	}
}

// TestAClassThatNeverReconcilesStopsTheRungAndSaysWhy, rather than creating
// Clusters that will be admitted without validation.
func TestAClassThatNeverReconcilesStopsTheRungAndSaysWhy(t *testing.T) {
	stuck := &clusterv1.ClusterClass{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "capi-scale-0001", Generation: 2},
		Status:     clusterv1.ClusterClassStatus{ObservedGeneration: 1},
	}
	sch, err := Scheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(sch).WithObjects(stuck).Build()

	ctx, cancel := contextWithImmediateDeadline(t)
	defer cancel()

	if err := WaitForBlueprint(ctx, cl, "capi-scale-0001", "demo"); err == nil {
		t.Fatal("a class that never reconciled let its Clusters be created")
	} else if !strings.Contains(err.Error(), "capi-scale-0001") {
		t.Errorf("the failure does not name the tenant: %v", err)
	}
}

// contextWithImmediateDeadline cancels as soon as the wait first sleeps, so
// the test exercises the give-up path without waiting out blueprintReady.
func contextWithImmediateDeadline(t *testing.T) (ctx context.Context, cancel context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 50*time.Millisecond)
}
