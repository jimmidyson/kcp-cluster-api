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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// WorkspaceKubeconfigName is what a demo run calls the kubeconfig it writes
// for tools that address a workspace by choosing a context rather than by
// rewriting a URL.
const WorkspaceKubeconfigName = "workspaces.kubeconfig"

// KubeconfigEntry is one context in a generated kubeconfig: a workspace, and
// the identity requests to it are made as.
type KubeconfigEntry struct {
	// Name is the context name, which is the name a UI shows. The workspace
	// path is the obvious answer and the one WorkspaceContexts uses, because
	// a chooser listing `root:capi-demo:alice:capi-demo-1` needs no key.
	Name string

	// Path is the workspace this context addresses.
	Path string

	// Impersonate is the user requests are evaluated as, or empty to use the
	// source credential unchanged. A named user is impersonated from the
	// shard admin - see ShardBaseContext.
	Impersonate string

	// SourceContext is the context in the source kubeconfig the cluster and
	// credential are taken from. Empty means BaseContext, or ShardBaseContext
	// when this entry impersonates.
	SourceContext string
}

// WorkspaceContexts is the set of contexts a run's tree deserves: every
// workspace from the parent down, each browsed as whoever owns it.
//
// Ordered from the top of the tree down, so a chooser that keeps the order it
// was given reads as the tree does.
func WorkspaceContexts(result Result) []KubeconfigEntry {
	var (
		entries []KubeconfigEntry
		seen    = map[string]bool{}
	)
	add := func(path, impersonate string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		entries = append(entries, KubeconfigEntry{Name: path, Path: path, Impersonate: impersonate})
	}

	// The parent and the org workspace are nobody's: a tenant is refused in
	// both, so browsing them as a tenant would show an empty tree and say
	// nothing about why. The admin credential is what makes them navigable.
	add(result.Parent, "")
	add(result.Org, "")
	for _, user := range result.Users {
		add(user.Home, user.Name)
	}
	for _, ws := range result.Workspaces {
		add(ws.Path, ws.Owner)
	}

	// And one context that is refused, because a refusal somebody can click
	// on is the only part of the isolation story a UI can show. The first
	// user, in the first workspace that is not theirs.
	if len(result.Users) > 0 {
		user := result.Users[0].Name
		for _, ws := range result.Workspaces {
			if ws.Owner != "" && ws.Owner != user {
				entries = append(entries, KubeconfigEntry{
					Name:        user + "@" + ws.Path,
					Path:        ws.Path,
					Impersonate: user,
				})
				break
			}
		}
	}
	return entries
}

// WriteWorkspaceKubeconfig writes a kubeconfig addressing each entry's
// workspace, built from the credentials in the kubeconfig at sourcePath.
//
// One context per workspace rather than one context and a note about
// --server: a UI picks a context, and a workspace it has no context for is a
// workspace nobody can reach from it. The credentials are copied in rather
// than referenced, so the file stands on its own.
func WriteWorkspaceKubeconfig(destPath, sourcePath string, entries []KubeconfigEntry) error {
	if len(entries) == 0 {
		return errors.New("no workspaces to write contexts for")
	}

	source, err := clientcmd.LoadFromFile(sourcePath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", sourcePath, err)
	}

	out := clientcmdapi.NewConfig()
	for _, entry := range entries {
		sourceContext := entry.SourceContext
		if sourceContext == "" {
			sourceContext = BaseContext
			if entry.Impersonate != "" {
				sourceContext = ShardBaseContext
			}
		}

		cluster, authInfo, err := contextParts(source, sourceContext)
		if err != nil {
			return fmt.Errorf("building the context for %s: %w", entry.Path, err)
		}

		scoped := cluster.DeepCopy()
		scoped.Server = workspaceServer(cluster.Server, entry.Path)
		credential := authInfo.DeepCopy()
		if entry.Impersonate != "" {
			credential.Impersonate = entry.Impersonate
		}

		out.Clusters[entry.Name] = scoped
		out.AuthInfos[entry.Name] = credential
		out.Contexts[entry.Name] = &clientcmdapi.Context{Cluster: entry.Name, AuthInfo: entry.Name}
	}
	out.CurrentContext = entries[0].Name

	return writeKubeconfig(destPath, out)
}

// contextParts resolves a context to the cluster and credential it names.
func contextParts(cfg *clientcmdapi.Config, name string) (*clientcmdapi.Cluster, *clientcmdapi.AuthInfo, error) {
	kubeContext, ok := cfg.Contexts[name]
	if !ok {
		return nil, nil, fmt.Errorf("no context %q", name)
	}
	cluster, ok := cfg.Clusters[kubeContext.Cluster]
	if !ok {
		return nil, nil, fmt.Errorf("context %q names cluster %q, which is not in the file", name, kubeContext.Cluster)
	}
	authInfo, ok := cfg.AuthInfos[kubeContext.AuthInfo]
	if !ok {
		return nil, nil, fmt.Errorf("context %q names user %q, which is not in the file", name, kubeContext.AuthInfo)
	}
	return cluster, authInfo, nil
}

// workspaceServer points a server URL at one workspace, replacing whatever
// workspace it addressed already.
//
// Replacing rather than appending because the source context need not be a
// cluster-unaware one: a kubeconfig somebody hands the demo may well have its
// current context inside a workspace, and joining onto that produces a URL
// with two /clusters/ segments and a 404 that says nothing about why.
func workspaceServer(server, path string) string {
	base := strings.TrimSuffix(server, "/")
	if index := strings.Index(base, "/clusters/"); index >= 0 {
		base = base[:index]
	}
	return base + "/clusters/" + path
}

// writeKubeconfig writes a kubeconfig, creating the directory it goes in.
//
// 0o600 because the file carries credentials: the demo's own admin
// certificate, and in a run with tenants the shard admin's, which can
// impersonate anybody.
func writeKubeconfig(path string, cfg *clientcmdapi.Config) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}
