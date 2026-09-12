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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/secret"
)

// certificateRequeue is how long a Cluster waits between looks for a CA that
// something else is expected to write. It only applies when this process is
// not generating the cluster secrets itself.
const certificateRequeue = 5 * time.Second

// ClusterReconciler gives each Cluster a fake API server at its declared
// control plane endpoint.
//
// It is keyed on the Cluster's name and namespace alone, and the fake state
// lives in this process, so a Cluster that has gone is torn down from its key
// rather than through a finalizer: nothing in the management cluster records
// that this process was ever involved.
type ClusterReconciler struct {
	client.Client
	Backend *Backend

	// GenerateClusterSecrets writes the cluster's CA certificates and its
	// kubeconfig secret when they are absent, standing in for a control plane
	// provider. See Options.
	GenerateClusterSecrets bool
}

// Reconcile serves a Cluster's endpoint once the Cluster declares one, and
// stops serving it once the Cluster is gone.
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	key := req.NamespacedName.String()

	cluster := &clusterv1.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.teardown(key)
		}
		return ctrl.Result{}, err
	}

	endpoint := cluster.Spec.ControlPlaneEndpoint
	if !endpoint.IsValid() {
		log.V(4).Info("Waiting for the Cluster to declare a control plane endpoint")
		return ctrl.Result{}, nil
	}
	if endpoint.Host != r.Backend.Host {
		// Not an error to retry: the endpoint is user input on the
		// infrastructure cluster, and it names somewhere this process does
		// not listen. Say so and wait for it to change.
		log.Info("Not serving the Cluster's control plane endpoint: its host is not this backend's",
			"endpoint", endpoint.String(), "host", r.Backend.Host)
		return ctrl.Result{}, nil
	}

	if r.GenerateClusterSecrets {
		if err := r.ensureClusterSecrets(ctx, cluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("generating cluster secrets for %s: %w", key, err)
		}
	}

	listener, err := r.Backend.Mux.InitWorkloadClusterListenerWithPort(key, endpoint.Port)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("initialising the listener for %s: %w", key, err)
	}
	if listener.Port() != endpoint.Port {
		log.Info("Not serving the Cluster's control plane endpoint: the listener was opened at another port and an endpoint cannot move",
			"endpoint", endpoint.String(), "listener", listener.Address())
		return ctrl.Result{}, nil
	}
	if err := r.Backend.Mux.RegisterResourceGroup(key, key); err != nil {
		return ctrl.Result{}, fmt.Errorf("registering the resource group for %s: %w", key, err)
	}
	r.Backend.Manager.AddResourceGroup(key)

	apiServer := apiServerName(cluster)
	if r.Backend.Mux.HasAPIServer(key, apiServer) {
		return ctrl.Result{}, nil
	}

	caCert, caKey, err := r.clusterCA(ctx, cluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(4).Info("Waiting for the cluster CA secret", "secret", secret.Name(cluster.Name, secret.ClusterCA))
			return ctrl.Result{RequeueAfter: certificateRequeue}, nil
		}
		return ctrl.Result{}, err
	}
	if err := r.Backend.Mux.AddAPIServer(key, apiServer, caCert, caKey); err != nil {
		return ctrl.Result{}, fmt.Errorf("starting the API server for %s: %w", key, err)
	}
	log.Info("Serving the Cluster's control plane endpoint", "endpoint", endpoint.String())
	return ctrl.Result{}, nil
}

// teardown releases everything held for a Cluster that no longer exists.
func (r *ClusterReconciler) teardown(key string) error {
	if err := r.Backend.Mux.DeleteWorkloadClusterListener(key); err != nil {
		return fmt.Errorf("deleting the listener for %s: %w", key, err)
	}
	r.Backend.Manager.DeleteResourceGroup(key)
	return nil
}

// ensureClusterSecrets does what a control plane provider would on its first
// reconcile: generate the cluster's certificate authorities and a kubeconfig
// pointing at the control plane endpoint. Both are looked up before they are
// generated, so a control plane provider that got there first wins.
func (r *ClusterReconciler) ensureClusterSecrets(ctx context.Context, cluster *clusterv1.Cluster) error {
	owner := metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
	}
	certificates := secret.NewCertificatesForInitialControlPlane(nil)
	if err := certificates.LookupOrGenerate(ctx, r.Client, client.ObjectKeyFromObject(cluster), owner); err != nil {
		return fmt.Errorf("certificates: %w", err)
	}

	if _, err := secret.Get(ctx, r.Client, client.ObjectKeyFromObject(cluster), secret.Kubeconfig); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	if err := kubeconfig.CreateSecret(ctx, r.Client, cluster); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	return nil
}

// clusterCA reads the cluster's CA, which signs the fake API server's serving
// certificate and the admin credentials the kubeconfig carries.
func (r *ClusterReconciler) clusterCA(ctx context.Context, cluster *clusterv1.Cluster) (*x509.Certificate, *rsa.PrivateKey, error) {
	s, err := secret.Get(ctx, r.Client, client.ObjectKeyFromObject(cluster), secret.ClusterCA)
	if err != nil {
		return nil, nil, err
	}
	cert, err := certs.DecodeCertPEM(s.Data[secret.TLSCrtDataName])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding the cluster CA certificate: %w", err)
	}
	signer, err := certs.DecodePrivateKeyPEM(s.Data[secret.TLSKeyDataName])
	if err != nil {
		return nil, nil, fmt.Errorf("decoding the cluster CA key: %w", err)
	}
	key, ok := signer.(*rsa.PrivateKey)
	if !ok {
		// The mux signs with RSA only. Cluster API's own generators produce
		// RSA unless the bootstrap config asks for ECDSA, which nothing in a
		// simulation should.
		return nil, nil, fmt.Errorf("the cluster CA key is %T, and the fake API server can only sign with RSA", signer)
	}
	return cert, key, nil
}

// apiServerName is the name of the one fake kube-apiserver instance a Cluster
// gets. The mux counts instances to decide when to stop listening, so it has
// to be stable across reconciles.
func apiServerName(cluster *clusterv1.Cluster) string {
	return "kube-apiserver-" + cluster.Name
}
