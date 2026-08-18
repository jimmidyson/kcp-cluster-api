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

package coremanager

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
)

// NewWorkspaceSecretReader returns a client.Reader that reads Secrets from the
// logical cluster named in each call's context, addressed on the kcp shard.
//
// # Why the fleet's own client cannot do this
//
// Every other client the fleet-wide controllers hold resolves through the
// multi-cluster manager, and so reads through the APIExport's virtual
// workspace. That is right for everything the export serves and wrong for
// everything it does not. A virtual workspace describes exactly the exported
// API surface, and `v1.Secret` is not part of it, so the ClusterCache's attempt
// to read a Cluster's kubeconfig fails at the RESTMapper before it reaches the
// wire:
//
//	error getting kubeconfig secret: failed to get informer for *v1.Secret:
//	failed to get REST mapping: no matches for kind "Secret" in version "v1"
//
// It is not a permissions problem and not a missing Secret. It is the wrong
// endpoint: the kubeconfig lives in the workspace on the shard, and only the
// shard serves core types. Wiring the cluster-aware client here — which is what
// this project did first — leaves the ClusterCache unable to connect to any
// workload cluster at all, which the idle sweeps could not see because an idle
// workspace holds no Cluster for it to try.
//
// # Why it is uncached
//
// A cache here would hold every tenant's kubeconfig in memory for the life of
// the process. The ClusterCache reads a Secret once per connection and caches
// the connection, so the read is rare and the saving would be small — a poor
// trade for holding every tenant's credentials resident.
//
// What is cached is one client per logical cluster, which holds a transport
// rather than any object. kcp's client cache does that and releases the entry
// when the workspace goes away.
//
// # Why it serves only Secrets
//
// Because that is all its consumer asks for: clustercache.Options.SecretClient
// is read exclusively through util/kubeconfig.FromSecret. Serving one kind lets
// this carry a fixed RESTMapper rather than doing discovery per workspace,
// which for a fleet is a request per workspace to answer a question whose
// answer is the same everywhere. Anything else is refused by name rather than
// mis-mapped.
func NewWorkspaceSecretReader(shard *rest.Config) (client.Reader, error) {
	if shard == nil {
		return nil, errors.New("a shard rest.Config is required: the virtual workspace does not serve Secrets")
	}

	httpClient, err := rest.HTTPClientFor(shard)
	if err != nil {
		return nil, fmt.Errorf("building the shard HTTP client: %w", err)
	}

	// One kind, mapped by hand. See "Why it serves only Secrets" above.
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("building the Secret scheme: %w", err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Secret"), meta.RESTScopeNamespace)

	clients := kcpclient.NewCache(shard, httpClient, &kcpclient.Constructor[client.Client]{
		NewForConfigAndClient: func(cfg *rest.Config, h *http.Client) (client.Client, error) {
			return client.New(cfg, client.Options{Scheme: scheme, Mapper: mapper, HTTPClient: h})
		},
	})

	return &workspaceSecretReader{clients: clients}, nil
}

type workspaceSecretReader struct {
	clients kcpclient.Cache[client.Client]
}

func (r *workspaceSecretReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	cl, err := r.clusterFor(ctx, obj)
	if err != nil {
		return err
	}
	return cl.Get(ctx, key, obj, opts...)
}

func (r *workspaceSecretReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	cl, err := r.clusterFor(ctx, list)
	if err != nil {
		return err
	}
	return cl.List(ctx, list, opts...)
}

func (r *workspaceSecretReader) clusterFor(ctx context.Context, obj runtime.Object) (client.Client, error) {
	switch obj.(type) {
	case *corev1.Secret, *corev1.SecretList:
	default:
		return nil, fmt.Errorf("workspace secret reader was asked for %T: it serves Secrets only, "+
			"because a fixed mapping is what lets it avoid discovery per workspace", obj)
	}

	// No cluster in the context is a wiring mistake — a handler that was not
	// lifted, or a bare context — and reading some default workspace's Secret
	// in response is how one tenant's kubeconfig reaches another's controller.
	// It errors on the call that made the mistake instead.
	name, ok := mccontext.ClusterFrom(ctx)
	if !ok || name == "" {
		return nil, errors.New("no cluster in context: a kubeconfig Secret cannot be read without knowing whose it is")
	}

	cl, err := r.clients.Cluster(logicalcluster.NewPath(string(name)))
	if err != nil {
		return nil, fmt.Errorf("building a client for workspace %q: %w", name, err)
	}
	return cl, nil
}
