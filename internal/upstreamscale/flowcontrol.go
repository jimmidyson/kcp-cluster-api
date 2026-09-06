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

	flowcontrolv1 "k8s.io/api/flowcontrol/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// FlowSchemaName is the schema this run installs.
	FlowSchemaName = "capi-leader-election"

	// LeaderElectionLevel is one of the API server's own suggested priority
	// levels, not one this run invents. It exists for exactly this problem:
	// keeping a controller's lease renewals out of the same queue as its bulk
	// traffic, so that a controller cannot starve its own heartbeat.
	LeaderElectionLevel = "leader-election"

	// leaderElectionPrecedence orders this schema against the built-in ones.
	//
	// Lower wins. It has to be below `service-accounts` (9000), which would
	// otherwise catch these requests and route them to workload-low along with
	// every Machine write the same manager is making — the arrangement that
	// killed a manager mid-run. 150 sits with the built-in leader-election
	// schemas rather than ahead of them: this is the same kind of rule, for
	// service accounts the built-ins do not name.
	leaderElectionPrecedence = 150
)

// LeaderElectionFlowSchema routes Cluster API's lease renewals to the API
// server's leader-election priority level.
//
// # The failure this prevents
//
// A stock climb lost a manager like this:
//
//	leaderelection.go:454 "Failed to update lease optimistically, falling back
//	to slow path" err="etcdserver: request timed out"
//	main.go:415 "Problem running manager" err="leader election lost"
//
// A lease renewal is a few hundred bytes every few seconds. It failed because
// it was queued behind the same manager's Machine and DevMachine writes, which
// at a fleet of thousands is a queue that does not drain. Priority and fairness
// exists to stop precisely that, and the API server ships a priority level for
// it — but the built-in schemas that use it name specific system components and
// kube-system service accounts, and Cluster API's managers run in capi-system,
// capd-system and their siblings.
//
// # Why this is not tuning the result
//
// Isolating a heartbeat does not let the cluster hold more clusters. It stops
// a manager exiting for a reason unrelated to capacity, which was aborting
// runs before they reached a ceiling — the measurement could not complete, so
// there was no number to inflate. That is different in kind from giving the
// managers' bulk traffic a level of its own, which would make Cluster API hold
// more by making it slower, and is a separate experiment rather than a default.
//
// Both sides of a comparison get this, or the side that keeps its leaders is
// being compared against a side that loses them.
func LeaderElectionFlowSchema(namespaces []string) *flowcontrolv1.FlowSchema {
	sorted := append([]string(nil), namespaces...)
	sort.Strings(sorted)

	subjects := make([]flowcontrolv1.Subject, 0, len(sorted))
	for _, ns := range sorted {
		subjects = append(subjects, flowcontrolv1.Subject{
			Kind: flowcontrolv1.SubjectKindServiceAccount,
			ServiceAccount: &flowcontrolv1.ServiceAccountSubject{
				// Every service account in the namespace, because clusterctl
				// names them per provider and a run that had to know the names
				// would break on the next provider added.
				Name:      flowcontrolv1.NameAll,
				Namespace: ns,
			},
		})
	}

	return &flowcontrolv1.FlowSchema{
		ObjectMeta: metav1.ObjectMeta{Name: FlowSchemaName},
		Spec: flowcontrolv1.FlowSchemaSpec{
			PriorityLevelConfiguration: flowcontrolv1.PriorityLevelConfigurationReference{
				Name: LeaderElectionLevel,
			},
			MatchingPrecedence: leaderElectionPrecedence,
			// By user, so that one manager renewing furiously cannot crowd out
			// another's renewals within the level.
			DistinguisherMethod: &flowcontrolv1.FlowDistinguisherMethod{
				Type: flowcontrolv1.FlowDistinguisherMethodByUserType,
			},
			Rules: []flowcontrolv1.PolicyRulesWithSubjects{{
				Subjects: subjects,
				ResourceRules: []flowcontrolv1.ResourcePolicyRule{{
					// Leases only. Widening this to the managers' other traffic
					// would put Machine writes in the level meant to protect
					// heartbeats from them, which is the opposite of the point.
					Verbs:        []string{"get", "create", "update"},
					APIGroups:    []string{"coordination.k8s.io"},
					Resources:    []string{"leases"},
					Namespaces:   []string{flowcontrolv1.NamespaceEvery},
					ClusterScope: false,
				}},
			}},
		},
	}
}

// ManagerNamespaces is where the controllers this run measures live, deduped.
func ManagerNamespaces(controllers []Controller) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range controllers {
		if c.Namespace == "" || seen[c.Namespace] {
			continue
		}
		seen[c.Namespace] = true
		out = append(out, c.Namespace)
	}
	sort.Strings(out)
	return out
}

// EnsureFlowSchema applies the schema and says whether the cluster changed.
//
// The priority level is checked first rather than assumed. A FlowSchema naming
// a level that does not exist is accepted by the API server and then does
// nothing — the requests it matches fall through as if it were not there — so
// a run that did not look would report protection it had not installed.
func EnsureFlowSchema(ctx context.Context, cl client.Client, want *flowcontrolv1.FlowSchema) (bool, error) {
	level := want.Spec.PriorityLevelConfiguration.Name
	var plc flowcontrolv1.PriorityLevelConfiguration
	if err := cl.Get(ctx, client.ObjectKey{Name: level}, &plc); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("the %q priority level does not exist on this cluster, so a "+
				"FlowSchema naming it would match requests and route them nowhere: the API server's "+
				"suggested flow-control configuration has been removed or replaced", level)
		}
		return false, fmt.Errorf("reading the %q priority level: %w", level, err)
	}

	var have flowcontrolv1.FlowSchema
	err := cl.Get(ctx, client.ObjectKey{Name: want.Name}, &have)
	switch {
	case apierrors.IsNotFound(err):
		if err := cl.Create(ctx, want); err != nil {
			return false, fmt.Errorf("creating the %q FlowSchema: %w", want.Name, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading the %q FlowSchema: %w", want.Name, err)
	}

	if equality(have.Spec, want.Spec) {
		return false, nil
	}
	have.Spec = want.Spec
	if err := cl.Update(ctx, &have); err != nil {
		return false, fmt.Errorf("updating the %q FlowSchema: %w", want.Name, err)
	}
	return true, nil
}

// equality compares the parts of a spec this run sets. Written out rather than
// reflected so that a field the API server defaults — and there are several in
// a FlowSchema — cannot read as a change and put the object into a rewrite
// loop against whatever else manages the cluster.
func equality(a, b flowcontrolv1.FlowSchemaSpec) bool {
	if a.PriorityLevelConfiguration.Name != b.PriorityLevelConfiguration.Name ||
		a.MatchingPrecedence != b.MatchingPrecedence ||
		len(a.Rules) != len(b.Rules) {
		return false
	}
	if (a.DistinguisherMethod == nil) != (b.DistinguisherMethod == nil) {
		return false
	}
	if a.DistinguisherMethod != nil && a.DistinguisherMethod.Type != b.DistinguisherMethod.Type {
		return false
	}
	for i := range a.Rules {
		if !sameSubjects(a.Rules[i].Subjects, b.Rules[i].Subjects) ||
			!sameResourceRules(a.Rules[i].ResourceRules, b.Rules[i].ResourceRules) {
			return false
		}
	}
	return true
}

func sameSubjects(a, b []flowcontrolv1.Subject) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind {
			return false
		}
		x, y := a[i].ServiceAccount, b[i].ServiceAccount
		if (x == nil) != (y == nil) {
			return false
		}
		if x != nil && (x.Name != y.Name || x.Namespace != y.Namespace) {
			return false
		}
	}
	return true
}

func sameResourceRules(a, b []flowcontrolv1.ResourcePolicyRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameStrings(a[i].Verbs, b[i].Verbs) ||
			!sameStrings(a[i].APIGroups, b[i].APIGroups) ||
			!sameStrings(a[i].Resources, b[i].Resources) ||
			!sameStrings(a[i].Namespaces, b[i].Namespaces) {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
