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
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"slices"
	"testing"
	"time"
)

// The serving certificate has to name the Service, because that is how every
// client inside the cluster addresses the shard - and localhost, because that
// is how the same certificate serves a port-forward. kcp offers no flag that
// adds a name to the certificate it generates for itself, which is why this
// package issues one.
func TestIssueServerNamesTheServiceAndLocalhost(t *testing.T) {
	t.Parallel()

	ca, err := NewAuthority("test-ca", time.Hour)
	if err != nil {
		t.Fatalf("creating the authority: %v", err)
	}
	names := ServerNames("kcp-demo")
	pair, err := ca.IssueServer(KcpName, names, []net.IP{net.ParseIP("127.0.0.1")}, time.Hour)
	if err != nil {
		t.Fatalf("issuing the serving certificate: %v", err)
	}

	cert := parse(t, pair.CertPEM)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("the authority's certificate is not PEM the client can trust")
	}
	for _, name := range names {
		if _, err := cert.Verify(x509.VerifyOptions{
			DNSName:   name,
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("a client asking for %q would reject the certificate: %v", name, err)
		}
	}
	if !slices.ContainsFunc(cert.IPAddresses, func(ip net.IP) bool { return ip.Equal(net.ParseIP("127.0.0.1")) }) {
		t.Error("the certificate names no loopback address, so a port-forward would fail verification")
	}
}

// kcp authenticates a client certificate as a Kubernetes API server does: the
// common name is the user and each organization is a group. Reproducing the
// shard's own two identities is the whole reason this package issues client
// certificates rather than fetching kcp's tokens out of its pod.
func TestIssueClientCarriesTheUserAndGroups(t *testing.T) {
	t.Parallel()

	ca, err := NewAuthority("test-ca", time.Hour)
	if err != nil {
		t.Fatalf("creating the authority: %v", err)
	}
	pair, err := ca.IssueClient(ShardAdminUser, []string{ShardAdminGroup}, time.Hour)
	if err != nil {
		t.Fatalf("issuing the client certificate: %v", err)
	}

	cert := parse(t, pair.CertPEM)
	if cert.Subject.CommonName != ShardAdminUser {
		t.Errorf("username is %q, want %q", cert.Subject.CommonName, ShardAdminUser)
	}
	if !slices.Contains(cert.Subject.Organization, ShardAdminGroup) {
		t.Errorf("groups are %v, want them to include %q", cert.Subject.Organization, ShardAdminGroup)
	}
	if !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Error("the certificate is not marked for client authentication, which an API server requires")
	}
	if _, err := tls.X509KeyPair(pair.CertPEM, pair.KeyPEM); err != nil {
		t.Errorf("the certificate and key are not a usable pair: %v", err)
	}
}

// A certificate authority signs; it does not serve. Getting this wrong is the
// kind of thing that works against a permissive client and fails against a
// strict one.
func TestAuthorityIsACertificateAuthority(t *testing.T) {
	t.Parallel()

	ca, err := NewAuthority("test-ca", time.Hour)
	if err != nil {
		t.Fatalf("creating the authority: %v", err)
	}
	cert := parse(t, ca.CertPEM)
	if !cert.IsCA || !cert.BasicConstraintsValid {
		t.Error("the authority's certificate is not marked as a CA")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("the authority's certificate may not sign certificates")
	}
}

func parse(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing the certificate: %v", err)
	}
	return cert
}
