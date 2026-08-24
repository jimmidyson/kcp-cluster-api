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

package kubedeploy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// The generated kubeconfig has to work against a server configured the way the
// deployment configures kcp: serving with the certificate this package issued,
// and verifying clients against the client CA it issued. This stands a server
// up on those terms and asks what it sees.
//
// It is a stand-in for kcp and says nothing about kcp's own behaviour. What it
// covers is the half that is this package's to get right: that the two halves
// of the PKI fit each other, that the kubeconfig carries them, and that a
// client-go client built from it authenticates as the user it is supposed to
// be. That the identities it authenticates as are the ones kcp privileges was
// established against a running kcp - see the note on KcpAdminUser.
func TestKubeconfigAuthenticatesAsTheKcpAdmin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		context string
		user    string
		group   string
	}{
		{context: demo.BaseContext, user: KcpAdminUser, group: KcpAdminGroup},
		{context: demo.ShardBaseContext, user: ShardAdminUser, group: ShardAdminGroup},
	} {
		t.Run(tc.context, func(t *testing.T) {
			t.Parallel()

			creds, err := NewCredentials(KcpName, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")}, time.Hour)
			if err != nil {
				t.Fatalf("issuing the credentials: %v", err)
			}
			server, seen := shardLike(t, creds)

			raw, err := Kubeconfig(server, "root", tc.context, creds)
			if err != nil {
				t.Fatalf("building the kubeconfig: %v", err)
			}
			cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
			if err != nil {
				t.Fatalf("reading the kubeconfig back: %v", err)
			}
			client, err := rest.HTTPClientFor(cfg)
			if err != nil {
				t.Fatalf("building a client: %v", err)
			}

			resp, err := client.Get(cfg.Host + "/readyz")
			if err != nil {
				t.Fatalf("calling the server: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck // test.
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("got status %d, want 200", resp.StatusCode)
			}

			if got := seen(); got.user != tc.user {
				t.Errorf("the server saw user %q, want %q", got.user, tc.user)
			} else if !slices.Contains(got.groups, tc.group) {
				t.Errorf("the server saw groups %v, want them to include %q", got.groups, tc.group)
			}
		})
	}
}

// Three contexts, because three kinds of client need three different things
// out of one file: the demo scopes itself and needs the cluster-unaware
// endpoint, a provider manager reads an endpoint slice out of the workspace
// the exports live in, and impersonating a tenant needs the shard admin.
func TestKubeconfigHasAContextForEachKindOfClient(t *testing.T) {
	t.Parallel()

	creds := testCredentials(t)
	raw, err := Kubeconfig("https://kcp.kcp-demo.svc.cluster.local:6443", "root:providers", demo.BaseContext, creds)
	if err != nil {
		t.Fatalf("building the kubeconfig: %v", err)
	}
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatalf("reading the kubeconfig back: %v", err)
	}

	for _, name := range []string{demo.BaseContext, demo.ShardBaseContext, "root:providers"} {
		if _, ok := cfg.Contexts[name]; !ok {
			t.Errorf("no context named %q", name)
		}
	}
	if cfg.CurrentContext != demo.BaseContext {
		t.Errorf("current context is %q, want %q", cfg.CurrentContext, demo.BaseContext)
	}

	base := cfg.Clusters[cfg.Contexts[demo.BaseContext].Cluster].Server
	if base != "https://kcp.kcp-demo.svc.cluster.local:6443" {
		t.Errorf("the base context addresses %q, which is not the Service", base)
	}
	// The scoped one is a URL path, which is the whole of what a kcp workspace
	// is. A manager pointed at the unscoped endpoint reads no endpoint slice.
	scoped := cfg.Clusters[cfg.Contexts["root:providers"].Cluster].Server
	if !strings.HasSuffix(scoped, "/clusters/root:providers") {
		t.Errorf("the provider context addresses %q, want it scoped to the workspace", scoped)
	}
	if user := cfg.Contexts[demo.ShardBaseContext].AuthInfo; user != ShardAdminUser {
		t.Errorf("the %s context authenticates as %q, want %q", demo.ShardBaseContext, user, ShardAdminUser)
	}
}

// The current context is how a manager is told which workspace it addresses,
// because controller-runtime's --kubeconfig has no --context to go with it. A
// name that is not in the file would produce a manager that failed at startup
// with a message about a missing context, so it is refused here instead.
func TestKubeconfigRefusesACurrentContextItDoesNotHave(t *testing.T) {
	t.Parallel()

	_, err := Kubeconfig("https://kcp:6443", "root", "typo", testCredentials(t))
	if err == nil {
		t.Fatal("a kubeconfig was built with a current context it does not contain")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Errorf("error %q does not name the context asked for", err)
	}
}

func testCredentials(t *testing.T) Credentials {
	t.Helper()
	creds, err := NewCredentials(KcpName, ServerNames("kcp-demo"), nil, time.Hour)
	if err != nil {
		t.Fatalf("issuing the credentials: %v", err)
	}
	return creds
}

type identity struct {
	user   string
	groups []string
}

// shardLike serves TLS the way the deployment configures kcp to, and reports
// the client identity it authenticated.
func shardLike(t *testing.T, creds Credentials) (string, func() identity) {
	t.Helper()

	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(creds.ClientCA) {
		t.Fatal("the client CA is not usable as a pool")
	}
	serving, err := tls.X509KeyPair(creds.Serving.CertPEM, creds.Serving.KeyPEM)
	if err != nil {
		t.Fatalf("loading the serving certificate: %v", err)
	}

	var seen identity
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) > 0 {
			seen = identity{
				user:   r.TLS.PeerCertificates[0].Subject.CommonName,
				groups: r.TLS.PeerCertificates[0].Subject.Organization,
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serving},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	// httptest picks a port; the certificate names localhost, so the URL has
	// to as well.
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parsing the test server's URL: %v", err)
	}
	return fmt.Sprintf("https://localhost:%s", parsed.Port()), func() identity { return seen }
}

// A second deploy has to reuse what the first one issued. Fresh credentials
// would hand the shard a client CA that does not match the certificates every
// running pod holds, so the redeploy would break exactly what it redeployed.
func TestLoadCredentialsRoundTripsWhatAnInstallationHolds(t *testing.T) {
	t.Parallel()

	want := testCredentials(t)
	objects, err := Objects(Options{Credentials: want})
	if err != nil {
		t.Fatalf("building the objects: %v", err)
	}

	secret := func(name string) *corev1.Secret {
		for _, obj := range objects {
			if typed, ok := obj.(*corev1.Secret); ok && obj.GetName() == name {
				return typed
			}
		}
		t.Fatalf("no Secret named %q", name)
		return nil
	}

	got, err := LoadCredentials(secret(KubeconfigSecretName), secret(ServingSecretName), secret(ClientCASecretName))
	if err != nil {
		t.Fatalf("reading the credentials back: %v", err)
	}
	for _, pair := range []struct {
		what      string
		want, got []byte
	}{
		{"serving certificate", want.Serving.CertPEM, got.Serving.CertPEM},
		{"serving key", want.Serving.KeyPEM, got.Serving.KeyPEM},
		{"serving CA", want.ServingCA, got.ServingCA},
		{"client CA", want.ClientCA, got.ClientCA},
		{"admin certificate", want.Admin.CertPEM, got.Admin.CertPEM},
		{"admin key", want.Admin.KeyPEM, got.Admin.KeyPEM},
		{"shard admin certificate", want.ShardAdmin.CertPEM, got.ShardAdmin.CertPEM},
		{"shard admin key", want.ShardAdmin.KeyPEM, got.ShardAdmin.KeyPEM},
	} {
		if !bytes.Equal(pair.want, pair.got) {
			t.Errorf("the %s did not survive the round trip", pair.what)
		}
	}
}

// Half an installation is not credentials to reuse. Reporting that as an empty
// set would deploy a shard whose clients hold certificates it does not accept.
func TestLoadCredentialsRefusesAnIncompleteInstallation(t *testing.T) {
	t.Parallel()

	if _, err := LoadCredentials(nil, nil, nil); err == nil {
		t.Error("credentials were reconstructed from nothing")
	}
	if _, err := LoadCredentials(&corev1.Secret{}, &corev1.Secret{}, &corev1.Secret{}); err == nil {
		t.Error("credentials were reconstructed from empty Secrets")
	}
}
