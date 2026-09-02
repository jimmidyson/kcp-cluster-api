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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Credentials are everything needed to talk to the deployed kcp, and
// everything kcp needs to accept it.
//
// # Why these are generated rather than read back out of kcp
//
// kcp will happily generate its own serving certificate and admin kubeconfig.
// Using them means two problems this avoids entirely.
//
// The first is the certificate's subject alternative names. A self-signed
// serving certificate covers the address kcp detected for itself, and what a
// pod needs is the Service DNS name, which kcp has no way to know. Getting
// that wrong produces a TLS failure at the first request, from a certificate
// nobody can inspect without a shell in the pod.
//
// The second is fetching the admin kubeconfig at all: it is a file inside a
// container, so reading it means an exec into a running pod, which is a
// privilege the harness would otherwise never need and a step that can fail
// halfway.
//
// Generating both sides removes both. The names are known before anything is
// deployed, the token is known before kcp starts, and the same credentials
// serve the managers inside the cluster and the driver outside it — which is
// what lets one measurement address kcp by two different names.
type Credentials struct {
	// CACertPEM signs the serving certificate, and is what every client
	// trusts.
	CACertPEM []byte
	// ServingCertPEM and ServingKeyPEM are kcp's serving certificate, valid
	// for every name in Names.
	ServingCertPEM []byte
	ServingKeyPEM  []byte
	// Token authenticates the managers and the driver.
	Token string
	// ProfilingToken authenticates reads of the shard's own /debug/pprof
	// endpoints, and nothing else. See ProfilingGroup for why it is separate.
	ProfilingToken string
	// Names are the subject alternative names the serving certificate carries.
	Names []string
	// IPs are the IP SANs it carries.
	IPs []net.IP
}

// AdminGroup is the group every identity in the token file belongs to.
//
// # Why not system:masters
//
// system:masters is the obvious choice for a harness that needs to do
// everything, and it is the wrong one: it silently breaks workspace
// initialization. kcp's workspace admission records the creating user on the
// Workspace, and skips doing so when the creator is system:masters:
//
//	isSystemPrivileged := sets.New(a.GetUserInfo().GetGroups()...).Has(kuser.SystemPrivilegedGroup)
//	if !isSystemPrivileged {
//		ws.Annotations[ExperimentalWorkspaceOwnerAnnotationKey] = userInfo
//	}
//
// That annotation is what the scheduler copies into the LogicalCluster's
// spec.createdBy, and spec.createdBy is the identity the initializing virtual
// workspace impersonates when an initializer's WorkspaceType declares no
// initializerPermissions — which is the case for kcp's own system:apibindings.
// With no owner to impersonate the proxy answers 500, the initializer is never
// removed, and every workspace sits in Initializing forever. kcp says so in its
// own source, on the group below:
//
//	We need a separate group (not the privileged system group) for this because
//	system-owned workspaces (e.g. root:users) need a workspace owner annotation
//	set, and the owner annotation is skipped/not set for the privileged system
//	group.
//
// system:kcp:admin is kcp's answer: its bootstrap policy binds cluster-admin to
// it in every workspace, and it is the group kcp puts its own generated
// kcp-admin kubeconfig in. So it is both as privileged as this harness needs
// and an ordinary enough identity to be recorded as an owner.
const AdminGroup = "system:kcp:admin"

// ProfilingGroup is system:masters, and only the profiling identity is in it.
//
// # Why a second identity exists at all
//
// The shard's own /debug/pprof endpoints are not workspace resources. They are
// non-resource URLs, and being cluster-admin in every workspace does not imply
// them — kcp ships system:kcp:metrics-reader as a ClusterRole holding exactly
// one such rule, get on /metrics, because that is the only way to grant one.
// Creating the equivalent ClusterRole and binding for /debug/pprof inside :root
// did not work either: the refusal came back unchanged.
//
// What does work is the bypass kcp inherits from Kubernetes. Its authorization
// options set AlwaysAllowGroups to the privileged group, so system:masters is
// allowed everything, non-resource URLs included.
//
// # Why not simply put the run's identity in it
//
// Because that was tried, and it broke every workspace. kcp's admission
// deliberately records no owner for a system:masters creator, the LogicalCluster
// it schedules therefore has no spec.createdBy, and the initializing virtual
// workspace has nobody to impersonate — so every workspace hangs in Initializing
// on system:apibindings. See AdminGroup.
//
// The two constraints are not in conflict once they are separated. The identity
// that creates workspaces must not be privileged; the identity that reads a
// profile creates nothing. So there are two, and the privileged one is used for
// exactly one thing.
const ProfilingGroup = "system:masters"

// TokenAuthCSV is the file kcp is started with as --token-auth-file.
//
// Two identities. kcp-admin does the run's work — publishing APIExports,
// creating workspaces, reading every one of them — and is deliberately not
// privileged, for the reason on AdminGroup. kcp-profiler exists to read
// /debug/pprof and does nothing else, for the reason on ProfilingGroup.
func (c Credentials) TokenAuthCSV() string {
	return fmt.Sprintf("%s,kcp-admin,kcp-admin,%q\n%s,kcp-profiler,kcp-profiler,%q\n",
		c.Token, AdminGroup, c.ProfilingToken, ProfilingGroup)
}

// Kubeconfig builds a kubeconfig addressing kcp at the given server URL.
//
// The URL is a parameter because one deployment is addressed by two names: the
// managers reach kcp at its Service DNS name, and a driver outside the cluster
// reaches the same server through a forwarded local port. Both are in the
// serving certificate, so both verify against the same CA.
func (c Credentials) Kubeconfig(server string) ([]byte, error) {
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kcp"] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: c.CACertPEM,
	}
	cfg.AuthInfos["kcp-admin"] = &clientcmdapi.AuthInfo{Token: c.Token}
	cfg.Contexts["kcp"] = &clientcmdapi.Context{Cluster: "kcp", AuthInfo: "kcp-admin"}
	cfg.CurrentContext = "kcp"

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serialising the kubeconfig: %w", err)
	}
	return out, nil
}

// NewCredentials mints a CA, a serving certificate covering every name kcp
// will be addressed by, and a bearer token.
//
// The lifetime is deliberately short. These credentials exist for the length of
// one measurement, and a long-lived administrative token generated by a test
// harness is the kind of thing that ends up somewhere it should not be.
func NewCredentials(names []string, ips []net.IP, validFor time.Duration) (*Credentials, error) {
	if len(names) == 0 && len(ips) == 0 {
		return nil, fmt.Errorf("a serving certificate needs at least one name or address to be valid for")
	}

	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	// A distinct secret, not a variant of the first: the profiling identity is
	// privileged and the run's is not, and one leaking must not imply the other.
	profilingToken, err := randomToken()
	if err != nil {
		return nil, err
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the CA key: %w", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kcp-cluster-api-scale-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("self-signing the CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("parsing the CA back: %w", err)
	}

	servingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating the serving key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generating a serial number: %w", err)
	}
	servingTemplate := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(validFor),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  ips,
	}
	servingDER, err := x509.CreateCertificate(rand.Reader, servingTemplate, caCert, &servingKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("signing the serving certificate: %w", err)
	}
	servingKeyDER, err := x509.MarshalECPrivateKey(servingKey)
	if err != nil {
		return nil, fmt.Errorf("marshalling the serving key: %w", err)
	}

	return &Credentials{
		CACertPEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		ServingCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: servingDER}),
		ServingKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: servingKeyDER}),
		Token:          token,
		ProfilingToken: profilingToken,
		Names:          names,
		IPs:            ips,
	}, nil
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a token: %w", err)
	}
	// Base64 without padding: kcp's token file is CSV, and a token containing
	// a comma or a quote would need escaping that nothing here would do.
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ServiceNames are the names a Service is reachable by from inside a cluster,
// plus the loopback names a forwarded port is reached by from outside it.
//
// All four in-cluster forms are included rather than only the fully qualified
// one, because which of them a client uses depends on its own search domains,
// and a certificate that covers only the long form fails for a client that
// resolved the short one.
func ServiceNames(service, namespace string) []string {
	return []string{
		service,
		fmt.Sprintf("%s.%s", service, namespace),
		fmt.Sprintf("%s.%s.svc", service, namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", service, namespace),
		"localhost",
	}
}

// LoopbackIPs are what a port-forwarded connection from outside the cluster
// arrives as.
func LoopbackIPs() []net.IP {
	return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
}

// Fingerprint identifies one set of credentials without disclosing them.
//
// The CA certificate is enough: it is public, it changes whenever the
// credentials are re-minted, and everything a client verifies chains to it. The
// token is deliberately not an input — this value lands in a pod annotation,
// which is readable by anything that can read pods.
func (c Credentials) Fingerprint() string {
	sum := sha256.Sum256(c.CACertPEM)
	return hex.EncodeToString(sum[:])
}
