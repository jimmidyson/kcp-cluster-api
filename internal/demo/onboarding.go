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
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/jimmidyson/kcp-cluster-api/internal/capiexports"
	"github.com/jimmidyson/kcp-cluster-api/internal/capiworkspaces"
)

// OnboardingStatus is what a run can say about how one workspace came to serve
// Cluster API: who owns it, what it was given without asking, what its tenant
// turned on, and what its roles ended up granting.
//
// It exists because the interesting part of onboarding leaves no trace in a
// Cluster: the workspace looks the same whether every APIBinding and every
// role was written out by an operator or none of them were.
type OnboardingStatus struct {
	Workspace string
	Owner     string

	// Bound is every Cluster API APIExport the workspace has an APIBinding to,
	// whether the WorkspaceType created it or the tenant did.
	//
	// kcp's own bindings - tenancy, topology - are left out. Every workspace
	// has them and they say nothing about onboarding.
	Bound []string

	// Onboarded are the ones the WorkspaceType bound without being asked:
	// Bound minus Enabled.
	Onboarded []string

	// Enabled are the ones somebody chose - the tenant's providers - and
	// EnabledBy is who chose them.
	Enabled   []string
	EnabledBy string

	// APIGroups are the Cluster API groups the workspace's admin role grants,
	// read back from the role rather than computed. This is the automation
	// under test: nobody edited that role, and it covers the provider the
	// tenant enabled.
	APIGroups []string

	// Roles are the Cluster API ClusterRoles present in the workspace.
	Roles []string
}

// SnapshotOnboarding reads back what each workspace was onboarded with.
//
// Read back from the server rather than reported from what the run did: a
// table built from the demo's own intentions would say the same thing whether
// or not any of it worked.
func SnapshotOnboarding(ctx context.Context, workspaces []Workspace) ([]OnboardingStatus, error) {
	statuses := make([]OnboardingStatus, 0, len(workspaces))
	for _, ws := range workspaces {
		status := OnboardingStatus{
			Workspace: ws.Path,
			Owner:     ws.Owner,
			Enabled:   ws.ProvidersEnabled,
			EnabledBy: ws.EnabledBy,
		}

		bindings := &apisv1alpha2.APIBindingList{}
		if err := ws.Client.List(ctx, bindings); err != nil {
			return nil, fmt.Errorf("listing APIBindings in %s: %w", ws.Path, err)
		}
		for i := range bindings.Items {
			ref := bindings.Items[i].Spec.Reference.Export
			if ref == nil || !strings.HasPrefix(ref.Name, clusterAPIExportPrefix) {
				continue
			}
			status.Bound = append(status.Bound, ref.Name)
			if !slices.Contains(status.Enabled, ref.Name) {
				status.Onboarded = append(status.Onboarded, ref.Name)
			}
		}
		slices.Sort(status.Bound)
		slices.Sort(status.Onboarded)
		slices.Sort(status.Enabled)

		for _, name := range []string{capiworkspaces.AdminRoleName, capiworkspaces.ViewRoleName} {
			role := &rbacv1.ClusterRole{}
			err := ws.Client.Get(ctx, client.ObjectKey{Name: name}, role)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading ClusterRole %s in %s: %w", name, ws.Path, err)
			}
			status.Roles = append(status.Roles, name)
			if name != capiworkspaces.AdminRoleName {
				continue
			}
			for _, rule := range role.Rules {
				for _, group := range rule.APIGroups {
					if group != capiworkspaces.ClusterAPIGroup && !strings.HasSuffix(group, "."+capiworkspaces.ClusterAPIGroup) {
						continue
					}
					if !slices.Contains(status.APIGroups, group) {
						status.APIGroups = append(status.APIGroups, group)
					}
				}
			}
			slices.Sort(status.APIGroups)
		}

		statuses = append(statuses, status)
	}
	return statuses, nil
}

// RenderOnboardingTable writes what each workspace was given and what its
// tenant turned on.
func RenderOnboardingTable(w io.Writer, statuses []OnboardingStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "WORKSPACE\tOWNER\tFROM THE TYPE\tENABLED\tENABLED BY\tROLE COVERS"); err != nil {
		return err
	}
	for _, s := range statuses {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Workspace, orNone(s.Owner), joinShort(s.Onboarded), joinShort(s.Enabled),
			orNone(s.EnabledBy), joinGroups(s.APIGroups)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// clusterAPIExportPrefix is what every export this project publishes is called
// something-of. It is how the onboarding table tells a Cluster API binding
// from the ones kcp gives every workspace.
const clusterAPIExportPrefix = "cluster-api"

// joinShort renders export names without the prefix they all share, because
// the column is read for what differs between rows.
func joinShort(values []string) string {
	short := make([]string, 0, len(values))
	for _, v := range values {
		short = append(short, strings.TrimPrefix(v, clusterAPIExportPrefix+"-"))
	}
	return join(short)
}

// joinGroups renders API groups by their contract, which is the part that says
// which provider a group belongs to. Cluster API's own group has no contract
// prefix, so it is named for what it is.
func joinGroups(groups []string) string {
	short := make([]string, 0, len(groups))
	for _, g := range groups {
		if g == capiworkspaces.ClusterAPIGroup {
			short = append(short, "core")
			continue
		}
		short = append(short, strings.TrimSuffix(g, "."+capiworkspaces.ClusterAPIGroup))
	}
	return join(short)
}

// ClaimStatus is one permission claim on one export, with the export it lands
// on resolved back from its identity hash.
type ClaimStatus struct {
	// Export is the APIExport carrying the claim.
	Export string

	// Resource is what is claimed, as group/resource.
	Resource string

	// From is the export publishing that resource - resolved from the claim's
	// identity hash, which is the only thing the claim itself carries - or
	// "(built in)" for a type kcp serves everywhere.
	From string

	// Verbs is what the claim grants.
	Verbs []string

	// Discovered says this claim was not written down anywhere: it exists
	// because a provider published a labelled APIExport, and it would exist
	// for a provider this repository has never heard of.
	Discovered bool
}

// SnapshotClaims reads the claims on each export back from the server and
// resolves the identity hashes to the exports that own them.
func SnapshotClaims(ctx context.Context, cl client.Client, exports []string) ([]ClaimStatus, error) {
	all := &apisv1alpha2.APIExportList{}
	if err := cl.List(ctx, all); err != nil {
		return nil, fmt.Errorf("listing APIExports: %w", err)
	}

	owner := map[string]string{}
	contract := map[string]capiexports.Contract{}
	for i := range all.Items {
		export := &all.Items[i]
		if export.Status.IdentityHash == "" {
			continue
		}
		owner[export.Status.IdentityHash] = export.Name
		contract[export.Name] = capiexports.Contract(export.Labels[capiexports.ContractLabel])
	}

	var claims []ClaimStatus
	for i := range all.Items {
		export := &all.Items[i]
		if !slices.Contains(exports, export.Name) {
			continue
		}
		for _, claim := range export.Spec.PermissionClaims {
			resource := claim.Resource
			if claim.Group != "" {
				resource = claim.Resource + "." + claim.Group
			}
			from := "(built in)"
			if claim.IdentityHash != "" {
				from = owner[claim.IdentityHash]
				if from == "" {
					from = "(unknown identity)"
				}
			}
			claims = append(claims, ClaimStatus{
				Export:     export.Name,
				Resource:   resource,
				From:       from,
				Verbs:      claim.Verbs,
				Discovered: claim.IdentityHash != "" && contract[from] != "" && contract[from] != capiexports.ContractCore,
			})
		}
	}

	slices.SortFunc(claims, func(a, b ClaimStatus) int {
		if a.Export != b.Export {
			return strings.Compare(a.Export, b.Export)
		}
		return strings.Compare(a.Resource, b.Resource)
	})
	return claims, nil
}

// RenderClaimsTable writes the claims, saying which of them nobody wrote down.
func RenderClaimsTable(w io.Writer, claims []ClaimStatus) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "EXPORT\tCLAIMS\tPUBLISHED BY\tVERBS\tSOURCE"); err != nil {
		return err
	}
	for _, c := range claims {
		source := "declared"
		if c.Discovered {
			source = "discovered"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			c.Export, c.Resource, c.From, strings.Join(c.Verbs, ","), source); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func join(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
