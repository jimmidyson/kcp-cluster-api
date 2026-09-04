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

package deployedscale

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

func testCredentials(t *testing.T) *Credentials {
	t.Helper()
	creds, err := NewCredentials(ServiceNames("kcp", "scale"), LoopbackIPs(), time.Hour)
	if err != nil {
		t.Fatalf("minting credentials: %v", err)
	}
	return creds
}

// TestServingCertificateVerifiesForEveryNameKcpIsAddressedBy is the property
// the whole generated-credentials design exists for. One deployment is reached
// by a Service DNS name from inside the cluster and by a forwarded loopback
// port from outside it, and both must verify against the one CA.
func TestServingCertificateVerifiesForEveryNameKcpIsAddressedBy(t *testing.T) {
	creds := testCredentials(t)

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(creds.CACertPEM) {
		t.Fatal("the CA PEM did not parse")
	}
	block, _ := pem.Decode(creds.ServingCertPEM)
	if block == nil {
		t.Fatal("the serving certificate PEM did not parse")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing the serving certificate: %v", err)
	}

	for _, name := range []string{
		"kcp",
		"kcp.scale",
		"kcp.scale.svc",
		"kcp.scale.svc.cluster.local",
		"localhost",
		"127.0.0.1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cert.Verify(x509.VerifyOptions{
				DNSName:     name,
				Roots:       roots,
				KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				CurrentTime: time.Now(),
			}); err != nil {
				t.Errorf("a client addressing kcp as %q would fail TLS: %v", name, err)
			}
		})
	}
}

// TestServingCertificateRejectsAnUnrelatedName guards against a certificate so
// permissive that the test above would pass for the wrong reason.
func TestServingCertificateRejectsAnUnrelatedName(t *testing.T) {
	creds := testCredentials(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(creds.CACertPEM)
	block, _ := pem.Decode(creds.ServingCertPEM)
	cert, _ := x509.ParseCertificate(block.Bytes)

	if _, err := cert.Verify(x509.VerifyOptions{
		DNSName:   "kcp.example.com",
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Error("the serving certificate verified for a name kcp is never addressed by")
	}
}

func TestKubeconfigAddressesTheGivenServerAndTrustsTheCA(t *testing.T) {
	creds := testCredentials(t)

	for _, server := range []string{"https://kcp.scale.svc:6443", "https://127.0.0.1:38121"} {
		raw, err := creds.Kubeconfig(server)
		if err != nil {
			t.Fatalf("building a kubeconfig for %s: %v", server, err)
		}
		cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
		if err != nil {
			t.Fatalf("loading the kubeconfig for %s: %v", server, err)
		}
		if cfg.Host != server {
			t.Errorf("host = %q, want %q", cfg.Host, server)
		}
		if cfg.BearerToken != creds.Token {
			t.Error("the kubeconfig does not carry the token kcp was given")
		}
		if len(cfg.CAData) == 0 {
			t.Error("the kubeconfig trusts no CA, so it would not verify kcp")
		}
		if cfg.Insecure {
			t.Error("the kubeconfig skips TLS verification")
		}
	}
}

// TestTokenAuthCSVIsWhatKcpParses pins the format, because a malformed line is
// not rejected loudly — it produces a server that starts and refuses every
// request as unauthenticated.
func TestTokenAuthCSVIsWhatKcpParses(t *testing.T) {
	creds := testCredentials(t)
	line := strings.TrimSuffix(creds.TokenAuthCSV(), "\n")

	fields := strings.Split(line, ",")
	if len(fields) < 4 {
		t.Fatalf("token file line %q has %d fields, want token,user,uid,groups", line, len(fields))
	}
	if fields[0] != creds.Token {
		t.Errorf("the first field is %q, not the token", fields[0])
	}
	if !strings.Contains(line, AdminGroup) {
		t.Errorf("the identity is not in %s, so it has no permissions in a workspace", AdminGroup)
	}
}

// TestTheIdentityIsNotSystemMasters is the regression test for a whole-run
// failure with no obvious cause: every workspace stuck in Initializing on
// system:apibindings, reported as "Initializers still exist".
//
// kcp's workspace admission does not record an owner for a system:masters
// creator, the LogicalCluster it schedules therefore has no spec.createdBy, and
// the initializing virtual workspace — which impersonates that owner, because
// the system:apibindings initializer's WorkspaceType declares no
// initializerPermissions — answers 500 with "had no createdBy recorded". The
// initializer is never removed and nothing else goes wrong, so the run looks
// like a slow workspace rather than an unusable identity.
//
// See AdminGroup.
func TestTheIdentityIsNotSystemMasters(t *testing.T) {
	creds := testCredentials(t)

	var work, profiling string
	for _, line := range strings.Split(strings.TrimSpace(creds.TokenAuthCSV()), "\n") {
		switch {
		case strings.Contains(line, ",kcp-admin,"):
			work = line
		case strings.Contains(line, ",kcp-profiler,"):
			profiling = line
		}
	}

	if work == "" {
		t.Fatal("no kcp-admin line in the token file; the run has no identity")
	}
	if strings.Contains(work, "system:masters") {
		t.Error("the identity that creates workspaces is in system:masters, so kcp will record no " +
			"owner on them and every one will hang in Initializing")
	}
	if !strings.Contains(work, AdminGroup) {
		t.Errorf("the run's identity is not in %s: %s", AdminGroup, work)
	}

	// The privileged identity is allowed to exist, and is allowed to be exactly
	// one thing: the profiler. It reads /debug/pprof, which is a non-resource
	// URL no ClusterRole in :root was able to grant, and it creates nothing —
	// which is what keeps it clear of the createdBy trap above.
	if profiling == "" {
		t.Fatal("no kcp-profiler line; the shard's profiles cannot be read")
	}
	if !strings.Contains(profiling, ProfilingGroup) {
		t.Errorf("the profiling identity is not in %s, so /debug/pprof stays refused: %s",
			ProfilingGroup, profiling)
	}
	if creds.ProfilingToken == creds.Token {
		t.Error("the profiling identity shares the run's token, so the privileged one is not " +
			"separable from the one every manager holds")
	}
	if creds.ProfilingToken == "" {
		t.Error("the profiling token is empty")
	}
}

// TestTokenIsCSVSafe is why the token is base64url rather than raw bytes: a
// comma or a quote in it would silently split the line into different fields.
func TestTokenIsCSVSafe(t *testing.T) {
	for range 50 {
		creds, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
		if err != nil {
			t.Fatalf("minting: %v", err)
		}
		if strings.ContainsAny(creds.Token, ",\"\n\r ") {
			t.Fatalf("token %q contains a character that would break the CSV token file", creds.Token)
		}
	}
}

func TestTokensDiffer(t *testing.T) {
	a, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	b, err := NewCredentials([]string{"kcp"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	if a.Token == b.Token {
		t.Error("two runs minted the same administrative token")
	}
}

func TestNewCredentialsRejectsACertificateValidForNothing(t *testing.T) {
	if _, err := NewCredentials(nil, nil, time.Hour); err == nil {
		t.Error("a serving certificate with no names and no addresses was accepted")
	}
}

func TestServiceNamesCoverEveryInClusterForm(t *testing.T) {
	names := ServiceNames("kcp", "scale")
	for _, want := range []string{"kcp", "kcp.scale", "kcp.scale.svc", "kcp.scale.svc.cluster.local"} {
		if !slices.Contains(names, want) {
			t.Errorf("%q is missing: a client whose search domains resolved that form would fail TLS", want)
		}
	}
}
