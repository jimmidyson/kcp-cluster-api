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
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/jimmidyson/kcp-cluster-api/internal/demo"
)

// The two identities a kcp shard mints for itself, reproduced as client
// certificates.
//
// These are not names this project chose: they are what kcp's own
// admin.kubeconfig authenticates as, read back from a running server with a
// SelfSubjectReview. A deployment issues certificates carrying them so that
// everything downstream - the demo, the managers, a person with kubectl - is
// the same user against a deployed shard as against a local one, and no code
// has to know which it is talking to.
//
// The distinction between them matters for one thing: impersonating a tenant.
// kcp scopes an impersonated user to the logical cluster the request addresses
// unless the impersonator is in system:masters, so the demo's access table
// needs ShardAdminUser and cannot be built by KcpAdminUser. See
// demo.Options.ImpersonationConfig.
const (
	KcpAdminUser   = "kcp-admin"
	KcpAdminGroup  = "system:kcp:admin"
	ShardAdminUser = "shard-admin"
	// ShardAdminGroup is system:masters: the shard admin is privileged on the
	// shard itself, which is what makes it the impersonator.
	ShardAdminGroup = "system:masters"
)

// Credentials is everything a client needs to reach a deployed kcp, and
// everything kcp needs to serve and authenticate them.
//
// It is generated once per deployment, before kcp starts. That ordering is the
// point: kcp writes its own kubeconfig inside its pod, where nothing else can
// read it without a sidecar or a shared volume, so a deployment that waited
// for that file would have to grow machinery to fetch it. Issuing the
// credentials first inverts the problem - kcp is handed a serving certificate
// and a client CA, and every kubeconfig is known before the first pod starts.
type Credentials struct {
	// Serving is the certificate kcp serves with, valid for its Service DNS
	// names and for localhost - the latter so the same file works through
	// `kubectl port-forward`.
	Serving KeyPair

	// ServingCA is what a client trusts to verify Serving.
	ServingCA []byte

	// ClientCA is what kcp verifies client certificates against
	// (--client-ca-file).
	ClientCA []byte

	// Admin and ShardAdmin are the two identities above.
	Admin      KeyPair
	ShardAdmin KeyPair
}

// NewCredentials issues the PKI for a deployment serving on the given DNS
// names and IPs.
func NewCredentials(commonName string, dnsNames []string, ips []net.IP, validity time.Duration) (Credentials, error) {
	serving, err := NewAuthority("kcp-serving-ca", validity)
	if err != nil {
		return Credentials{}, err
	}
	// A separate authority for clients, rather than one CA doing both jobs.
	// The serving CA is handed to every client that talks to kcp; the client
	// CA is what kcp accepts identities from. Sharing them would mean anything
	// trusting the server also holds the authority that mints administrators.
	clients, err := NewAuthority("kcp-client-ca", validity)
	if err != nil {
		return Credentials{}, err
	}

	servingCert, err := serving.IssueServer(commonName, dnsNames, ips, validity)
	if err != nil {
		return Credentials{}, err
	}
	admin, err := clients.IssueClient(KcpAdminUser, []string{KcpAdminGroup}, validity)
	if err != nil {
		return Credentials{}, err
	}
	shardAdmin, err := clients.IssueClient(ShardAdminUser, []string{ShardAdminGroup}, validity)
	if err != nil {
		return Credentials{}, err
	}

	return Credentials{
		Serving:    servingCert,
		ServingCA:  serving.CertPEM,
		ClientCA:   clients.CertPEM,
		Admin:      admin,
		ShardAdmin: shardAdmin,
	}, nil
}

// Kubeconfig builds a kubeconfig for a kcp reached at server, with the same
// contexts a kcp server writes for itself:
//
//   - base, the cluster-unaware endpoint, which anything that scopes itself to
//     a workspace needs (the demo, and the workspace manager);
//   - one named after parent, scoped to the workspace the APIExports live in,
//     which is what every provider manager reads its APIExportEndpointSlice
//     from;
//   - shard-base, the same endpoint as the shard admin, for impersonation.
//
// currentContext decides which of them a client that selects none gets. It
// matters because controller-runtime's --kubeconfig has no companion
// --context: a manager takes the current context or nothing, so the two kinds
// of manager are given files whose current context differs rather than a flag.
func Kubeconfig(server, parent, currentContext string, creds Credentials) ([]byte, error) {
	if parent == "" {
		parent = demo.DefaultParent
	}
	if _, err := url.Parse(server); err != nil {
		return nil, fmt.Errorf("parsing the kcp server URL %q: %w", server, err)
	}
	scoped := server + "/clusters/" + logicalcluster.NewPath(parent).String()

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[demo.BaseContext] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: creds.ServingCA,
	}
	cfg.Clusters[parent] = &clientcmdapi.Cluster{
		Server:                   scoped,
		CertificateAuthorityData: creds.ServingCA,
	}
	cfg.AuthInfos[KcpAdminUser] = &clientcmdapi.AuthInfo{
		ClientCertificateData: creds.Admin.CertPEM,
		ClientKeyData:         creds.Admin.KeyPEM,
	}
	cfg.AuthInfos[ShardAdminUser] = &clientcmdapi.AuthInfo{
		ClientCertificateData: creds.ShardAdmin.CertPEM,
		ClientKeyData:         creds.ShardAdmin.KeyPEM,
	}
	cfg.Contexts[demo.BaseContext] = &clientcmdapi.Context{
		Cluster:  demo.BaseContext,
		AuthInfo: KcpAdminUser,
	}
	cfg.Contexts[parent] = &clientcmdapi.Context{
		Cluster:  parent,
		AuthInfo: KcpAdminUser,
	}
	cfg.Contexts[demo.ShardBaseContext] = &clientcmdapi.Context{
		Cluster:  demo.BaseContext,
		AuthInfo: ShardAdminUser,
	}

	if currentContext == "" {
		currentContext = parent
	}
	if _, ok := cfg.Contexts[currentContext]; !ok {
		return nil, fmt.Errorf("no context named %q to make current: this kubeconfig has %s, %s and %s",
			currentContext, demo.BaseContext, parent, demo.ShardBaseContext)
	}
	cfg.CurrentContext = currentContext

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serialising the kcp kubeconfig: %w", err)
	}
	return out, nil
}

// LoadCredentials reads back the credentials an installation is already
// running with.
//
// A second `deploy` against the same namespace has to converge rather than
// rotate: fresh credentials would hand the shard a new client CA and every
// existing client a certificate it no longer accepts, which is a redeploy that
// breaks what it redeploys. So the second run reuses what the first one
// issued, and only an installation that was deleted gets a new set.
//
// Reconstructed from the objects rather than kept anywhere: the serving pair
// is its Secret, the client CA is its Secret, and the CA a client trusts and
// the two client certificates are in the kubeconfig, which is the same file
// every pod already mounts. Nothing is stored twice.
func LoadCredentials(kubeconfig, serving, clientCA *corev1.Secret) (Credentials, error) {
	if kubeconfig == nil || serving == nil || clientCA == nil {
		return Credentials{}, errors.New("an installation's credentials are three Secrets, and one of them is missing")
	}

	cfg, err := clientcmd.Load(kubeconfig.Data[BaseKubeconfigKey])
	if err != nil {
		return Credentials{}, fmt.Errorf("reading %s back out of the %s Secret: %w",
			BaseKubeconfigKey, KubeconfigSecretName, err)
	}
	cluster, ok := cfg.Clusters[demo.BaseContext]
	if !ok {
		return Credentials{}, fmt.Errorf("the %s kubeconfig has no %s cluster", KubeconfigSecretName, demo.BaseContext)
	}
	admin, ok := cfg.AuthInfos[KcpAdminUser]
	if !ok {
		return Credentials{}, fmt.Errorf("the %s kubeconfig has no %s credential", KubeconfigSecretName, KcpAdminUser)
	}
	shardAdmin, ok := cfg.AuthInfos[ShardAdminUser]
	if !ok {
		return Credentials{}, fmt.Errorf("the %s kubeconfig has no %s credential", KubeconfigSecretName, ShardAdminUser)
	}

	creds := Credentials{
		Serving: KeyPair{
			CertPEM: serving.Data[corev1.TLSCertKey],
			KeyPEM:  serving.Data[corev1.TLSPrivateKeyKey],
		},
		ServingCA:  cluster.CertificateAuthorityData,
		ClientCA:   clientCA.Data["ca.crt"],
		Admin:      KeyPair{CertPEM: admin.ClientCertificateData, KeyPEM: admin.ClientKeyData},
		ShardAdmin: KeyPair{CertPEM: shardAdmin.ClientCertificateData, KeyPEM: shardAdmin.ClientKeyData},
	}
	for what, pem := range map[string][]byte{
		"the serving certificate": creds.Serving.CertPEM,
		"the serving key":         creds.Serving.KeyPEM,
		"the serving CA":          creds.ServingCA,
		"the client CA":           creds.ClientCA,
		"the admin certificate":   creds.Admin.CertPEM,
		"the shard admin's key":   creds.ShardAdmin.KeyPEM,
	} {
		if len(pem) == 0 {
			return Credentials{}, fmt.Errorf("the installation's Secrets hold no %s", what)
		}
	}
	return creds, nil
}
