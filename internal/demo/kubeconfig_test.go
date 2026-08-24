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
	"reflect"
	"strings"
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

func setOf(sets []KubeconfigSet, name string) (KubeconfigSet, bool) {
	for _, set := range sets {
		if set.Name == name {
			return set, true
		}
	}
	return KubeconfigSet{}, false
}

func entryFor(set KubeconfigSet, path string) (KubeconfigEntry, bool) {
	for _, entry := range set.Entries {
		if entry.Path == path {
			return entry, true
		}
	}
	return KubeconfigEntry{}, false
}

func demoResult() Result {
	return Result{
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
}

// The operator's file is the whole tree, as the admin. It is the only one that
// can see the workspaces above the tenants, because nothing grants a tenant
// anything there.
func TestWorkspaceKubeconfigsGiveTheOperatorTheWholeTree(t *testing.T) {
	sets := WorkspaceKubeconfigs(demoResult())

	operator, ok := setOf(sets, OperatorKubeconfigName)
	if !ok {
		t.Fatalf("no %s set: got %v", OperatorKubeconfigName, sets)
	}
	if operator.Owner != "" {
		t.Errorf("the operator set belongs to %q", operator.Owner)
	}
	for _, path := range []string{
		"root", "root:capi-demo",
		"root:capi-demo:alice", "root:capi-demo:bob",
		"root:capi-demo:alice:capi-demo-1", "root:capi-demo:bob:capi-demo-1",
	} {
		entry, ok := entryFor(operator, path)
		if !ok {
			t.Fatalf("the operator set has no context for %s", path)
		}
		if entry.Impersonate != "" {
			t.Errorf("the operator's context for %s impersonates %q", path, entry.Impersonate)
		}
	}
}

// A tenant's file holds that tenant and nothing else. Seeing the other one
// means being handed the other one's kubeconfig, which is the whole point of
// there being two files.
func TestWorkspaceKubeconfigsAreOnePerTenant(t *testing.T) {
	sets := WorkspaceKubeconfigs(demoResult())

	alice, ok := setOf(sets, "alice")
	if !ok {
		t.Fatalf("no alice set: got %v", sets)
	}
	if alice.Owner != "alice" {
		t.Errorf("the alice set belongs to %q", alice.Owner)
	}
	for _, entry := range alice.Entries {
		if entry.Impersonate != "alice" {
			t.Errorf("alice's file has a context for %s as %q", entry.Path, entry.Impersonate)
		}
	}
	if _, ok := entryFor(alice, "root:capi-demo:alice"); !ok {
		t.Error("alice's file cannot reach her own home")
	}
	if _, ok := entryFor(alice, "root:capi-demo:alice:capi-demo-1"); !ok {
		t.Error("alice's file cannot reach her own workspace")
	}
	// Not the org workspace, and not the parent: a tenant is refused in both,
	// and a context that only ever errors is noise rather than a lesson.
	if _, ok := entryFor(alice, "root:capi-demo"); ok {
		t.Error("alice's file has a context for the org workspace")
	}
	if _, ok := entryFor(alice, "root"); ok {
		t.Error("alice's file has a context for the parent workspace")
	}

	if _, ok := setOf(sets, "bob"); !ok {
		t.Error("no bob set")
	}
}

// A tenant's file offers no way into another tenant's workspaces - not even a
// context that would be refused. Headlamp cannot enter such a workspace at
// all, so it asks for a login token rather than reporting a refusal, and a
// login box teaches the opposite of what it should. The isolation is in there
// being one file per tenant.
func TestWorkspaceKubeconfigsGiveATenantNoRouteToAnother(t *testing.T) {
	sets := WorkspaceKubeconfigs(demoResult())

	alice, _ := setOf(sets, "alice")
	for _, entry := range alice.Entries {
		if strings.HasPrefix(entry.Path, "root:capi-demo:bob") {
			t.Errorf("alice's file has a context for %s", entry.Path)
		}
	}
	if len(alice.Entries) != 2 {
		t.Errorf("alice's file has %d contexts, want her home and her workspace: %+v",
			len(alice.Entries), alice.Entries)
	}
}

func TestWorkspaceKubeconfigsWithoutUsers(t *testing.T) {
	sets := WorkspaceKubeconfigs(Result{
		Parent:     "root",
		Workspaces: []Workspace{{Path: "root:capi-demo-1"}},
	})

	if len(sets) != 1 {
		t.Fatalf("got %d sets, want only the operator's: %+v", len(sets), sets)
	}
	for _, entry := range sets[0].Entries {
		if entry.Impersonate != "" {
			t.Errorf("context %q impersonates %q in a run with no users", entry.Name, entry.Impersonate)
		}
	}
	if len(sets[0].Entries) != 2 {
		t.Fatalf("got %d contexts, want root and the workspace: %+v", len(sets[0].Entries), sets[0].Entries)
	}
}

// A run that happened somewhere else deserves the same files as one that
// happened here. The deployed demo runs in a Job, in a pod that exits, so the
// kubeconfigs it would have written are gone - the deployment writes them
// instead, and has only the plan to write them from.
//
// This asserts the two agree, which is what keeps "what files a run deserves"
// one description rather than two.
func TestPlannedKubeconfigsMatchARunsOwn(t *testing.T) {
	t.Parallel()

	const (
		parent = "root"
		prefix = DefaultWorkspacePrefix
	)
	users := []string{"alice", "bob"}

	// The Result a run with this shape produces.
	result := Result{Parent: parent, Org: OrgPath(parent, prefix)}
	for _, name := range users {
		result.Users = append(result.Users, User{Name: name, Home: HomePath(parent, prefix, name)})
	}
	for _, plan := range PlanWorkspaces(parent, prefix, users, 4) {
		result.Workspaces = append(result.Workspaces, Workspace{Path: plan.Path, Owner: plan.Owner})
	}

	want := WorkspaceKubeconfigs(result)
	got := PlannedKubeconfigs(parent, prefix, users, 4)

	if !reflect.DeepEqual(want, got) {
		t.Errorf("the planned kubeconfigs differ from the ones a run writes:\n  run:     %+v\n  planned: %+v", want, got)
	}
}

// A run with no tenants has no tenant files, and the operator's holds the
// workspaces anyway.
func TestPlannedKubeconfigsWithoutTenants(t *testing.T) {
	t.Parallel()

	sets := PlannedKubeconfigs("root", DefaultWorkspacePrefix, nil, 2)
	if len(sets) != 1 {
		t.Fatalf("got %d files, want only the operator's", len(sets))
	}
	if sets[0].Name != OperatorKubeconfigName {
		t.Errorf("the only file is %q, want %q", sets[0].Name, OperatorKubeconfigName)
	}
	if len(sets[0].Entries) == 0 {
		t.Error("the operator's file has no workspaces in it")
	}
}
