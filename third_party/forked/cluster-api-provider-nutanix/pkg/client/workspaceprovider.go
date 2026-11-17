/*
Copyright 2026 Nutanix

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

package client

import (
	"context"
	"fmt"
	"net/url"

	corev1 "k8s.io/api/core/v1"
	ctlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nutanix-cloud-native/prism-go-client/environment/credentials"
	envTypes "github.com/nutanix-cloud-native/prism-go-client/environment/types"
)

// certBundleDataKey is the key an additional trust bundle is stored under, in
// either a ConfigMap's Data or its BinaryData. It matches the SDK's Kubernetes
// provider, which this one replaces.
const certBundleDataKey = "ca.crt"

// clientProvider resolves a Prism endpoint's credentials through a
// controller-runtime client rather than through shared informers.
//
// # Why not the SDK's Kubernetes provider
//
// That provider reads its Secret and ConfigMap from a SharedInformerFactory,
// which is built over one clientset and so addresses one API server. Under kcp
// a workspace is a whole API server and one process serves many, and the
// informer interface offers nowhere to say which: Lister().Secrets(ns).Get(name)
// takes no context. Two workspaces holding a credentials Secret of the same
// namespace and name are indistinguishable to it, so one tenant's Prism
// credentials are handed to another tenant's cluster — silently, because the
// wrong credentials are still valid credentials.
//
// A controller-runtime client resolves per call against the cluster named in
// the context, so the reader and the context captured here are the workspace.
// The cost is that each lookup is an API read rather than a cache hit; the
// clients built from them are still cached, keyed per workspace, so this is
// paid once per Prism client rather than once per reconcile.
type clientProvider struct {
	prismEndpoint credentials.NutanixPrismEndpoint

	// reader addresses the workspace the NutanixCluster was read from, and ctx
	// is what carries that. They are captured together because neither is
	// meaningful without the other.
	reader ctlclient.Reader
	ctx    context.Context //nolint:containedctx // see the field comment above
}

// NewWorkspaceProvider builds an environment provider that reads credentials
// from the cluster named in ctx.
func NewWorkspaceProvider(ctx context.Context, reader ctlclient.Reader, prismEndpoint credentials.NutanixPrismEndpoint) envTypes.Provider {
	return &clientProvider{prismEndpoint: prismEndpoint, reader: reader, ctx: ctx}
}

func (p *clientProvider) getAdditionalTrustBundle() (string, error) {
	ref := p.prismEndpoint.AdditionalTrustBundle
	if ref == nil {
		return "", nil
	}
	if ref.Kind == credentials.NutanixTrustBundleKindString {
		return ref.Data, nil
	}

	cm := &corev1.ConfigMap{}
	key := ctlclient.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
	if err := p.reader.Get(p.ctx, key, cm); err != nil {
		return "", fmt.Errorf("reading trust bundle ConfigMap %s: %w", key, err)
	}
	if cert, ok := cm.Data[certBundleDataKey]; ok {
		return cert, nil
	}
	if cert, ok := cm.BinaryData[certBundleDataKey]; ok {
		return string(cert), nil
	}
	return "", nil
}

func (p *clientProvider) getCredentials() (*envTypes.ApiCredentials, error) {
	ref := p.prismEndpoint.CredentialRef
	if ref == nil {
		return nil, ErrCredentialRefNotSet
	}

	secret := &corev1.Secret{}
	key := ctlclient.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
	if err := p.reader.Get(p.ctx, key, secret); err != nil {
		return nil, fmt.Errorf("reading credentials Secret %s: %w", key, err)
	}

	credsData, ok := secret.Data[credentials.KeyName]
	if !ok {
		return nil, fmt.Errorf("no %q data found in secret %s", credentials.KeyName, key)
	}

	//nolint:wrapcheck // the SDK's parse error is already descriptive
	return credentials.ParseCredentials(credsData)
}

// GetManagementEndpoint retrieves the management endpoint, credentials and all.
func (p *clientProvider) GetManagementEndpoint(_ envTypes.Topology) (*envTypes.ManagementEndpoint, error) {
	creds, err := p.getCredentials()
	if err != nil {
		return nil, err
	}
	addr, err := url.Parse(fmt.Sprintf("https://%s:%d", p.prismEndpoint.Address, p.prismEndpoint.Port))
	if err != nil {
		return nil, fmt.Errorf("parsing the Prism Central address: %w", err)
	}
	trustBundle, err := p.getAdditionalTrustBundle()
	if err != nil {
		return nil, err
	}
	return &envTypes.ManagementEndpoint{
		Address:               addr,
		Insecure:              p.prismEndpoint.Insecure,
		AdditionalTrustBundle: trustBundle,
		ApiCredentials:        *creds,
	}, nil
}

// Get retrieves environment settings, of which this provider has none. It
// matches the SDK's Kubernetes provider, which returns the same.
func (p *clientProvider) Get(_ envTypes.Topology, _ string) (interface{}, error) {
	return nil, envTypes.ErrNotFound
}
