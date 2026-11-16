/*
Copyright 2026 Nutanix

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

package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/nutanix-cloud-native/cluster-api-provider-nutanix/api/v1beta1"
	"github.com/nutanix-cloud-native/prism-go-client/environment/credentials"
)

// workspaceKey is how the fake reader below tells one workspace from another.
type workspaceKey struct{}

// readerPerWorkspace is a stand-in for the cluster-aware client: it resolves
// each read against the workspace named in the context, which is exactly the
// property the informers could not offer.
type readerPerWorkspace struct {
	byWorkspace map[string]ctlclient.Client
}

func (r readerPerWorkspace) Get(ctx context.Context, key ctlclient.ObjectKey, obj ctlclient.Object, opts ...ctlclient.GetOption) error {
	ws, _ := ctx.Value(workspaceKey{}).(string)
	return r.byWorkspace[ws].Get(ctx, key, obj, opts...)
}

func (r readerPerWorkspace) List(ctx context.Context, list ctlclient.ObjectList, opts ...ctlclient.ListOption) error {
	ws, _ := ctx.Value(workspaceKey{}).(string)
	return r.byWorkspace[ws].List(ctx, list, opts...)
}

func credentialsSecret(password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nutanix-creds", Namespace: "default"},
		Data: map[string][]byte{
			credentials.KeyName: []byte(`[{"type":"basic_auth","data":{"prismCentral":{"username":"admin","password":"` + password + `"}}}]`),
		},
	}
}

// TestWorkspaceProviderReadsTheRightWorkspacesCredentials is the breach this
// provider exists to close.
//
// Two workspaces each hold a Secret called default/nutanix-creds, holding
// different passwords. Resolved through a shared informer they are one Secret
// and the second tenant gets the first's credentials — which works, because the
// wrong tenant's credentials are still valid credentials. Resolved through a
// cluster-aware reader they are two.
func TestWorkspaceProviderReadsTheRightWorkspacesCredentials(t *testing.T) {
	alphaClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(credentialsSecret("alpha-password")).Build()
	betaClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(credentialsSecret("beta-password")).Build()

	reader := readerPerWorkspace{byWorkspace: map[string]ctlclient.Client{
		"workspace-alpha": alphaClient,
		"workspace-beta":  betaClient,
	}}

	endpoint := credentials.NutanixPrismEndpoint{
		Address: "pc.example.com",
		Port:    9440,
		CredentialRef: &credentials.NutanixCredentialReference{
			Kind:      credentials.SecretKind,
			Name:      "nutanix-creds",
			Namespace: "default",
		},
	}

	passwordIn := func(workspace string) string {
		ctx := context.WithValue(t.Context(), workspaceKey{}, workspace)
		me, err := NewWorkspaceProvider(ctx, reader, endpoint).GetManagementEndpoint(nil)
		require.NoError(t, err)
		return me.ApiCredentials.Password
	}

	alpha, beta := passwordIn("workspace-alpha"), passwordIn("workspace-beta")

	assert.Equal(t, "alpha-password", alpha)
	assert.Equal(t, "beta-password", beta)
	assert.NotEqual(t, alpha, beta,
		"two workspaces' identically named credentials Secrets must not resolve to one")
}

// TestWorkspaceProviderMissingSecret checks the failure is a named error rather
// than a nil dereference, since this path runs before anything is provisioned.
func TestWorkspaceProviderMissingSecret(t *testing.T) {
	empty := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	reader := readerPerWorkspace{byWorkspace: map[string]ctlclient.Client{"": empty}}

	endpoint := credentials.NutanixPrismEndpoint{
		Address: "pc.example.com",
		Port:    9440,
		CredentialRef: &credentials.NutanixCredentialReference{
			Kind:      credentials.SecretKind,
			Name:      "absent",
			Namespace: "default",
		},
	}

	_, err := NewWorkspaceProvider(t.Context(), reader, endpoint).GetManagementEndpoint(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading credentials Secret")
}

// TestWorkspaceHelperRefusesManagerCredentials is the escalation this refuses.
//
// CAPX falls back to /etc/nutanix/config/prismCentral — the operator's Prism
// Central and the operator's account, mounted into the manager's pod — when a
// NutanixCluster does not name its own. Serving one tenant that is a
// convenience. Serving many it is a way for any tenant to obtain the
// operator's credentials by leaving a field out, and it succeeds quietly,
// because the operator's credentials are perfectly valid credentials.
//
// Credentials are per cluster or the reconcile fails.
func TestWorkspaceHelperRefusesManagerCredentials(t *testing.T) {
	empty := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	reader := readerPerWorkspace{byWorkspace: map[string]ctlclient.Client{"": empty}}

	// A NutanixCluster naming no credentials at all: spec.PrismCentral is nil,
	// which is exactly what sends the single-cluster path to the file.
	cluster := &infrav1.NutanixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}

	_, err := NewWorkspaceHelper(reader).BuildManagementEndpoint(t.Context(), cluster)

	require.Error(t, err, "a NutanixCluster naming no credentials must fail rather than borrow the manager's")

	// The guard's own wording, which the fallback path cannot produce. Asserting
	// on anything both layers say — "spec.prismCentral", "more than one tenant" —
	// passes whichever refused, so it would not notice the guard being removed.
	assert.Contains(t, err.Error(), "does not set spec.prismCentral",
		"the refusal must come from the guard, not from whatever the fallback found")
	assert.NotContains(t, err.Error(), "environment provider from file",
		"the fallback must not be entered at all")
}

// TestWorkspaceHelperFileReaderIsUnreachable covers the second layer on its own.
//
// BuildManagementEndpoint refuses before reaching the fallback, so this calls
// the fallback directly: if a later edit reorders those branches, the manager's
// credentials still must not be obtainable in workspace mode.
func TestWorkspaceHelperFileReaderIsUnreachable(t *testing.T) {
	empty := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	reader := readerPerWorkspace{byWorkspace: map[string]ctlclient.Client{"": empty}}

	_, err := NewWorkspaceHelper(reader).buildProviderFromFile(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available when serving more than one tenant")
}

// TestSingleClusterHelperStillFallsBack pins the behaviour that is not being
// changed: a CAPX serving one cluster keeps its manager-level fallback, which
// is why the refusal above is conditional rather than a removal.
func TestSingleClusterHelperStillFallsBack(t *testing.T) {
	helper := NewHelper(nil, nil).withCustomNutanixPrismEndpointReader(
		func() (*credentials.NutanixPrismEndpoint, error) {
			return nil, assert.AnError
		},
	)

	cluster := &infrav1.NutanixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}

	_, err := helper.BuildManagementEndpoint(t.Context(), cluster)

	// It reached the fallback: the error is the reader's, not the refusal.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "more than one tenant",
		"the single-cluster path must still reach its manager-level fallback")
}
