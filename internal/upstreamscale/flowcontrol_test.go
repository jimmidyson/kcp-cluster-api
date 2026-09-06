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
	"strings"
	"testing"

	flowcontrolv1 "k8s.io/api/flowcontrol/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func level(name string) *flowcontrolv1.PriorityLevelConfiguration {
	return &flowcontrolv1.PriorityLevelConfiguration{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// TestLeaseRenewalsAreLiftedOutOfTheManagersOwnQueue.
//
// The failure: a manager lost its leader election to "etcdserver: request
// timed out" while saturating the API server with its own Machine writes. A
// lease renewal is a few hundred bytes; it failed because it was queued behind
// them.
func TestLeaseRenewalsAreLiftedOutOfTheManagersOwnQueue(t *testing.T) {
	fs := LeaderElectionFlowSchema([]string{"capi-system", "capd-system"})

	if got := fs.Spec.PriorityLevelConfiguration.Name; got != LeaderElectionLevel {
		t.Errorf("priority level = %q, want the API server's own %q", got, LeaderElectionLevel)
	}
	// Below service-accounts (9000), which would otherwise catch these and
	// route them back to the queue they are being lifted out of.
	if got := fs.Spec.MatchingPrecedence; got >= 9000 {
		t.Errorf("matchingPrecedence = %d, so the built-in service-accounts schema wins", got)
	}

	rule := fs.Spec.Rules[0].ResourceRules[0]
	if len(rule.Resources) != 1 || rule.Resources[0] != "leases" {
		t.Errorf("the rule covers %v, and anything beyond leases puts bulk writes in the level "+
			"meant to protect heartbeats from them", rule.Resources)
	}
	if rule.APIGroups[0] != "coordination.k8s.io" {
		t.Errorf("api group = %v", rule.APIGroups)
	}
}

// TestEveryManagerNamespaceIsCovered, by wildcard rather than by service
// account name: clusterctl names them per provider, and a schema that had to
// know the names would silently stop covering the next provider added.
func TestEveryManagerNamespaceIsCovered(t *testing.T) {
	want := ManagerNamespaces(Controllers())
	if len(want) < 4 {
		t.Fatalf("only %d manager namespaces: %v", len(want), want)
	}

	fs := LeaderElectionFlowSchema(want)
	covered := map[string]bool{}
	for _, s := range fs.Spec.Rules[0].Subjects {
		if s.Kind != flowcontrolv1.SubjectKindServiceAccount || s.ServiceAccount == nil {
			t.Fatalf("subject %+v is not a service account", s)
		}
		if s.ServiceAccount.Name != flowcontrolv1.NameAll {
			t.Errorf("%s is named rather than wildcarded, so a new provider's account is missed",
				s.ServiceAccount.Name)
		}
		covered[s.ServiceAccount.Namespace] = true
	}
	for _, ns := range want {
		if !covered[ns] {
			t.Errorf("%s has managers in it and no subject covering them", ns)
		}
	}
}

// TestAMissingPriorityLevelIsRefusedRatherThanInstalled.
//
// The API server accepts a FlowSchema naming a level that does not exist and
// then does nothing with it: matched requests fall through as if the schema
// were absent. A run that did not check would report protection it had not
// installed.
func TestAMissingPriorityLevelIsRefusedRatherThanInstalled(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()

	_, err := EnsureFlowSchema(t.Context(), cl, LeaderElectionFlowSchema([]string{"capi-system"}))
	if err == nil {
		t.Fatal("a schema pointing at a level that does not exist was installed as protection")
	}
	if !strings.Contains(err.Error(), "route requests nowhere") &&
		!strings.Contains(err.Error(), "nowhere") {
		t.Errorf("the failure does not say what would happen: %v", err)
	}
}

// TestApplyingTwiceChangesTheClusterOnce, so a prepare run that is repeated —
// which it is, between every pair of scale runs — does not rewrite the object
// and does not report a change that did not happen.
func TestApplyingTwiceChangesTheClusterOnce(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(level(LeaderElectionLevel)).Build()
	want := LeaderElectionFlowSchema(ManagerNamespaces(Controllers()))

	changed, err := EnsureFlowSchema(t.Context(), cl, want)
	if err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	changed, err = EnsureFlowSchema(t.Context(), cl, LeaderElectionFlowSchema(ManagerNamespaces(Controllers())))
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if changed {
		t.Error("an unchanged schema was rewritten, so a repeated prepare churns the object")
	}
}

// TestAnEditedSchemaIsPutBack. Someone widening the rule by hand would move
// the managers' bulk writes into the leader-election level, which protects
// nothing and starves the level for every other component using it.
func TestAnEditedSchemaIsPutBack(t *testing.T) {
	drifted := LeaderElectionFlowSchema([]string{"capi-system"})
	drifted.Spec.Rules[0].ResourceRules[0].Resources = []string{"*"}

	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(level(LeaderElectionLevel), drifted).Build()

	changed, err := EnsureFlowSchema(t.Context(), cl, LeaderElectionFlowSchema([]string{"capi-system"}))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !changed {
		t.Error("a schema widened to every resource was left in place")
	}
}
