//go:build integration

package integration

import (
	"testing"
	"time"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/jimmidyson/kcp-cluster-api/internal/kcpfixtures"
	"github.com/jimmidyson/kcp-cluster-api/test/integration/envtest"
)

// TestEnsureWorkspaceBelowRoot creates a workspace inside another workspace,
// which is the ordinary case — an org, a tenant's home — and not the one a
// fixture written against :root happens to cover.
//
// It is a regression test. EnsureWorkspaceOfType used to wait for its
// WorkspaceType to be readable before creating anything, and did that with a
// get by name in whatever workspace the client was scoped to. A
// WorkspaceTypeReference resolves by path, so root:universal is readable in
// :root and nowhere below it: every workspace created inside another one hung
// for the full timeout on a type that was there the whole time. Nothing at
// root level noticed, because at root level the get succeeds.
func TestEnsureWorkspaceBelowRoot(t *testing.T) {
	cfg := envtest.Environment(t, "")

	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("building the scheme: %v", err)
	}
	rootClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a client for the root workspace: %v", err)
	}

	if _, err := kcpfixtures.EnsureWorkspace(t.Context(), rootClient, "parent", 2*time.Minute); err != nil {
		t.Fatalf("creating the parent workspace at root: %v", err)
	}

	// The same server, addressed one workspace down: a root config's host ends
	// in /clusters/root, and a child is that path plus ":parent".
	childCfg := rest.CopyConfig(cfg)
	childCfg.Host = cfg.Host + ":parent"
	childClient, err := client.New(childCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("building a client for the parent workspace: %v", err)
	}

	// The type is root:universal — declared in :root, used from :root:parent.
	if _, err := kcpfixtures.EnsureWorkspace(t.Context(), childClient, "child", 2*time.Minute); err != nil {
		t.Fatalf("creating a workspace inside another workspace, with a type "+
			"declared in :root: %v", err)
	}
}
