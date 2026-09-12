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

package workloadsim

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/secret"
)

// loadCA reads one of a cluster's certificate authorities from its secret.
// The cluster CA signs the fake API server's serving certificate and the
// admin credentials in the kubeconfig; the etcd CA signs each fake etcd
// member's serving certificate.
//
// The mux signs with RSA only. Cluster API's own generators produce RSA
// unless a bootstrap config asks for ECDSA, which nothing in a simulation
// should.
func loadCA(ctx context.Context, c client.Reader, cluster client.ObjectKey, purpose secret.Purpose) (*x509.Certificate, *rsa.PrivateKey, error) {
	s, err := secret.Get(ctx, c, cluster, purpose)
	if err != nil {
		return nil, nil, err
	}
	cert, err := certs.DecodeCertPEM(s.Data[secret.TLSCrtDataName])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding the %s certificate: %w", purpose, err)
	}
	signer, err := certs.DecodePrivateKeyPEM(s.Data[secret.TLSKeyDataName])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding the %s key: %w", purpose, err)
	}
	key, ok := signer.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("the %s key is %T, and the fake servers can only sign with RSA", purpose, signer)
	}
	return cert, key, nil
}
