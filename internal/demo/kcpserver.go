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
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// BaseContext is the kubeconfig context of a kcp server's cluster-unaware
// endpoint: the one a caller scopes to a workspace itself, rather than one
// already pointed at a logical cluster.
const BaseContext = "base"

// KcpServer is a kcp server process this package started.
type KcpServer struct {
	// BaseConfig addresses the server, cluster-unaware.
	BaseConfig *rest.Config

	// KubeconfigPath is where the server wrote its admin kubeconfig, so a
	// person can point kubectl at the same server the demo is using.
	KubeconfigPath string

	// LogPath is where the server's own output went.
	LogPath string

	cmd *exec.Cmd
}

// Stop terminates the server and waits for it to exit.
func (s *KcpServer) Stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

// StartKcp starts a single-shard kcp server with its state under dir and
// waits for it to serve requests.
//
// The demo starts its own rather than requiring one because "run the demo" and
// "have a kcp server" are the same request in practice, and the second half is
// the part that has no obvious answer for someone meeting the project. Point
// the demo at an existing server instead by giving it a kubeconfig.
//
// The binary is resolved from PATH as "kcp", which is where `task tools` puts
// the pinned one (bin/).
func StartKcp(ctx context.Context, dir string, timeout time.Duration, log logr.Logger) (*KcpServer, error) {
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating the kcp state directory %s: %w", dir, err)
	}

	binary, err := exec.LookPath("kcp")
	if err != nil {
		return nil, fmt.Errorf("no kcp binary on PATH: run `task tools` and add bin/ to PATH: %w", err)
	}

	logPath := filepath.Join(dir, "kcp.log")
	logFile, err := os.Create(logPath) //nolint:gosec // dir is operator-supplied, not user input.
	if err != nil {
		return nil, fmt.Errorf("creating the kcp log file: %w", err)
	}
	defer logFile.Close() //nolint:errcheck // the process holds its own handle.

	// Ports are chosen rather than defaulted. A demo that took kcp's default
	// 6443 would collide with a Kubernetes API server, with a second demo, and
	// with the kcp somebody left running from the last one - each of which
	// fails as "address already in use" inside a server log nobody has opened
	// yet.
	ports, err := freePorts(3)
	if err != nil {
		return nil, err
	}

	// Every URL kcp hands out is pinned to localhost, not left to the address
	// it detects for itself. Three of them matter and only the first is
	// visible in the kubeconfig: the shard's base and external URLs, and the
	// virtual workspace URL that ends up in the APIExportEndpointSlice - which
	// is the address the manager connects to, so leaving that one alone
	// undoes the fix for the other two.
	//
	// Both reasons from viaLoopback apply, and the second one bites hardest
	// here: an HTTPS proxy in the environment swallows the manager's
	// connection to a virtual workspace on a non-local-looking address, and
	// the failure surfaces halfway through wiring the reconcilers as a reset
	// connection rather than as anything about proxies.
	local := fmt.Sprintf("https://localhost:%d", ports[0])
	cmd := exec.CommandContext(ctx, binary, "start", //nolint:gosec // binary is resolved from PATH.
		"--root-directory", dir,
		fmt.Sprintf("--secure-port=%d", ports[0]),
		fmt.Sprintf("--embedded-etcd-client-port=%d", ports[1]),
		fmt.Sprintf("--embedded-etcd-peer-port=%d", ports[2]),
		"--shard-base-url="+local,
		"--shard-external-url="+local,
		"--shard-virtual-workspace-url="+local,
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting kcp: %w", err)
	}

	server := &KcpServer{
		KubeconfigPath: filepath.Join(dir, "admin.kubeconfig"),
		LogPath:        logPath,
		cmd:            cmd,
	}

	log.Info("Starting kcp", "binary", binary, "directory", dir, "log", logPath)
	cfg, err := waitForKcp(ctx, server.KubeconfigPath, timeout)
	if err != nil {
		server.Stop()
		return nil, fmt.Errorf("%w (see %s)", err, logPath)
	}
	server.BaseConfig = cfg

	log.Info("kcp is serving", "kubeconfig", server.KubeconfigPath)
	return server, nil
}

// waitForKcp polls until the server has written its kubeconfig and answers a
// request through it.
//
// Both halves matter: the kubeconfig appears well before the server is ready,
// so a demo that waited only for the file would fail its first call with a
// connection refused that looks like a configuration error.
//
// The last failure is carried out of the poll rather than discarded. A demo
// that says only "did not become ready" makes the reader open a 500,000-line
// server log to find out that a port was taken.
func waitForKcp(ctx context.Context, kubeconfigPath string, timeout time.Duration) (*rest.Config, error) {
	var (
		cfg  *rest.Config
		last error
	)
	err := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if _, err := os.Stat(kubeconfigPath); err != nil {
			last = err
			return false, nil
		}
		built, err := ConfigFromKubeconfig(kubeconfigPath, BaseContext)
		if err != nil {
			last = err
			return false, nil
		}
		built = viaLoopback(built)
		if err := ping(ctx, built); err != nil {
			last = err
			return false, nil
		}
		cfg = built
		return true, nil
	})
	if err != nil {
		if last != nil {
			return nil, fmt.Errorf("kcp did not become ready: %w (last attempt: %v)", err, last)
		}
		return nil, fmt.Errorf("kcp did not become ready: %w", err)
	}
	return cfg, nil
}

// freePorts returns n ports nothing is listening on.
//
// Racy by nature - anything could take one between the check and kcp's bind -
// but the window is small and the alternative is a fixed port, which does not
// race so much as reliably collide.
func freePorts(n int) ([]int, error) {
	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()

	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("finding a free port: %w", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// viaLoopback points a config at localhost, keeping its port and path.
//
// A kcp server writes its kubeconfig against the address it advertises, which
// is whatever address the machine has - and for a server this process started
// on this machine, that address is the wrong way to reach it twice over. It
// need not be routable from here at all, and where an HTTPS proxy is
// configured in the environment (as it is in this project's own sandboxes and
// in most corporate networks), client-go will send the connection to the proxy
// rather than to the local port, where it times out looking like a server that
// never came up. localhost is in every sensible no-proxy list and is a SAN on
// the certificate kcp generates, so it avoids both.
//
// Only for a server this package started. A config for somebody else's kcp is
// used exactly as given.
func viaLoopback(cfg *rest.Config) *rest.Config {
	u, err := url.Parse(cfg.Host)
	if err != nil || u.Host == "" {
		return cfg
	}
	port := u.Port()
	u.Host = "localhost"
	if port != "" {
		u.Host = net.JoinHostPort("localhost", port)
	}

	out := rest.CopyConfig(cfg)
	out.Host = u.String()
	return out
}

// ConfigFromKubeconfig builds a config from an existing kubeconfig and
// context, for a demo run against a kcp server somebody else started.
//
// The context has to be a cluster-unaware one: everything here scopes the
// config to a workspace itself, and scoping an already-scoped config produces
// a URL with two /clusters/ segments and a 404 that says nothing useful.
func ConfigFromKubeconfig(path, context string) (*rest.Config, error) {
	if context == "" {
		context = BaseContext
	}
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loader, &clientcmd.ConfigOverrides{CurrentContext: context}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building a config from %s (context %q): %w", path, context, err)
	}
	return cfg, nil
}
