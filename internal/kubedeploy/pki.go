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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

// DefaultCertificateValidity is how long the generated certificates last.
//
// A year, because these belong to one installation of a demo rather than to a
// production control plane: short enough that nothing here is mistaken for a
// credential to keep, long enough that a cluster left up over a holiday still
// works when somebody comes back to it. Nothing rotates them; a redeploy
// issues a new set.
const DefaultCertificateValidity = 365 * 24 * time.Hour

// KeyPair is a certificate and the private key it was issued against, PEM
// encoded, ready to become the two halves of a kubernetes.io/tls Secret.
type KeyPair struct {
	CertPEM []byte
	KeyPEM  []byte
}

// Authority is a certificate authority this package created and can issue
// from.
//
// There is one of these per deployment and it exists for the length of a
// `deploy` run: the key is never written down, because nothing after the run
// issues anything. What outlives it are the certificates in the Secrets and
// the CA certificate the clients trust.
type Authority struct {
	// CertPEM is what a client trusts, or what kcp verifies clients against.
	CertPEM []byte

	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewAuthority creates a self-signed certificate authority.
func NewAuthority(commonName string, validity time.Duration) (*Authority, error) {
	if validity <= 0 {
		validity = DefaultCertificateValidity
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating a key for %s: %w", commonName, err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("creating the %s certificate: %w", commonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing the %s certificate back: %w", commonName, err)
	}

	return &Authority{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		cert:    cert,
		key:     key,
	}, nil
}

// IssueServer issues a serving certificate for the given names.
//
// The names are the whole reason this package issues certificates at all. kcp
// generates its own serving certificate when it is given none, and that one
// names "localhost" and a placeholder IP - which is right for a process on the
// machine that talks to it and wrong for every client in a Kubernetes cluster,
// where kcp is reached by the DNS name of its Service. There is no kcp flag
// that adds a name to the certificate it generates, so the certificate has to
// come from outside.
func (a *Authority) IssueServer(commonName string, dnsNames []string, ips []net.IP, validity time.Duration) (KeyPair, error) {
	return a.issue(commonName, nil, dnsNames, ips,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, validity)
}

// IssueClient issues a client certificate for a user in the given groups.
//
// kcp authenticates these exactly as a Kubernetes API server does: the common
// name is the username and each organization is a group. That is what lets a
// deployment reproduce the two identities kcp mints for itself - see
// KcpAdminUser and ShardAdminUser.
func (a *Authority) IssueClient(commonName string, groups []string, validity time.Duration) (KeyPair, error) {
	return a.issue(commonName, groups, nil, nil,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, validity)
}

func (a *Authority) issue(
	commonName string,
	groups []string,
	dnsNames []string,
	ips []net.IP,
	usages []x509.ExtKeyUsage,
	validity time.Duration,
) (KeyPair, error) {
	if validity <= 0 {
		validity = DefaultCertificateValidity
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generating a key for %s: %w", commonName, err)
	}
	serial, err := serialNumber()
	if err != nil {
		return KeyPair{}, err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: groups,
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.cert, key.Public(), a.key)
	if err != nil {
		return KeyPair{}, fmt.Errorf("creating the %s certificate: %w", commonName, err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return KeyPair{}, fmt.Errorf("encoding the %s key: %w", commonName, err)
	}

	return KeyPair{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// serialNumber returns a random 128-bit serial, which is what a certificate
// nobody keeps a register for needs: unique by construction rather than by
// bookkeeping.
func serialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generating a certificate serial number: %w", err)
	}
	return serial, nil
}
