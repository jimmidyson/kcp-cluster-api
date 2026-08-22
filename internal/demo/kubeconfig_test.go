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
	"os"
	"path/filepath"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// sourceKubeconfig writes a kubeconfig shaped like the one a kcp server hands
// out: a cluster-unaware base context, the shard admin's version of it, and a
// workspace-scoped context the generator must not mistake for either.
func sourceKubeconfig(t *testing.T) string {
	t.Helper()

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["base"] = &clientcmdapi.Cluster{
		Server:                   "https://localhost:6443",
		CertificateAuthorityData: []byte("base-ca"),
	}
	cfg.Clusters["root"] = &clientcmdapi.Cluster{
		Server:                   "https://localhost:6443/clusters/root",
		CertificateAuthorityData: []byte("base-ca"),
	}
	cfg.AuthInfos["kcp-admin"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: []byte("admin-cert"),
		ClientKeyData:         []byte("admin-key"),
	}
	cfg.AuthInfos["shard-admin"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: []byte("shard-cert"),
		ClientKeyData:         []byte("shard-key"),
	}
	cfg.Contexts[BaseContext] = &clientcmdapi.Context{Cluster: "base", AuthInfo: "kcp-admin"}
	cfg.Contexts[ShardBaseContext] = &clientcmdapi.Context{Cluster: "base", AuthInfo: "shard-admin"}
	cfg.Contexts["root"] = &clientcmdapi.Context{Cluster: "root", AuthInfo: "kcp-admin"}
	cfg.CurrentContext = "root"

	path := filepath.Join(t.TempDir(), "admin.kubeconfig")
	if err := writeKubeconfig(path, cfg); err != nil {
		t.Fatalf("writing the source kubeconfig: %v", err)
	}
	return path
}

func TestWriteWorkspaceKubeconfigScopesEachContextToItsWorkspace(t *testing.T) {
	source := sourceKubeconfig(t)
	dest := filepath.Join(t.TempDir(), "workspaces.kubeconfig")

	entries := []KubeconfigEntry{
		{Name: "root", Path: "root"},
		{Name: "root:capi-demo:alice:capi-demo-1", Path: "root:capi-demo:alice:capi-demo-1"},
	}
	if err := WriteWorkspaceKubeconfig(dest, source, entries); err != nil {
		t.Fatalf("WriteWorkspaceKubeconfig: %v", err)
	}

	for _, entry := range entries {
		cfg, err := ConfigFromKubeconfig(dest, entry.Name)
		if err != nil {
			t.Fatalf("reading back context %q: %v", entry.Name, err)
		}
		want := "https://localhost:6443/clusters/" + entry.Path
		if cfg.Host != want {
			t.Errorf("context %q Host = %q, want %q", entry.Name, cfg.Host, want)
		}
		// The credential travels with the context: a generated kubeconfig
		// that needs the original one beside it is no use to a UI that was
		// handed only this file.
		if string(cfg.TLSClientConfig.CertData) != "admin-cert" {
			t.Errorf("context %q client certificate = %q, want the base context's", entry.Name, cfg.TLSClientConfig.CertData)
		}
		if string(cfg.TLSClientConfig.CAData) != "base-ca" {
			t.Errorf("context %q CA = %q, want the base context's", entry.Name, cfg.TLSClientConfig.CAData)
		}
		if cfg.Impersonate.UserName != "" {
			t.Errorf("context %q impersonates %q, want nobody", entry.Name, cfg.Impersonate.UserName)
		}
	}
}

// The source context is already scoped in a kubeconfig somebody handed the
// demo. Joining a workspace path onto it would produce a URL with two
// /clusters/ segments and a 404 that says nothing about why.
func TestWriteWorkspaceKubeconfigReplacesAnAlreadyScopedServer(t *testing.T) {
	source := sourceKubeconfig(t)
	dest := filepath.Join(t.TempDir(), "workspaces.kubeconfig")

	if err := WriteWorkspaceKubeconfig(dest, source, []KubeconfigEntry{
		{Name: "child", Path: "root:capi-demo", SourceContext: "root"},
	}); err != nil {
		t.Fatalf("WriteWorkspaceKubeconfig: %v", err)
	}

	cfg, err := ConfigFromKubeconfig(dest, "child")
	if err != nil {
		t.Fatalf("reading back the context: %v", err)
	}
	if want := "https://localhost:6443/clusters/root:capi-demo"; cfg.Host != want {
		t.Errorf("Host = %q, want %q", cfg.Host, want)
	}
}

func TestWriteWorkspaceKubeconfigImpersonatesFromTheShardAdmin(t *testing.T) {
	source := sourceKubeconfig(t)
	dest := filepath.Join(t.TempDir(), "workspaces.kubeconfig")

	entry := KubeconfigEntry{
		Name:        "alice@root:capi-demo:alice:capi-demo-1",
		Path:        "root:capi-demo:alice:capi-demo-1",
		Impersonate: "alice",
	}
	if err := WriteWorkspaceKubeconfig(dest, source, []KubeconfigEntry{entry}); err != nil {
		t.Fatalf("WriteWorkspaceKubeconfig: %v", err)
	}

	cfg, err := ConfigFromKubeconfig(dest, entry.Name)
	if err != nil {
		t.Fatalf("reading back context %q: %v", entry.Name, err)
	}
	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("Impersonate.UserName = %q, want alice", cfg.Impersonate.UserName)
	}
	// kcp scopes an impersonated user to the logical cluster the request
	// addresses unless the impersonator is privileged, so a tenant context
	// built on the ordinary admin credential is refused in every workspace
	// the demo wants to show it working in. See ShardBaseContext.
	if string(cfg.TLSClientConfig.CertData) != "shard-cert" {
		t.Errorf("client certificate = %q, want the shard admin's", cfg.TLSClientConfig.CertData)
	}
}

func TestWriteWorkspaceKubeconfigRejectsAnEmptyEntryList(t *testing.T) {
	source := sourceKubeconfig(t)
	dest := filepath.Join(t.TempDir(), "workspaces.kubeconfig")

	if err := WriteWorkspaceKubeconfig(dest, source, nil); err == nil {
		t.Fatal("WriteWorkspaceKubeconfig with no entries returned no error, want one")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("a rejected run left %s behind", dest)
	}
}

func TestWorkspaceContextsCoverTheTreeAndBrowseAsTheOwner(t *testing.T) {
	result := Result{
		Parent: "root",
		Org:    "root:capi-demo",
		Users: []User{
			{Name: "alice", Home: "root:capi-demo:alice"},
			{Name: "bob", Home: "root:capi-demo:bob"},
		},
		Workspaces: []Workspace{
			{Path: "root:capi-demo:alice:capi-demo-1", Owner: "alice"},
			{Path: "root:capi-demo:bob:capi-demo-1", Owner: "bob"},
		},
	}

	byName := map[string]KubeconfigEntry{}
	for _, entry := range WorkspaceContexts(result) {
		if _, seen := byName[entry.Name]; seen {
			t.Errorf("context %q generated twice", entry.Name)
		}
		byName[entry.Name] = entry
	}

	// Every workspace in the tree is reachable, top to bottom: a navigator
	// that cannot reach the parent cannot show what is under it.
	for _, path := range []string{
		"root", "root:capi-demo",
		"root:capi-demo:alice", "root:capi-demo:bob",
		"root:capi-demo:alice:capi-demo-1", "root:capi-demo:bob:capi-demo-1",
	} {
		entry, ok := byName[path]
		if !ok {
			t.Fatalf("no context named %s", path)
		}
		if entry.Path != path {
			t.Errorf("context %q addresses %q", path, entry.Path)
		}
	}

	// A tenant's own workspaces are browsed as the tenant, so what the UI
	// shows is that tenant's authorization rather than the admin's.
	if got := byName["root:capi-demo:alice:capi-demo-1"].Impersonate; got != "alice" {
		t.Errorf("alice's workspace is browsed as %q, want alice", got)
	}
	if got := byName["root:capi-demo:alice"].Impersonate; got != "alice" {
		t.Errorf("alice's home is browsed as %q, want alice", got)
	}
	// The workspaces above them belong to nobody and no tenant can read
	// them, so browsing those as a tenant would show an empty tree and say
	// nothing about why.
	if got := byName["root:capi-demo"].Impersonate; got != "" {
		t.Errorf("the org workspace is browsed as %q, want the admin", got)
	}

	// One deliberate wrong-tenant context, because a refusal somebody can
	// click on is the only part of the isolation story a UI can show.
	refused, ok := byName["alice@root:capi-demo:bob:capi-demo-1"]
	if !ok {
		t.Fatal("no context browsing bob's workspace as alice")
	}
	if refused.Impersonate != "alice" || refused.Path != "root:capi-demo:bob:capi-demo-1" {
		t.Errorf("the wrong-tenant context is %+v", refused)
	}
}

func TestWorkspaceContextsWithoutUsers(t *testing.T) {
	result := Result{
		Parent:     "root",
		Workspaces: []Workspace{{Path: "root:capi-demo-1"}},
	}

	entries := WorkspaceContexts(result)
	for _, entry := range entries {
		if entry.Impersonate != "" {
			t.Errorf("context %q impersonates %q in a run with no users", entry.Name, entry.Impersonate)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("got %d contexts, want root and the workspace: %+v", len(entries), entries)
	}
}
