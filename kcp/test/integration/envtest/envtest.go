// Package envtest is a KCP-aware analogue of controller-runtime's envtest
// package. Plain envtest only stands up a vanilla kube-apiserver and etcd,
// which has no concept of KCP's logical clusters/workspaces, so it cannot
// exercise KCP-aware behavior. This package instead starts a real kcp
// server process (via github.com/kcp-dev/sdk/testing) for the lifetime of
// a test.
package envtest

import (
	"testing"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// DefaultWorkspace is the kubeconfig context name of the logical cluster
// Environment targets when no workspace is given: kcp's initial "root"
// workspace.
const DefaultWorkspace = "root"

// Environment starts an isolated kcp server process for the lifetime of t
// and returns a REST config scoped to the given workspace (a kubeconfig
// context name from the server's generated kubeconfig, e.g. "root"). Pass
// "" to use DefaultWorkspace. The server is stopped automatically via
// t.Cleanup.
//
// A config scoped to a single workspace is what plain client-go and
// controller-runtime clients expect: kcp rejects requests made against the
// bare server root (the "base", cluster-unaware config) since every
// request must target a specific logical cluster.
//
// It requires a kcp server binary discoverable on PATH; see
// `make -C kcp kcp-binary`.
func Environment(t *testing.T, workspace string, opts ...kcptestingserver.Option) *rest.Config {
	t.Helper()

	if workspace == "" {
		workspace = DefaultWorkspace
	}

	opts = append([]kcptestingserver.Option{kcptesting.WithDefaultTokenAuthFile(t)}, opts...)
	server := kcptesting.PrivateKcpServer(t, opts...)

	raw, err := server.RawConfig()
	if err != nil {
		t.Fatalf("failed to load kcp server kubeconfig: %v", err)
	}

	cfg, err := clientcmd.NewNonInteractiveClientConfig(raw, workspace, nil, nil).ClientConfig()
	if err != nil {
		t.Fatalf("failed to build REST config for workspace %q: %v", workspace, err)
	}
	return cfg
}
